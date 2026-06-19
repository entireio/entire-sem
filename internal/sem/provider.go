package sem

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/suhaanthayyil/entire-sem/internal/gitutil"
)

const (
	// SchemaVersion is bumped to 1.1 for the additive snapshot fields introduced
	// alongside boundary source locations (the `external` flag on external records
	// and the per-symbol source-location fields). The shape is backward compatible
	// for tolerant readers; the bump lets consumers detect the new fields.
	SchemaVersion         = "1.1"
	ProviderName          = "entire-sem"
	StableSymbolIDVersion = "compound-v1"
	defaultMaxParseBytes  = 4 * 1024 * 1024
)

var relationTypes = []string{
	"DEFINES",
	"CONTAINS",
	"IMPORTS",
	"CALLS",
	"EXTENDS",
	"IMPLEMENTS",
	"OVERRIDES",
	"USES_TYPE",
	"READS_FIELD",
	"WRITES_FIELD",
	"ACCESSES",
	"HANDLES_ROUTE",
	"HTTP_CALLS",
	"EMITS",
	"LISTENS_ON",
	"HANDLES_TOOL",
	"SIMILAR_TO",
	"TESTS",
	"RESOURCE_DEPENDS_ON",
}

// ooRelationSupport lists the additional (non-structural) relation types the
// provider can extract for each language, used by the capabilities matrix.
// OVERRIDES is derived from a resolved supertype's methods, so it is advertised
// only for class-based languages with clear method containers.
var ooRelationSupport = map[string][]string{
	"Java":       {"EXTENDS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES"},
	"TypeScript": {"EXTENDS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES"},
	"JavaScript": {"EXTENDS"},
	"C#":         {"EXTENDS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES"},
	"PHP":        {"EXTENDS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE"},
	"Python":     {"EXTENDS", "OVERRIDES", "USES_TYPE"},
	"Rust":       {"EXTENDS", "IMPLEMENTS", "USES_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES"},
	"Go":         {"USES_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES"},
	"HCL":        {"RESOURCE_DEPENDS_ON"},
}

// schemaFeatures lists the optional schema 1.1 features this build emits. It
// lets consumers detect available fields without inspecting every record. Keep
// sorted and stable; only add entries when the field is actually populated.
var schemaFeatures = []string{
	"boundary_source_locations",
	"completeness_breakdown",
	"language_versions",
	"relation_evidence",
	"relation_resolution",
	"relation_scope",
	"relation_target_kind",
}

// parserVersions reports the parser/grammar libraries backing extraction. It is
// shared by the snapshot header (language_versions) and the capabilities report.
func parserVersions() map[string]string {
	return map[string]string{
		"go-tree-sitter": "github.com/smacker/go-tree-sitter",
	}
}

type ProviderRecord struct {
	RecordType string `json:"record_type,omitempty"`
}

type SnapshotHeader struct {
	SchemaVersion    string             `json:"schema_version"`
	Provider         string             `json:"provider"`
	ProviderVersion  string             `json:"provider_version"`
	RepoRoot         string             `json:"repo_root"`
	RepoKey          string             `json:"repo_key"`
	Commit           string             `json:"commit"`
	Tree             string             `json:"tree"`
	Languages        []string           `json:"languages"`
	Capabilities     []string           `json:"capabilities"`
	SchemaFeatures   []string           `json:"schema_features"`
	LanguageVersions map[string]string  `json:"language_versions,omitempty"`
	Profile          string             `json:"profile"`
	ProfileLimits    ProfileLimits      `json:"profile_limits"`
	RelationSet      []string           `json:"relation_set"`
	SkippedRelations []string           `json:"skipped_relation_families"`
	Warnings         []ProviderWarning  `json:"warnings"`
	PartialFailures  []PartialFailure   `json:"partial_failures"`
	Stats            ProviderStats      `json:"stats"`
	Completeness     CompletenessReport `json:"completeness"`
	BenchmarkProfile string             `json:"benchmark_profile,omitempty"`
}

// ProfileLimits documents the depth limits the selected profile applies.
type ProfileLimits struct {
	Evidence       string `json:"evidence"`        // "full" | "none"
	CallResolution string `json:"call_resolution"` // "full" | "shallow" | "none"
}

// CompletenessReport breaks down parse/index coverage by language and by emitted
// relation type so consumers can reason about which facts are dense or sparse.
type CompletenessReport struct {
	Languages map[string]LanguageCompleteness `json:"languages"`
	Relations map[string]int                  `json:"relations"`
}

type LanguageCompleteness struct {
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
}

type ProviderStats struct {
	Files             int    `json:"files"`
	ParsedFiles       int    `json:"parsed_files"`
	Symbols           int    `json:"symbols"`
	Relations         int    `json:"relations"`
	PartialFailures   int    `json:"partial_failures"`
	CompletenessLevel string `json:"completeness_level"`
}

type ProviderWarning struct {
	Code                 string `json:"code"`
	Severity             string `json:"severity"`
	FilePath             string `json:"file_path,omitempty"`
	EffectOnCompleteness string `json:"effect_on_semantic_completeness"`
	Detail               string `json:"detail,omitempty"`
}

type PartialFailure struct {
	Code                 string `json:"code"`
	Severity             string `json:"severity"`
	FilePath             string `json:"file_path,omitempty"`
	EffectOnCompleteness string `json:"effect_on_semantic_completeness"`
	Detail               string `json:"detail,omitempty"`
}

type FileRecord struct {
	RecordType string `json:"record_type"`
	ID         string `json:"id"`
	Path       string `json:"path"`
	Blob       string `json:"blob"`
	Language   string `json:"language,omitempty"`
	Bytes      int    `json:"bytes"`
}

type ExternalRecord struct {
	RecordType    string `json:"record_type"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Value         string `json:"value"`
	FilePath      string `json:"file_path,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	Signature     string `json:"signature,omitempty"`
	Language      string `json:"language,omitempty"`
	External      bool   `json:"external"`
	SourceSymbol  string `json:"source_symbol,omitempty"`
	SourceDetails string `json:"source_details,omitempty"`
}

type SymbolRecord struct {
	RecordType      string `json:"record_type"`
	ID              string `json:"id"`
	StableIDVersion string `json:"stable_id_version"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	QualifiedName   string `json:"qualified_name"`
	FilePath        string `json:"file_path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	Signature       string `json:"signature"`
	BodyHash        string `json:"body_hash"`
	Language        string `json:"language"`
	ContainerID     string `json:"container_id,omitempty"`
}

type RelationRecord struct {
	RecordType    string     `json:"record_type"`
	FromID        string     `json:"from_id"`
	ToID          string     `json:"to_id"`
	Type          string     `json:"type"`
	Confidence    float64    `json:"confidence"`
	Reason        string     `json:"reason"`
	RelationScope string     `json:"relation_scope,omitempty"`
	Resolution    string     `json:"resolution,omitempty"`
	TargetKind    string     `json:"target_kind,omitempty"`
	Evidence      []Evidence `json:"evidence,omitempty"`
	WarningCodes  []string   `json:"warning_codes"`
}

