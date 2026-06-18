package sem

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStreamSnapshotOrderIsDeterministic confirms that streaming a fixed input
// twice produces a byte-identical record sequence — file, symbol, and relation
// order are all stable. It stresses the orderings that derive from Go maps
// (a caller invoking several functions; a subclass overriding several methods).
func TestStreamSnapshotOrderIsDeterministic(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "calls.go", `package p

func driver() {
	alpha()
	bravo()
	charlie()
	delta()
}

func alpha()   {}
func bravo()   {}
func charlie() {}
func delta()   {}
`)
	writeFile(t, repo, "Animals.java", `class Animal {
	String describe() { return "animal"; }
	String sound() { return "?"; }
	String legs() { return "4"; }
}

class Dog extends Animal {
	String describe() { return "dog"; }
	String sound() { return "woof"; }
	String legs() { return "4"; }
}
`)

	capture := func() string {
		var buf bytes.Buffer
		err := StreamSnapshot(t.Context(), repo, "v", ProviderSnapshotOptions{Worktree: true}, func(rec any) error {
			data, marshalErr := json.Marshal(rec)
			if marshalErr != nil {
				return marshalErr
			}
			buf.Write(data)
			buf.WriteByte('\n')
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	first, second := capture(), capture()
	if first != second {
		t.Fatalf("stream output is not deterministic across runs")
	}
	// Sanity: the fixture actually produced the relation kinds we want stable.
	if !strings.Contains(first, `"type":"CALLS"`) || !strings.Contains(first, `"type":"OVERRIDES"`) {
		t.Fatalf("fixture did not exercise CALLS/OVERRIDES ordering")
	}
}

// TestStreamSnapshotStreamsIncrementally proves the streaming contract: a lean
// header is emitted first (before parsing finishes), file and symbol records are
// emitted before relation resolution produces any relation, and a trailing
// summary carries the totals the lean header omits. This is what makes the path
// memory-bounded: nothing waits for the whole repo to be parsed.
func TestStreamSnapshotStreamsIncrementally(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-fields")
	copyFixtureTree(t, filepath.Join("testdata", "fixtures", "go-fields"), dir)

	var header SnapshotHeader
	var summary SnapshotSummary
	haveHeader, haveSummary := false, false
	firstFile, firstSymbol, firstRelation := -1, -1, -1
	index := 0
	err := StreamSnapshot(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true}, func(rec any) error {
		switch r := rec.(type) {
		case SnapshotHeader:
			header, haveHeader = r, true
			if index != 0 {
				t.Fatalf("header must be the first record, was at %d", index)
			}
		case FileRecord:
			if firstFile < 0 {
				firstFile = index
			}
		case SymbolRecord:
			if firstSymbol < 0 {
				firstSymbol = index
			}
		case RelationRecord:
			if firstRelation < 0 {
				firstRelation = index
			}
		case SnapshotSummary:
			summary, haveSummary = r, true
		}
		index++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Lean header: emitted before the repo is fully processed, so its totals are
	// empty and reported in the summary instead.
	if !haveHeader || len(header.Languages) != 0 || header.Stats.Relations != 0 {
		t.Fatalf("expected a lean header (empty languages, zero relation stat): %#v", header)
	}
	// File and symbol records must precede relation resolution.
	if firstFile < 0 || firstSymbol < 0 || firstRelation < 0 {
		t.Fatalf("missing record kinds: file=%d symbol=%d relation=%d", firstFile, firstSymbol, firstRelation)
	}
	if firstFile >= firstRelation || firstSymbol >= firstRelation {
		t.Fatalf("file/symbol records must stream before relations: file=%d symbol=%d relation=%d", firstFile, firstSymbol, firstRelation)
	}
	// Summary carries the totals the lean header omitted.
	if !haveSummary || len(summary.Languages) == 0 || summary.Stats.Relations == 0 || summary.Stats.Symbols == 0 {
		t.Fatalf("summary must carry languages and stats: %#v", summary)
	}
}

// updateGolden regenerates the committed NDJSON baselines instead of asserting
// against them. Run:
//
//	go test ./internal/sem -run TestProviderGoldenSnapshots -update
var updateGolden = flag.Bool("update", false, "regenerate golden NDJSON baseline files")

// goldenFixtures enumerates the fixture repos under testdata/fixtures. Each
// fixture is a self-contained source tree; the harness snapshots it in worktree
// mode and compares the full NDJSON stream against a committed baseline. The
// baselines are the machine-readable record of the current provider contract,
// so any change to symbols, relations, externals, or header stats shows up as a
// golden diff in review.
//
// Adding a fixture is just dropping a directory under testdata/fixtures, listing
// its name here, and running the test with -update to create the baseline.
var goldenFixtures = []string{
	"csharp-basic",
	"csharp-fields",
	"csharp-oo",
	"go-basic",
	"go-clones",
	"go-fields",
	"go-tests",
	"go-types",
	"java-basic",
	"java-fields",
	"java-oo",
	"php-basic",
	"php-oo",
	"python-basic",
	"python-imports",
	"python-oo",
	"rust-basic",
	"rust-oo",
	"terraform-basic",
	"typescript-basic",
	"typescript-events",
	"typescript-fields",
	"typescript-http",
	"typescript-imports",
	"typescript-oo",
}

func TestProviderGoldenSnapshots(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			got := buildFixtureNDJSON(t, name)
			goldenPath := filepath.Join("testdata", "fixtures", name+".ndjson.golden")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Fatalf("snapshot for %s does not match golden; run:\n\tgo test ./internal/sem -run TestProviderGoldenSnapshots -update\n\n--- got ---\n%s", name, got)
			}
		})
	}
}

// buildFixtureNDJSON copies a fixture into an isolated temp directory (outside
// any git tree, so repo_key resolves to a stable local/<name>), snapshots it in
// worktree mode, and returns the normalized NDJSON stream. Worktree mode avoids
// a machine-specific git error in the no-HEAD warning detail, leaving repo_root
// as the only path-dependent field, which is normalized below.
func buildFixtureNDJSON(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "fixtures", name)
	dir := filepath.Join(t.TempDir(), name)
	copyFixtureTree(t, src, dir)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "0.0.0-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSnapshotNDJSON(&buf, snapshot); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(buf.String(), dir, "<repo>")
}

func copyFixtureTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
