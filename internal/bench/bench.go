// Package bench measures the performance and quality of the semantic provider
// over a set of repositories. It is the measured core of the sem-bench tool:
// the analysis it runs is local-only (no egress), so cloning happens elsewhere
// and this package only reads already-present source.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/entireio/entire-sem/internal/sem"
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
	OutputEstimated   bool           `json:"output_bytes_estimated,omitempty"`
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
	Progress         func(sem.ProgressEvent)
	MaxRSSBytes      uint64
	ExactOutputBytes bool
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

	var summary sem.SnapshotSummary
	outputBytes := 0
	start := time.Now()
	measureCtx, stopRSSGuard, rssExceeded := startRSSGuard(ctx, opts.MaxRSSBytes)
	defer stopRSSGuard()
	err := sem.StreamSnapshot(measureCtx, dir, providerVersion, sem.ProviderSnapshotOptions{NoNetwork: true, Profile: profile, Progress: opts.Progress}, func(record any) error {
		if rss := rssExceeded.Load(); rss > 0 {
			return fmt.Errorf("memory guardrail failed during measurement: max RSS %d exceeds ceiling %d", rss, opts.MaxRSSBytes)
		}
		outputBytes += recordOutputBytes(record, opts.ExactOutputBytes)
		switch r := record.(type) {
		case sem.SnapshotHeader:
			metrics.Commit = r.Commit
			metrics.RelationSet = append([]string(nil), r.RelationSet...)
		case sem.FileRecord:
			metrics.Files++
			metrics.SourceBytes += r.Bytes
			metrics.LOC += r.Lines
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
	if rss := rssExceeded.Load(); rss > 0 {
		err = fmt.Errorf("memory guardrail failed during measurement: max RSS %d exceeds ceiling %d", rss, opts.MaxRSSBytes)
	}
	if err != nil {
		metrics.Error = err.Error()
		return metrics, err
	}
	runtime.ReadMemStats(&after)

	metrics.WallMS = round2(float64(wall.Microseconds()) / 1000)
	metrics.OutputBytes = outputBytes
	metrics.OutputEstimated = !opts.ExactOutputBytes
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

	if seconds := wall.Seconds(); seconds > 0 {
		metrics.FilesPerSec = round2(float64(metrics.Files) / seconds)
		metrics.LOCPerSec = round2(float64(metrics.LOC) / seconds)
	}
	return metrics, nil
}

func recordOutputBytes(record any, exact bool) int {
	if exact {
		if encoded, err := json.Marshal(record); err == nil {
			return len(encoded) + 1
		}
		return 0
	}
	switch r := record.(type) {
	case sem.FileRecord:
		return 96 + len(r.ID) + len(r.Path) + len(r.Blob) + len(r.Language)
	case sem.SymbolRecord:
		return 176 + len(r.ID) + len(r.StableIDVersion) + len(r.Kind) + len(r.Name) + len(r.QualifiedName) + len(r.FilePath) + len(r.Signature) + len(r.BodyHash) + len(r.Language) + len(r.ContainerID)
	case sem.RelationRecord:
		size := 160 + len(r.FromID) + len(r.ToID) + len(r.Type) + len(r.Reason) + len(r.RelationScope) + len(r.Resolution) + len(r.TargetKind)
		for _, evidence := range r.Evidence {
			size += 64 + len(evidence.Kind) + len(evidence.FilePath) + len(evidence.Detail)
		}
		for _, code := range r.WarningCodes {
			size += 4 + len(code)
		}
		return size
	case sem.ExternalRecord:
		return 144 + len(r.ID) + len(r.Kind) + len(r.Value) + len(r.FilePath) + len(r.Signature) + len(r.Language) + len(r.SourceSymbol) + len(r.SourceDetails)
	default:
		if encoded, err := json.Marshal(record); err == nil {
			return len(encoded) + 1
		}
		return 0
	}
}

func startRSSGuard(ctx context.Context, maxRSSBytes uint64) (context.Context, func(), *atomic.Uint64) {
	var exceeded atomic.Uint64
	if maxRSSBytes == 0 {
		return ctx, func() {}, &exceeded
	}
	guardCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	check := func() bool {
		rss := maxRSSBytesCurrent()
		if rss > maxRSSBytes {
			exceeded.CompareAndSwap(0, rss)
			cancel()
			return true
		}
		return false
	}
	if check() {
		return guardCtx, func() {
			cancel()
			close(done)
		}, &exceeded
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if check() {
					return
				}
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	stop := func() {
		cancel()
		close(done)
	}
	return guardCtx, stop, &exceeded
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
	return maxRSSBytesCurrent()
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