// Evidence is a compact pointer to the source location that justifies a
// relation, so consumers can show provenance without re-parsing.
type Evidence struct {
	Kind      string `json:"kind"`
	FilePath  string `json:"file_path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type CapabilityReport struct {
	SchemaVersion                   string              `json:"schema_version"`
	Provider                        string              `json:"provider"`
	SupportedFileExtensions         []string            `json:"supported_file_extensions"`
	SupportedLanguages              []string            `json:"supported_languages"`
	ParserVersions                  map[string]string   `json:"parser_versions"`
	SupportedRelationTypes          []string            `json:"supported_relation_types"`
	RelationSupportByLanguage       map[string][]string `json:"relation_support_by_language"`
	RelationSupportByProfile        map[string][]string `json:"relation_support_by_profile"`
	HeuristicRelationTypes          []string            `json:"heuristic_relation_types"`
	UnsupportedButDetectedLanguages []string            `json:"unsupported_but_detected_language_hints"`
	OptionalLocalOnlyFeatures       map[string]bool     `json:"optional_local_only_features"`
	FeaturesRequiringNetworkAccess  map[string]bool     `json:"features_requiring_network_access"`
}

type ProviderSnapshot struct {
	Header    SnapshotHeader
	Files     []FileRecord
	Externals []ExternalRecord
	Symbols   []SymbolRecord
	Relations []RelationRecord
}

type ProviderSnapshotOptions struct {
	NoNetwork    bool
	Worktree     bool
	IgnoreFiles  []string
	IncludeFiles []string
	// MaxParseBytes caps parser input per file. Zero uses the provider default;
	// negative disables the cap. Oversized files still emit file records and a
	// partial failure, but symbol parsing is skipped.
	MaxParseBytes int
	// Profile selects the indexing depth: full (all relations), fast (symbol
	// inventory, imports, shallow local calls, boundaries, IaC, no evidence), or
	// syntax-only (file/symbol inventory and structure only). Empty means full.
	Profile Profile
	// Progress, when non-nil, receives coarse local-only indexing telemetry.
	// Callbacks run synchronously and should return quickly.
	Progress func(ProgressEvent)
}

type ProgressEvent struct {
	Phase       string
	FilesTotal  int
	FilesDone   int
	Symbols     int
	Relations   int
	HeapAlloc   uint64
	MaxRSSBytes uint64
	Elapsed     time.Duration
}

// Profile names the indexing depth a snapshot is produced at.
type Profile string

const (
	ProfileFull       Profile = "full"
	ProfileFast       Profile = "fast"
	ProfileSyntaxOnly Profile = "syntax-only"
)

// profileSpec describes what a profile emits and how deeply it resolves.
type profileSpec struct {
	name            Profile
	relations       map[string]bool
	includeEvidence bool
	callResolution  string // "full" | "shallow" | "none"
}

func (s profileSpec) emits(relationType string) bool { return s.relations[relationType] }

// relationSet returns the sorted relation types this profile emits.
func (s profileSpec) relationSet() []string {
	out := make([]string, 0, len(s.relations))
	for t := range s.relations {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// skippedRelationFamilies returns the sorted relation types this profile omits.
func (s profileSpec) skippedRelationFamilies() []string {
	out := []string{}
	for _, t := range relationTypes {
		if !s.relations[t] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// profileNeedsPerFileScan reports whether any content-derived per-file relation
// family is enabled, so the streaming path can skip re-reading file content
// entirely for syntax-only profiles.
func profileNeedsPerFileScan(spec profileSpec) bool {
	for _, t := range []string{"IMPORTS", "CALLS", "HANDLES_ROUTE", "HTTP_CALLS", "EMITS", "LISTENS_ON", "EXTENDS", "IMPLEMENTS", "READS_FIELD", "WRITES_FIELD", "ACCESSES"} {
		if spec.relations[t] {
			return true
		}
	}
	return false
}

// relationSupportByProfile reports the relation types emitted at each indexing
// profile.
func relationSupportByProfile() map[string][]string {
	out := map[string][]string{}
	for _, p := range []Profile{ProfileFull, ProfileFast, ProfileSyntaxOnly} {
		out[string(p)] = resolveProfile(p).relationSet()
	}
	return out
}

func relationTypeSet(types ...string) map[string]bool {
	set := make(map[string]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	return set
}

// resolveProfile maps a profile name (empty = full) to its spec. Initial
// behavior is conservative: fast and syntax-only restrict the relation set and
// skip the expensive families rather than approximating them.
func resolveProfile(p Profile) profileSpec {
	switch p {
	case ProfileSyntaxOnly:
		return profileSpec{name: ProfileSyntaxOnly, relations: relationTypeSet("DEFINES", "CONTAINS"), includeEvidence: false, callResolution: "none"}
	case ProfileFast:
		return profileSpec{name: ProfileFast, relations: relationTypeSet("DEFINES", "CONTAINS", "IMPORTS", "CALLS", "HANDLES_ROUTE", "HANDLES_TOOL", "RESOURCE_DEPENDS_ON"), includeEvidence: false, callResolution: "shallow"}
	default:
		return profileSpec{name: ProfileFull, relations: relationTypeSet(relationTypes...), includeEvidence: true, callResolution: "full"}
	}
}

func Capabilities() CapabilityReport {
	extensions := make([]string, 0, len(treeSitterLanguages))
	languageSet := map[string]struct{}{}
	for extension, spec := range treeSitterLanguages {
		extensions = append(extensions, extension)
		languageSet[spec.language] = struct{}{}
	}
	sort.Strings(extensions)
	languages := make([]string, 0, len(languageSet))
	for language := range languageSet {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	return CapabilityReport{
		SchemaVersion:                   SchemaVersion,
		Provider:                        ProviderName,
		SupportedFileExtensions:         extensions,
		SupportedLanguages:              languages,
		UnsupportedButDetectedLanguages: []string{},
		ParserVersions:                  parserVersions(),
		SupportedRelationTypes:          append([]string(nil), relationTypes...),
		RelationSupportByLanguage:       relationSupportByLanguage(),
		RelationSupportByProfile:        relationSupportByProfile(),
		HeuristicRelationTypes:          []string{"HANDLES_ROUTE", "HTTP_CALLS", "EMITS", "LISTENS_ON", "HANDLES_TOOL", "SIMILAR_TO", "TESTS"},
		OptionalLocalOnlyFeatures: map[string]bool{
			"stable_symbol_ids":    true,
			"semantic_diff":        true,
			"ndjson_snapshot":      true,
			"near_clone_detection": true,
		},
		FeaturesRequiringNetworkAccess: map[string]bool{
			"grammar_download":  false,
			"hosted_models":     false,
			"remote_embeddings": false,
			"telemetry_upload":  false,
			"remote_code_fetch": false,
			"network_discovery": false,
		},
	}
}

// relationSupportByLanguage reports which relation types the provider can
// extract for each supported language. DEFINES, CONTAINS, and CALLS are
// produced structurally from parsed entities for every language; IMPORTS is
// added only where importsFor has a language-specific scanner. HANDLES_ROUTE
// and HANDLES_TOOL are reported separately in HeuristicRelationTypes because
// they are detected by file-path and body patterns rather than per-language
// grammar, so they are not attributed to individual languages here.
func relationSupportByLanguage() map[string][]string {
	importCapable := map[string]bool{}
	for ext, spec := range treeSitterLanguages {
		if importCapableExtension(ext) {
			importCapable[spec.language] = true
		}
	}
	support := map[string][]string{}
	for _, spec := range treeSitterLanguages {
		if _, done := support[spec.language]; done {
			continue
		}
		types := []string{"CALLS", "CONTAINS", "DEFINES"}
		if importCapable[spec.language] {
			types = append(types, "IMPORTS")
		}
		types = append(types, ooRelationSupport[spec.language]...)
		sort.Strings(types)
		support[spec.language] = types
	}
	return support
}

func BuildProviderSnapshot(ctx context.Context, repo, providerVersion string) (ProviderSnapshot, error) {
	return BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, ProviderSnapshotOptions{})
}

// contentReader returns a file's content on demand. The snapshot reads source
// per file rather than holding all file contents in memory at once.
type contentReader func(path string) (string, bool)

// sourceContext is the repository state needed to stream a snapshot: identity,
// the file list, a per-file content reader, and git-state warnings. It holds no
// file content itself.
type sourceContext struct {
	absRepo  string
	key      string
	commit   string
	tree     string
	paths    []string
	read     contentReader
	close    func() error
	warnings []ProviderWarning
}

// leanHeader is the streaming preamble emitted before any file is parsed. It
// carries only what is known up front; languages, warnings, partial failures,
// stats, and completeness are reported in the trailing SnapshotSummary because
// they require the full parse. BuildProviderSnapshot merges the two back into a
// complete in-memory header.
func leanHeader(sc sourceContext, providerVersion string, spec profileSpec) SnapshotHeader {
	return SnapshotHeader{
		SchemaVersion:    SchemaVersion,
		Provider:         ProviderName,
		ProviderVersion:  providerVersion,
		RepoRoot:         sc.absRepo,
		RepoKey:          sc.key,
		Commit:           sc.commit,
		Tree:             sc.tree,
		Languages:        []string{},
		Capabilities:     []string{"ndjson", "stable-symbol-id-v1", "local-only", "partial-failures"},
		SchemaFeatures:   append([]string(nil), schemaFeatures...),
		LanguageVersions: parserVersions(),
		Profile:          string(spec.name),
		ProfileLimits:    ProfileLimits{Evidence: evidenceLimit(spec), CallResolution: spec.callResolution},
		RelationSet:      spec.relationSet(),
		SkippedRelations: spec.skippedRelationFamilies(),
		Warnings:         []ProviderWarning{},
		PartialFailures:  []PartialFailure{},
	}
}

func evidenceLimit(spec profileSpec) string {
	if spec.includeEvidence {
		return "full"
	}
	return "none"
}

// BuildProviderSnapshotWithOptions is an accumulating sink over the streaming
// path: it collects every streamed record into an in-memory ProviderSnapshot
// and reconstructs the full header (merging the lean streamed header with the
// trailing summary), sorting relations for stable output. Intended for tests
// and small repositories; large repositories should consume StreamSnapshot.
func BuildProviderSnapshotWithOptions(ctx context.Context, repo, providerVersion string, options ProviderSnapshotOptions) (ProviderSnapshot, error) {
	var snapshot ProviderSnapshot
	var summary SnapshotSummary
	err := StreamSnapshot(ctx, repo, providerVersion, options, func(record any) error {
		switch r := record.(type) {
		case SnapshotHeader:
			snapshot.Header = r
		case FileRecord:
			snapshot.Files = append(snapshot.Files, r)
		case SymbolRecord:
			snapshot.Symbols = append(snapshot.Symbols, r)
		case ExternalRecord:
			snapshot.Externals = append(snapshot.Externals, r)
		case RelationRecord:
			snapshot.Relations = append(snapshot.Relations, r)
		case SnapshotSummary:
			summary = r
		}
		return nil
	})
	if err != nil {
		return ProviderSnapshot{}, err
	}
	snapshot.Header.Languages = summary.Languages
	snapshot.Header.Warnings = summary.Warnings
	snapshot.Header.PartialFailures = summary.PartialFailures
	snapshot.Header.Stats = summary.Stats
	snapshot.Header.Completeness = summary.Completeness
	sort.Slice(snapshot.Relations, func(i, j int) bool {
		left := snapshot.Relations[i].Type + snapshot.Relations[i].FromID + snapshot.Relations[i].ToID
		right := snapshot.Relations[j].Type + snapshot.Relations[j].FromID + snapshot.Relations[j].ToID
		return left < right
	})
	return snapshot, nil
}

// StreamSnapshot emits a snapshot as a stream of records with bounded memory.
// Phase 1 lists the files, then parses each one and emits its file and symbol
// records immediately while building compact metadata indexes — file contents
// are read per file and discarded, never held all at once. Phase 2 resolves
// relations against those indexes (re-reading content per file), emitting
// relation and external records. A trailing SnapshotSummary carries the totals
// (languages, warnings, partial failures, stats, completeness) that are only
// known once the whole repository has been processed.
//
// Memory is bounded by the symbol/index metadata (no source content) plus the
// relation dedup key set; it does not scale with held relation records or held
// file contents.
func StreamSnapshot(ctx context.Context, repo, providerVersion string, options ProviderSnapshotOptions, emit func(record any) error) error {
	sc, err := prepareSource(ctx, repo, options)
	if err != nil {
		return err
	}
	if sc.close != nil {
		defer sc.close()
	}
	spec := resolveProfile(options.Profile)
	maxParseBytes := options.MaxParseBytes
	if maxParseBytes == 0 {
		maxParseBytes = defaultMaxParseBytes
	}
	progressStart := time.Now()
	progressEvery := 1024
	emitProgress := func(phase string, filesDone int, symbols int, relations int) {
		if options.Progress == nil {
			return
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		options.Progress(ProgressEvent{
			Phase:       phase,
			FilesTotal:  len(sc.paths),
			FilesDone:   filesDone,
			Symbols:     symbols,
			Relations:   relations,
			HeapAlloc:   mem.HeapAlloc,
			MaxRSSBytes: maxRSSBytes(),
			Elapsed:     time.Since(progressStart),
		})
	}
	emitProgress("start", 0, 0, 0)
	if err := emit(leanHeader(sc, providerVersion, spec)); err != nil {
		return err
	}

	parser := TreeSitterParser{}
	languageSet := map[string]struct{}{}
	completenessLangs := map[string]LanguageCompleteness{}
	var failures []PartialFailure
	var files []FileRecord
	recordsByFile := map[string][]SymbolRecord{}
	structuralByFile := map[string][]structuralSymbol{}
	symbolCount := 0
	relationCount := 0
	parsedFileCount := 0

	// Phase 1: parse + emit file/symbol records, build indexes, discard content.
	for i, path := range sc.paths {
		if !Supported(path) {
			if hint := unsupportedLanguageHint(path); hint != "" {
				failures = append(failures, PartialFailure{
					Code:                 "E_UNSUPPORTED_LANGUAGE",
					Severity:             "warning",
					FilePath:             path,
					EffectOnCompleteness: "file omitted because no parser is available",
					Detail:               hint,
				})
			}
			continue
		}
		content, ok := sc.read(path)
		if !ok {
			failures = append(failures, PartialFailure{
				Code:                 "E_FILE_READ",
				Severity:             "error",
				FilePath:             path,
				EffectOnCompleteness: "file omitted from semantic snapshot",
				Detail:               "file listed but content was unavailable",
			})
			continue
		}
		contentBytes := []byte(content)
		langSpec, ok := languageForPath(path)
		if !ok {
			failures = append(failures, PartialFailure{
				Code:                 "E_UNSUPPORTED_LANGUAGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file omitted because no parser is available",
			})
			continue
		}
		language := langSpec.language
		file := FileRecord{
			RecordType: "file",
			ID:         fileID(sc.key, path),
			Path:       path,
			Blob:       contentHash(contentBytes),
			Language:   language,
			Bytes:      len(contentBytes),
		}
		if maxParseBytes > 0 && len(contentBytes) > maxParseBytes {
			languageSet[language] = struct{}{}
			if err := emit(file); err != nil {
				return err
			}
			files = append(files, file)
			lc := completenessLangs[language]
			lc.Files++
			completenessLangs[language] = lc
			failures = append(failures, PartialFailure{
				Code:                 "E_FILE_TOO_LARGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file record emitted but symbol parsing skipped",
				Detail:               fmt.Sprintf("file is %d bytes, above max parser input %d bytes", len(contentBytes), maxParseBytes),
			})
			continue
		}
		entities, language, parseStatus := parser.ParseWithStatus(path, content)
		if language == "" {
			failures = append(failures, PartialFailure{
				Code:                 "E_UNSUPPORTED_LANGUAGE",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file omitted because no parser is available",
			})
			continue
		}
		if parseStatus.ParseError {
			failures = append(failures, PartialFailure{
				Code:                 "E_PARSE_ERROR",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file parsed with syntax errors; semantic facts may be incomplete",
				Detail:               parseStatus.Detail,
			})
		}
		languageSet[language] = struct{}{}
		if err := emit(file); err != nil {
			return err
		}
		files = append(files, file)
		parsedFileCount++
		fileSymbols := entitySymbols(sc.key, path, language, entities)
		fileSymbols = append(fileSymbols, syntheticBoundarySymbols(sc.key, path, language, content, fileSymbols)...)
		for _, symbol := range fileSymbols {
			if err := emit(symbol); err != nil {
				return err
			}
		}
		if spec.name == ProfileSyntaxOnly {
			structuralByFile[path] = compactStructuralSymbols(fileSymbols)
		} else {
			recordsByFile[path] = retainedSymbolsForProfile(fileSymbols, spec)
		}
		symbolCount += len(fileSymbols)
		lc := completenessLangs[language]
		lc.Files++
		lc.Symbols += len(fileSymbols)
		completenessLangs[language] = lc
		if (i+1)%progressEvery == 0 {
			emitProgress("parse", i+1, symbolCount, relationCount)
		}
	}
	emitProgress("parse", len(sc.paths), symbolCount, relationCount)

	// Phase 2: resolve relations from indexes, re-reading content per file.
	// Relation dedup uses compact 64-bit hashed keys rather than the full
	// from+to+type string, so the set's memory is ~one machine word per unique
	// relation instead of a ~100-byte key. The set is bounded by the unique
	// relation count (== the relations reported in the summary). FNV-1a/64
	// collisions across realistic relation counts are negligible.
	seenRelation := map[uint64]struct{}{}
	externalsByID := map[string]ExternalRecord{}
	relationsByType := map[string]int{}
	var emitErr error
	emitRelation := func(r RelationRecord, symbolsByID map[string]SymbolRecord, filesByID map[string]FileRecord) {
		if emitErr != nil {
			return
		}
		// Profile filter: emit only relation families the profile includes; in
		// shallow call resolution, keep only exact (same-file) calls; drop
		// evidence when the profile omits it.
		if !spec.emits(r.Type) {
			return
		}
		if r.Type == "CALLS" && spec.callResolution == "shallow" && r.Resolution != "exact" {
			return
		}
		if !spec.includeEvidence {
			r.Evidence = nil
		}
		dedupKey := relationDedupKey(r)
		if _, seen := seenRelation[dedupKey]; seen {
			return
		}
		seenRelation[dedupKey] = struct{}{}
		for _, id := range []string{r.FromID, r.ToID} {
			if strings.HasPrefix(id, "external:") {
				mergeExternalRecord(externalsByID, externalRecordFor(r, id, symbolsByID, filesByID))
			}
		}
		relationsByType[r.Type]++
		relationCount++
		if relationCount%progressEvery == 0 {
			emitProgress("relations", len(sc.paths), symbolCount, relationCount)
		}
		emitErr = emit(r)
	}
	if spec.name == ProfileSyntaxOnly {
		emitStructuralRelationsCompact(sc.key, files, structuralByFile, func(r RelationRecord) {
			emitRelation(r, nil, nil)
		})
	} else {
		symbolsByID, filesByID := recordIndexes(files, recordsByFile)
		forEachRelation(sc.key, files, recordsByFile, sc.read, spec, func(r RelationRecord) {
			emitRelation(r, symbolsByID, filesByID)
		})
	}
	if emitErr != nil {
		return emitErr
	}
	emitProgress("relations", len(sc.paths), symbolCount, relationCount)

	externalIDs := make([]string, 0, len(externalsByID))
	for id := range externalsByID {
		externalIDs = append(externalIDs, id)
	}
	sort.Strings(externalIDs)
	for _, id := range externalIDs {
		if err := emit(externalsByID[id]); err != nil {
			return err
		}
	}
	emitProgress("summary", len(sc.paths), symbolCount, relationCount)

	warnings := sc.warnings
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	if failures == nil {
		failures = []PartialFailure{}
	}
	return emit(SnapshotSummary{
		RecordType:      "summary",
		Languages:       sortedKeys(languageSet),
		Warnings:        warnings,
		PartialFailures: failures,
		Stats: ProviderStats{
			Files:             len(files),
			ParsedFiles:       parsedFileCount,
			Symbols:           symbolCount,
			Relations:         relationCount,
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(len(failures), len(files)),
		},
		Completeness: CompletenessReport{Languages: completenessLangs, Relations: relationsByType},
	})
}

// relationDedupKey hashes a relation's identity (from, to, type) to a compact
// 64-bit key for the streaming dedup set, keeping that set's memory small on
// large repositories.
func relationDedupKey(r RelationRecord) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(r.FromID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(r.ToID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(r.Type))
	return h.Sum64()
}

// SnapshotSummary is the trailing record of a streamed snapshot. It carries the
// totals known only after the whole repository is processed; a streaming
// consumer reads it for languages, warnings, partial failures, stats, and the
// completeness breakdown (the leading header leaves these empty).
type SnapshotSummary struct {
	RecordType      string             `json:"record_type"`
	Languages       []string           `json:"languages"`
	Warnings        []ProviderWarning  `json:"warnings"`
	PartialFailures []PartialFailure   `json:"partial_failures"`
	Stats           ProviderStats      `json:"stats"`
	Completeness    CompletenessReport `json:"completeness"`
}

// prepareSource resolves repository identity, lists the source files, and
// builds a per-file content reader without loading any content.
func prepareSource(ctx context.Context, repo string, options ProviderSnapshotOptions) (sourceContext, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return sourceContext{}, err
	}
	key := repoKey(ctx, absRepo)
	commit, commitErr := gitutil.RevParse(ctx, absRepo, "HEAD")
	tree, treeErr := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")

	// The provider is local-only. NoNetwork is accepted to make that contract
	// explicit for callers that enforce no-egress provider execution.
	_ = options.NoNetwork
	useHead := !options.Worktree && commitErr == nil && treeErr == nil
	paths, read, closeSource, err := openSource(ctx, absRepo, useHead, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return sourceContext{}, err
	}

	var warnings []ProviderWarning
	if options.Worktree {
		warnings = append(warnings, ProviderWarning{
			Code:                 "W_WORKTREE_SNAPSHOT",
			Severity:             "warning",
			EffectOnCompleteness: "snapshot records are read from the working tree because --worktree was requested",
		})
	} else if commitErr != nil || treeErr != nil {
		warnings = append(warnings, ProviderWarning{
			Code:                 "E_NO_GIT_HEAD",
			Severity:             "warning",
			EffectOnCompleteness: "snapshot records are read from the working tree because no HEAD tree is available",
			Detail:               firstError(commitErr, treeErr).Error(),
		})
	}
	return sourceContext{
		absRepo:  absRepo,
		key:      key,
		commit:   commit,
		tree:     tree,
		paths:    paths,
		read:     read,
		close:    closeSource,
		warnings: warnings,
	}, nil
}

func WriteSnapshotNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	if err := writeJSONLine(out, snapshot.Header); err != nil {
		return err
	}
	for _, record := range snapshot.Files {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Externals {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Symbols {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Relations {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func WriteSymbolsNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	if err := writeJSONLine(out, snapshot.Header); err != nil {
		return err
	}
	for _, record := range snapshot.Symbols {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func WriteRelationsNDJSON(out io.Writer, snapshot ProviderSnapshot) error {
	if err := writeJSONLine(out, snapshot.Header); err != nil {
		return err
	}
	for _, record := range snapshot.Relations {
		if err := writeJSONLine(out, record); err != nil {
			return err
		}
	}
	return nil
}

func entitySymbols(repoKey, path, language string, entities []Entity) []SymbolRecord {
	byName := map[string]string{}
	baseCounts := map[string]int{}
	sigOrdinals := map[string]int{}
	for _, entity := range entities {
		baseCounts[symbolID(repoKey, language, path, entity.Kind, entity.Name)]++
	}
	var symbols []SymbolRecord
	for _, entity := range entities {
		qualified := entity.Name
		id := symbolID(repoKey, language, path, entity.Kind, qualified)
		if baseCounts[id] > 1 {
			// Disambiguate same-name symbols by signature hash plus an ordinal
			// within the matching-signature group. This is stable across edits
			// that shift line numbers, unlike the previous line-range scheme;
			// overloads with distinct signatures get distinct, stable IDs, and
			// genuine duplicates fall back to a stable definition ordinal.
			signatureHash := hash(entity.Signature)
			ordinalKey := id + "\x00" + signatureHash
			sigOrdinals[ordinalKey]++
			id = fmt.Sprintf("%s#sig:%s", id, signatureHash)
			if ordinal := sigOrdinals[ordinalKey]; ordinal > 1 {
				id = fmt.Sprintf("%s#%d", id, ordinal)
			}
		}
		containerID := ""
		if containerName := containerName(qualified); containerName != "" {
			if parentID, ok := byName[containerName]; ok {
				containerID = parentID
			}
		}
		symbol := SymbolRecord{
			RecordType:      "symbol",
			ID:              id,
			StableIDVersion: StableSymbolIDVersion,
			Kind:            entity.Kind,
			Name:            shortEntityName(entity.Name),
			QualifiedName:   qualified,
			FilePath:        path,
			StartLine:       entity.StartLine,
			EndLine:         entity.EndLine,
			Signature:       entity.Signature,
			BodyHash:        entity.BodyHash,
			Language:        language,
			ContainerID:     containerID,
		}
		symbols = append(symbols, symbol)
		byName[qualified] = id
	}
	return symbols
}

func syntheticBoundarySymbols(repoKey, path, language, content string, fileSymbols []SymbolRecord) []SymbolRecord {
	var symbols []SymbolRecord
	lines := strings.Split(content, "\n")
	if route := nextRouteBoundary(path); route != "" {
		source := routeBoundarySource(path, language, fileSymbols)
		symbols = append(symbols, SymbolRecord{
			RecordType:      "symbol",
			ID:              symbolID(repoKey, language, path, "route", route),
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "route",
			Name:            route,
			QualifiedName:   route,
			FilePath:        path,
			StartLine:       source.StartLine,
			EndLine:         source.EndLine,
			Signature:       "route " + route,
			BodyHash:        hash(normalize(content)),
			Language:        language,
			ContainerID:     source.ContainerID,
		})
	}
	for _, source := range fileSymbols {
		block := symbolBlockFromLines(lines, source)
		if !looksLikeToolHandler(source, block) {
			continue
		}
		symbols = append(symbols, SymbolRecord{
			RecordType:      "symbol",
			ID:              symbolID(repoKey, language, path, "tool", source.QualifiedName),
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "tool",
			Name:            source.Name,
			QualifiedName:   source.QualifiedName,
			FilePath:        path,
			StartLine:       source.StartLine,
			EndLine:         source.EndLine,
			Signature:       "tool " + source.QualifiedName,
			BodyHash:        source.BodyHash,
			Language:        language,
			ContainerID:     source.ID,
		})
	}
	if workflow := applicationWorkflowBoundary(path); workflow != "" {
		source := routeBoundarySource(path, language, fileSymbols)
		symbols = append(symbols, SymbolRecord{
			RecordType:      "symbol",
			ID:              symbolID(repoKey, language, path, "workflow", workflow),
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "workflow",
			Name:            workflow,
			QualifiedName:   workflow,
			FilePath:        path,
			StartLine:       source.StartLine,
			EndLine:         source.EndLine,
			Signature:       "workflow " + workflow,
			BodyHash:        hash(normalize(content)),
			Language:        language,
			ContainerID:     source.ContainerID,
		})
	}
	return symbols
}

type routeSource struct {
	StartLine   int
	EndLine     int
	ContainerID string
}

type resolvedCallTarget struct {
	SymbolRecord
	Confidence float64
	Reason     string
	Resolution string
	Scope      string
}

func routeBoundarySource(path, language string, fileSymbols []SymbolRecord) routeSource {
	file := FileRecord{Path: path, Language: language}
	sourceID := routeBoundarySourceID("", file, fileSymbols)
	for _, symbol := range fileSymbols {
		if symbol.ID == sourceID {
			return routeSource{StartLine: symbol.StartLine, EndLine: symbol.EndLine, ContainerID: symbol.ID}
		}
	}
	return routeSource{StartLine: 1, EndLine: 1}
}

func resolveCallTargets(name string, from SymbolRecord, candidates, sameFile []SymbolRecord, importsByName map[string][]string) []resolvedCallTarget {
	var local []resolvedCallTarget
	for _, to := range sameFile {
		if to.ID == from.ID || to.Name != name {
			continue
		}
		local = append(local, resolvedCallTarget{
			SymbolRecord: to,
			Confidence:   0.92,
			Reason:       "direct call expression resolved to same-file symbol",
			Resolution:   "exact",
			Scope:        "file",
		})
	}
	if len(local) > 0 {
		return local
	}

	var imported []resolvedCallTarget
	for _, to := range candidates {
		if to.ID == from.ID {
			continue
		}
		if importedNameMatchesFile(importsByName[name], to.FilePath) {
			imported = append(imported, resolvedCallTarget{
				SymbolRecord: to,
				Confidence:   0.86,
				Reason:       "direct call expression resolved through import path",
				Resolution:   "import_resolved",
				Scope:        "module",
			})
		}
	}
	if len(imported) > 0 {
		return imported
	}

	var remaining []SymbolRecord
	for _, to := range candidates {
		if to.ID != from.ID {
			remaining = append(remaining, to)
		}
	}
	if len(remaining) == 1 {
		return []resolvedCallTarget{{
			SymbolRecord: remaining[0],
			Confidence:   0.68,
			Reason:       "direct call expression matched globally unique symbol name",
			Resolution:   "name_only",
			Scope:        "workspace",
		}}
	}
	return nil
}

// buildRelations collects every relation, deduplicates (first occurrence wins,
// in emission order), and sorts for stable output. Used by the in-memory
// snapshot path; the streaming path uses forEachRelation directly.
func buildRelations(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	forEachRelation(repoKey, files, recordsByFile, readContent, resolveProfile(ProfileFull), func(r RelationRecord) {
		relations = append(relations, r)
	})
	relations = dedupeRelations(relations)
	sort.Slice(relations, func(i, j int) bool {
		left := relations[i].Type + relations[i].FromID + relations[i].ToID
		right := relations[j].Type + relations[j].FromID + relations[j].ToID
		return left < right
	})
	return relations
}

type structuralSymbol struct {
	ID          string
	FilePath    string
	ContainerID string
}

func compactStructuralSymbols(records []SymbolRecord) []structuralSymbol {
	out := make([]structuralSymbol, 0, len(records))
	for _, record := range records {
		out = append(out, structuralSymbol{
			ID:          record.ID,
			FilePath:    record.FilePath,
			ContainerID: record.ContainerID,
		})
	}
	return out
}

func retainedSymbolsForProfile(records []SymbolRecord, spec profileSpec) []SymbolRecord {
	if spec.name == ProfileFull {
		return records
	}
	out := make([]SymbolRecord, 0, len(records))
	for _, record := range records {
		record.StableIDVersion = ""
		record.Signature = ""
		record.BodyHash = ""
		out = append(out, record)
	}
	return out
}

func emitStructuralRelations(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, emit func(RelationRecord)) {
	for _, file := range files {
		for _, symbol := range recordsByFile[file.Path] {
			emit(RelationRecord{
				RecordType:    "relation",
				FromID:        fileID(repoKey, symbol.FilePath),
				ToID:          symbol.ID,
				Type:          "DEFINES",
				Confidence:    1,
				Reason:        "symbol parsed from file",
				RelationScope: "file",
				Resolution:    "exact",
				TargetKind:    "symbol",
				WarningCodes:  []string{},
			})
			if symbol.ContainerID != "" {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ContainerID,
					ToID:          symbol.ID,
					Type:          "CONTAINS",
					Confidence:    1,
					Reason:        "symbol qualified name is nested in container",
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					WarningCodes:  []string{},
				})
			}
		}
	}
}

func emitStructuralRelationsCompact(repoKey string, files []FileRecord, recordsByFile map[string][]structuralSymbol, emit func(RelationRecord)) {
	for _, file := range files {
		for _, symbol := range recordsByFile[file.Path] {
			emit(RelationRecord{
				RecordType:    "relation",
				FromID:        fileID(repoKey, symbol.FilePath),
				ToID:          symbol.ID,
				Type:          "DEFINES",
				Confidence:    1,
				Reason:        "symbol parsed from file",
				RelationScope: "file",
				Resolution:    "exact",
				TargetKind:    "symbol",
				WarningCodes:  []string{},
			})
			if symbol.ContainerID != "" {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ContainerID,
					ToID:          symbol.ID,
					Type:          "CONTAINS",
					Confidence:    1,
					Reason:        "symbol qualified name is nested in container",
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					WarningCodes:  []string{},
				})
			}
		}
	}
}

// forEachRelation drives all relation extraction, passing each relation to emit
// as it is produced. It never accumulates the full relation set, so a streaming
// caller can write records out with bounded memory. Callers deduplicate:
// buildRelations collects-then-dedupes; the streaming path dedupes on emit.
func forEachRelation(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, spec profileSpec, emit func(RelationRecord)) {
	if spec.name == ProfileSyntaxOnly {
		emitStructuralRelations(repoKey, files, recordsByFile, emit)
		return
	}
	needsTool := spec.emits("HANDLES_TOOL")
	needsCallScan := spec.emits("CALLS") && spec.callResolution != "none"
	needsReceiverCalls := spec.emits("CALLS") && spec.callResolution == "full"
	needsFields := spec.emits("READS_FIELD") || spec.emits("WRITES_FIELD") || spec.emits("ACCESSES")
	needsTypes := spec.emits("EXTENDS") || spec.emits("IMPLEMENTS") || spec.emits("OVERRIDES")
	needsOverrides := spec.emits("OVERRIDES")
	symbolsByShortName := map[string][]SymbolRecord{}
	symbolsByFile := map[string][]SymbolRecord{}
	childNamesByContainer := map[string]map[string]bool{}
	methodsByContainer := map[string]map[string]SymbolRecord{}
	fieldsByContainer := map[string]map[string]SymbolRecord{}
	var inheritanceEdges []RelationRecord // captured for OVERRIDES derivation
	// Iterate files in their (stable) slice order, not the recordsByFile map, so
	// structural relations stream deterministically.
	for _, file := range files {
		records := recordsByFile[file.Path]
		for _, symbol := range records {
			emit(RelationRecord{
				RecordType:    "relation",
				FromID:        fileID(repoKey, symbol.FilePath),
				ToID:          symbol.ID,
				Type:          "DEFINES",
				Confidence:    1,
				Reason:        "symbol parsed from file",
				RelationScope: "file",
				Resolution:    "exact",
				TargetKind:    "symbol",
				WarningCodes:  []string{},
			})
			if symbol.ContainerID != "" {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ContainerID,
					ToID:          symbol.ID,
					Type:          "CONTAINS",
					Confidence:    1,
					Reason:        "symbol qualified name is nested in container",
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					WarningCodes:  []string{},
				})
			}
			if needsTool && symbol.Kind == "tool" && symbol.ContainerID != "" {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ContainerID,
					ToID:          symbol.ID,
					Type:          "HANDLES_TOOL",
					Confidence:    0.85,
					Reason:        "tool boundary inferred from handler symbol body",
					RelationScope: "file",
					Resolution:    "pattern",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "symbol_body",
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    symbol.QualifiedName,
					}},
					WarningCodes: []string{},
				})
			}
			symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
			symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
			if symbol.ContainerID != "" && (needsCallScan || needsReceiverCalls || needsFields || needsOverrides) {
				if childNamesByContainer[symbol.ContainerID] == nil {
					childNamesByContainer[symbol.ContainerID] = map[string]bool{}
				}
				childNamesByContainer[symbol.ContainerID][symbol.Name] = true
				if (needsReceiverCalls || needsOverrides) && symbol.Kind == "method" {
					if methodsByContainer[symbol.ContainerID] == nil {
						methodsByContainer[symbol.ContainerID] = map[string]SymbolRecord{}
					}
					methodsByContainer[symbol.ContainerID][symbol.Name] = symbol
				}
				if needsFields && symbol.Kind == "field" {
					if fieldsByContainer[symbol.ContainerID] == nil {
						fieldsByContainer[symbol.ContainerID] = map[string]SymbolRecord{}
					}
					fieldsByContainer[symbol.ContainerID][symbol.Name] = symbol
				}
			}
		}
	}
	handledRoutes := map[string]struct{}{}
	knownFiles := map[string]bool{}
	for _, file := range files {
		knownFiles[file.Path] = true
		if route := nextRouteBoundary(file.Path); route != "" {
			handledRoutes[route] = struct{}{}
		}
	}

	for _, file := range files {
		if !profileNeedsPerFileScan(spec) {
			break // syntax-only: no content-derived relations
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")
		fromID := fileID(repoKey, file.Path)
		for _, imported := range importsFor(file.Path, content) {
			if resolved, ok := resolveLocalImport(file.Path, imported, knownFiles); ok {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        fromID,
					ToID:          fileID(repoKey, resolved),
					Type:          "IMPORTS",
					Confidence:    0.95,
					Reason:        "relative import resolved to local file",
					RelationScope: "module",
					Resolution:    "import_resolved",
					TargetKind:    "file",
					Evidence: []Evidence{{
						Kind:     "import_statement",
						FilePath: file.Path,
						Detail:   imported,
					}},
					WarningCodes: []string{},
				})
				continue
			}
			warningCodes := []string{}
			if isRelativeImportSpec(file.Path, imported) {
				warningCodes = []string{"UNRESOLVED_RELATIVE_IMPORT"}
			}
			emit(RelationRecord{
				RecordType:    "relation",
				FromID:        fromID,
				ToID:          externalID("import", imported),
				Type:          "IMPORTS",
				Confidence:    0.8,
				Reason:        "import declaration matched by language-specific scanner",
				RelationScope: "external",
				Resolution:    "name_only",
				TargetKind:    "external",
				Evidence: []Evidence{{
					Kind:     "import_statement",
					FilePath: file.Path,
					Detail:   imported,
				}},
				WarningCodes: warningCodes,
			})
		}
		importsByName := importedNamesFor(file.Path, content)
		for _, from := range recordsByFile[file.Path] {
			block := symbolBlockFromLines(lines, from)
			for _, name := range sortedKeysOf(callLikeIdentifiers(block)) {
				if name == from.Name {
					continue
				}
				// A container's block spans its members' definition lines, which
				// look like calls (e.g. `def validate(self):`). Skip the names of
				// direct children so a class is not credited with calling its own
				// methods; the real call site lives in the calling function.
				if childNamesByContainer[from.ID][name] {
					continue
				}
				for _, to := range resolveCallTargets(name, from, symbolsByShortName[name], symbolsByFile[file.Path], importsByName) {
					emit(RelationRecord{
						RecordType:    "relation",
						FromID:        from.ID,
						ToID:          to.ID,
						Type:          "CALLS",
						Confidence:    to.Confidence,
						Reason:        to.Reason,
						RelationScope: to.Scope,
						Resolution:    to.Resolution,
						TargetKind:    "symbol",
						Evidence: []Evidence{{
							Kind:      "call_site",
							FilePath:  from.FilePath,
							StartLine: from.StartLine,
							EndLine:   from.EndLine,
							Detail:    name,
						}},
						WarningCodes: []string{},
					})
				}
			}
			for _, route := range routeLiterals(block) {
				if _, ok := handledRoutes[route]; ok {
					continue
				}
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        from.ID,
					ToID:          externalID("route", route),
					Type:          "HANDLES_ROUTE",
					Confidence:    0.7,
					Reason:        "route-like string literal found inside handler symbol",
					RelationScope: "external",
					Resolution:    "pattern",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      "route_literal",
						FilePath:  from.FilePath,
						StartLine: from.StartLine,
						EndLine:   from.EndLine,
						Detail:    route,
					}},
					WarningCodes: []string{},
				})
			}
			httpAndChannels := spec.emits("HTTP_CALLS") || spec.emits("EMITS") || spec.emits("LISTENS_ON")
			for _, call := range httpCalls(block) {
				if !httpAndChannels {
					break
				}
				confidence := 0.7
				if call.Absolute {
					confidence = 0.6 // host ignored; cross-service path match is weaker
				}
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        from.ID,
					ToID:          externalID("route", call.Path),
					Type:          "HTTP_CALLS",
					Confidence:    confidence,
					Reason:        "outbound HTTP client call to " + call.Method + " " + call.Path,
					RelationScope: "external",
					Resolution:    "pattern",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      "http_call_site",
						FilePath:  from.FilePath,
						StartLine: from.StartLine,
						EndLine:   from.EndLine,
						Detail:    call.Method + " " + call.Path,
					}},
					WarningCodes: []string{},
				})
			}
			for _, event := range channelEvents(block) {
				if !httpAndChannels {
					break
				}
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        from.ID,
					ToID:          externalID("channel", event.Name),
					Type:          event.Relation,
					Confidence:    0.6,
					Reason:        "event/channel name detected by emit/listen pattern",
					RelationScope: "external",
					Resolution:    "pattern",
					TargetKind:    "channel",
					Evidence: []Evidence{{
						Kind:      "channel_call_site",
						FilePath:  from.FilePath,
						StartLine: from.StartLine,
						EndLine:   from.EndLine,
						Detail:    event.Name,
					}},
					WarningCodes: []string{"WEAK_PATTERN"},
				})
			}
			if spec.callResolution == "full" {
				for _, r := range receiverCallRelations(from, block, methodsByContainer, symbolsByShortName) {
					emit(r)
				}
			}
			if needsFields {
				for _, r := range fieldAccessRelations(from, block, fieldsByContainer, symbolsByShortName) {
					emit(r)
				}
			}
		}

		if needsTypes {
			for _, r := range typeRelationsForFile(repoKey, file, content, recordsByFile[file.Path], symbolsByFile[file.Path], symbolsByShortName) {
				if r.Type == "EXTENDS" || r.Type == "IMPLEMENTS" {
					inheritanceEdges = append(inheritanceEdges, r)
				}
				emit(r)
			}
		}
	}

	if needsOverrides {
		for _, r := range overrideRelations(inheritanceEdges, methodsByContainer) {
			emit(r)
		}
	}
	if spec.emits("USES_TYPE") {
		for _, r := range usesTypeRelations(recordsByFile, symbolsByFile, symbolsByShortName) {
			emit(r)
		}
	}
	if spec.emits("TESTS") {
		for _, r := range testRelations(recordsByFile, symbolsByShortName) {
			emit(r)
		}
	}
	if spec.emits("RESOURCE_DEPENDS_ON") {
		for _, r := range resourceDependsOnRelations(recordsByFile, readContent) {
			emit(r)
		}
	}
	if spec.emits("SIMILAR_TO") {
		for _, r := range similarityRelations(recordsByFile, readContent) {
			emit(r)
		}
	}
}

// typeRelationsForFile emits EXTENDS/IMPLEMENTS relations for the type symbols
// in a file. Most languages declare supertypes in the class/interface header,
// which the parser captured in the symbol signature; Rust states them in impl
// blocks and supertrait bounds, scanned from content. Each supertype is
// resolved to a local type symbol when possible, otherwise to an external type
// endpoint.
func typeRelationsForFile(repoKey string, file FileRecord, content string, fileSymbols, sameFileSymbols []SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	var relations []RelationRecord
	for _, symbol := range fileSymbols {
		if !typeLikeKind(symbol.Kind) {
			continue
		}
		for _, edge := range supertypesFromSignature(symbol.Language, symbol.Signature) {
			relations = append(relations, buildTypeRelation(repoKey, symbol, edge.Super, edge.Relation, edge.Confidence, sameFileSymbols, symbolsByShortName))
		}
	}
	if file.Language == "Rust" {
		for _, edge := range rustSupertypeEdges(content) {
			anchor, ok := firstTypeLikeNamed(fileSymbols, edge.Anchor)
			if !ok {
				continue
			}
			relations = append(relations, buildTypeRelation(repoKey, anchor, edge.Super, edge.Relation, edge.Confidence, sameFileSymbols, symbolsByShortName))
		}
	}
	return relations
}

func buildTypeRelation(repoKey string, anchor SymbolRecord, super, relation string, baseConfidence float64, sameFileSymbols []SymbolRecord, symbolsByShortName map[string][]SymbolRecord) RelationRecord {
	toID := externalID("type", super)
	targetKind, resolution, scope := "external", "name_only", "external"
	confidence := minFloat(baseConfidence, 0.8)
	if sym, ok := firstTypeLikeNamed(sameFileSymbols, super); ok && sym.ID != anchor.ID {
		toID, targetKind, resolution, scope, confidence = sym.ID, "symbol", "exact", "file", baseConfidence
	} else if sym, ok := firstTypeLikeNamed(symbolsByShortName[super], super); ok && sym.ID != anchor.ID {
		toID, targetKind, resolution, scope, confidence = sym.ID, "symbol", "name_only", "module", minFloat(baseConfidence, 0.85)
	}
	return RelationRecord{
		RecordType:    "relation",
		FromID:        anchor.ID,
		ToID:          toID,
		Type:          relation,
		Confidence:    confidence,
		Reason:        typeRelationReason(relation, resolution),
		RelationScope: scope,
		Resolution:    resolution,
		TargetKind:    targetKind,
		Evidence: []Evidence{{
			Kind:      "type_declaration",
			FilePath:  anchor.FilePath,
			StartLine: anchor.StartLine,
			EndLine:   anchor.EndLine,
			Detail:    super,
		}},
		WarningCodes: []string{},
	}
}

// receiverCallRelations resolves `receiver.method()` calls inside a caller's
// body to the target method symbol by inferring the receiver's type. Two cases
// are handled: a this/self receiver resolves to the caller's enclosing type,
// and a local variable resolves through a constructor assignment in the same
// body. These calls are otherwise dropped (a name preceded by '.'/'->' is not a
// plain call), so this is purely additive. Type containers are skipped as
// callers — calls live in methods/functions, not in the class declaration.
func receiverCallRelations(from SymbolRecord, block string, methodsByContainer map[string]map[string]SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	if typeLikeKind(from.Kind) {
		return nil
	}
	calls := receiverCalls(block)
	if len(calls) == 0 {
		return nil
	}
	varTypes := localVarTypes(block)
	var relations []RelationRecord
	for _, call := range calls {
		var targetID string
		confidence := 0.85
		reason := "method call resolved via inferred receiver type"
		switch call.Receiver {
		case "this", "self":
			if from.ContainerID == "" {
				continue
			}
			targetID = from.ContainerID
			confidence = 0.9
			reason = "method call on this/self resolved to the enclosing type"
		default:
			typeName, ok := varTypes[call.Receiver]
			if !ok {
				continue
			}
			sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
			if !ok {
				continue
			}
			targetID = sym.ID
		}
		method, ok := methodsByContainer[targetID][call.Method]
		if !ok || method.ID == from.ID {
			continue
		}
		scope := "file"
		if method.FilePath != from.FilePath {
			scope = "module"
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from.ID,
			ToID:          method.ID,
			Type:          "CALLS",
			Confidence:    confidence,
			Reason:        reason,
			RelationScope: scope,
			Resolution:    "type_inferred",
			TargetKind:    "symbol",
			Evidence: []Evidence{{
				Kind:      "call_site",
				FilePath:  from.FilePath,
				StartLine: from.StartLine,
				EndLine:   from.EndLine,
				Detail:    call.Receiver + "." + call.Method,
			}},
			WarningCodes: []string{},
		})
	}
	return relations
}

// resourceDependsOnRelations builds the Terraform/HCL resource graph: a
// resource or module block that references another block (e.g. aws_vpc.main.id,
// module.network.id) emits RESOURCE_DEPENDS_ON to that block. Block symbols are
// indexed by their referenceable name (the form used inside expressions).
func resourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	index := map[string]SymbolRecord{}
	for _, symbols := range recordsByFile {
		for _, symbol := range symbols {
			if symbol.Language == "HCL" && symbol.Kind == "block" {
				index[hclReferenceableName(symbol.QualifiedName)] = symbol
			}
		}
	}
	if len(index) == 0 {
		return nil
	}

	paths := make([]string, 0, len(recordsByFile))
	for path := range recordsByFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var relations []RelationRecord
	for _, path := range paths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, symbol := range recordsByFile[path] {
			if symbol.Language != "HCL" || symbol.Kind != "block" {
				continue
			}
			body := symbolBlockFromLines(lines, symbol)
			emitted := map[string]bool{}
			for _, ref := range hclReferences(body) {
				target, ok := lookupHCLReference(index, ref)
				if !ok || target.ID == symbol.ID || emitted[target.ID] {
					continue
				}
				emitted[target.ID] = true
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          target.ID,
					Type:          "RESOURCE_DEPENDS_ON",
					Confidence:    0.85,
					Reason:        "block references another block",
					RelationScope: "module",
					Resolution:    "exact",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "hcl_reference",
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    ref,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

// hclReferenceableName maps a block symbol name to the form used to reference it
// in expressions: resource.<t>.<n> -> <t>.<n>, variable.<n> -> var.<n>; module,
// data, output, local blocks are referenced by their own name.
func hclReferenceableName(name string) string {
	switch {
	case strings.HasPrefix(name, "resource."):
		return strings.TrimPrefix(name, "resource.")
	case strings.HasPrefix(name, "variable."):
		return "var." + strings.TrimPrefix(name, "variable.")
	default:
		return name
	}
}

// lookupHCLReference matches a dotted reference token against the index, trying
// the longest prefix first (aws_vpc.main.id -> aws_vpc.main).
func lookupHCLReference(index map[string]SymbolRecord, ref string) (SymbolRecord, bool) {
	parts := strings.Split(ref, ".")
	for n := len(parts); n >= 2; n-- {
		if sym, ok := index[strings.Join(parts[:n], ".")]; ok {
			return sym, true
		}
	}
	return SymbolRecord{}, false
}

// testRelations links a test function to the unit it covers, using the test
// naming convention (TestFoo -> Foo, test_foo -> foo, FooTest -> Foo). The
// subject must resolve to a non-test function/method/type symbol. This is a
// high-precision convention match, not call-graph analysis.
func testRelations(recordsByFile map[string][]SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	paths := make([]string, 0, len(recordsByFile))
	for path := range recordsByFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var relations []RelationRecord
	for _, path := range paths {
		for _, symbol := range recordsByFile[path] {
			if symbol.Kind != "function" && symbol.Kind != "method" {
				continue
			}
			subject := testSubjectName(symbol.Name)
			if subject == "" {
				continue
			}
			target, ok := firstTestSubject(symbolsByShortName[subject], symbol.ID)
			if !ok {
				continue
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        symbol.ID,
				ToID:          target.ID,
				Type:          "TESTS",
				Confidence:    0.8,
				Reason:        "test name maps to the unit under test by convention",
				RelationScope: "module",
				Resolution:    "name_only",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "test_name",
					FilePath:  symbol.FilePath,
					StartLine: symbol.StartLine,
					EndLine:   symbol.EndLine,
					Detail:    subject,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}

// firstTestSubject returns the first non-test function/method/type symbol with
// the given name (other than the test itself), the unit a test covers.
func firstTestSubject(candidates []SymbolRecord, testID string) (SymbolRecord, bool) {
	for _, symbol := range candidates {
		if symbol.ID == testID || isTestName(symbol.Name) {
			continue
		}
		if symbol.Kind == "function" || symbol.Kind == "method" || typeLikeKind(symbol.Kind) {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

// usesTypeRelations emits USES_TYPE from a function/method to each local type
// symbol whose name appears in its signature. Resolving against known type
// symbols means primitives and library types (which have no local symbol) are
// naturally excluded, keeping the edges high-precision without per-language
// signature grammar.
func usesTypeRelations(recordsByFile map[string][]SymbolRecord, symbolsByFile, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	paths := make([]string, 0, len(recordsByFile))
	for path := range recordsByFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var relations []RelationRecord
	for _, path := range paths {
		for _, symbol := range recordsByFile[path] {
			if symbol.Kind != "function" && symbol.Kind != "method" || symbol.Signature == "" {
				continue
			}
			names := make([]string, 0)
			seen := map[string]bool{}
			for name := range identifiersIn(symbol.Signature) {
				if name == symbol.Name || seen[name] {
					continue
				}
				seen[name] = true
				names = append(names, name)
			}
			sort.Strings(names)
			emitted := map[string]bool{}
			for _, name := range names {
				target, resolution, scope, confidence, ok := resolveTypeReference(name, symbol, symbolsByFile[path], symbolsByShortName)
				if !ok || emitted[target.ID] {
					continue
				}
				emitted[target.ID] = true
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          target.ID,
					Type:          "USES_TYPE",
					Confidence:    confidence,
					Reason:        "type referenced in signature",
					RelationScope: scope,
					Resolution:    resolution,
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "signature",
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    name,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

func resolveTypeReference(name string, from SymbolRecord, sameFile []SymbolRecord, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, string, string, float64, bool) {
	if sym, ok := firstTypeLikeNamed(sameFile, name); ok && sym.ID != from.ID {
		return sym, "exact", "file", 0.85, true
	}
	if sym, ok := firstTypeLikeNamed(symbolsByShortName[name], name); ok && sym.ID != from.ID {
		return sym, "name_only", "module", 0.75, true
	}
	return SymbolRecord{}, "", "", 0, false
}

// fieldAccessRelations resolves `receiver.field` accesses in a function/method
// body to a known local field symbol and emits READS_FIELD, WRITES_FIELD, or
// ACCESSES (address-of). The receiver type is resolved from this/self, a Go
// method receiver variable, or a local constructor assignment; accesses whose
// receiver type cannot be resolved, or whose field is not a known local field,
// are skipped (no guessed edges).
func fieldAccessRelations(from SymbolRecord, block string, fieldsByContainer map[string]map[string]SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	if from.Kind != "function" && from.Kind != "method" {
		return nil
	}
	selfContainers := map[string]string{} // receiver var -> container symbol id
	if from.ContainerID != "" {
		selfContainers["this"] = from.ContainerID
		selfContainers["self"] = from.ContainerID
		if recv := goReceiverVar(from.Signature); recv != "" {
			selfContainers[recv] = from.ContainerID
		}
	}
	varTypes := localVarTypes(block)

	var relations []RelationRecord
	emitted := map[string]bool{}
	for _, access := range fieldAccesses(block) {
		containerID := ""
		confidence := 0.9
		if id, ok := selfContainers[access.Receiver]; ok {
			containerID = id
		} else if typeName, ok := varTypes[access.Receiver]; ok {
			if sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName); ok {
				containerID = sym.ID
				confidence = 0.85
			}
		}
		if containerID == "" {
			continue
		}
		field, ok := fieldsByContainer[containerID][access.Field]
		if !ok || field.ID == from.ID {
			continue
		}
		relationType := "READS_FIELD"
		switch {
		case access.AddressOf:
			relationType = "ACCESSES"
		case access.Write:
			relationType = "WRITES_FIELD"
		}
		key := relationType + "\x00" + field.ID
		if emitted[key] {
			continue
		}
		emitted[key] = true
		scope := "file"
		if field.FilePath != from.FilePath {
			scope = "module"
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from.ID,
			ToID:          field.ID,
			Type:          relationType,
			Confidence:    confidence,
			Reason:        "field access resolved via inferred receiver type",
			RelationScope: scope,
			Resolution:    "type_inferred",
			TargetKind:    "symbol",
			Evidence: []Evidence{{
				Kind:      "field_access",
				FilePath:  from.FilePath,
				StartLine: from.StartLine,
				EndLine:   from.EndLine,
				Detail:    access.Receiver + "." + access.Field,
			}},
			WarningCodes: []string{},
		})
	}
	return relations
}

// overrideRelations derives OVERRIDES edges from resolved EXTENDS/IMPLEMENTS
// relations: a method on the subtype that shares a name with a method on the
// resolved supertype overrides it. It only fires when both the supertype and
// its methods are known local symbols, so external base classes never produce
// guessed overrides.
func overrideRelations(relations []RelationRecord, methodsByContainer map[string]map[string]SymbolRecord) []RelationRecord {
	var overrides []RelationRecord
	for _, relation := range relations {
		if relation.Type != "EXTENDS" && relation.Type != "IMPLEMENTS" {
			continue
		}
		if relation.TargetKind != "symbol" {
			continue
		}
		subMethods := methodsByContainer[relation.FromID]
		superMethods := methodsByContainer[relation.ToID]
		if len(subMethods) == 0 || len(superMethods) == 0 {
			continue
		}
		for _, name := range sortedKeysOf(subMethods) {
			subMethod := subMethods[name]
			superMethod, ok := superMethods[name]
			if !ok || superMethod.ID == subMethod.ID {
				continue
			}
			overrides = append(overrides, RelationRecord{
				RecordType:    "relation",
				FromID:        subMethod.ID,
				ToID:          superMethod.ID,
				Type:          "OVERRIDES",
				Confidence:    0.85,
				Reason:        "method shares a name with a method on a resolved supertype",
				RelationScope: "module",
				Resolution:    "exact",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "method_declaration",
					FilePath:  subMethod.FilePath,
					StartLine: subMethod.StartLine,
					EndLine:   subMethod.EndLine,
					Detail:    name,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return overrides
}

func firstTypeLikeNamed(records []SymbolRecord, name string) (SymbolRecord, bool) {
	for _, symbol := range records {
		if symbol.Name == name && typeLikeKind(symbol.Kind) {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

func typeRelationReason(relation, resolution string) string {
	switch resolution {
	case "exact":
		return relation + " resolved to local type declaration"
	case "name_only":
		return relation + " matched a type by name"
	default:
		return relation + " references an external type"
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// recordIndexes builds id lookups for file and symbol records.
func recordIndexes(files []FileRecord, recordsByFile map[string][]SymbolRecord) (map[string]SymbolRecord, map[string]FileRecord) {
	filesByID := make(map[string]FileRecord, len(files))
	for _, file := range files {
		filesByID[file.ID] = file
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			symbolsByID[symbol.ID] = symbol
		}
	}
	return symbolsByID, filesByID
}

// externalRecordFor builds the external endpoint record implied by one relation
// endpoint id, attaching a boundary source location when the relation provides
// one.
func externalRecordFor(relation RelationRecord, id string, symbolsByID map[string]SymbolRecord, filesByID map[string]FileRecord) ExternalRecord {
	kind, value := externalParts(id)
	record := ExternalRecord{RecordType: "external", ID: id, Kind: kind, Value: value, External: true}
	if source, ok := boundarySourceLocation(relation, id, symbolsByID, filesByID); ok {
		record.FilePath = source.FilePath
		record.StartLine = source.StartLine
		record.EndLine = source.EndLine
		record.Signature = source.Signature
		record.Language = source.Language
		record.External = false
		record.SourceSymbol = source.SourceSymbol
		record.SourceDetails = relation.Reason
	}
	return record
}

// mergeExternalRecord keeps the best record per id: a source-located record
// wins over a bare external endpoint.
func mergeExternalRecord(seen map[string]ExternalRecord, record ExternalRecord) {
	if existing, ok := seen[record.ID]; ok && existing.FilePath != "" {
		return
	}
	seen[record.ID] = record
}

type boundarySource struct {
	FilePath     string
	StartLine    int
	EndLine      int
	Signature    string
	Language     string
	SourceSymbol string
}

func boundarySourceLocation(relation RelationRecord, externalID string, symbolsByID map[string]SymbolRecord, filesByID map[string]FileRecord) (boundarySource, bool) {
	if !isBoundaryRelation(relation.Type) {
		return boundarySource{}, false
	}
	sourceID := relation.FromID
	if sourceID == externalID {
		sourceID = relation.ToID
	}
	if symbol, ok := symbolsByID[sourceID]; ok {
		return boundarySource{
			FilePath:     symbol.FilePath,
			StartLine:    symbol.StartLine,
			EndLine:      symbol.EndLine,
			Signature:    symbol.Signature,
			Language:     symbol.Language,
			SourceSymbol: symbol.QualifiedName,
		}, true
	}
	if file, ok := filesByID[sourceID]; ok {
		return boundarySource{
			FilePath:  file.Path,
			StartLine: 1,
			EndLine:   1,
			Signature: file.Path,
			Language:  file.Language,
		}, true
	}
	return boundarySource{}, false
}

func isBoundaryRelation(relationType string) bool {
	switch relationType {
	case "HANDLES_ROUTE":
		return true
	default:
		return false
	}
}

func externalParts(id string) (string, string) {
	rest := strings.TrimPrefix(id, "external:")
	kind, value, ok := strings.Cut(rest, ":")
	if !ok {
		return "unknown", rest
	}
	return kind, value
}

// openSource lists the repository's files and returns a per-file content reader
// that fetches one file at a time (from the git HEAD tree or the working tree),
// so the snapshot never holds all source content in memory.
func openSource(ctx context.Context, repo string, useHead bool, ignoreFiles, includeFiles []string) ([]string, contentReader, func() error, error) {
	if useHead {
		paths, err := gitutil.ListFiles(ctx, repo, "HEAD")
		if err != nil {
			return nil, nil, nil, err
		}
		batch, err := gitutil.NewBatchFileReader(ctx, repo, "HEAD")
		if err != nil {
			return nil, nil, nil, err
		}
		read := func(path string) (string, bool) {
			if strings.Contains(path, "\n") {
				content, ok, err := gitutil.ShowFile(ctx, repo, "HEAD", path)
				if err != nil || !ok {
					return "", false
				}
				return content, true
			}
			content, ok, err := batch.ReadFile(path)
			if err != nil || !ok {
				return "", false
			}
			return content, true
		}
		return paths, read, batch.Close, nil
	}
	ignores, err := loadWorktreeIgnoreMatcher(repo, ignoreFiles, includeFiles)
	if err != nil {
		return nil, nil, nil, err
	}
	paths, err := workingTreeFiles(repo, ignores)
	if err != nil {
		return nil, nil, nil, err
	}
	read := func(path string) (string, bool) {
		full := filepath.Join(repo, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", false
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return "", false
		}
		return string(content), true
	}
	return paths, read, nil, nil
}

func workingTreeFiles(repo string, ignores ignoreMatcher) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			switch name {
			case ".git", "node_modules", "vendor", ".next", "dist", "build":
				return filepath.SkipDir
			}
			if path != repo {
				rel, err := filepath.Rel(repo, path)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if ignores.Ignored(rel, true) && !ignores.MayIncludeDescendant(rel) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ignores.Ignored(rel, false) {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// importCapableExtension reports whether importsFor has a dedicated import
// scanner for the extension. It must mirror the non-nil cases in importsFor;
// TestCapabilitiesReportRelationSupportPerLanguage guards key cases against
// drift. Extensions that fall through importsFor's default (or explicitly
// return nil, like .hcl/.tf/.tfvars/.sql/.yaml) are not import-capable.
func importCapableExtension(ext string) bool {
	switch ext {
	case ".bash", ".sh", ".zsh",
		".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx",
		".cs", ".cue", ".ex", ".exs", ".go", ".gradle", ".groovy",
		".java", ".kt", ".kts", ".scala", ".sc", ".sbt", ".py",
		".js", ".jsx", ".ts", ".tsx", ".lua", ".ml", ".mli", ".php",
		".proto", ".rb", ".rs", ".swift":
		return true
	default:
		return false
	}
}

// isRelativeImportSpec reports whether an import spec is a repo-relative path
// (rather than an external package) for the importing file's language.
func isRelativeImportSpec(importingPath, spec string) bool {
	switch strings.ToLower(filepath.Ext(importingPath)) {
	case ".js", ".jsx", ".ts", ".tsx":
		return strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")
	case ".py":
		return strings.HasPrefix(spec, ".")
	default:
		return false
	}
}

// resolveLocalImport maps a relative import spec to a known repo file path. It
// returns the resolved path and true when the import points at a file present
// in the snapshot, so the IMPORTS edge can target a local file record instead
// of an external endpoint. Only relative specs are resolved here; module-root
// resolution via manifests is a later WP3 step.
func resolveLocalImport(importingPath, spec string, knownFiles map[string]bool) (string, bool) {
	switch strings.ToLower(filepath.Ext(importingPath)) {
	case ".js", ".jsx", ".ts", ".tsx":
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			return "", false
		}
		base := filepath.ToSlash(filepath.Join(filepath.Dir(importingPath), spec))
		exts := []string{".ts", ".tsx", ".js", ".jsx"}
		var candidates []string
		if filepath.Ext(base) != "" {
			candidates = append(candidates, base)
		}
		for _, ext := range exts {
			candidates = append(candidates, base+ext)
		}
		for _, ext := range exts {
			candidates = append(candidates, filepath.ToSlash(filepath.Join(base, "index"+ext)))
		}
		for _, candidate := range candidates {
			if knownFiles[candidate] {
				return candidate, true
			}
		}
		return "", false
	case ".py":
		if !strings.HasPrefix(spec, ".") {
			return "", false
		}
		level := 0
		for level < len(spec) && spec[level] == '.' {
			level++
		}
		module := spec[level:]
		dir := filepath.Dir(importingPath)
		for i := 1; i < level; i++ {
			dir = filepath.Dir(dir)
		}
		relPath := strings.ReplaceAll(module, ".", "/")
		base := filepath.ToSlash(filepath.Join(dir, relPath))
		for _, candidate := range []string{base + ".py", filepath.ToSlash(filepath.Join(base, "__init__.py"))} {
			if knownFiles[candidate] {
				return candidate, true
			}
		}
		return "", false
	default:
		return "", false
	}
}

func importsFor(path, content string) []string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".bash", ".sh", ".zsh":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:source|\.)\s+["']?([^"'\s]+)`))
	case ".c", ".h":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*#\s*include\s+[<"]([^>"]+)[>"]`))
	case ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*#\s*include\s+[<"]([^>"]+)[>"]`))
	case ".cs":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*using\s+([A-Za-z0-9_\.]+)\s*;`))
	case ".cue":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+(?:\(\s*)?["]([^"]+)["]`))
	case ".ex", ".exs":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:alias|import|require|use)\s+([A-Za-z0-9_\.]+)`))
	case ".go":
		return scanGoImports(content)
	case ".gradle", ".groovy":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z0-9_\.]+)`))
	case ".hcl", ".tf", ".tfvars":
		return nil
	case ".java", ".kt", ".kts", ".scala", ".sc", ".sbt":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z0-9_\.\*]+)`))
	case ".py":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:from\s+([A-Za-z0-9_\.]+)\s+import|import\s+([A-Za-z0-9_\.]+))`))
	case ".js", ".jsx", ".ts", ".tsx":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]|^\s*import\s+['"]([^'"]+)['"]`))
	case ".lua":
		return scanImports(content, regexp.MustCompile(`(?m)require\s*(?:\(|\s)\s*["']([^"']+)["']`))
	case ".ml", ".mli":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*open\s+([A-Za-z0-9_\.]+)`))
	case ".php":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:use|include|require|include_once|require_once)\s+['"]?([^'";]+)`))
	case ".proto":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+["]([^"]+)["]`))
	case ".rb":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*require(?:_relative)?\s+['"]([^'"]+)['"]`))
	case ".rs":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*use\s+([^;]+);`))
	case ".sql":
		return nil
	case ".swift":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z0-9_]+)`))
	default:
		return nil
	}
}

func scanGoImports(content string) []string {
	seen := map[string]struct{}{}
	singleImport := regexp.MustCompile(`^\s*import\s+(?:\w+\s+)?["]([^"]+)["]`)
	blockImport := regexp.MustCompile(`^\s*(?:\w+\s+)?["]([^"]+)["]`)
	inBlock := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !inBlock {
			if strings.HasPrefix(line, "import (") {
				inBlock = true
				continue
			}
			if matches := singleImport.FindStringSubmatch(line); len(matches) > 1 {
				seen[matches[1]] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(line, ")") {
			inBlock = false
			continue
		}
		if matches := blockImport.FindStringSubmatch(line); len(matches) > 1 {
			seen[matches[1]] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func scanImports(content string, expressions ...*regexp.Regexp) []string {
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		for _, expression := range expressions {
			matches := expression.FindStringSubmatch(line)
			if len(matches) == 0 {
				continue
			}
			for _, match := range matches[1:] {
				if match == "" {
					continue
				}
				seen[strings.TrimSpace(match)] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

func importedNamesFor(path, content string) map[string][]string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".jsx", ".ts", ".tsx":
		return importedJavaScriptNames(content)
	default:
		return map[string][]string{}
	}
}

func importedJavaScriptNames(content string) map[string][]string {
	imports := map[string][]string{}
	namedImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?\{([^}]+)\}\s+from\s+['"]([^'"]+)['"]`)
	defaultImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	namespaceImport := regexp.MustCompile(`(?m)^\s*import\s+\*\s+as\s+([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	for _, match := range namedImport.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			item = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "type "))
			if item == "" {
				continue
			}
			parts := strings.Fields(item)
			local := parts[0]
			if len(parts) >= 3 && parts[len(parts)-2] == "as" {
				local = parts[len(parts)-1]
			}
			imports[local] = append(imports[local], match[2])
		}
	}
	for _, match := range defaultImport.FindAllStringSubmatch(content, -1) {
		imports[match[1]] = append(imports[match[1]], match[2])
	}
	for _, match := range namespaceImport.FindAllStringSubmatch(content, -1) {
		imports[match[1]] = append(imports[match[1]], match[2])
	}
	return imports
}

func importedNameMatchesFile(modules []string, targetPath string) bool {
	for _, module := range modules {
		if importModuleMatchesFile(module, targetPath) {
			return true
		}
	}
	return false
}

func importModuleMatchesFile(module, targetPath string) bool {
	module = strings.TrimSpace(module)
	if module == "" || strings.HasPrefix(module, ".") {
		return false
	}
	module = strings.TrimPrefix(module, "@/")
	module = strings.TrimPrefix(module, "src/")
	target := strings.TrimSuffix(filepath.ToSlash(targetPath), filepath.Ext(targetPath))
	return strings.HasSuffix(target, module) || strings.HasSuffix(target, "/"+module) || target == module
}

func callLikeIdentifiers(content string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(content)
	identifiers := map[string]struct{}{}
	call := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*(?:<[^>\n;{}()]*>)?\(`)
	for _, match := range call.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) < 4 {
			continue
		}
		start := match[2]
		name := stripped[match[2]:match[3]]
		if callNameIgnored(stripped, start, name) {
			continue
		}
		identifiers[name] = struct{}{}
	}
	return identifiers
}

func callNameIgnored(content string, start int, name string) bool {
	for i := start - 1; i >= 0; i-- {
		switch content[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '.':
			return true
		default:
			i = -1
		}
	}
	switch name {
	case "if", "for", "while", "switch", "catch", "function", "return", "typeof", "sizeof":
		return true
	case "append", "cap", "close", "complex", "copy", "delete", "imag", "len", "make", "new", "panic", "print", "println", "real", "recover":
		return true
	case "Array", "Boolean", "Date", "Error", "Map", "Math", "Number", "Object", "Promise", "RegExp", "Request", "Response", "Set", "String", "URL", "URLSearchParams":
		return true
	default:
		return false
	}
}

func stripCodeLiteralsAndComments(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '"', '\'':
			quote := bytes[i]
			for j := i + 1; j < len(bytes); j++ {
				if bytes[j] == '\n' || bytes[j] == '\r' {
					i = j
					break
				}
				if bytes[j] == '\\' {
					j++
					continue
				}
				if bytes[j] == quote {
					maskBytes(bytes, i, j+1)
					i = j
					break
				}
			}
		case '`':
			for j := i + 1; j < len(bytes); j++ {
				if bytes[j] == '\\' {
					j++
					continue
				}
				if bytes[j] == '`' {
					maskBytes(bytes, i, j+1)
					i = j
					break
				}
			}
		case '/':
			if i+1 >= len(bytes) {
				continue
			}
			switch bytes[i+1] {
			case '/':
				j := i + 2
				for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
					j++
				}
				maskBytes(bytes, i, j)
				i = j
			case '*':
				j := i + 2
				for j+1 < len(bytes) && !(bytes[j] == '*' && bytes[j+1] == '/') {
					j++
				}
				if j+1 < len(bytes) {
					j += 2
				}
				maskBytes(bytes, i, j)
				i = j
			}
		}
	}
	return string(bytes)
}

