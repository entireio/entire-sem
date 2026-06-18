// Package bench measures the performance and quality of the semantic provider
// over a set of repositories. It is the measured core of the sem-bench tool:
// the analysis it runs is local-only (no egress), so cloning happens elsewhere
// and this package only reads already-present source.
package bench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/suhaanthayyil/entire-sem/internal/sem"
)

// RepoMetrics captures the performance and quality of analyzing one repository.
// It is emitted verbatim in the JSON report so trends can be compared across
// work phases.
type RepoMetrics struct {
	Name              string         `json:"name"`
	Language          string         `json:"language"`
	Commit            string         `json:"commit,omitempty"`
	WallMS            float64        `json:"wall_ms"`
	Files             int            `json:"files"`
	ParsedFiles       int            `json:"parsed_files"`
	LOC               int            `json:"loc"`
	SourceBytes       int            `json:"source_bytes"`
	OutputBytes       int            `json:"output_bytes"`
	Symbols           int            `json:"symbols"`
	Relations         int            `json:"relations"`
	Externals         int            `json:"externals"`
	ParseFailures     int            `json:"parse_failures"`
	CompletenessLevel string         `json:"completeness_level"`
	FilesPerSec       float64        `json:"files_per_sec"`
	LOCPerSec         float64        `json:"loc_per_sec"`
	AllocBytes        uint64         `json:"alloc_bytes"`
	Languages         []string       `json:"languages"`
	RelationsByType   map[string]int `json:"relations_by_type"`
	ResolutionCounts  map[string]int `json:"resolution_counts"`
	ConfidenceBands   map[string]int `json:"confidence_bands"`
	FailureCodes      map[string]int `json:"failure_codes"`
	UnresolvedImports int            `json:"unresolved_relative_imports"`
	Error             string         `json:"error,omitempty"`
}

