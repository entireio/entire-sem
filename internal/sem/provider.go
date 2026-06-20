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
	"ASYNC_CALLS",
	"EXTENDS",
	"INHERITS",
	"IMPLEMENTS",
	"OVERRIDES",
	"USES_TYPE",
	"PARAM_TYPE",
	"RETURNS_TYPE",
	"READS_FIELD",
	"WRITES_FIELD",
	"ACCESSES",
	"HANDLES_ROUTE",
	"HANDLES_GRPC",
	"HANDLES_GRAPHQL",
	"HANDLES_TRPC",
	"HTTP_CALLS",
	"EMITS",
	"LISTENS_ON",
	"HANDLES_TOOL",
	"CONFIGURES",
	"SIMILAR_TO",
	"TESTS",
	"RESOURCE_DEPENDS_ON",
	"DATA_FLOWS",
	"FILE_CHANGES_WITH",
}

// ooRelationSupport lists the additional (non-structural) relation types the
// provider can extract for each language, used by the capabilities matrix.
// OVERRIDES is derived from a resolved supertype's methods, so it is advertised
// only for class-based languages with clear method containers.
var ooRelationSupport = map[string][]string{
	"Java":             {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"TypeScript":       {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "HANDLES_GRAPHQL", "HANDLES_TRPC", "ASYNC_CALLS", "DATA_FLOWS"},
	"JavaScript":       {"EXTENDS", "INHERITS", "HANDLES_GRAPHQL", "HANDLES_TRPC", "ASYNC_CALLS", "DATA_FLOWS"},
	"C#":               {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"PHP":              {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "DATA_FLOWS"},
	"Python":           {"EXTENDS", "INHERITS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "HANDLES_GRAPHQL", "ASYNC_CALLS", "DATA_FLOWS"},
	"Rust":             {"EXTENDS", "INHERITS", "IMPLEMENTS", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"Go":               {"USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"HCL":              {"CONFIGURES", "RESOURCE_DEPENDS_ON"},
	"Protocol Buffers": {"HANDLES_GRPC"},
	"YAML":             {"CONFIGURES", "RESOURCE_DEPENDS_ON"},
	"Dockerfile":       {"CONFIGURES", "RESOURCE_DEPENDS_ON"},
	"Kustomize":        {"CONFIGURES", "RESOURCE_DEPENDS_ON"},
	"JSON":             {"CONFIGURES"},
	"JSON5":            {"CONFIGURES"},
	"TOML":             {"CONFIGURES"},
	"XML":              {"CONFIGURES"},
	"Make":             {"CONFIGURES"},
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
	for _, t := range []string{"IMPORTS", "CALLS", "ASYNC_CALLS", "HANDLES_ROUTE", "HANDLES_GRPC", "HANDLES_GRAPHQL", "HANDLES_TRPC", "HTTP_CALLS", "EMITS", "LISTENS_ON", "EXTENDS", "INHERITS", "IMPLEMENTS", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "CONFIGURES", "DATA_FLOWS"} {
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
		return profileSpec{name: ProfileFast, relations: relationTypeSet("DEFINES", "CONTAINS", "IMPORTS", "CALLS", "HANDLES_ROUTE", "HANDLES_TOOL", "CONFIGURES", "RESOURCE_DEPENDS_ON"), includeEvidence: false, callResolution: "shallow"}
	default:
		return profileSpec{name: ProfileFull, relations: relationTypeSet(relationTypes...), includeEvidence: true, callResolution: "full"}
	}
}

func Capabilities() CapabilityReport {
	specs := supportedLanguageSpecs()
	extensions := make([]string, 0, len(specs))
	languageSet := map[string]struct{}{}
	for extension, spec := range specs {
		extensions = append(extensions, extension)
		languageSet[spec.language] = struct{}{}
	}
	for _, spec := range inventoryLanguageFilenames {
		languageSet[spec.language] = struct{}{}
	}
	for _, language := range specialFilenameLanguages() {
		languageSet[language] = struct{}{}
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
			"git_cochange_edges":   true,
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
	for ext, spec := range supportedLanguageSpecs() {
		if importCapableExtension(ext) {
			importCapable[spec.language] = true
		}
	}
	support := map[string][]string{}
	for _, spec := range supportedLanguageSpecs() {
		mergeLanguageSupport(support, spec.language, spec, importCapable[spec.language])
	}
	for _, spec := range inventoryLanguageFilenames {
		mergeLanguageSupport(support, spec.language, spec, false)
	}
	for _, language := range specialFilenameLanguages() {
		mergeLanguageSupport(support, language, languageSpec{language: language}, false)
	}
	return support
}

func supportedLanguageSpecs() map[string]languageSpec {
	specs := make(map[string]languageSpec, len(treeSitterLanguages)+len(inventoryLanguageExtensions))
	for ext, spec := range treeSitterLanguages {
		specs[ext] = spec
	}
	for ext, spec := range inventoryLanguageExtensions {
		specs[ext] = spec
	}
	return specs
}

func specialFilenameLanguages() []string {
	return []string{"Dockerfile", "Kustomize", "Make"}
}

func mergeLanguageSupport(support map[string][]string, language string, spec languageSpec, importCapable bool) {
	set := map[string]bool{"CONTAINS": true, "DEFINES": true}
	for _, relation := range support[language] {
		set[relation] = true
	}
	if supportsCallExtraction(spec) {
		set["CALLS"] = true
	}
	if importCapable {
		set["IMPORTS"] = true
	}
	for _, relation := range ooRelationSupport[language] {
		set[relation] = true
	}
	types := make([]string, 0, len(set))
	for relation := range set {
		types = append(types, relation)
	}
	sort.Strings(types)
	support[language] = types
}

func supportsCallExtraction(spec languageSpec) bool {
	if spec.inventoryOnly {
		return false
	}
	switch spec.language {
	case "Bash", "C", "C++", "C#", "Elixir", "Go", "Groovy", "Java", "JavaScript", "Kotlin", "Lua", "OCaml", "PHP", "Python", "Ruby", "Rust", "Scala", "Swift", "TypeScript":
		return true
	default:
		return false
	}
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
		if r.WarningCodes == nil {
			r.WarningCodes = []string{}
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
		if spec.emits("FILE_CHANGES_WITH") {
			for _, r := range fileChangesWithRelations(ctx, sc.absRepo, sc.key, files) {
				emitRelation(r, symbolsByID, filesByID)
			}
		}
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
	needsTypes := spec.emits("EXTENDS") || spec.emits("INHERITS") || spec.emits("IMPLEMENTS") || spec.emits("OVERRIDES")
	needsOverrides := spec.emits("OVERRIDES")
	needsAsyncCalls := spec.emits("ASYNC_CALLS")
	needsDataFlow := spec.emits("DATA_FLOWS")
	needsServiceRelations := spec.emits("HANDLES_GRPC") || spec.emits("HANDLES_GRAPHQL") || spec.emits("HANDLES_TRPC")
	symbolsByID := map[string]SymbolRecord{}
	symbolsByShortName := map[string][]SymbolRecord{}
	symbolsByFile := map[string][]SymbolRecord{}
	childNamesByContainer := map[string]map[string]bool{}
	methodsByContainer := map[string]map[string]SymbolRecord{}
	fieldsByContainer := map[string]map[string]SymbolRecord{}
	returnTypesBySymbolNameAndFile := map[string]map[string][]string{}
	var inheritanceEdges []RelationRecord // captured for OVERRIDES derivation
	routeHandlers := map[string][]SymbolRecord{}
	httpCallsByRoute := map[string][]RelationRecord{}
	// Iterate files in their (stable) slice order, not the recordsByFile map, so
	// structural relations stream deterministically.
	for _, file := range files {
		records := recordsByFile[file.Path]
		for _, symbol := range records {
			if symbol.Kind == "route" {
				routeHandlers[symbol.Name] = append(routeHandlers[symbol.Name], symbol)
			}
			symbolsByID[symbol.ID] = symbol
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
			if needsReceiverCalls && !typeLikeKind(symbol.Kind) {
				for _, typeName := range signatureTypeReferences(symbol.Language, symbol.Signature)["RETURNS_TYPE"] {
					if returnTypesBySymbolNameAndFile[symbol.Name] == nil {
						returnTypesBySymbolNameAndFile[symbol.Name] = map[string][]string{}
					}
					returnTypesBySymbolNameAndFile[symbol.Name][symbol.FilePath] = append(returnTypesBySymbolNameAndFile[symbol.Name][symbol.FilePath], typeName)
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
	if spec.emits("HANDLES_ROUTE") {
		for _, r := range goHTTPRouteRelations(files, recordsByFile, readContent) {
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range djangoRouteRelations(files, recordsByFile, readContent) {
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
	}
	manifestImports := buildManifestImportResolver(files, readContent)

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
			if resolved, ok := manifestImports.resolve(file.Path, imported); ok {
				emit(RelationRecord{
					RecordType:    "relation",
					FromID:        fromID,
					ToID:          fileID(repoKey, resolved.Path),
					Type:          "IMPORTS",
					Confidence:    resolved.Confidence,
					Reason:        resolved.Reason,
					RelationScope: resolved.Scope,
					Resolution:    "import_resolved",
					TargetKind:    "file",
					Evidence: []Evidence{{
						Kind:     resolved.EvidenceKind,
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
				targets := resolveCallTargets(name, from, symbolsByShortName[name], symbolsByFile[file.Path], importsByName)
				for _, to := range targets {
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
				if len(targets) == 0 {
					for _, relation := range importedExternalCallRelationsForName(from, name, importsByName[name]) {
						emit(relation)
					}
				}
			}
			callableSymbol := !typeLikeKind(from.Kind)
			if needsAsyncCalls && callableSymbol {
				for _, name := range asyncCallNames(block) {
					if name == from.Name {
						continue
					}
					for _, to := range resolveCallTargets(name, from, symbolsByShortName[name], symbolsByFile[file.Path], importsByName) {
						emit(RelationRecord{
							RecordType:    "relation",
							FromID:        from.ID,
							ToID:          to.ID,
							Type:          "ASYNC_CALLS",
							Confidence:    minFloat(to.Confidence, 0.85),
							Reason:        "async call site resolved to symbol",
							RelationScope: to.Scope,
							Resolution:    to.Resolution,
							TargetKind:    "symbol",
							Evidence: []Evidence{{
								Kind:      "async_call_site",
								FilePath:  from.FilePath,
								StartLine: from.StartLine,
								EndLine:   from.EndLine,
								Detail:    name,
							}},
							WarningCodes: []string{},
						})
					}
				}
			}
			if needsDataFlow && callableSymbol {
				for _, flow := range returnFlowCalls(block, from.Signature) {
					if flow.Name == from.Name {
						continue
					}
					for _, to := range resolveCallTargets(flow.Name, from, symbolsByShortName[flow.Name], symbolsByFile[file.Path], importsByName) {
						if flow.Direction == "caller_to_callee" && to.Resolution == "name_only" {
							continue
						}
						fromID, toID := to.ID, from.ID
						confidenceCap := 0.75
						if flow.Direction == "caller_to_callee" {
							fromID, toID = from.ID, to.ID
							confidenceCap = 0.7
						}
						emit(RelationRecord{
							RecordType:    "relation",
							FromID:        fromID,
							ToID:          toID,
							Type:          "DATA_FLOWS",
							Confidence:    minFloat(to.Confidence, confidenceCap),
							Reason:        flow.Reason,
							RelationScope: to.Scope,
							Resolution:    to.Resolution,
							TargetKind:    "symbol",
							Evidence: []Evidence{{
								Kind:      flow.EvidenceKind,
								FilePath:  from.FilePath,
								StartLine: from.StartLine,
								EndLine:   from.EndLine,
								Detail:    flow.Detail,
							}},
							WarningCodes: []string{},
						})
					}
				}
			}
			if needsServiceRelations {
				for _, boundary := range serviceBoundaries(from, block) {
					if !spec.emits(boundary.Relation) {
						continue
					}
					emit(RelationRecord{
						RecordType:    "relation",
						FromID:        from.ID,
						ToID:          externalID(boundary.Kind, boundary.Name),
						Type:          boundary.Relation,
						Confidence:    boundary.Confidence,
						Reason:        boundary.Reason,
						RelationScope: "external",
						Resolution:    "pattern",
						TargetKind:    boundary.Kind,
						Evidence: []Evidence{{
							Kind:      boundary.EvidenceKind,
							FilePath:  from.FilePath,
							StartLine: from.StartLine,
							EndLine:   from.EndLine,
							Detail:    boundary.Name,
						}},
						WarningCodes: boundary.WarningCodes,
					})
				}
			}
			for _, route := range routeLiteralsForSymbol(file.Path, content, block, from, symbolsByID) {
				if _, ok := handledRoutes[route]; ok {
					continue
				}
				relation := RelationRecord{
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
				}
				emit(relation)
				routeHandlers[route] = append(routeHandlers[route], from)
			}
			if callableSymbol {
				for _, call := range httpCalls(block) {
					if !spec.emits("HTTP_CALLS") {
						break
					}
					confidence := 0.7
					if call.Absolute {
						confidence = 0.6 // host ignored; cross-service path match is weaker
					}
					relation := RelationRecord{
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
					}
					emit(relation)
					httpCallsByRoute[call.Path] = append(httpCallsByRoute[call.Path], relation)
				}
			}
			for _, event := range channelEvents(block) {
				if !spec.emits(event.Relation) {
					continue
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
				for _, r := range receiverCallRelations(from, block, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
					emit(r)
				}
				for _, r := range importedReceiverCallRelations(from, block, importsByName) {
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
	if spec.emits("HANDLES_ROUTE") {
		for _, r := range crossFileExpressRouterRelations(files, recordsByFile, readContent, knownFiles) {
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
		}
		for _, r := range pythonIncludeRouterRelations(files, recordsByFile, readContent, knownFiles) {
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
		}
	}
	if spec.emits("CALLS") {
		for _, r := range routeBridgeRelations(routeHandlers, httpCallsByRoute) {
			emit(r)
		}
	}
	if spec.emits("USES_TYPE") {
		for _, r := range usesTypeRelations(recordsByFile, symbolsByFile, symbolsByShortName) {
			emit(r)
		}
	}
	if spec.emits("PARAM_TYPE") || spec.emits("RETURNS_TYPE") {
		for _, r := range signatureTypeRelations(recordsByFile, symbolsByFile, symbolsByShortName, spec) {
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
	if spec.emits("CONFIGURES") {
		for _, r := range configuresRelations(recordsByFile, readContent) {
			emit(r)
		}
	}
	if spec.emits("SIMILAR_TO") {
		for _, r := range similarityRelations(recordsByFile, readContent) {
			emit(r)
		}
	}
}

func routeBridgeRelations(routeHandlers map[string][]SymbolRecord, httpCallsByRoute map[string][]RelationRecord) []RelationRecord {
	var routes []string
	for route := range httpCallsByRoute {
		if len(routeHandlers[route]) > 0 {
			routes = append(routes, route)
		}
	}
	sort.Strings(routes)
	var relations []RelationRecord
	for _, route := range routes {
		handlers := append([]SymbolRecord(nil), routeHandlers[route]...)
		sort.Slice(handlers, func(i, j int) bool {
			return handlers[i].ID < handlers[j].ID
		})
		calls := append([]RelationRecord(nil), httpCallsByRoute[route]...)
		sort.Slice(calls, func(i, j int) bool {
			if calls[i].FromID == calls[j].FromID {
				return calls[i].ToID < calls[j].ToID
			}
			return calls[i].FromID < calls[j].FromID
		})
		for _, call := range calls {
			for _, handler := range handlers {
				if call.FromID == handler.ID {
					continue
				}
				confidence := minFloat(call.Confidence, 0.72)
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        call.FromID,
					ToID:          handler.ID,
					Type:          "CALLS",
					Confidence:    confidence,
					Reason:        "HTTP client call resolved to local route handler through shared route endpoint",
					RelationScope: "workspace",
					Resolution:    "pattern",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:     "route_endpoint_match",
						FilePath: handler.FilePath,
						Detail:   route,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
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
			if edge.Relation == "EXTENDS" {
				relations = append(relations, buildTypeRelation(repoKey, symbol, edge.Super, "INHERITS", edge.Confidence, sameFileSymbols, symbolsByShortName))
			}
		}
	}
	if file.Language == "Rust" {
		for _, edge := range rustSupertypeEdges(content) {
			anchor, ok := firstTypeLikeNamed(fileSymbols, edge.Anchor)
			if !ok {
				continue
			}
			relations = append(relations, buildTypeRelation(repoKey, anchor, edge.Super, edge.Relation, edge.Confidence, sameFileSymbols, symbolsByShortName))
			if edge.Relation == "EXTENDS" {
				relations = append(relations, buildTypeRelation(repoKey, anchor, edge.Super, "INHERITS", edge.Confidence, sameFileSymbols, symbolsByShortName))
			}
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
func receiverCallRelations(from SymbolRecord, block string, methodsByContainer map[string]map[string]SymbolRecord, symbolsByShortName map[string][]SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string) []RelationRecord {
	if typeLikeKind(from.Kind) {
		return nil
	}
	calls := receiverCalls(block)
	chainedCalls := chainedConstructorCalls(block)
	returnedCalls := returnedReceiverCalls(block)
	if len(calls) == 0 && len(chainedCalls) == 0 && len(returnedCalls) == 0 {
		return nil
	}
	varTypes := parameterVarTypes(from.Signature)
	localTypes := localVarTypes(block)
	for name, typeName := range localTypes {
		varTypes[name] = typeName
	}
	paramTypes := parameterVarTypes(from.Signature)
	for name := range localTypes {
		delete(paramTypes, name)
	}
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
			if _, ok := paramTypes[call.Receiver]; ok {
				confidence = 0.83
				reason = "method call resolved via typed parameter receiver"
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
	for _, call := range chainedCalls {
		sym, ok := firstTypeLikeNamed(symbolsByShortName[call.TypeName], call.TypeName)
		if !ok {
			continue
		}
		method, ok := methodsByContainer[sym.ID][call.Method]
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
			Confidence:    0.8,
			Reason:        "method call resolved via chained constructor type",
			RelationScope: scope,
			Resolution:    "type_inferred",
			TargetKind:    "symbol",
			Evidence: []Evidence{{
				Kind:      "call_site",
				FilePath:  from.FilePath,
				StartLine: from.StartLine,
				EndLine:   from.EndLine,
				Detail:    call.Detail,
			}},
			WarningCodes: []string{},
		})
	}
	for _, call := range returnedCalls {
		for _, typeName := range returnTypesBySymbolNameAndFile[call.Factory][from.FilePath] {
			sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
			if !ok {
				continue
			}
			method, ok := methodsByContainer[sym.ID][call.Method]
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
				Confidence:    0.78,
				Reason:        "method call resolved via returned receiver type",
				RelationScope: scope,
				Resolution:    "type_inferred",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    call.Detail,
				}},
				WarningCodes: []string{},
			})
			break
		}
	}
	return relations
}

func importedExternalCallRelationsForName(from SymbolRecord, name string, modules []string) []RelationRecord {
	if typeLikeKind(from.Kind) || len(modules) == 0 {
		return nil
	}
	var relations []RelationRecord
	seen := map[string]bool{}
	for _, module := range modules {
		target := importedExternalSymbolName(module, name)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		relations = append(relations, importedExternalCallRelation(from, target, name))
	}
	return relations
}

func importedReceiverCallRelations(from SymbolRecord, block string, importsByName map[string][]string) []RelationRecord {
	if typeLikeKind(from.Kind) {
		return nil
	}
	var relations []RelationRecord
	seen := map[string]bool{}
	for _, call := range receiverCalls(block) {
		for _, module := range importsByName[call.Receiver] {
			target := importedExternalSymbolName(module, call.Method)
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			relations = append(relations, importedExternalCallRelation(from, target, call.Receiver+"."+call.Method))
		}
	}
	return relations
}

func importedExternalCallRelation(from SymbolRecord, target, detail string) RelationRecord {
	return RelationRecord{
		RecordType:    "relation",
		FromID:        from.ID,
		ToID:          externalID("symbol", target),
		Type:          "CALLS",
		Confidence:    0.78,
		Reason:        "call expression resolved to imported external symbol",
		RelationScope: "external",
		Resolution:    "import_external",
		TargetKind:    "external",
		Evidence: []Evidence{{
			Kind:      "imported_call_site",
			FilePath:  from.FilePath,
			StartLine: from.StartLine,
			EndLine:   from.EndLine,
			Detail:    detail,
		}},
		WarningCodes: []string{},
	}
}

func importedExternalSymbolName(module, member string) string {
	module = strings.Trim(strings.TrimSpace(module), `"'`)
	member = strings.TrimSpace(member)
	if module == "" || member == "" || strings.HasPrefix(module, ".") {
		return ""
	}
	module = strings.TrimSuffix(module, "/")
	return module + "." + member
}

// resourceDependsOnRelations builds the Terraform/HCL resource graph: a
// resource or module block that references another block (e.g. aws_vpc.main.id,
// module.network.id) emits RESOURCE_DEPENDS_ON to that block. Block symbols are
// indexed by their referenceable name (the form used inside expressions).
func resourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	relations = append(relations, hclResourceDependsOnRelations(recordsByFile, readContent)...)
	relations = append(relations, dockerfileResourceDependsOnRelations(recordsByFile, readContent)...)
	relations = append(relations, kubernetesResourceDependsOnRelations(recordsByFile, readContent)...)
	relations = append(relations, kubernetesNamedResourceReferenceRelations(recordsByFile, readContent)...)
	relations = append(relations, kubernetesSelectorResourceRelations(recordsByFile, readContent)...)
	relations = append(relations, kustomizeResourceDependsOnRelations(recordsByFile, readContent)...)
	relations = append(relations, composeResourceDependsOnRelations(recordsByFile, readContent)...)
	return relations
}

func hclResourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
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

func dockerfileResourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	for _, path := range sortedKeysOf(recordsByFile) {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		stages := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[path] {
			if symbol.Language == "Dockerfile" && symbol.Kind == "stage" {
				stages[strings.ToLower(symbol.Name)] = symbol
				stages[strings.ToLower(symbol.QualifiedName)] = symbol
			}
		}
		if len(stages) == 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, symbol := range recordsByFile[path] {
			if symbol.Language != "Dockerfile" || symbol.Kind != "stage" {
				continue
			}
			body := symbolBlockFromLines(lines, symbol)
			for _, sourceStage := range dockerCopyFromStages(body) {
				target, ok := stages[strings.ToLower(sourceStage)]
				if !ok || target.ID == symbol.ID {
					continue
				}
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          target.ID,
					Type:          "RESOURCE_DEPENDS_ON",
					Confidence:    0.9,
					Reason:        "Dockerfile stage copies artifacts from another stage",
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "dockerfile_copy_from",
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    sourceStage,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

func dockerCopyFromStages(content string) []string {
	re := regexp.MustCompile(`(?im)^\s*COPY\s+(?:--[^\s=]+(?:=\S+)?\s+)*--from=([A-Za-z0-9_.-]+)\b`)
	matches := re.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		key := strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
	return out
}

func kubernetesResourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	for _, path := range sortedKeysOf(recordsByFile) {
		content, ok := readContent(path)
		if !ok || !(isKubernetesPath(path) || looksLikeKubernetesManifest(content)) {
			continue
		}
		source, ok := firstSymbol(recordsByFile[path], "YAML", "spec", "template", "metadata")
		if !ok {
			continue
		}
		for _, dep := range kubernetesResourceReferences(content) {
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        source.ID,
				ToID:          externalID("config", "kubernetes/"+dep.Kind+"/"+dep.Name),
				Type:          "RESOURCE_DEPENDS_ON",
				Confidence:    dep.Confidence,
				Reason:        "Kubernetes manifest references another resource",
				RelationScope: "external",
				Resolution:    "pattern",
				TargetKind:    "config",
				Evidence: []Evidence{{
					Kind:      dep.EvidenceKind,
					FilePath:  source.FilePath,
					StartLine: source.StartLine,
					EndLine:   source.EndLine,
					Detail:    dep.Kind + "/" + dep.Name,
				}},
				WarningCodes: []string{"WEAK_PATTERN"},
			})
		}
	}
	return relations
}

type resourceReference struct {
	Kind         string
	Name         string
	EvidenceKind string
	Confidence   float64
}

func kubernetesResourceReferences(content string) []resourceReference {
	var refs []resourceReference
	add := func(kind, name, evidence string, confidence float64) {
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if name == "" {
			return
		}
		refs = append(refs, resourceReference{Kind: strings.ToLower(kind), Name: name, EvidenceKind: evidence, Confidence: confidence})
	}
	for _, match := range regexp.MustCompile(`(?is)\bconfigMapRef:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("configmap", match[1], "kubernetes_configmap_ref", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*(?:-\s*)?configMap:\s*\n\s+name:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("configmap", match[1], "kubernetes_configmap_volume", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bsecretRef:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_secret_ref", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*(?:-\s*)?secret:\s*\n\s+name:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_secret_volume", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*secretName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_secret_name", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bimagePullSecrets:\s*\n(?:\s+-\s*)?name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_image_pull_secret", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*serviceAccountName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("serviceaccount", match[1], "kubernetes_service_account", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*claimName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("persistentvolumeclaim", match[1], "kubernetes_pvc_claim", 0.78)
	}
	for _, ref := range kubernetesKindNameBlockReferences(content, "roleRef", "kubernetes_rbac_role_ref", 0.82) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesKindNameBlockReferences(content, "scaleTargetRef", "kubernetes_hpa_scale_target", 0.84) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesKindNameBlockReferences(content, "ownerReferences", "kubernetes_owner_reference", 0.78) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, match := range regexp.MustCompile(`(?is)\bsubjects:\s*\n(?:\s+-\s*)?kind:\s*ServiceAccount\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("serviceaccount", match[1], "kubernetes_rbac_subject", 0.82)
	}
	for _, match := range regexp.MustCompile(`(?is)\bservice:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("service", match[1], "kubernetes_ingress_service", 0.82)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*serviceName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("service", match[1], "kubernetes_ingress_service_name", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bbackendRefs:\s*\n(?:\s+-\s*)?(?:(?:kind:\s*Service\s*\n)|(?:[A-Za-z0-9_-]+:\s*[^\n]*\n))*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("service", match[1], "kubernetes_gateway_backend_ref", 0.82)
	}
	return dedupeResourceReferences(refs)
}

func kubernetesKindNameBlockReferences(content, blockKey, evidence string, confidence float64) []resourceReference {
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(blockKey) + `:\s*\n(?:\s+(?:-\s*)?[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+(?:-\s*)?kind:\s*([A-Za-z][A-Za-z0-9_.-]+)\s*\n(?:\s+(?:-\s*)?[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`)
	var refs []resourceReference
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			refs = append(refs, resourceReference{Kind: strings.ToLower(match[1]), Name: match[2], EvidenceKind: evidence, Confidence: confidence})
		}
	}
	return refs
}

func kubernetesNamedResourceReferenceRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	resources := map[string]SymbolRecord{}
	var sources []SymbolRecord
	for _, path := range sortedKeysOf(recordsByFile) {
		content, ok := readContent(path)
		if !ok || !(isKubernetesPath(path) || looksLikeKubernetesManifest(content)) {
			continue
		}
		for _, symbol := range recordsByFile[path] {
			if symbol.Language != "YAML" || symbol.Kind != "resource" {
				continue
			}
			kind, name, ok := strings.Cut(symbol.QualifiedName, ".")
			if !ok {
				continue
			}
			resources[kubernetesResourceKey(kind, name)] = symbol
			sources = append(sources, symbol)
		}
	}
	if len(resources) == 0 || len(sources) == 0 {
		return nil
	}
	var relations []RelationRecord
	emitted := map[string]bool{}
	for _, source := range sources {
		content, ok := readContent(source.FilePath)
		if !ok {
			continue
		}
		for _, dep := range kubernetesResourceReferences(content) {
			target, ok := resources[kubernetesResourceKey(dep.Kind, dep.Name)]
			if !ok || target.ID == source.ID {
				continue
			}
			key := source.ID + "\x00" + target.ID + "\x00" + dep.EvidenceKind
			if emitted[key] {
				continue
			}
			emitted[key] = true
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        source.ID,
				ToID:          target.ID,
				Type:          "RESOURCE_DEPENDS_ON",
				Confidence:    minFloat(dep.Confidence+0.08, 0.9),
				Reason:        "Kubernetes resource reference resolved to local resource manifest",
				RelationScope: "workspace",
				Resolution:    "exact",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      dep.EvidenceKind,
					FilePath:  source.FilePath,
					StartLine: source.StartLine,
					EndLine:   source.EndLine,
					Detail:    dep.Kind + "/" + dep.Name,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}

func kubernetesResourceKey(kind, name string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + "\x00" + strings.Trim(strings.TrimSpace(name), `"'`)
}

type kubernetesResourceInfo struct {
	Symbol              SymbolRecord
	Kind                string
	Name                string
	Labels              map[string]string
	Selector            map[string]string
	SelectorTargetKinds map[string]bool
	SelectorEvidence    string
	SelectorReason      string
	SelectorConfidence  float64
}

type yamlPathFrame struct {
	indent int
	key    string
}

func kubernetesSelectorResourceRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var resources []kubernetesResourceInfo
	for _, path := range sortedKeysOf(recordsByFile) {
		content, ok := readContent(path)
		if !ok || !(isKubernetesPath(path) || looksLikeKubernetesManifest(content)) {
			continue
		}
		for _, symbol := range recordsByFile[path] {
			if symbol.Language != "YAML" || symbol.Kind != "resource" {
				continue
			}
			kind, name, ok := strings.Cut(symbol.QualifiedName, ".")
			if !ok {
				continue
			}
			labels := yamlMapAtPath(content, "spec", "template", "metadata", "labels")
			if len(labels) == 0 {
				labels = yamlMapAtPath(content, "metadata", "labels")
			}
			selector, targetKinds, evidence, reason, confidence := kubernetesSelectorForResource(strings.ToLower(kind), content)
			resources = append(resources, kubernetesResourceInfo{
				Symbol:              symbol,
				Kind:                strings.ToLower(kind),
				Name:                name,
				Labels:              labels,
				Selector:            selector,
				SelectorTargetKinds: targetKinds,
				SelectorEvidence:    evidence,
				SelectorReason:      reason,
				SelectorConfidence:  confidence,
			})
		}
	}
	var relations []RelationRecord
	for _, source := range resources {
		if len(source.Selector) == 0 {
			continue
		}
		for _, target := range resources {
			if target.Symbol.ID == source.Symbol.ID || len(target.Labels) == 0 {
				continue
			}
			if len(source.SelectorTargetKinds) > 0 && !source.SelectorTargetKinds[target.Kind] {
				continue
			}
			if !kubernetesSelectorMatches(source.Selector, target.Labels) {
				continue
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        source.Symbol.ID,
				ToID:          target.Symbol.ID,
				Type:          "RESOURCE_DEPENDS_ON",
				Confidence:    source.SelectorConfidence,
				Reason:        source.SelectorReason,
				RelationScope: "file",
				Resolution:    "exact",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      source.SelectorEvidence,
					FilePath:  source.Symbol.FilePath,
					StartLine: source.Symbol.StartLine,
					EndLine:   source.Symbol.EndLine,
					Detail:    source.Name + " -> " + target.Name,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}

func kubernetesSelectorForResource(kind, content string) (map[string]string, map[string]bool, string, string, float64) {
	switch kind {
	case "service":
		return yamlMapAtPath(content, "spec", "selector"),
			kubernetesWorkloadKinds(),
			"kubernetes_service_selector",
			"Kubernetes Service selector matches workload labels",
			0.88
	case "poddisruptionbudget":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			kubernetesWorkloadKinds(),
			"kubernetes_pdb_selector",
			"Kubernetes PodDisruptionBudget selector matches workload labels",
			0.84
	case "networkpolicy":
		return yamlMapAtPath(content, "spec", "podSelector", "matchLabels"),
			kubernetesWorkloadKinds(),
			"kubernetes_network_policy_selector",
			"Kubernetes NetworkPolicy podSelector matches workload labels",
			0.82
	case "servicemonitor":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			map[string]bool{"service": true},
			"kubernetes_service_monitor_selector",
			"Kubernetes ServiceMonitor selector matches Service labels",
			0.8
	case "podmonitor":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			kubernetesWorkloadKinds(),
			"kubernetes_pod_monitor_selector",
			"Kubernetes PodMonitor selector matches workload labels",
			0.8
	default:
		return nil, nil, "", "", 0
	}
}

func kubernetesWorkloadKinds() map[string]bool {
	return map[string]bool{
		"pod":                   true,
		"deployment":            true,
		"statefulset":           true,
		"daemonset":             true,
		"replicaset":            true,
		"replicationcontroller": true,
		"job":                   true,
		"cronjob":               true,
	}
}

func yamlMapAtPath(content string, path ...string) map[string]string {
	var stack []yamlPathFrame
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		if yamlIgnoreLine(line) {
			continue
		}
		indent := yamlIndent(line)
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		key, ok := yamlLineKey(line)
		if !ok {
			continue
		}
		value := strings.Trim(strings.TrimSpace(yamlLineValue(line)), `"'`)
		if len(stack) == len(path) && yamlPathMatches(stack, path) && value != "" {
			out[key] = value
		}
		stack = append(stack, yamlPathFrame{indent: indent, key: key})
	}
	return out
}

func yamlPathMatches(stack []yamlPathFrame, path []string) bool {
	if len(stack) != len(path) {
		return false
	}
	for i := range path {
		if stack[i].key != path[i] {
			return false
		}
	}
	return true
}

func kubernetesSelectorMatches(selector, labels map[string]string) bool {
	for key, want := range selector {
		if labels[key] != want {
			return false
		}
	}
	return true
}

func kustomizeResourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	for _, path := range sortedKeysOf(recordsByFile) {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		source, ok := firstSymbol(recordsByFile[path], "Kustomize", "resources", "patches", "components")
		if !ok {
			continue
		}
		for _, ref := range kustomizeFileReferences(content) {
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        source.ID,
				ToID:          externalID("config", "kustomize/file/"+ref),
				Type:          "RESOURCE_DEPENDS_ON",
				Confidence:    0.82,
				Reason:        "Kustomize manifest references another resource file",
				RelationScope: "external",
				Resolution:    "pattern",
				TargetKind:    "config",
				Evidence: []Evidence{{
					Kind:      "kustomize_file_reference",
					FilePath:  source.FilePath,
					StartLine: source.StartLine,
					EndLine:   source.EndLine,
					Detail:    ref,
				}},
				WarningCodes: []string{"WEAK_PATTERN"},
			})
		}
	}
	return relations
}

func kustomizeFileReferences(content string) []string {
	var refs []string
	inList := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		switch {
		case regexp.MustCompile(`^(resources|patches|components):\s*$`).MatchString(trimmed):
			inList = true
			continue
		case regexp.MustCompile(`^[A-Za-z0-9_-]+:`).MatchString(trimmed):
			inList = false
		}
		if !inList || !strings.HasPrefix(trimmed, "-") {
			continue
		}
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		ref = strings.Trim(ref, `"'`)
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return dedupeStrings(refs)
}

func composeResourceDependsOnRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	for _, path := range sortedKeysOf(recordsByFile) {
		if !yamlDockerComposePath(path) {
			continue
		}
		content, ok := readContent(path)
		if !ok {
			continue
		}
		services := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[path] {
			if !composeServiceSymbol(symbol) {
				continue
			}
			services[composeServiceName(symbol)] = symbol
		}
		if len(services) == 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, symbol := range recordsByFile[path] {
			if !composeServiceSymbol(symbol) {
				continue
			}
			body := symbolBlockFromLines(lines, symbol)
			for _, dep := range composeServiceDependencyRefs(body) {
				target, ok := services[dep]
				if !ok || target.ID == symbol.ID {
					continue
				}
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          target.ID,
					Type:          "RESOURCE_DEPENDS_ON",
					Confidence:    0.9,
					Reason:        "Docker Compose service depends_on references another service",
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "compose_depends_on",
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    composeServiceName(symbol) + " -> " + dep,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

func composeServiceDependencyRefs(block string) []string {
	refs := composeBlockListValues(block, "depends_on")
	refs = append(refs, composeBlockMapKeys(block, "depends_on")...)
	for i := range refs {
		refs[i] = strings.Trim(strings.TrimSpace(refs[i]), `"'`)
	}
	sort.Strings(refs)
	return dedupeStrings(refs)
}

func firstSymbol(symbols []SymbolRecord, language string, preferredNames ...string) (SymbolRecord, bool) {
	for _, name := range preferredNames {
		for _, symbol := range symbols {
			if symbol.Language == language && strings.EqualFold(symbol.Name, name) {
				return symbol, true
			}
		}
	}
	for _, symbol := range symbols {
		if symbol.Language == language {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

func dedupeResourceReferences(refs []resourceReference) []resourceReference {
	seen := map[string]bool{}
	var out []resourceReference
	for _, ref := range refs {
		key := ref.Kind + "/" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func composeServiceSymbol(symbol SymbolRecord) bool {
	return symbol.Language == "YAML" && symbol.Kind == "resource" && strings.HasPrefix(symbol.QualifiedName, "compose.service.")
}

func composeServiceName(symbol SymbolRecord) string {
	return strings.TrimPrefix(symbol.QualifiedName, "compose.service.")
}

func composeServiceConfigTargets(symbol SymbolRecord, content string) []configTarget {
	if !composeServiceSymbol(symbol) {
		return nil
	}
	body := symbolBlockFromLines(strings.Split(content, "\n"), symbol)
	serviceName := composeServiceName(symbol)
	targets := []configTarget{{
		Name:         "compose/service/" + serviceName,
		Confidence:   0.9,
		Reason:       "Docker Compose file declares a service",
		EvidenceKind: "compose_service",
	}}
	for _, image := range composeServiceImages(body) {
		targets = append(targets, configTarget{
			Name:         "compose/image/" + image,
			Confidence:   0.82,
			Reason:       "Docker Compose service references a container image",
			EvidenceKind: "compose_image",
		})
	}
	for _, env := range composeServiceEnvVars(body) {
		targets = append(targets, configTarget{
			Name:         "compose/env/" + env,
			Confidence:   0.78,
			Reason:       "Docker Compose service declares an environment variable",
			EvidenceKind: "compose_env",
		})
	}
	for _, port := range composeServicePorts(body) {
		targets = append(targets, configTarget{
			Name:         "compose/port/" + port,
			Confidence:   0.78,
			Reason:       "Docker Compose service declares a port mapping",
			EvidenceKind: "compose_port",
		})
	}
	return targets
}

func composeServiceImages(block string) []string {
	var images []string
	for _, match := range regexp.MustCompile(`(?im)^\s*image:\s*["']?([^"'\s#]+)`).FindAllStringSubmatch(block, -1) {
		if len(match) == 2 {
			images = append(images, strings.TrimSpace(match[1]))
		}
	}
	sort.Strings(images)
	return dedupeStrings(images)
}

func composeServiceEnvVars(block string) []string {
	var envs []string
	for _, value := range composeBlockListValues(block, "environment") {
		name := value
		if before, _, ok := strings.Cut(value, "="); ok {
			name = before
		}
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if name != "" {
			envs = append(envs, name)
		}
	}
	envs = append(envs, composeBlockMapKeys(block, "environment")...)
	sort.Strings(envs)
	return dedupeStrings(envs)
}

func composeServicePorts(block string) []string {
	var ports []string
	for _, value := range composeBlockListValues(block, "ports") {
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value != "" {
			ports = append(ports, value)
		}
	}
	sort.Strings(ports)
	return dedupeStrings(ports)
}

func composeBlockListValues(block, key string) []string {
	lines := strings.Split(block, "\n")
	var values []string
	inBlock := false
	blockIndent := 0
	for _, line := range lines {
		if yamlIgnoreLine(line) {
			continue
		}
		indent := yamlIndent(line)
		lineKey, hasKey := yamlLineKey(line)
		if inBlock && indent <= blockIndent {
			inBlock = false
		}
		if hasKey && lineKey == key {
			value := strings.Trim(strings.TrimSpace(yamlLineValue(line)), `"'`)
			if value != "" && value != "[]" && value != "{}" {
				values = append(values, value)
			}
			inBlock = true
			blockIndent = indent
			continue
		}
		if !inBlock || indent <= blockIndent {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		if value != "" {
			values = append(values, strings.Trim(value, `"'`))
		}
	}
	return values
}

func composeBlockMapKeys(block, key string) []string {
	lines := strings.Split(block, "\n")
	var keys []string
	inBlock := false
	blockIndent := 0
	childIndent := -1
	for _, line := range lines {
		if yamlIgnoreLine(line) {
			continue
		}
		indent := yamlIndent(line)
		lineKey, hasKey := yamlLineKey(line)
		if inBlock && indent <= blockIndent {
			inBlock = false
			childIndent = -1
		}
		if hasKey && lineKey == key {
			inBlock = true
			blockIndent = indent
			childIndent = -1
			continue
		}
		if !inBlock || !hasKey || indent <= blockIndent {
			continue
		}
		if childIndent < 0 {
			childIndent = indent
		}
		if indent == childIndent {
			keys = append(keys, strings.Trim(lineKey, `"'`))
		}
	}
	sort.Strings(keys)
	return dedupeStrings(keys)
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

func signatureTypeRelations(recordsByFile map[string][]SymbolRecord, symbolsByFile, symbolsByShortName map[string][]SymbolRecord, spec profileSpec) []RelationRecord {
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
			refs := signatureTypeReferences(symbol.Language, symbol.Signature)
			for _, relationType := range []string{"PARAM_TYPE", "RETURNS_TYPE"} {
				if !spec.emits(relationType) {
					continue
				}
				names := refs[relationType]
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
						Type:          relationType,
						Confidence:    confidence,
						Reason:        signatureTypeReason(relationType),
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
	}
	return relations
}

func signatureTypeReason(relationType string) string {
	if relationType == "RETURNS_TYPE" {
		return "return type referenced in signature"
	}
	return "parameter type referenced in signature"
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

func configuresRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
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
		for _, symbol := range recordsByFile[path] {
			for _, cfg := range configTargets(symbol, content) {
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          externalID("config", cfg.Name),
					Type:          "CONFIGURES",
					Confidence:    cfg.Confidence,
					Reason:        cfg.Reason,
					RelationScope: "external",
					Resolution:    "pattern",
					TargetKind:    "config",
					Evidence: []Evidence{{
						Kind:      cfg.EvidenceKind,
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    cfg.Name,
					}},
					WarningCodes: cfg.WarningCodes,
				})
			}
		}
	}
	return relations
}

func fileChangesWithRelations(ctx context.Context, repo, repoKey string, files []FileRecord) []RelationRecord {
	cochanges, err := gitutil.FileCochanges(ctx, repo, 256)
	if err != nil || len(cochanges) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, file := range files {
		known[file.Path] = true
	}
	var relations []RelationRecord
	for _, pair := range cochanges {
		if !known[pair.Left] || !known[pair.Right] {
			continue
		}
		confidence := 0.55
		if pair.Count >= 3 {
			confidence = 0.7
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        fileID(repoKey, pair.Left),
			ToID:          fileID(repoKey, pair.Right),
			Type:          "FILE_CHANGES_WITH",
			Confidence:    confidence,
			Reason:        fmt.Sprintf("files changed together in %d recent commits", pair.Count),
			RelationScope: "workspace",
			Resolution:    "git_history",
			TargetKind:    "file",
			Evidence: []Evidence{{
				Kind:   "git_log",
				Detail: fmt.Sprintf("%d commits", pair.Count),
			}},
			WarningCodes: []string{},
		})
	}
	return relations
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
	varTypes := parameterVarTypes(from.Signature)
	localTypes := localVarTypes(block)
	for name, typeName := range localTypes {
		varTypes[name] = typeName
	}
	paramTypes := parameterVarTypes(from.Signature)
	for name := range localTypes {
		delete(paramTypes, name)
	}

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
				if _, ok := paramTypes[access.Receiver]; ok {
					confidence = 0.83
				}
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
	case "HANDLES_ROUTE", "HANDLES_GRPC", "HANDLES_GRAPHQL", "HANDLES_TRPC", "HTTP_CALLS", "EMITS", "LISTENS_ON", "CONFIGURES":
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
// of an external endpoint. Module-root resolution is handled by
// manifestImportResolver after this relative-import pass.
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

type manifestImportResolver struct {
	goModule          string
	goPackages        map[string]string
	jsPackageName     string
	jsPackageExports  map[string]string
	jsPackageImports  map[string]string
	jsImportMap       map[string]string
	jsModuleFiles     map[string]string
	tsPathMappings    []tsPathMapping
	pythonPackages    []string
	pythonSourceRoots []string
	pythonModules     map[string]string
	pythonNamespaces  map[string]bool
	jvmTypes          map[string]string
	rustCrateName     string
	rustModules       map[string]string
	rustAliases       map[string]string
}

type manifestImportResolution struct {
	Path         string
	Confidence   float64
	Scope        string
	Reason       string
	EvidenceKind string
}

type tsPathMapping struct {
	Pattern string
	Targets []string
}

func buildManifestImportResolver(files []FileRecord, readContent contentReader) manifestImportResolver {
	resolver := manifestImportResolver{goPackages: map[string]string{}, jsPackageExports: map[string]string{}, jsPackageImports: map[string]string{}, jsImportMap: map[string]string{}, jsModuleFiles: map[string]string{}, pythonSourceRoots: []string{"src"}, pythonModules: map[string]string{}, pythonNamespaces: map[string]bool{}, jvmTypes: map[string]string{}, rustModules: map[string]string{}, rustAliases: map[string]string{}}
	if content, ok := readContent("go.mod"); ok {
		resolver.goModule = parseGoModulePath(content)
	}
	if content, ok := readContent("package.json"); ok {
		resolver.jsPackageName = parsePackageJSONName(content)
		resolver.jsPackageExports = parsePackageJSONTargets(content, "exports")
		resolver.jsPackageImports = parsePackageJSONTargets(content, "imports")
	}
	if content, ok := readContent("tsconfig.json"); ok {
		resolver.tsPathMappings = parseTSConfigPaths(content)
	}
	for _, importMapPath := range []string{"import-map.json", "importmap.json"} {
		if content, ok := readContent(importMapPath); ok {
			resolver.jsImportMap = parseJSImportMapTargets(content)
			break
		}
	}
	if content, ok := readContent("pyproject.toml"); ok {
		resolver.pythonPackages = append(resolver.pythonPackages, parsePyProjectName(content))
		resolver.pythonSourceRoots = append(resolver.pythonSourceRoots, parsePyProjectPythonSourceRoots(content)...)
	}
	if content, ok := readContent("setup.cfg"); ok {
		resolver.pythonPackages = append(resolver.pythonPackages, parseSetupCFGName(content))
		resolver.pythonSourceRoots = append(resolver.pythonSourceRoots, parseSetupCFGPythonSourceRoots(content)...)
	}
	resolver.pythonPackages = normalizePythonPackageNames(resolver.pythonPackages)
	if content, ok := readContent("Cargo.toml"); ok {
		resolver.rustCrateName = normalizeRustCrateName(parseCargoPackageName(content))
	}
	var goPaths []string
	var jsPaths []string
	var pyPaths []string
	var jvmPaths []string
	var rustPaths []string
	for _, file := range files {
		if strings.EqualFold(filepath.Ext(file.Path), ".go") {
			goPaths = append(goPaths, filepath.ToSlash(file.Path))
		}
		if jsLikeExtension(filepath.Ext(file.Path)) {
			jsPaths = append(jsPaths, filepath.ToSlash(file.Path))
		}
		if strings.EqualFold(filepath.Ext(file.Path), ".py") {
			pyPaths = append(pyPaths, filepath.ToSlash(file.Path))
		}
		if jvmLikeExtension(filepath.Ext(file.Path)) {
			jvmPaths = append(jvmPaths, filepath.ToSlash(file.Path))
		}
		if strings.EqualFold(filepath.Ext(file.Path), ".rs") {
			rustPaths = append(rustPaths, filepath.ToSlash(file.Path))
		}
	}
	sort.Slice(goPaths, func(i, j int) bool {
		leftTest := strings.HasSuffix(goPaths[i], "_test.go")
		rightTest := strings.HasSuffix(goPaths[j], "_test.go")
		if leftTest != rightTest {
			return !leftTest
		}
		return goPaths[i] < goPaths[j]
	})
	if resolver.goModule != "" {
		for _, path := range goPaths {
			dir := filepath.ToSlash(filepath.Dir(path))
			importPath := resolver.goModule
			if dir != "." {
				importPath += "/" + dir
			}
			if _, exists := resolver.goPackages[importPath]; !exists {
				resolver.goPackages[importPath] = path
			}
		}
	}
	sort.Strings(jsPaths)
	for _, path := range jsPaths {
		key := strings.TrimSuffix(path, filepath.Ext(path))
		if _, exists := resolver.jsModuleFiles[key]; !exists {
			resolver.jsModuleFiles[key] = path
		}
		if strings.HasSuffix(key, "/index") {
			dir := strings.TrimSuffix(key, "/index")
			if dir != "" {
				if _, exists := resolver.jsModuleFiles[dir]; !exists {
					resolver.jsModuleFiles[dir] = path
				}
			}
		}
	}
	sort.Strings(pyPaths)
	resolver.pythonSourceRoots = normalizePythonSourceRoots(append(resolver.pythonSourceRoots, inferPythonSourceRoots(pyPaths)...))
	pyFileSet := map[string]bool{}
	for _, path := range pyPaths {
		pyFileSet[path] = true
	}
	for _, path := range pyPaths {
		for _, key := range pythonModuleKeysForPath(path, resolver.pythonSourceRoots, pyFileSet) {
			if _, exists := resolver.pythonModules[key.Module]; !exists {
				resolver.pythonModules[key.Module] = path
				if key.Namespace {
					resolver.pythonNamespaces[key.Module] = true
				}
			}
		}
	}
	sort.Strings(jvmPaths)
	for _, path := range jvmPaths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		if qualifiedName := jvmQualifiedTypeName(path, content); qualifiedName != "" {
			if _, exists := resolver.jvmTypes[qualifiedName]; !exists {
				resolver.jvmTypes[qualifiedName] = path
			}
		}
	}
	sort.Strings(rustPaths)
	for _, path := range rustPaths {
		for _, module := range rustModuleKeysForPath(path) {
			if _, exists := resolver.rustModules[module]; !exists {
				resolver.rustModules[module] = path
			}
		}
	}
	for _, path := range rustPaths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		for _, alias := range rustAliasesForFile(path, content) {
			if alias.From != "" && alias.To != "" {
				resolver.rustAliases[alias.From] = alias.To
			}
		}
	}
	return resolver
}

func parseGoModulePath(content string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
		}
	}
	return ""
}

func parsePackageJSONName(content string) string {
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.Name)
}

func parsePackageJSONTargets(content, field string) map[string]string {
	var data map[string]any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return map[string]string{}
	}
	raw, ok := data[field]
	if !ok {
		return map[string]string{}
	}
	out := map[string]string{}
	if entries, ok := raw.(map[string]any); ok {
		if packageJSONTargetObjectIsEntryMap(field, entries) {
			for key, value := range entries {
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				if target := jsManifestTarget(value); target != "" {
					out[key] = target
				}
			}
			return out
		}
		if field == "exports" {
			if target := jsManifestTarget(entries); target != "" {
				out["."] = target
			}
		}
		return out
	}
	if field == "exports" {
		if target := jsManifestTarget(raw); target != "" {
			out["."] = target
		}
	}
	return out
}

func packageJSONTargetObjectIsEntryMap(field string, entries map[string]any) bool {
	switch field {
	case "imports":
		return true
	case "exports":
		for key := range entries {
			if strings.HasPrefix(strings.TrimSpace(key), ".") {
				return true
			}
		}
	}
	return false
}

func parseJSImportMapTargets(content string) map[string]string {
	var data struct {
		Imports map[string]any `json:"imports"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for key, value := range data.Imports {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if target := jsManifestTarget(value); target != "" {
			out[key] = target
		}
	}
	return out
}

func jsManifestTarget(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if target := jsManifestTarget(item); target != "" {
				return target
			}
		}
	case map[string]any:
		for _, key := range []string{"import", "module", "browser", "default", "types", "require"} {
			if target := jsManifestTarget(v[key]); target != "" {
				return target
			}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if target := jsManifestTarget(v[key]); target != "" {
				return target
			}
		}
	}
	return ""
}

func parseTSConfigPaths(content string) []tsPathMapping {
	var data struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil {
		return nil
	}
	var mappings []tsPathMapping
	for pattern, targets := range data.CompilerOptions.Paths {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || len(targets) == 0 {
			continue
		}
		var cleanTargets []string
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target != "" {
				cleanTargets = append(cleanTargets, target)
			}
		}
		if len(cleanTargets) > 0 {
			mappings = append(mappings, tsPathMapping{Pattern: pattern, Targets: cleanTargets})
		}
	}
	sort.Slice(mappings, func(i, j int) bool {
		return mappings[i].Pattern < mappings[j].Pattern
	})
	return mappings
}

func parsePyProjectName(content string) string {
	inProject := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inProject = line == "[project]"
			continue
		}
		if inProject && strings.HasPrefix(line, "name") {
			if name, ok := parseSimpleConfigValue(line); ok {
				return name
			}
		}
	}
	return ""
}

func parsePyProjectPythonSourceRoots(content string) []string {
	inSetuptoolsFind := false
	var roots []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSetuptoolsFind = line == "[tool.setuptools.packages.find]"
			continue
		}
		if inSetuptoolsFind && strings.HasPrefix(line, "where") {
			roots = append(roots, parsePythonSourceRootValues(line)...)
		}
	}
	return normalizePythonSourceRoots(roots)
}

func parseSetupCFGName(content string) string {
	inMetadata := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inMetadata = strings.EqualFold(line, "[metadata]")
			continue
		}
		if inMetadata && strings.HasPrefix(strings.ToLower(line), "name") {
			if name, ok := parseSimpleConfigValue(line); ok {
				return name
			}
		}
	}
	return ""
}

func parseSetupCFGPythonSourceRoots(content string) []string {
	inFind := false
	var roots []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inFind = strings.EqualFold(line, "[options.packages.find]")
			continue
		}
		if inFind && strings.HasPrefix(strings.ToLower(line), "where") {
			roots = append(roots, parsePythonSourceRootValues(line)...)
		}
	}
	return normalizePythonSourceRoots(roots)
}

func parseCargoPackageName(content string) string {
	inPackage := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inPackage = line == "[package]"
			continue
		}
		if inPackage && strings.HasPrefix(strings.ToLower(line), "name") {
			if name, ok := parseSimpleConfigValue(line); ok {
				return name
			}
		}
	}
	return ""
}

func parseSimpleConfigValue(line string) (string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 || strings.ToLower(strings.TrimSpace(parts[0])) != "name" {
		return "", false
	}
	value := strings.TrimSpace(parts[1])
	value = strings.Trim(value, `"'`)
	return strings.TrimSpace(value), strings.TrimSpace(value) != ""
}

func stripTOMLComment(line string) string {
	inString := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = !inString
			continue
		}
		if !inString && ch == '#' {
			return line[:i]
		}
	}
	return line
}

func stripJSONLineComments(content string) string {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		inString := false
		escaped := false
		for i := 0; i < len(line)-1; i++ {
			ch := line[i]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && inString {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if !inString && ch == '/' && line[i+1] == '/' {
				line = line[:i]
				break
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (resolver manifestImportResolver) resolve(importingPath, spec string) (manifestImportResolution, bool) {
	ext := strings.ToLower(filepath.Ext(importingPath))
	if ext == ".go" {
		return resolver.resolveGoImport(importingPath, spec)
	}
	if jsLikeExtension(ext) {
		return resolver.resolveJSImport(importingPath, spec)
	}
	if ext == ".py" {
		return resolver.resolvePythonImport(importingPath, spec)
	}
	if jvmLikeExtension(ext) {
		return resolver.resolveJVMImport(importingPath, spec)
	}
	if ext == ".rs" {
		return resolver.resolveRustImport(importingPath, spec)
	}
	return manifestImportResolution{}, false
}

func (resolver manifestImportResolver) resolveGoImport(importingPath, spec string) (manifestImportResolution, bool) {
	if strings.ToLower(filepath.Ext(importingPath)) != ".go" {
		return manifestImportResolution{}, false
	}
	spec = strings.TrimSpace(spec)
	if spec == "" || resolver.goModule == "" || (spec != resolver.goModule && !strings.HasPrefix(spec, resolver.goModule+"/")) {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.goPackages[spec]
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   0.93,
		Scope:        "module",
		Reason:       "Go module import resolved through go.mod",
		EvidenceKind: "go_mod_import",
	}, true
}

func (resolver manifestImportResolver) resolveJSImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasPrefix(spec, ".") {
		return manifestImportResolution{}, false
	}
	if resolver.jsPackageName != "" && (spec == resolver.jsPackageName || strings.HasPrefix(spec, resolver.jsPackageName+"/")) {
		exportKey := "."
		if spec != resolver.jsPackageName {
			exportKey = "./" + strings.TrimPrefix(strings.TrimPrefix(spec, resolver.jsPackageName), "/")
		}
		if targetModule, ok := resolver.resolveJSTargetMap(resolver.jsPackageExports, exportKey); ok {
			if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
				return manifestImportResolution{
					Path:         path,
					Confidence:   0.92,
					Scope:        "module",
					Reason:       "JS/TS package export resolved through package.json exports",
					EvidenceKind: "package_exports_import",
				}, true
			}
		}
		module := strings.TrimPrefix(exportKey, "./")
		if module == "." || module == "" {
			module = "index"
		}
		if path, ok := resolver.resolveJSModulePath(module); ok && path != filepath.ToSlash(importingPath) {
			return manifestImportResolution{
				Path:         path,
				Confidence:   0.9,
				Scope:        "module",
				Reason:       "JS/TS package self-import resolved through package.json",
				EvidenceKind: "package_json_import",
			}, true
		}
	}
	if strings.HasPrefix(spec, "#") {
		if targetModule, ok := resolver.resolveJSTargetMap(resolver.jsPackageImports, spec); ok {
			if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
				return manifestImportResolution{
					Path:         path,
					Confidence:   0.91,
					Scope:        "module",
					Reason:       "JS/TS package import alias resolved through package.json imports",
					EvidenceKind: "package_imports_import",
				}, true
			}
		}
	}
	if targetModule, ok := resolver.resolveJSTargetMap(resolver.jsImportMap, spec); ok {
		if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
			return manifestImportResolution{
				Path:         path,
				Confidence:   0.89,
				Scope:        "module",
				Reason:       "JS/TS import resolved through import map",
				EvidenceKind: "import_map_import",
			}, true
		}
	}
	for _, mapping := range resolver.tsPathMappings {
		if targetModule, ok := applyTSPathMapping(mapping, spec); ok {
			if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
				return manifestImportResolution{
					Path:         path,
					Confidence:   0.9,
					Scope:        "module",
					Reason:       "JS/TS path alias resolved through tsconfig.json",
					EvidenceKind: "tsconfig_paths_import",
				}, true
			}
		}
	}
	return manifestImportResolution{}, false
}

func (resolver manifestImportResolver) resolveJSTargetMap(targets map[string]string, spec string) (string, bool) {
	if len(targets) == 0 {
		return "", false
	}
	spec = filepath.ToSlash(strings.TrimSpace(spec))
	if target, ok := targets[spec]; ok && strings.TrimSpace(target) != "" {
		return target, true
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		if strings.Contains(key, "*") || strings.HasSuffix(key, "/") {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	for _, key := range keys {
		target := strings.TrimSpace(targets[key])
		if target == "" {
			continue
		}
		if strings.Contains(key, "*") {
			if value, ok := applyJSWildcardTarget(key, target, spec); ok {
				return value, true
			}
			continue
		}
		if strings.HasSuffix(key, "/") && strings.HasPrefix(spec, key) {
			return filepath.ToSlash(target) + strings.TrimPrefix(spec, key), true
		}
	}
	return "", false
}

func applyJSWildcardTarget(pattern, target, spec string) (string, bool) {
	star := strings.Index(pattern, "*")
	if star < 0 {
		return "", false
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
		return "", false
	}
	wildcard := strings.TrimSuffix(strings.TrimPrefix(spec, prefix), suffix)
	if strings.Contains(target, "*") {
		return strings.Replace(filepath.ToSlash(target), "*", wildcard, 1), true
	}
	return target, true
}

func (resolver manifestImportResolver) resolveJSModulePath(module string) (string, bool) {
	module = strings.Trim(strings.TrimSpace(filepath.ToSlash(module)), "/")
	module = strings.TrimPrefix(module, "./")
	if module == "" {
		return "", false
	}
	if jsLikeExtension(filepath.Ext(module)) {
		module = strings.TrimSuffix(module, filepath.Ext(module))
	}
	if path, ok := resolver.jsModuleFiles[module]; ok {
		return path, true
	}
	return "", false
}

func applyTSPathMapping(mapping tsPathMapping, spec string) (string, bool) {
	pattern := filepath.ToSlash(strings.TrimSpace(mapping.Pattern))
	spec = filepath.ToSlash(strings.TrimSpace(spec))
	star := strings.Index(pattern, "*")
	if star < 0 {
		if spec != pattern {
			return "", false
		}
		for _, target := range mapping.Targets {
			target = filepath.ToSlash(strings.TrimSpace(target))
			if target != "" {
				return target, true
			}
		}
		return "", false
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
		return "", false
	}
	wildcard := strings.TrimSuffix(strings.TrimPrefix(spec, prefix), suffix)
	for _, target := range mapping.Targets {
		target = filepath.ToSlash(strings.TrimSpace(target))
		if target == "" {
			continue
		}
		if strings.Contains(target, "*") {
			return strings.Replace(target, "*", wildcard, 1), true
		}
		return target, true
	}
	return "", false
}

func jsLikeExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".js", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func (resolver manifestImportResolver) resolvePythonImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = strings.Trim(strings.TrimSpace(spec), ".")
	if spec == "" {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.pythonModules[spec]
	if !ok {
		return manifestImportResolution{}, false
	}
	if path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	confidence := 0.88
	reason := "Python module import resolved through local module files"
	evidenceKind := "python_module_import"
	if resolver.pythonNamespaces[spec] {
		confidence = 0.89
		reason = "Python namespace package import resolved through discovered source roots"
		evidenceKind = "python_namespace_import"
	}
	for _, name := range resolver.pythonPackages {
		if spec == name || strings.HasPrefix(spec, name+".") {
			confidence = 0.9
			reason = "Python package import resolved through project metadata"
			evidenceKind = "python_project_import"
			break
		}
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   confidence,
		Scope:        "module",
		Reason:       reason,
		EvidenceKind: evidenceKind,
	}, true
}

type pythonModuleKey struct {
	Module    string
	Namespace bool
}

func pythonModuleKeysForPath(path string, sourceRoots []string, pyFileSet map[string]bool) []pythonModuleKey {
	path = filepath.ToSlash(path)
	if !strings.HasSuffix(path, ".py") {
		return nil
	}
	withoutExt := strings.TrimSuffix(path, ".py")
	if strings.HasSuffix(withoutExt, "/__init__") {
		withoutExt = strings.TrimSuffix(withoutExt, "/__init__")
	}
	var keys []pythonModuleKey
	seen := map[string]bool{}
	add := func(module string, namespace bool) {
		module = strings.Trim(strings.ReplaceAll(filepath.ToSlash(module), "/", "."), ".")
		if module != "" && !seen[module] {
			seen[module] = true
			keys = append(keys, pythonModuleKey{Module: module, Namespace: namespace})
		}
	}
	add(withoutExt, false)
	for _, root := range sourceRoots {
		root = strings.Trim(filepath.ToSlash(root), "/")
		if root == "" || !strings.HasPrefix(withoutExt, root+"/") {
			continue
		}
		add(strings.TrimPrefix(withoutExt, root+"/"), pythonPathUnderNamespaceRoot(path, root, pyFileSet))
	}
	return keys
}

func parsePythonSourceRootValues(line string) []string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "where") {
		return nil
	}
	value := strings.TrimSpace(parts[1])
	if value == "" {
		return nil
	}
	var roots []string
	if strings.HasPrefix(value, "[") {
		matches := regexp.MustCompile(`["']([^"']+)["']`).FindAllStringSubmatch(value, -1)
		for _, match := range matches {
			roots = append(roots, match[1])
		}
		return roots
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			roots = append(roots, part)
		}
	}
	return roots
}

func normalizePythonSourceRoots(roots []string) []string {
	seen := map[string]bool{}
	var normalized []string
	for _, root := range roots {
		root = strings.Trim(filepath.ToSlash(strings.TrimSpace(root)), "/")
		if root == "" || root == "." || seen[root] {
			continue
		}
		seen[root] = true
		normalized = append(normalized, root)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if len(normalized[i]) == len(normalized[j]) {
			return normalized[i] < normalized[j]
		}
		return len(normalized[i]) > len(normalized[j])
	})
	return normalized
}

func inferPythonSourceRoots(paths []string) []string {
	seen := map[string]bool{}
	var roots []string
	for _, path := range paths {
		parts := strings.Split(filepath.ToSlash(path), "/")
		for i, part := range parts {
			if part != "src" || i >= len(parts)-1 {
				continue
			}
			root := strings.Join(parts[:i+1], "/")
			if root != "" && !seen[root] {
				seen[root] = true
				roots = append(roots, root)
			}
		}
	}
	return normalizePythonSourceRoots(roots)
}

func pythonPathUnderNamespaceRoot(path, sourceRoot string, pyFileSet map[string]bool) bool {
	withoutRoot := strings.TrimPrefix(filepath.ToSlash(path), strings.Trim(filepath.ToSlash(sourceRoot), "/")+"/")
	parts := strings.Split(withoutRoot, "/")
	if len(parts) < 2 || parts[0] == "" {
		return false
	}
	return !pyFileSet[strings.Trim(filepath.ToSlash(sourceRoot), "/")+"/"+parts[0]+"/__init__.py"]
}

func normalizePythonPackageNames(names []string) []string {
	seen := map[string]bool{}
	var normalized []string
	for _, name := range names {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		name = strings.ReplaceAll(name, "-", "_")
		if !seen[name] {
			seen[name] = true
			normalized = append(normalized, name)
		}
	}
	sort.Strings(normalized)
	return normalized
}

func (resolver manifestImportResolver) resolveJVMImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasSuffix(spec, ".*") {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.jvmTypes[spec]
	if !ok {
		probe := spec
		for strings.Contains(probe, ".") {
			probe = probe[:strings.LastIndex(probe, ".")]
			if path, ok = resolver.jvmTypes[probe]; ok {
				break
			}
		}
	}
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   0.9,
		Scope:        "module",
		Reason:       "JVM package import resolved through package declaration",
		EvidenceKind: "jvm_package_import",
	}, true
}

func jvmQualifiedTypeName(path, content string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base == "" {
		return ""
	}
	pkg := ""
	re := regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_\.]*)\s*;?`)
	if match := re.FindStringSubmatch(content); len(match) == 2 {
		pkg = strings.TrimSpace(match[1])
	}
	if pkg == "" {
		return base
	}
	return pkg + "." + base
}

func jvmLikeExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".java", ".kt", ".kts", ".scala", ".sc":
		return true
	default:
		return false
	}
}

func (resolver manifestImportResolver) resolveRustImport(importingPath, spec string) (manifestImportResolution, bool) {
	module := normalizeRustImportSpec(spec)
	if module == "" {
		return manifestImportResolution{}, false
	}
	evidenceKind := "rust_crate_import"
	if strings.HasPrefix(module, "crate::") {
		module = strings.TrimPrefix(module, "crate::")
	} else if resolver.rustCrateName != "" && (module == resolver.rustCrateName || strings.HasPrefix(module, resolver.rustCrateName+"::")) {
		module = strings.TrimPrefix(strings.TrimPrefix(module, resolver.rustCrateName), "::")
		evidenceKind = "cargo_package_import"
	} else if strings.HasPrefix(module, "self::") {
		module = strings.TrimPrefix(module, "self::")
	} else {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.resolveRustModulePath(module)
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   0.88,
		Scope:        "module",
		Reason:       "Rust crate import resolved through local module files",
		EvidenceKind: evidenceKind,
	}, true
}

func (resolver manifestImportResolver) resolveRustModulePath(module string) (string, bool) {
	module = strings.Trim(module, ":")
	seen := map[string]bool{}
	for module != "" {
		if path, ok := resolver.rustModules[module]; ok {
			return path, true
		}
		if seen[module] {
			break
		}
		seen[module] = true
		if expanded, ok := resolver.expandRustAlias(module); ok && expanded != module {
			module = expanded
			continue
		}
		idx := strings.LastIndex(module, "::")
		if idx < 0 {
			break
		}
		module = module[:idx]
	}
	return "", false
}

func (resolver manifestImportResolver) expandRustAlias(module string) (string, bool) {
	probe := strings.Trim(module, ":")
	for probe != "" {
		if target, ok := resolver.rustAliases[probe]; ok {
			suffix := strings.TrimPrefix(strings.TrimPrefix(module, probe), "::")
			if suffix == "" {
				return target, true
			}
			return strings.Trim(target+"::"+suffix, ":"), true
		}
		idx := strings.LastIndex(probe, "::")
		if idx < 0 {
			break
		}
		probe = probe[:idx]
	}
	return "", false
}

func normalizeRustImportSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	if idx := strings.Index(spec, " as "); idx >= 0 {
		spec = spec[:idx]
	}
	if idx := strings.Index(spec, "::{"); idx >= 0 {
		spec = spec[:idx]
	}
	spec = strings.Trim(spec, "{} ")
	return strings.Trim(spec, ":")
}

func rustModuleKeysForPath(path string) []string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "src/") || !strings.HasSuffix(path, ".rs") {
		return nil
	}
	rel := strings.TrimSuffix(strings.TrimPrefix(path, "src/"), ".rs")
	if rel == "lib" || rel == "main" {
		return nil
	}
	if strings.HasSuffix(rel, "/mod") {
		rel = strings.TrimSuffix(rel, "/mod")
	}
	module := strings.ReplaceAll(rel, "/", "::")
	if module == "" {
		return nil
	}
	return []string{module}
}

type rustAlias struct {
	From string
	To   string
}

func rustAliasesForFile(path, content string) []rustAlias {
	current := rustCurrentModuleForPath(path)
	var aliases []rustAlias
	aliases = append(aliases, rustPathModuleAliases(path, content, current)...)
	aliases = append(aliases, rustPubUseAliases(content, current)...)
	return aliases
}

func rustPathModuleAliases(path, content, current string) []rustAlias {
	re := regexp.MustCompile(`(?m)#\s*\[\s*path\s*=\s*"([^"]+)"\s*\]\s*(?:pub\s+)?mod\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)
	var aliases []rustAlias
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		targetPath := filepath.ToSlash(filepath.Join(filepath.Dir(path), match[1]))
		keys := rustModuleKeysForPath(targetPath)
		if len(keys) == 0 {
			continue
		}
		from := rustJoinModule(current, match[2])
		aliases = append(aliases, rustAlias{From: from, To: keys[0]})
	}
	return aliases
}

func rustPubUseAliases(content, current string) []rustAlias {
	re := regexp.MustCompile(`(?m)^\s*pub\s+use\s+([^;]+);`)
	var aliases []rustAlias
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		source, exported, ok := parseRustPubUseAlias(match[1], current)
		if ok {
			aliases = append(aliases, rustAlias{From: exported, To: source})
		}
	}
	return aliases
}

func parseRustPubUseAlias(expr, current string) (string, string, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.Contains(expr, "*") || strings.Contains(expr, "{") || strings.Contains(expr, "}") {
		return "", "", false
	}
	alias := ""
	if idx := strings.LastIndex(expr, " as "); idx >= 0 {
		alias = strings.TrimSpace(expr[idx+4:])
		expr = strings.TrimSpace(expr[:idx])
	}
	expr = normalizeRustImportSpec(expr)
	target := rustNormalizeUsePath(expr, current)
	if target == "" {
		return "", "", false
	}
	if alias == "" {
		parts := strings.Split(target, "::")
		alias = parts[len(parts)-1]
	}
	exported := rustJoinModule(current, alias)
	return target, exported, true
}

func rustNormalizeUsePath(path, current string) string {
	path = strings.Trim(path, ": ")
	switch {
	case strings.HasPrefix(path, "crate::"):
		return strings.TrimPrefix(path, "crate::")
	case strings.HasPrefix(path, "self::"):
		return rustJoinModule(current, strings.TrimPrefix(path, "self::"))
	case strings.HasPrefix(path, "super::"):
		parent := current
		for strings.HasPrefix(path, "super::") {
			path = strings.TrimPrefix(path, "super::")
			if idx := strings.LastIndex(parent, "::"); idx >= 0 {
				parent = parent[:idx]
			} else {
				parent = ""
			}
		}
		return rustJoinModule(parent, path)
	default:
		return rustJoinModule(current, path)
	}
}

func rustCurrentModuleForPath(path string) string {
	keys := rustModuleKeysForPath(path)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func rustJoinModule(prefix, suffix string) string {
	prefix = strings.Trim(prefix, ":")
	suffix = strings.Trim(suffix, ":")
	if prefix == "" {
		return suffix
	}
	if suffix == "" {
		return prefix
	}
	return prefix + "::" + suffix
}

func normalizeRustCrateName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	return strings.ReplaceAll(name, "-", "_")
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
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z0-9_\.\*]+)`))
	case ".py":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*(?:from\s+(\.*[A-Za-z0-9_\.]+)\s+import|import\s+([A-Za-z0-9_\.]+))`))
	case ".js", ".jsx", ".ts", ".tsx":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]|^\s*import\s+['"]([^'"]+)['"]|require\s*\(\s*['"]([^'"]+)['"]\s*\)|import\s*\(\s*['"]([^'"]+)['"]\s*\)`))
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
	case ".go":
		return importedGoNames(content)
	case ".js", ".jsx", ".ts", ".tsx":
		return importedJavaScriptNames(content)
	case ".py":
		return importedPythonNames(content)
	default:
		return map[string][]string{}
	}
}

func importedGoNames(content string) map[string][]string {
	imports := map[string][]string{}
	add := func(alias, module string) {
		if module == "" || alias == "." || alias == "_" {
			return
		}
		if alias == "" {
			alias = goImportDefaultName(module)
		}
		if alias != "" {
			imports[alias] = append(imports[alias], module)
		}
	}
	singleImport := regexp.MustCompile(`^\s*import\s+(?:(\w+)\s+)?["]([^"]+)["]`)
	blockImport := regexp.MustCompile(`^\s*(?:(\w+)\s+)?["]([^"]+)["]`)
	inBlock := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !inBlock {
			if strings.HasPrefix(line, "import (") {
				inBlock = true
				continue
			}
			if matches := singleImport.FindStringSubmatch(line); len(matches) == 3 {
				add(matches[1], matches[2])
			}
			continue
		}
		if strings.HasPrefix(line, ")") {
			inBlock = false
			continue
		}
		if matches := blockImport.FindStringSubmatch(line); len(matches) == 3 {
			add(matches[1], matches[2])
		}
	}
	return imports
}

func goImportDefaultName(module string) string {
	module = strings.Trim(strings.TrimSpace(module), "/")
	if module == "" {
		return ""
	}
	base := filepath.Base(filepath.ToSlash(module))
	base = strings.TrimSuffix(base, ".git")
	return strings.ReplaceAll(base, "-", "_")
}

func importedJavaScriptNames(content string) map[string][]string {
	imports := map[string][]string{}
	namedImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?\{([^}]+)\}\s+from\s+['"]([^'"]+)['"]`)
	defaultImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	namespaceImport := regexp.MustCompile(`(?m)^\s*import\s+\*\s+as\s+([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	requireNamed := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+\{([^}]+)\}\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	requireDefault := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:await\s+)?(?:require|import)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	for _, match := range namedImport.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			if local := javascriptImportedLocalName(item); local != "" {
				imports[local] = append(imports[local], match[2])
			}
		}
	}
	for _, match := range defaultImport.FindAllStringSubmatch(content, -1) {
		imports[match[1]] = append(imports[match[1]], match[2])
	}
	for _, match := range namespaceImport.FindAllStringSubmatch(content, -1) {
		imports[match[1]] = append(imports[match[1]], match[2])
	}
	for _, match := range requireNamed.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			if local := javascriptImportedLocalName(item); local != "" {
				imports[local] = append(imports[local], match[2])
			}
		}
	}
	for _, match := range requireDefault.FindAllStringSubmatch(content, -1) {
		imports[match[1]] = append(imports[match[1]], match[2])
	}
	return imports
}

func importedJavaScriptBindings(content string) map[string][]jsImportBinding {
	bindings := map[string][]jsImportBinding{}
	add := func(local string, binding jsImportBinding) {
		local = strings.TrimSpace(local)
		if local == "" || binding.Module == "" {
			return
		}
		bindings[local] = append(bindings[local], binding)
	}
	namedImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?\{([^}]+)\}\s+from\s+['"]([^'"]+)['"]`)
	defaultImport := regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	namespaceImport := regexp.MustCompile(`(?m)^\s*import\s+\*\s+as\s+([A-Za-z_$][A-Za-z0-9_$]*)\s+from\s+['"]([^'"]+)['"]`)
	requireNamed := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+\{([^}]+)\}\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	requireDefault := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:await\s+)?(?:require|import)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	for _, match := range namedImport.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			imported, local := javascriptImportNames(item)
			add(local, jsImportBinding{Module: match[2], Imported: imported})
		}
	}
	for _, match := range defaultImport.FindAllStringSubmatch(content, -1) {
		add(match[1], jsImportBinding{Module: match[2], Imported: "default"})
	}
	for _, match := range namespaceImport.FindAllStringSubmatch(content, -1) {
		add(match[1], jsImportBinding{Module: match[2], Namespace: true})
	}
	for _, match := range requireNamed.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			imported, local := javascriptImportNames(item)
			add(local, jsImportBinding{Module: match[2], Imported: imported})
		}
	}
	for _, match := range requireDefault.FindAllStringSubmatch(content, -1) {
		add(match[1], jsImportBinding{Module: match[2]})
	}
	return bindings
}

func javascriptImportedLocalName(item string) string {
	_, local := javascriptImportNames(item)
	return local
}

func javascriptImportNames(item string) (string, string) {
	item = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "type "))
	if item == "" {
		return "", ""
	}
	if before, after, ok := strings.Cut(item, ":"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	parts := strings.Fields(item)
	if len(parts) == 0 {
		return "", ""
	}
	imported := parts[0]
	local := imported
	if len(parts) >= 3 && parts[len(parts)-2] == "as" {
		local = parts[len(parts)-1]
	}
	return imported, local
}

func importedPythonNames(content string) map[string][]string {
	imports := map[string][]string{}
	importRe := regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromRe := regexp.MustCompile(`^\s*from\s+(\.*[A-Za-z_][A-Za-z0-9_\.]*)\s+import\s+(.+)$`)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if matches := importRe.FindStringSubmatch(line); len(matches) == 2 {
			for _, item := range strings.Split(matches[1], ",") {
				module, alias := parsePythonImportItem(item)
				if module == "" {
					continue
				}
				local := alias
				if local == "" {
					local = strings.Split(module, ".")[0]
				}
				imports[local] = append(imports[local], module)
			}
			continue
		}
		if matches := fromRe.FindStringSubmatch(line); len(matches) == 3 {
			module := matches[1]
			for _, item := range strings.Split(matches[2], ",") {
				name, alias := parsePythonImportItem(item)
				if name == "" || name == "*" {
					continue
				}
				local := alias
				if local == "" {
					local = name
				}
				imports[local] = append(imports[local], module)
			}
		}
	}
	return imports
}

func parsePythonImportItem(item string) (name, alias string) {
	item = strings.TrimSpace(item)
	if item == "" {
		return "", ""
	}
	parts := strings.Fields(item)
	if len(parts) >= 3 && parts[len(parts)-2] == "as" {
		return parts[0], parts[len(parts)-1]
	}
	return parts[0], ""
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
	routeLiteralRe      = regexp.MustCompile(`["'](/[A-Za-z0-9_\-/{}:.]*)["']`)
	staticRouteConcatRe = regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\+\s*["']([^"']*)["']`)
	staticStringConstRe = regexp.MustCompile(`(?m)\b(?:(?:const|let|var)\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s*(?::[^=\n]+)?=\s*["'](/[A-Za-z0-9_\-/{}:.]*)["']`)
	staticConcatConstRe = regexp.MustCompile(`(?m)\b(?:(?:const|let|var)\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s*(?::[^=\n]+)?=\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\+\s*["']([^"']*)["']`)
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
	return routeLiteralsWithConstants(content, staticStringConstants(content))
}

func routeLiteralsWithConstants(content string, constants map[string]string) []string {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(content, "\n") {
		if !routingCallRe.MatchString(line) || httpClientRe.MatchString(line) {
			continue // skip client HTTP calls; those are HTTP_CALLS, not routes
		}
		for _, match := range staticRouteConcatRe.FindAllStringSubmatch(line, -1) {
			if len(match) == 3 && constants[match[1]] != "" {
				seen[joinRoutePaths(constants[match[1]], match[2])] = struct{}{}
			}
		}
		for _, match := range routeLiteralRe.FindAllStringSubmatchIndex(line, -1) {
			if len(match) >= 4 {
				if routeLiteralPartOfConcat(line, match[0], match[1]) {
					continue
				}
				seen[line[match[2]:match[3]]] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

func routeLiteralsForSymbol(path, content, block string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord) []string {
	seen := map[string]struct{}{}
	if jvmLikeExtension(filepath.Ext(path)) {
		if typeLikeKind(symbol.Kind) {
			return nil
		}
		for _, route := range jvmAnnotationRouteLiterals(content, symbol, symbolsByID) {
			seen[route] = struct{}{}
		}
		if len(seen) > 0 {
			return sortedKeys(seen)
		}
	}
	for _, route := range routeLiteralsWithConstants(block, staticStringConstants(content)) {
		seen[route] = struct{}{}
	}
	if jsLikeExtension(filepath.Ext(path)) {
		for _, route := range jsRouterComposedRouteLiterals(block, staticStringConstants(content)) {
			seen[route] = struct{}{}
		}
	}
	if strings.EqualFold(filepath.Ext(path), ".py") {
		for _, route := range pythonDecoratorRouteLiterals(content, symbol) {
			seen[route] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func goHTTPRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".go") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		handlers := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				continue
			}
			if _, exists := handlers[symbol.Name]; !exists {
				handlers[symbol.Name] = symbol
			}
		}
		for _, registration := range goHTTPRouteRegistrations(content) {
			handler, ok := handlers[registration.Handler]
			if !ok {
				continue
			}
			key := handler.ID + "\x00" + registration.Route
			if seen[key] {
				continue
			}
			seen[key] = true
			relations = append(relations, expressRouteRelation{
				Route:   registration.Route,
				Handler: handler,
				Relation: RelationRecord{
					RecordType:    "relation",
					FromID:        handler.ID,
					ToID:          externalID("route", registration.Route),
					Type:          "HANDLES_ROUTE",
					Confidence:    0.86,
					Reason:        "Go net/http route registration resolved to local handler",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      registration.EvidenceKind,
						FilePath:  handler.FilePath,
						StartLine: handler.StartLine,
						EndLine:   handler.EndLine,
						Detail:    registration.Detail,
					}},
					WarningCodes: []string{},
				},
			})
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].Route != relations[j].Route {
			return relations[i].Route < relations[j].Route
		}
		return relations[i].Handler.ID < relations[j].Handler.ID
	})
	return relations
}

func goHTTPRouteRegistrations(content string) []goHTTPRouteRegistration {
	constants := staticStringConstants(content)
	var registrations []goHTTPRouteRegistration
	add := func(routeExpr, handler, evidence string) {
		route, ok := staticRouteExpressionValue(routeExpr, constants)
		if !ok || handler == "" {
			return
		}
		registrations = append(registrations, goHTTPRouteRegistration{
			Route:        route,
			Handler:      handler,
			EvidenceKind: evidence,
			Detail:       route + " -> " + handler,
		})
	}
	handleFuncRe := regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?HandleFunc\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	handleFuncWrapperRe := regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?Handle\s*\(\s*([^,\n]+)\s*,\s*(?:http\.)?HandlerFunc\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*\)`)
	for _, match := range handleFuncRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			add(match[1], match[2], "go_http_handle_func")
		}
	}
	for _, match := range handleFuncWrapperRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			add(match[1], match[2], "go_http_handler_func")
		}
	}
	return registrations
}

func djangoRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".py") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		handlers := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				continue
			}
			if _, exists := handlers[symbol.Name]; !exists {
				handlers[symbol.Name] = symbol
			}
		}
		for _, registration := range djangoRouteRegistrations(content) {
			handler, ok := handlers[registration.Handler]
			if !ok {
				continue
			}
			key := handler.ID + "\x00" + registration.Route
			if seen[key] {
				continue
			}
			seen[key] = true
			relations = append(relations, expressRouteRelation{
				Route:   registration.Route,
				Handler: handler,
				Relation: RelationRecord{
					RecordType:    "relation",
					FromID:        handler.ID,
					ToID:          externalID("route", registration.Route),
					Type:          "HANDLES_ROUTE",
					Confidence:    0.84,
					Reason:        "Django URL pattern resolved to local handler",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      registration.EvidenceKind,
						FilePath:  handler.FilePath,
						StartLine: handler.StartLine,
						EndLine:   handler.EndLine,
						Detail:    registration.Detail,
					}},
					WarningCodes: []string{},
				},
			})
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].Route != relations[j].Route {
			return relations[i].Route < relations[j].Route
		}
		return relations[i].Handler.ID < relations[j].Handler.ID
	})
	return relations
}

func djangoRouteRegistrations(content string) []djangoRouteRegistration {
	var registrations []djangoRouteRegistration
	add := func(pattern, handler, evidence string) {
		route := djangoRoutePatternValue(pattern, evidence == "django_re_path")
		if route == "" || handler == "" {
			return
		}
		registrations = append(registrations, djangoRouteRegistration{
			Route:        route,
			Handler:      handler,
			EvidenceKind: evidence,
			Detail:       route + " -> " + handler,
		})
	}
	pathRe := regexp.MustCompile(`\bpath\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)
	rePathRe := regexp.MustCompile(`\bre_path\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)
	for _, match := range pathRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			add(match[1], match[2], "django_path")
		}
	}
	for _, match := range rePathRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			add(match[1], match[2], "django_re_path")
		}
	}
	return registrations
}

func djangoRoutePatternValue(pattern string, regex bool) string {
	pattern = strings.TrimSpace(pattern)
	for pattern != "" {
		first := pattern[0]
		if first == 'r' || first == 'R' || first == 'u' || first == 'U' || first == 'b' || first == 'B' {
			pattern = strings.TrimSpace(pattern[1:])
			continue
		}
		break
	}
	if len(pattern) < 2 {
		return ""
	}
	quote := pattern[0]
	if (quote != '"' && quote != '\'') || pattern[len(pattern)-1] != quote {
		return ""
	}
	value := pattern[1 : len(pattern)-1]
	if regex {
		value = strings.TrimPrefix(value, "^")
		value = strings.TrimSuffix(value, "$")
	}
	value = strings.Trim(value, "/")
	if value == "" {
		return "/"
	}
	return "/" + value
}

func routeLiteralPartOfConcat(line string, start, end int) bool {
	before := strings.TrimSpace(line[:start])
	after := strings.TrimSpace(line[end:])
	return strings.HasSuffix(before, "+") || strings.HasPrefix(after, "+")
}

func staticStringConstants(content string) map[string]string {
	constants := map[string]string{}
	for _, match := range staticStringConstRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 3 {
			constants[match[1]] = match[2]
		}
	}
	for i := 0; i < 3; i++ {
		changed := false
		for _, match := range staticConcatConstRe.FindAllStringSubmatch(content, -1) {
			if len(match) != 4 || constants[match[2]] == "" {
				continue
			}
			value := joinRoutePaths(constants[match[2]], match[3])
			if constants[match[1]] == value {
				continue
			}
			constants[match[1]] = value
			changed = true
		}
		if !changed {
			break
		}
	}
	return constants
}

type jsRouterMount struct {
	Receiver string
	Prefix   string
	Target   string
}

type jsRouterRoute struct {
	Receiver string
	Route    string
	Handler  string
}

type expressRouteRelation struct {
	Route    string
	Handler  SymbolRecord
	Relation RelationRecord
}

type goHTTPRouteRegistration struct {
	Route        string
	Handler      string
	EvidenceKind string
	Detail       string
}

type djangoRouteRegistration struct {
	Route        string
	Handler      string
	EvidenceKind string
	Detail       string
}

type jsImportBinding struct {
	Module    string
	Imported  string
	Namespace bool
}

type pythonRouterMount struct {
	Prefix string
	Target string
}

type pythonRouterRoute struct {
	Receiver string
	Route    string
	Handler  SymbolRecord
}

func jsRouterComposedRouteLiterals(block string, constants map[string]string) []string {
	mounts := jsRouterMounts(block, constants)
	routes := jsRouterRoutes(block, constants)
	if len(mounts) == 0 || len(routes) == 0 {
		return nil
	}
	prefixes := map[string]string{}
	for i := 0; i < len(mounts)+1; i++ {
		changed := false
		for _, mount := range mounts {
			base := ""
			if receiverPrefix, ok := prefixes[mount.Receiver]; ok {
				base = receiverPrefix
			}
			prefix := joinRoutePaths(base, mount.Prefix)
			if existing, ok := prefixes[mount.Target]; ok && len(existing) >= len(prefix) {
				continue
			}
			prefixes[mount.Target] = prefix
			changed = true
		}
		if !changed {
			break
		}
	}
	seen := map[string]struct{}{}
	for _, route := range routes {
		prefix, ok := prefixes[route.Receiver]
		if !ok {
			continue
		}
		seen[joinRoutePaths(prefix, route.Route)] = struct{}{}
	}
	return sortedKeys(seen)
}

func jsRouterMounts(block string, constants map[string]string) []jsRouterMount {
	re := regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.use\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)`)
	var mounts []jsRouterMount
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) != 4 {
			continue
		}
		prefix, ok := staticRouteExpressionValue(match[2], constants)
		if ok {
			mounts = append(mounts, jsRouterMount{Receiver: match[1], Prefix: prefix, Target: match[3]})
		}
	}
	return mounts
}

func jsRouterRoutes(block string, constants map[string]string) []jsRouterRoute {
	re := regexp.MustCompile(`(?i)\b([A-Za-z_$][\w$]*)\.(get|post|put|patch|delete|head|options)\s*\(\s*([^,\n)]+)(?:\s*,\s*([A-Za-z_$][\w$]*))?`)
	var routes []jsRouterRoute
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) != 5 {
			continue
		}
		route, ok := staticRouteExpressionValue(match[3], constants)
		if ok {
			routes = append(routes, jsRouterRoute{Receiver: match[1], Route: route, Handler: match[4]})
		}
	}
	return routes
}

func staticRouteExpressionValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ";"))
	if expr == "" {
		return "", false
	}
	if (strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`)) || (strings.HasPrefix(expr, `'`) && strings.HasSuffix(expr, `'`)) {
		route := strings.Trim(expr, `"'`)
		return route, strings.HasPrefix(route, "/")
	}
	if route := constants[expr]; route != "" {
		return route, true
	}
	for _, match := range staticRouteConcatRe.FindAllStringSubmatch(expr, -1) {
		if len(match) == 3 && constants[match[1]] != "" {
			return joinRoutePaths(constants[match[1]], match[2]), true
		}
	}
	return "", false
}

