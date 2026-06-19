// Package bench measures the performance and quality of the semantic provider
// over a set of repositories. It is the measured core of the sem-bench tool:
// the analysis it runs is local-only (no egress), so cloning happens elsewhere
// and this package only reads already-present source.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/suhaanthayyil/entire-sem/internal/sem"
)

// RepoMetrics captures the performance and quality of analyzing one repository.
// It is emitted verbatim in the JSON report so trends can be compared across
// work phases.
type RepoMetrics struct {
	Name              string         `json:"name"`
	Language          string         `json:"language"`
	Profile           string         `json:"profile"`
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
	RelationSet       []string       `json:"relation_set"`
	RelationsByType   map[string]int `json:"relations_by_type"`
	ResolutionCounts  map[string]int `json:"resolution_counts"`
	ConfidenceBands   map[string]int `json:"confidence_bands"`
	FailureCodes      map[string]int `json:"failure_codes"`
	UnresolvedImports int            `json:"unresolved_relative_imports"`
	Error             string         `json:"error,omitempty"`
}

type MeasureOptions struct {
	Progress func(sem.ProgressEvent)
}

// MeasureRepo measures the streaming provider path (the production path) over
// dir at the given profile and returns its metrics. StreamSnapshot is run with
// NoNetwork set so the measured path stays no-egress; metrics are tallied from
// the streamed records and the trailing summary. LOC is counted afterward so
// the extra file reads do not skew wall time.
func MeasureRepo(ctx context.Context, name, language, dir, providerVersion string, profile sem.Profile) (RepoMetrics, error) {
	return MeasureRepoWithOptions(ctx, name, language, dir, providerVersion, profile, MeasureOptions{})
}

func MeasureRepoWithOptions(ctx context.Context, name, language, dir, providerVersion string, profile sem.Profile, opts MeasureOptions) (RepoMetrics, error) {
	if profile == "" {
		profile = sem.ProfileFull
	}
	metrics := RepoMetrics{
		Name:             name,
		Language:         language,
		Profile:          string(profile),
		RelationsByType:  map[string]int{},
		ResolutionCounts: map[string]int{},
		ConfidenceBands:  map[string]int{},
		FailureCodes:     map[string]int{},
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	var filePaths []string
	var summary sem.SnapshotSummary
	outputBytes := 0
	start := time.Now()
	err := sem.StreamSnapshot(ctx, dir, providerVersion, sem.ProviderSnapshotOptions{NoNetwork: true, Profile: profile, Progress: opts.Progress}, func(record any) error {
		if encoded, marshalErr := json.Marshal(record); marshalErr == nil {
			outputBytes += len(encoded) + 1 // record + newline, the NDJSON byte cost
		}
		switch r := record.(type) {
		case sem.SnapshotHeader:
			metrics.Commit = r.Commit
			metrics.RelationSet = append([]string(nil), r.RelationSet...)
		case sem.FileRecord:
			metrics.Files++
			metrics.SourceBytes += r.Bytes
			filePaths = append(filePaths, r.Path)
		case sem.SymbolRecord:
			metrics.Symbols++
		case sem.ExternalRecord:
			metrics.Externals++
		case sem.RelationRecord:
			metrics.Relations++
			metrics.RelationsByType[r.Type]++
			resolution := r.Resolution
			if resolution == "" {
				resolution = "unspecified"
			}
			metrics.ResolutionCounts[resolution]++
			metrics.ConfidenceBands[confidenceBand(r.Confidence)]++
			for _, code := range r.WarningCodes {
				if code == "UNRESOLVED_RELATIVE_IMPORT" {
					metrics.UnresolvedImports++
				}
			}
		case sem.SnapshotSummary:
			summary = r
		}
		return nil
	})
	wall := time.Since(start)
	if err != nil {
		metrics.Error = err.Error()
		return metrics, err
	}
	runtime.ReadMemStats(&after)

	metrics.WallMS = round2(float64(wall.Microseconds()) / 1000)
	metrics.OutputBytes = outputBytes
	metrics.ParsedFiles = summary.Stats.ParsedFiles
	metrics.ParseFailures = summary.Stats.PartialFailures
	metrics.CompletenessLevel = summary.Stats.CompletenessLevel
	metrics.Languages = append([]string(nil), summary.Languages...)
	for _, failure := range summary.PartialFailures {
		metrics.FailureCodes[failure.Code]++
	}
	if after.TotalAlloc >= before.TotalAlloc {
		metrics.AllocBytes = after.TotalAlloc - before.TotalAlloc
	}

	for _, path := range filePaths {
		if content, readErr := os.ReadFile(filepath.Join(dir, path)); readErr == nil {
			metrics.LOC += countLines(content)
		}
	}

	if seconds := wall.Seconds(); seconds > 0 {
		metrics.FilesPerSec = round2(float64(metrics.Files) / seconds)
		metrics.LOCPerSec = round2(float64(metrics.LOC) / seconds)
	}
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

// Hardware records the machine the benchmark ran on, for comparing results.
type Hardware struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	CPUs int    `json:"cpus"`
}

// maxRSSBytes returns the process peak resident set size (best-effort). It is a
// process-wide peak, not per-repo; Linux reports kilobytes, macOS reports bytes.
func maxRSSBytes() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	rss := uint64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return rss
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
	Profile         string               `json:"profile"`
	Hardware        Hardware             `json:"hardware"`
	MaxRSSBytes     uint64               `json:"max_rss_bytes"`
	Repos           []RepoMetrics        `json:"repos"`
	ByLanguage      map[string]Aggregate `json:"by_language"`
	Totals          Aggregate            `json:"totals"`
}

// BuildReport assembles per-language and total aggregates from repo metrics.
// generatedAt is passed in so callers control timestamp determinism; profile is
// the indexing profile the run measured.
func BuildReport(generatedAt, providerVersion string, profile sem.Profile, metrics []RepoMetrics) Report {
	if profile == "" {
		profile = sem.ProfileFull
	}
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
		Profile:         string(profile),
		Hardware:        Hardware{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()},
		MaxRSSBytes:     maxRSSBytes(),
		Repos:           sorted,
		ByLanguage:      aggByLanguage,
		Totals:          aggregate(metrics),
	}
}