// MeasureRepo runs the provider snapshot over dir and returns its metrics. The
// snapshot is built with NoNetwork set so the measured path stays no-egress;
// timing and Go allocation stats bracket only the snapshot build, while LOC is
// counted afterward so file reads do not skew the wall time.
func MeasureRepo(ctx context.Context, name, language, dir, providerVersion string) (RepoMetrics, error) {
	metrics := RepoMetrics{
		Name:             name,
		Language:         language,
		RelationsByType:  map[string]int{},
		ResolutionCounts: map[string]int{},
		ConfidenceBands:  map[string]int{},
		FailureCodes:     map[string]int{},
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	snapshot, err := sem.BuildProviderSnapshotWithOptions(ctx, dir, providerVersion, sem.ProviderSnapshotOptions{NoNetwork: true})
	wall := time.Since(start)
	if err != nil {
		metrics.Error = err.Error()
		return metrics, err
	}
	runtime.ReadMemStats(&after)

	counter := &countingWriter{}
	_ = sem.WriteSnapshotNDJSON(counter, snapshot)

	metrics.Commit = snapshot.Header.Commit
	metrics.WallMS = float64(wall.Microseconds()) / 1000
	metrics.Files = snapshot.Header.Stats.Files
	metrics.ParsedFiles = snapshot.Header.Stats.ParsedFiles
	metrics.Symbols = snapshot.Header.Stats.Symbols
	metrics.Relations = snapshot.Header.Stats.Relations
	metrics.Externals = len(snapshot.Externals)
	metrics.ParseFailures = snapshot.Header.Stats.PartialFailures
	metrics.CompletenessLevel = snapshot.Header.Stats.CompletenessLevel
	metrics.OutputBytes = counter.n
	metrics.Languages = append([]string(nil), snapshot.Header.Languages...)
	if after.TotalAlloc >= before.TotalAlloc {
		metrics.AllocBytes = after.TotalAlloc - before.TotalAlloc
	}

	for _, file := range snapshot.Files {
		metrics.SourceBytes += file.Bytes
		if content, readErr := os.ReadFile(filepath.Join(dir, file.Path)); readErr == nil {
			metrics.LOC += countLines(content)
		}
	}

	for _, relation := range snapshot.Relations {
		metrics.RelationsByType[relation.Type]++
		resolution := relation.Resolution
		if resolution == "" {
			resolution = "unspecified"
		}
		metrics.ResolutionCounts[resolution]++
		metrics.ConfidenceBands[confidenceBand(relation.Confidence)]++
		for _, code := range relation.WarningCodes {
			if code == "UNRESOLVED_RELATIVE_IMPORT" {
				metrics.UnresolvedImports++
			}
		}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		metrics.FailureCodes[failure.Code]++
	}

	if seconds := wall.Seconds(); seconds > 0 {
		metrics.FilesPerSec = round2(float64(metrics.Files) / seconds)
		metrics.LOCPerSec = round2(float64(metrics.LOC) / seconds)
	}
	metrics.WallMS = round2(metrics.WallMS)
	return metrics, nil
}

// confidenceBand maps a numeric confidence to the v2-plan bands.
func confidenceBand(confidence float64) string {
	switch {
	case confidence >= 0.90:
		return "exact"
	case confidence >= 0.70:
		return "strong"
	case confidence >= 0.40:
		return "heuristic"
	default:
		return "weak"
	}
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}

type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// Aggregate summarizes a set of repo metrics for a language or the whole run.
type Aggregate struct {
	Repos             int     `json:"repos"`
	Files             int     `json:"files"`
	LOC               int     `json:"loc"`
	Symbols           int     `json:"symbols"`
	Relations         int     `json:"relations"`
	ParseFailures     int     `json:"parse_failures"`
	WallMS            float64 `json:"wall_ms"`
	OutputBytes       int     `json:"output_bytes"`
	LOCPerSec         float64 `json:"loc_per_sec"`
	FilesPerSec       float64 `json:"files_per_sec"`
	UnresolvedImports int     `json:"unresolved_relative_imports"`
}

func aggregate(metrics []RepoMetrics) Aggregate {
	var agg Aggregate
	for _, m := range metrics {
		if m.Error != "" {
			continue
		}
		agg.Repos++
		agg.Files += m.Files
		agg.LOC += m.LOC
		agg.Symbols += m.Symbols
		agg.Relations += m.Relations
		agg.ParseFailures += m.ParseFailures
		agg.WallMS += m.WallMS
		agg.OutputBytes += m.OutputBytes
		agg.UnresolvedImports += m.UnresolvedImports
	}
	if seconds := agg.WallMS / 1000; seconds > 0 {
		agg.LOCPerSec = round2(float64(agg.LOC) / seconds)
		agg.FilesPerSec = round2(float64(agg.Files) / seconds)
	}
	agg.WallMS = round2(agg.WallMS)
	return agg
}

// Report is the full benchmark output for one run.
type Report struct {
	GeneratedAt     string               `json:"generated_at"`
	ProviderVersion string               `json:"provider_version"`
	SchemaVersion   string               `json:"schema_version"`
	Repos           []RepoMetrics        `json:"repos"`
	ByLanguage      map[string]Aggregate `json:"by_language"`
	Totals          Aggregate            `json:"totals"`
}

// BuildReport assembles per-language and total aggregates from repo metrics.
// generatedAt is passed in so callers control timestamp determinism.
func BuildReport(generatedAt, providerVersion string, metrics []RepoMetrics) Report {
	byLanguage := map[string][]RepoMetrics{}
	for _, m := range metrics {
		byLanguage[m.Language] = append(byLanguage[m.Language], m)
	}
	aggByLanguage := map[string]Aggregate{}
	for language, group := range byLanguage {
		aggByLanguage[language] = aggregate(group)
	}
	sorted := append([]RepoMetrics(nil), metrics...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Language != sorted[j].Language {
			return sorted[i].Language < sorted[j].Language
		}
		return sorted[i].Name < sorted[j].Name
	})
	return Report{
		GeneratedAt:     generatedAt,
		ProviderVersion: providerVersion,
		SchemaVersion:   sem.SchemaVersion,
		Repos:           sorted,
		ByLanguage:      aggByLanguage,
		Totals:          aggregate(metrics),
	}
}