func splitJavaScriptMember(value string) (string, string) {
	before, after, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok {
		return strings.TrimSpace(value), ""
	}
	return strings.TrimSpace(before), strings.TrimSpace(after)
}

func crossFileExpressRouterRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, knownFiles map[string]bool) []expressRouteRelation {
	routesByFile := map[string][]jsRouterRoute{}
	mountsByFile := map[string][]jsRouterMount{}
	importBindingsByFile := map[string]map[string][]jsImportBinding{}
	symbolsByFileAndName := map[string]map[string]SymbolRecord{}
	for _, file := range files {
		if !jsLikeExtension(filepath.Ext(file.Path)) {
			continue
		}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				continue
			}
			if symbolsByFileAndName[file.Path] == nil {
				symbolsByFileAndName[file.Path] = map[string]SymbolRecord{}
			}
			if _, exists := symbolsByFileAndName[file.Path][symbol.Name]; !exists {
				symbolsByFileAndName[file.Path][symbol.Name] = symbol
			}
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		constants := staticStringConstants(content)
		routesByFile[file.Path] = jsRouterRoutes(content, constants)
		mountsByFile[file.Path] = jsRouterMounts(content, constants)
		importBindingsByFile[file.Path] = importedJavaScriptBindings(content)
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		for _, mount := range mountsByFile[file.Path] {
			targetLocal, targetMember := splitJavaScriptMember(mount.Target)
			for _, binding := range importBindingsByFile[file.Path][targetLocal] {
				routeFile, ok := resolveLocalImport(file.Path, binding.Module, knownFiles)
				if !ok || routeFile == file.Path {
					continue
				}
				routeReceiver := binding.Imported
				if binding.Namespace {
					routeReceiver = targetMember
				}
				if routeReceiver == "" {
					routeReceiver = targetLocal
				}
				for _, route := range routesByFile[routeFile] {
					if route.Receiver != routeReceiver || route.Handler == "" {
						continue
					}
					handler, ok := symbolsByFileAndName[routeFile][route.Handler]
					if !ok {
						continue
					}
					fullRoute := joinRoutePaths(mount.Prefix, route.Route)
					key := handler.ID + "\x00" + fullRoute
					if seen[key] {
						continue
					}
					seen[key] = true
					relations = append(relations, expressRouteRelation{
						Route:   fullRoute,
						Handler: handler,
						Relation: RelationRecord{
							RecordType:    "relation",
							FromID:        handler.ID,
							ToID:          externalID("route", fullRoute),
							Type:          "HANDLES_ROUTE",
							Confidence:    0.82,
							Reason:        "Express router route resolved through local imported router mount",
							RelationScope: "external",
							Resolution:    "import_resolved",
							TargetKind:    "route",
							Evidence: []Evidence{{
								Kind:      "express_router_mount",
								FilePath:  handler.FilePath,
								StartLine: handler.StartLine,
								EndLine:   handler.EndLine,
								Detail:    mount.Prefix + " + " + route.Receiver + "." + route.Route,
							}},
							WarningCodes: []string{},
						},
					})
				}
			}
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].Route != relations[j].Route {
			return relations[i].Route < relations[j].Route
		}
		return relations[i].Handler.ID < relations[j].Handler.ID
	})
	return relations
}

func pythonIncludeRouterRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, knownFiles map[string]bool) []expressRouteRelation {
	routesByFile := map[string][]pythonRouterRoute{}
	mountsByFile := map[string][]pythonRouterMount{}
	importsByFile := map[string]map[string][]string{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".py") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		routesByFile[file.Path] = pythonRouterRoutes(content, recordsByFile[file.Path])
		mountsByFile[file.Path] = pythonRouterMounts(content)
		importsByFile[file.Path] = importedNamesFor(file.Path, content)
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if len(mountsByFile[file.Path]) == 0 {
			continue
		}
		for _, mount := range mountsByFile[file.Path] {
			for _, routeFile := range pythonRouterTargetFiles(file.Path, mount.Target, importsByFile[file.Path], knownFiles) {
				for _, route := range routesByFile[routeFile] {
					if route.Receiver != mount.Target {
						continue
					}
					fullRoute := joinRoutePaths(mount.Prefix, route.Route)
					key := route.Handler.ID + "\x00" + fullRoute
					if seen[key] {
						continue
					}
					seen[key] = true
					relations = append(relations, expressRouteRelation{
						Route:   fullRoute,
						Handler: route.Handler,
						Relation: RelationRecord{
							RecordType:    "relation",
							FromID:        route.Handler.ID,
							ToID:          externalID("route", fullRoute),
							Type:          "HANDLES_ROUTE",
							Confidence:    0.82,
							Reason:        "Python router route resolved through local include_router mount",
							RelationScope: "external",
							Resolution:    "import_resolved",
							TargetKind:    "route",
							Evidence: []Evidence{{
								Kind:      "python_router_mount",
								FilePath:  route.Handler.FilePath,
								StartLine: route.Handler.StartLine,
								EndLine:   route.Handler.EndLine,
								Detail:    mount.Prefix + " + " + route.Receiver + "." + route.Route,
							}},
							WarningCodes: []string{},
						},
					})
				}
			}
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].Route != relations[j].Route {
			return relations[i].Route < relations[j].Route
		}
		return relations[i].Handler.ID < relations[j].Handler.ID
	})
	return relations
}

