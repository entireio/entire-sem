package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-sem/internal/bench"
	"github.com/entireio/entire-sem/internal/sem"
)

func TestParseProfile(t *testing.T) {
	cases := map[string]sem.Profile{
		"":            sem.ProfileFull,
		"full":        sem.ProfileFull,
		"fast":        sem.ProfileFast,
		"syntax-only": sem.ProfileSyntaxOnly,
	}
	for input, want := range cases {
		got, err := parseProfile(input)
		if err != nil {
			t.Fatalf("parseProfile(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseProfile(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseProfile("bogus"); err == nil {
		t.Fatalf("parseProfile(bogus) should error")
	}
}

// A repo that is not cloned is skipped, but its metrics row must still report
// the run's selected profile so a fast/syntax-only report does not leave the
// skipped rows' profile blank.
func TestSkippedRepoReportsSelectedProfile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := `{"languages":{"Go":["owner/not-cloned"]}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache") // empty: the repo is "not cloned"
	outDir := filepath.Join(dir, "out")
	lockPath := filepath.Join(dir, "lock.json")

	// skip-clone so no network is touched; syntax-only is the selected profile.
	err := run(manifestPath, cacheDir, outDir, lockPath, "", "syntax-only", 0, 1, 1, true, false, "bench-test", false, 0, 0, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := readOnlyReport(t, outDir)
	if report.Profile != "syntax-only" {
		t.Fatalf("report profile = %q, want syntax-only", report.Profile)
	}
	if len(report.Repos) != 1 {
		t.Fatalf("repos = %#v, want one skipped row", report.Repos)
	}
	got := report.Repos[0]
	if got.Error == "" {
		t.Fatalf("skipped repo should record an error: %#v", got)
	}
	if got.Profile != "syntax-only" {
		t.Fatalf("skipped repo profile = %q, want syntax-only", got.Profile)
	}
}

func TestGuardrailFailureAfterReport(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"languages":{"Go":["owner/not-cloned"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	err := run(manifestPath, filepath.Join(dir, "cache"), outDir, filepath.Join(dir, "lock.json"), "", "syntax-only", 0, 1, 1, true, false, "bench-test", false, 1, 0, false)
	if err == nil {
		t.Fatalf("expected guardrail failure")
	}
	if report := readOnlyReport(t, outDir); report.Profile != "syntax-only" {
		t.Fatalf("report was not written before guardrail failure: %#v", report)
	}
}

func readOnlyReport(t *testing.T, outDir string) bench.Report {
	t.Helper()
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one report file, got %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(outDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var report bench.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return report
}