func maskBytes(bytes []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(bytes) {
		end = len(bytes)
	}
	for i := start; i < end; i++ {
		if bytes[i] != '\n' && bytes[i] != '\r' {
			bytes[i] = ' '
		}
	}
}

var (
	routeLiteralRe = regexp.MustCompile(`["'](/[A-Za-z0-9_\-/{}:.]*)["']`)
	// routingCallRe marks a line as a route registration: an HTTP-verb or
	// routing method call, or a mapping decorator, immediately before "(".
	// Requiring this context next to the path literal avoids treating every
	// path-like string (URLs, file paths, test data) as a handled route.
	routingCallRe = regexp.MustCompile(`(?i)\b(get|post|put|patch|delete|head|options|route|handle|handlefunc|group|mapping|getmapping|postmapping|putmapping|deletemapping|patchmapping|requestmapping)\s*\(`)
)

// routeLiterals extracts route paths that appear on a line carrying routing
// context, so plain path-like string literals are not misreported as handled
// routes.
func routeLiterals(content string) []string {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(content, "\n") {
		if !routingCallRe.MatchString(line) || httpClientRe.MatchString(line) {
			continue // skip client HTTP calls; those are HTTP_CALLS, not routes
		}
		for _, match := range routeLiteralRe.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				seen[match[1]] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

func nextRouteBoundary(path string) string {
	slashPath := filepath.ToSlash(path)
	const rootMarker = "src/app/"
	const nestedMarker = "/src/app/"
	var relative string
	switch {
	case strings.HasPrefix(slashPath, rootMarker):
		relative = strings.TrimPrefix(slashPath, rootMarker)
	case strings.Contains(slashPath, nestedMarker):
		index := strings.Index(slashPath, nestedMarker)
		relative = slashPath[index+len(nestedMarker):]
	default:
		return ""
	}
	switch {
	case strings.HasSuffix(relative, "/route.ts"):
		relative = strings.TrimSuffix(relative, "/route.ts")
	case strings.HasSuffix(relative, "/route.tsx"):
		relative = strings.TrimSuffix(relative, "/route.tsx")
	case strings.HasSuffix(relative, "/page.ts"):
		relative = strings.TrimSuffix(relative, "/page.ts")
	case strings.HasSuffix(relative, "/page.tsx"):
		relative = strings.TrimSuffix(relative, "/page.tsx")
	default:
		return ""
	}
	var segments []string
	for _, segment := range strings.Split(relative, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" || strings.HasPrefix(segment, "(") && strings.HasSuffix(segment, ")") {
			continue
		}
		if strings.HasPrefix(segment, "@") {
			continue
		}
		segments = append(segments, nextRouteSegment(segment))
	}
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

func nextRouteSegment(segment string) string {
	if strings.HasPrefix(segment, "[[...") && strings.HasSuffix(segment, "]]") {
		return "{..." + strings.TrimSuffix(strings.TrimPrefix(segment, "[[..."), "]]") + "}"
	}
	if strings.HasPrefix(segment, "[...") && strings.HasSuffix(segment, "]") {
		return "{..." + strings.TrimSuffix(strings.TrimPrefix(segment, "[..."), "]") + "}"
	}
	if strings.HasPrefix(segment, "[") && strings.HasSuffix(segment, "]") {
		return "{" + strings.TrimSuffix(strings.TrimPrefix(segment, "["), "]") + "}"
	}
	return segment
}

func routeBoundarySourceID(repoKey string, file FileRecord, symbols []SymbolRecord) string {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		for _, symbol := range symbols {
			if symbol.Name == method || symbol.QualifiedName == method {
				return symbol.ID
			}
		}
	}
	for _, symbol := range symbols {
		if symbol.Kind == "function" || symbol.Kind == "method" {
			return symbol.ID
		}
	}
	return fileID(repoKey, file.Path)
}

func applicationWorkflowBoundary(path string) string {
	route := nextRouteBoundary(path)
	if !strings.HasPrefix(route, "/api/internal/") {
		return ""
	}
	switch {
	case route == "/api/internal/feed-crawler/tick":
		return "feed-crawler"
	case route == "/api/internal/post-transcription/tick":
		return "post-transcription"
	case route == "/api/internal/jobs/recover":
		return "transcription-recovery"
	case strings.HasSuffix(route, "/attribute-speakers"):
		return "speaker-attribution"
	case strings.HasSuffix(route, "/chunk"):
		return "transcript-chunking"
	case strings.HasSuffix(route, "/classify-ads"):
		return "ad-classification"
	case strings.HasSuffix(route, "/insights"):
		return "transcript-insights"
	default:
		return ""
	}
}

func looksLikeToolHandler(symbol SymbolRecord, block string) bool {
	value := strings.ToLower(symbol.QualifiedName + "\n" + block)
	tokens := tokenSet(value)
	return tokens["tool"] && (tokens["handler"] || tokens["execute"] || tokens["schema"])
}

func symbolBlock(content string, symbol SymbolRecord) string {
	return symbolBlockFromLines(strings.Split(content, "\n"), symbol)
}

func symbolBlockFromLines(lines []string, symbol SymbolRecord) string {
	start := symbol.StartLine - 1
	if start < 0 {
		start = 0
	}
	end := symbol.EndLine
	if end > len(lines) {
		end = len(lines)
	}
	if end <= start {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

func completenessLevel(failures, files int) string {
	switch {
	case failures == 0:
		return "ok"
	case files == 0 || failures*4 > files:
		return "unsafe"
	default:
		return "degraded"
	}
}

func dedupeRelations(relations []RelationRecord) []RelationRecord {
	seen := map[string]struct{}{}
	out := make([]RelationRecord, 0, len(relations))
	for _, relation := range relations {
		key := relation.FromID + "\x00" + relation.ToID + "\x00" + relation.Type
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, relation)
	}
	return out
}

func writeJSONLine(out io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

func symbolID(repoKey, language, path, kind, qualifiedName string) string {
	return strings.Join([]string{repoKey, language, path, kind, qualifiedName}, ":")
}

func fileID(repoKey, path string) string {
	return repoKey + ":file:" + path
}

func externalID(kind, value string) string {
	return "external:" + kind + ":" + value
}

func repoKey(ctx context.Context, repo string) string {
	for _, remoteURL := range githubRemoteURLs(ctx, repo) {
		if key, ok := githubRepoKey(remoteURL); ok {
			return key
		}
	}
	return "local/" + filepath.Base(repo)
}

func githubRemoteURLs(ctx context.Context, repo string) []string {
	urls, err := gitutil.RemoteURLs(ctx, repo)
	if err != nil {
		return nil
	}
	return urls
}

func githubRepoKey(remoteURL string) (string, bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	remoteURL = strings.TrimRight(remoteURL, "/")
	remoteURL = strings.TrimSuffix(remoteURL, ".git")
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^git@github\.com:([^/]+)/(.+)$`),
		regexp.MustCompile(`^https://github\.com/([^/]+)/(.+)$`),
		regexp.MustCompile(`^http://github\.com/([^/]+)/(.+)$`),
		regexp.MustCompile(`^ssh://git@github\.com/([^/]+)/(.+)$`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(remoteURL)
		if len(matches) != 3 {
			continue
		}
		owner := strings.TrimSpace(matches[1])
		name := strings.TrimSpace(matches[2])
		if owner == "" || name == "" || strings.Contains(name, "/") {
			continue
		}
		return "gh/" + owner + "/" + name, true
	}
	return "", false
}

func containerName(qualifiedName string) string {
	index := strings.LastIndex(qualifiedName, ".")
	if index < 0 {
		return ""
	}
	return qualifiedName[:index]
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// sortedKeysOf returns a map's string keys in sorted order, so iterating them
// yields a deterministic stream order regardless of Go's randomized map order.
func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func unsupportedLanguageHint(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dart", ".erb", ".f90", ".for", ".fs", ".fsharp", ".m", ".mm", ".pl", ".pm", ".svelte", ".vue", ".zig":
		return "unsupported source extension " + filepath.Ext(path)
	default:
		return ""
	}
}