func pythonRouterTargetFiles(importingPath, target string, importsByName map[string][]string, knownFiles map[string]bool) []string {
	seen := map[string]bool{importingPath: true}
	files := []string{importingPath}
	for _, imported := range importsByName[target] {
		resolved, ok := resolveLocalImport(importingPath, imported, knownFiles)
		if !ok || seen[resolved] {
			continue
		}
		seen[resolved] = true
		files = append(files, resolved)
	}
	return files
}

func pythonRouterMounts(content string) []pythonRouterMount {
	includeRouterRe := regexp.MustCompile(`\.include_router\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	prefixRe := regexp.MustCompile(`\bprefix\s*=\s*["']([^"']+)["']`)
	var mounts []pythonRouterMount
	for _, line := range strings.Split(content, "\n") {
		targetMatch := includeRouterRe.FindStringSubmatch(line)
		if len(targetMatch) != 2 {
			continue
		}
		prefixMatch := prefixRe.FindStringSubmatch(line)
		if len(prefixMatch) != 2 || !strings.HasPrefix(prefixMatch[1], "/") {
			continue
		}
		mounts = append(mounts, pythonRouterMount{Prefix: prefixMatch[1], Target: targetMatch[1]})
	}
	return mounts
}

func pythonRouterRoutes(content string, symbols []SymbolRecord) []pythonRouterRoute {
	var routes []pythonRouterRoute
	for _, symbol := range symbols {
		if typeLikeKind(symbol.Kind) {
			continue
		}
		for _, decorator := range pythonRouteDecoratorsNearSymbol(content, symbol) {
			routes = append(routes, pythonRouterRoute{
				Receiver: decorator.Receiver,
				Route:    decorator.Route,
				Handler:  symbol,
			})
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Route != routes[j].Route {
			return routes[i].Route < routes[j].Route
		}
		return routes[i].Handler.ID < routes[j].Handler.ID
	})
	return routes
}

type pythonRouteDecorator struct {
	Receiver string
	Route    string
}

func pythonRouteDecoratorsNearSymbol(content string, symbol SymbolRecord) []pythonRouteDecorator {
	if symbol.StartLine <= 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	routeDecoratorRe := regexp.MustCompile(`^@([A-Za-z_][A-Za-z0-9_]*)\.(?:get|post|put|patch|delete|head|options|route)\s*\(\s*["']([^"']+)["']`)
	seen := map[string]bool{}
	var routes []pythonRouteDecorator
	for i := index; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "def ") || strings.HasPrefix(line, "async def ") {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		match := routeDecoratorRe.FindStringSubmatch(line)
		if len(match) != 3 || !strings.HasPrefix(match[2], "/") {
			continue
		}
		key := match[1] + "\x00" + match[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		routes = append(routes, pythonRouteDecorator{Receiver: match[1], Route: match[2]})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Route != routes[j].Route {
			return routes[i].Route < routes[j].Route
		}
		return routes[i].Receiver < routes[j].Receiver
	})
	return routes
}

func pythonDecoratorRouteLiterals(content string, symbol SymbolRecord) []string {
	return annotationRouteLiteralsNearSymbol(content, symbol, false)
}

func jvmAnnotationRouteLiterals(content string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord) []string {
	if symbol.Kind != "method" && symbol.Kind != "function" {
		return nil
	}
	methodRoutes := annotationRouteLiteralsNearSymbol(content, symbol, true)
	if len(methodRoutes) == 0 {
		return nil
	}
	var classPrefixes []string
	if symbol.ContainerID != "" {
		if container, ok := symbolsByID[symbol.ContainerID]; ok && typeLikeKind(container.Kind) {
			classPrefixes = annotationRouteLiteralsNearSymbol(content, container, true)
		}
	}
	if len(classPrefixes) == 0 {
		return methodRoutes
	}
	seen := map[string]struct{}{}
	for _, prefix := range classPrefixes {
		for _, route := range methodRoutes {
			seen[joinRoutePaths(prefix, route)] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func annotationRouteLiteralsNearSymbol(content string, symbol SymbolRecord, springOnly bool) []string {
	if symbol.StartLine <= 0 {
		return nil
	}
	if springOnly {
		return springAnnotationRouteLiteralsAroundSymbol(content, symbol)
	}
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	for i := index; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "def ") || strings.HasPrefix(line, "async def ") {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		for _, route := range routeLiterals(line) {
			seen[route] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func springAnnotationRouteLiteralsAroundSymbol(content string, symbol SymbolRecord) []string {
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	collect := func(line string) {
		if !springRouteAnnotationLine(line) {
			return
		}
		for _, route := range routeLiterals(line) {
			seen[route] = struct{}{}
		}
	}
	current := ""
	if index >= 0 && index < len(lines) {
		current = strings.TrimSpace(lines[index])
	}
	if strings.HasPrefix(current, "@") {
		collect(current)
		for i := index + 1; i < len(lines) && i-index <= 8; i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "@") {
				break
			}
			collect(line)
		}
	}
	for i := index - 1; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		collect(line)
	}
	return sortedKeys(seen)
}

func springRouteAnnotationLine(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"@requestmapping",
		"@getmapping",
		"@postmapping",
		"@putmapping",
		"@deletemapping",
		"@patchmapping",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func joinRoutePaths(prefix, route string) string {
	if prefix == "" || prefix == "/" {
		if route == "" {
			return "/"
		}
		if strings.HasPrefix(route, "/") {
			return route
		}
		return "/" + route
	}
	if route == "" || route == "/" {
		if strings.HasPrefix(prefix, "/") {
			return prefix
		}
		return "/" + prefix
	}
	left := strings.TrimRight(prefix, "/")
	right := strings.TrimLeft(route, "/")
	if !strings.HasPrefix(left, "/") {
		left = "/" + left
	}
	return left + "/" + right
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
	case ".f90", ".for", ".fsharp", ".mm":
		return "unsupported source extension " + filepath.Ext(path)
	default:
		return ""
	}
}
