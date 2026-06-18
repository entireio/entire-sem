package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureRepoComputesPerformanceAndQuality(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.go", `package auth

import "strings"

func Validate(token string) bool {
	return strings.TrimSpace(token) != ""
}

func Check(token string) bool {
	return Validate(token)
}
`)

	metrics, err := MeasureRepo(t.Context(), "local/sample", "Go", dir, "bench-test")
	if err != nil {
		t.Fatal(err)
	}

	if metrics.Files != 1 || metrics.ParsedFiles != 1 {
		t.Fatalf("file counts = %#v", metrics)
	}
	if metrics.LOC < 10 {
		t.Fatalf("loc = %d, want >= 10", metrics.LOC)
	}
	if metrics.Symbols == 0 || metrics.Relations == 0 {
		t.Fatalf("symbol/relation counts = %#v", metrics)
	}
	if metrics.OutputBytes == 0 {
		t.Fatalf("output bytes not measured: %#v", metrics)
	}
	if metrics.RelationsByType["DEFINES"] == 0 {
		t.Fatalf("relations_by_type missing DEFINES: %#v", metrics.RelationsByType)
	}
	if metrics.ConfidenceBands["exact"] == 0 {
		t.Fatalf("confidence bands missing exact: %#v", metrics.ConfidenceBands)
	}
	if metrics.ResolutionCounts["exact"] == 0 {
		t.Fatalf("resolution counts missing exact: %#v", metrics.ResolutionCounts)
	}
	if !contains(metrics.Languages, "Go") {
		t.Fatalf("languages = %#v", metrics.Languages)
	}
}

func TestBuildReportAggregatesByLanguage(t *testing.T) {
	metrics := []RepoMetrics{
		{Name: "a", Language: "Go", Files: 10, LOC: 1000, Symbols: 50, Relations: 80, WallMS: 100},
		{Name: "b", Language: "Go", Files: 5, LOC: 500, Symbols: 20, Relations: 30, WallMS: 50},
		{Name: "c", Language: "Python", Files: 8, LOC: 800, Symbols: 40, Relations: 60, WallMS: 80},
		{Name: "broken", Language: "Python", Error: "boom"},
	}

	report := BuildReport("2026-06-18T00:00:00Z", "bench-test", metrics)

	if report.SchemaVersion == "" {
		t.Fatalf("schema version missing")
	}
	if report.ByLanguage["Go"].Repos != 2 || report.ByLanguage["Go"].Files != 15 {
		t.Fatalf("go aggregate = %#v", report.ByLanguage["Go"])
	}
	// The errored repo must not inflate aggregates.
	if report.ByLanguage["Python"].Repos != 1 || report.ByLanguage["Python"].Files != 8 {
		t.Fatalf("python aggregate = %#v", report.ByLanguage["Python"])
	}
	if report.Totals.Repos != 3 {
		t.Fatalf("totals repos = %d, want 3", report.Totals.Repos)
	}
	if report.Totals.LOCPerSec <= 0 {
		t.Fatalf("totals loc/sec = %v", report.Totals.LOCPerSec)
	}
	// Repos are sorted by language then name for stable diffs.
	if report.Repos[0].Name != "a" || report.Repos[len(report.Repos)-1].Language != "Python" {
		t.Fatalf("repos not sorted: %#v", report.Repos)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
