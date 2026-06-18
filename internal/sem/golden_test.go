package sem

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestStreamSnapshotMatchesBuild asserts the streaming path emits the same set
// of file/symbol/external/relation records as the in-memory path for every
// fixture (order aside, which the streaming contract does not promise).
func TestStreamSnapshotMatchesBuild(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			copyFixtureTree(t, filepath.Join("testdata", "fixtures", name), dir)
			opts := ProviderSnapshotOptions{Worktree: true}

			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "0.0.0-test", opts)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, r := range snapshot.Files {
				want = append(want, marshalRecord(t, r))
			}
			for _, r := range snapshot.Externals {
				want = append(want, marshalRecord(t, r))
			}
			for _, r := range snapshot.Symbols {
				want = append(want, marshalRecord(t, r))
			}
			for _, r := range snapshot.Relations {
				want = append(want, marshalRecord(t, r))
			}

			var got []string
			err = StreamSnapshot(t.Context(), dir, "0.0.0-test", opts, func(rec any) error {
				switch rec.(type) {
				case FileRecord, ExternalRecord, SymbolRecord, RelationRecord:
					got = append(got, marshalRecord(t, rec))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			sort.Strings(want)
			sort.Strings(got)
			if len(want) != len(got) {
				t.Fatalf("record count: build=%d stream=%d", len(want), len(got))
			}
			for i := range want {
				if want[i] != got[i] {
					t.Fatalf("record mismatch at %d:\n build:  %s\n stream: %s", i, want[i], got[i])
				}
			}
		})
	}
}

func marshalRecord(t *testing.T, rec any) string {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
	"csharp-oo",
	"go-basic",
	"go-clones",
	"go-tests",
	"go-types",
	"java-basic",
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
