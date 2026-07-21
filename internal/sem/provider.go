package sem

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const (
	// SchemaVersion is bumped to 1.1 for the additive snapshot fields introduced
	// alongside boundary source locations (the `external` flag on external records
	// and the per-symbol source-location fields). The shape is backward compatible
	// for tolerant readers; the bump lets consumers detect the new fields.
	SchemaVersion         = "1.1"
	ProviderName          = "entire-graph"
	StableSymbolIDVersion = "compound-v1"
	defaultMaxParseBytes  = 4 * 1024 * 1024
)

var relationTypes = []string{
	"DEFINES",
	"CONTAINS",
	"IMPORTS",
	"CALLS",
	"CONSTRUCTS",
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
	"Kotlin":           {"USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD"},
	"C#":               {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"PHP":              {"EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "DATA_FLOWS"},
	"Python":           {"EXTENDS", "INHERITS", "OVERRIDES", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "HANDLES_GRAPHQL", "ASYNC_CALLS", "DATA_FLOWS"},
	"Rust":             {"EXTENDS", "INHERITS", "IMPLEMENTS", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"Go":               {"USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD", "ACCESSES", "ASYNC_CALLS", "DATA_FLOWS"},
	"HCL":              {"CONFIGURES", "RESOURCE_DEPENDS_ON"},
	"GraphQL":          {"HANDLES_GRAPHQL"},
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
	SchemaVersion   string   `json:"schema_version"`
	Provider        string   `json:"provider"`
	ProviderVersion string   `json:"provider_version"`
	RepoRoot        string   `json:"repo_root"`
	RepoKey         string   `json:"repo_key"`
	Commit          string   `json:"commit"`
	Tree            string   `json:"tree"`
	Languages       []string `json:"languages"`
	// LanguageTiers classifies each language present in this snapshot as
	// "semantic" (grammar-backed extraction) or "inventory-only" (file
	// discovery + basic symbols), so a consumer can scope trust per language.
	LanguageTiers    map[string]string  `json:"language_tiers,omitempty"`
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
	Lines      int    `json:"-"`
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
	// Local: a callable defined inside another function (nested/closure). Only
	// callable within its enclosing function, so it is excluded from cross-scope
	// name-match call resolution. Not serialized (internal to resolution).
	Local bool `json:"-"`
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
	SemanticLanguages               []string            `json:"semantic_languages"`
	InventoryOnlyLanguages          []string            `json:"inventory_only_languages"`
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
	// OnlyFiles restricts parsing to these exact repository-relative paths.
	// Empty means all discovered files. It is intended for query-time selective
	// indexing; ignore and vendored-file rules still apply first.
	OnlyFiles []string
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
		return profileSpec{name: ProfileFast, relations: relationTypeSet("DEFINES", "CONTAINS", "IMPORTS", "CALLS", "CONSTRUCTS", "HANDLES_ROUTE", "HANDLES_TOOL", "CONFIGURES", "RESOURCE_DEPENDS_ON"), includeEvidence: false, callResolution: "shallow"}
	default:
		return profileSpec{name: ProfileFull, relations: relationTypeSet(relationTypes...), includeEvidence: true, callResolution: "full"}
	}
}

func Capabilities() CapabilityReport {
	specs := supportedLanguageSpecs()
	extensions := make([]string, 0, len(specs))
	languageSet := map[string]struct{}{}
	semanticLanguageSet := map[string]struct{}{}
	inventoryOnlyLanguageSet := map[string]struct{}{}
	for extension, spec := range specs {
		extensions = append(extensions, extension)
		languageSet[spec.language] = struct{}{}
		if supportsSemanticExtraction(spec) {
			semanticLanguageSet[spec.language] = struct{}{}
		} else {
			inventoryOnlyLanguageSet[spec.language] = struct{}{}
		}
	}
	for _, spec := range inventoryLanguageFilenames {
		languageSet[spec.language] = struct{}{}
		inventoryOnlyLanguageSet[spec.language] = struct{}{}
	}
	for _, language := range specialFilenameLanguages() {
		languageSet[language] = struct{}{}
		inventoryOnlyLanguageSet[language] = struct{}{}
	}
	sort.Strings(extensions)
	languages := make([]string, 0, len(languageSet))
	for language := range languageSet {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	semanticLanguages := sortedSetKeys(semanticLanguageSet)
	inventoryOnlyLanguages := sortedSetKeys(inventoryOnlyLanguageSet)

	return CapabilityReport{
		SchemaVersion:                   SchemaVersion,
		Provider:                        ProviderName,
		SupportedFileExtensions:         extensions,
		SupportedLanguages:              languages,
		SemanticLanguages:               semanticLanguages,
		InventoryOnlyLanguages:          inventoryOnlyLanguages,
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
			"hybrid_source_search": true,
			"near_clone_detection": true,
			"git_cochange_edges":   true,
			"durable_preindex":     true,
			"focused_neighbors":    true,
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

func sortedSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func supportsSemanticExtraction(spec languageSpec) bool {
	if spec.inventoryOnly {
		return false
	}
	if spec.grammar != nil {
		return true
	}
	// SQL and Groovy have no grammar in the extension table but are backed by
	// dedicated parsers (pgsql grammar and the structural Groovy scanner).
	return spec.language == "SQL" || spec.language == "Groovy"
}

// languageTiers classifies the languages present in a snapshot as "semantic"
// (grammar-backed extraction) or "inventory-only" (file discovery + basic
// symbols). It reuses the authoritative capabilities classification so the
// tier reported per-repo matches `capabilities --json`, without duplicating
// the semantic/inventory split.
func languageTiers(languageSet map[string]struct{}) map[string]string {
	if len(languageSet) == 0 {
		return nil
	}
	semantic := make(map[string]struct{})
	for _, language := range Capabilities().SemanticLanguages {
		semantic[language] = struct{}{}
	}
	tiers := make(map[string]string, len(languageSet))
	for language := range languageSet {
		if _, ok := semantic[language]; ok {
			tiers[language] = "semantic"
		} else {
			tiers[language] = "inventory-only"
		}
	}
	return tiers
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
	case "Bash", "C", "C++", "C#", "Clojure", "ClojureScript", "Dart", "Elixir", "Erlang", "F#", "Go", "Groovy", "Haskell", "Java", "JavaScript", "Kotlin", "Lua", "Objective-C", "OCaml", "Perl", "PHP", "Python", "Ruby", "Rust", "Scala", "SQL", "Swift", "TypeScript", "Zig", "Zsh":
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

// prefixReader returns up to limit leading bytes of a file's content. It backs
// cheap first-line sniffing (shebang routing of extensionless executables)
// without reading whole files.
type prefixReader func(path string, limit int) (string, bool)

// sourceContext is the repository state needed to stream a snapshot: identity,
// the file list, a per-file content reader, and git-state warnings. It holds no
// file content itself.
type sourceContext struct {
	absRepo    string
	key        string
	commit     string
	tree       string
	paths      []string
	read       contentReader
	readPrefix prefixReader
	close      func() error
	warnings   []ProviderWarning
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
	snapshot.Header.LanguageTiers = summary.LanguageTiers
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
	precomputedImports := map[string][]string{}
	symbolCount := 0
	relationCount := 0
	parsedFileCount := 0

	// Phase 1: parse + emit file/symbol records, build indexes, discard content.
	for i, path := range sc.paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Path-based routing first; files the path cannot classify (extensionless
		// executables like pyenv's libexec/* scripts) get one bounded prefix read
		// to route by shebang before being declared unsupported.
		if !Supported(path) && !shebangRoutable(sc.readPrefix, path) {
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
		langSpec, ok := languageForContent(path, content)
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
		// Skip Go files the default build excludes (build-tag / _GOOS_GOARCH), so
		// the snapshot matches what the compiler compiles and alternate-tag files
		// don't poison cross-file type inference.
		if language == "Go" && !goFileMatchesDefaultBuild(path, content) {
			continue
		}
		file := FileRecord{
			RecordType: "file",
			ID:         fileID(sc.key, path),
			Path:       path,
			Blob:       contentHash(contentBytes),
			Language:   language,
			Bytes:      len(contentBytes),
			Lines:      sourceLineCount(content),
		}
		if skipFastProfilePerSymbolScan(spec, language) {
			precomputedImports[path] = importsFor(path, content)
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
		// Skip minified/bundled files (e.g. site assets like main.js / *.min.js):
		// their thousands of single-letter symbols in one file form a near-complete
		// same-file call graph (the dominant relation-count + time blow-up on repos
		// that vendor web assets), and they are not meaningful source to analyze.
		// Overlong lines must dominate the file; a few giant data lines embedded
		// in otherwise ordinary source do not disqualify it.
		if looksMinified(content) {
			languageSet[language] = struct{}{}
			if err := emit(file); err != nil {
				return err
			}
			files = append(files, file)
			lc := completenessLangs[language]
			lc.Files++
			completenessLangs[language] = lc
			failures = append(failures, PartialFailure{
				Code:                 "E_MINIFIED",
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: "file record emitted but symbol parsing skipped",
				Detail:               "file appears minified/bundled (very long lines); not analyzed as source",
			})
			continue
		}
		entities, language, parseStatus := parseWithProfile(parser, spec, langSpec, path, content)
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
			code := parseStatus.Code
			if code == "" {
				code = "E_PARSE_ERROR"
			}
			effect := "file parsed with syntax errors; semantic facts may be incomplete"
			if code == "E_PARSE_TIMEOUT" {
				effect = "file record emitted but symbol parsing skipped because parser time budget was exceeded"
			}
			failures = append(failures, PartialFailure{
				Code:                 code,
				Severity:             "warning",
				FilePath:             path,
				EffectOnCompleteness: effect,
				Detail:               parseStatus.Detail,
			})
		}
		file.Language = language
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
		if err := ctx.Err(); err != nil {
			emitErr = err
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
		var symbolsByID map[string]SymbolRecord
		var filesByID map[string]FileRecord
		if spec.includeEvidence {
			symbolsByID, filesByID = recordIndexes(files, recordsByFile)
		}
		forEachRelation(sc.key, files, recordsByFile, sc.read, precomputedImports, spec, func() bool {
			return emitErr != nil || ctx.Err() != nil
		}, func(r RelationRecord) {
			emitRelation(r, symbolsByID, filesByID)
		})
		if spec.emits("FILE_CHANGES_WITH") {
			for _, r := range fileChangesWithRelations(ctx, sc.absRepo, sc.commit, sc.key, files) {
				if emitErr != nil || ctx.Err() != nil {
					break
				}
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
		LanguageTiers:   languageTiers(languageSet),
		Warnings:        warnings,
		PartialFailures: failures,
		Stats: ProviderStats{
			Files:             len(files),
			ParsedFiles:       parsedFileCount,
			Symbols:           symbolCount,
			Relations:         relationCount,
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(len(failures), len(files), parsedFileCount, symbolCount),
		},
		Completeness: CompletenessReport{Languages: completenessLangs, Relations: relationsByType},
	})
}

func parseWithProfile(parser TreeSitterParser, spec profileSpec, langSpec languageSpec, path, content string) ([]Entity, string, ParseStatus) {
	if useFastCFamilyParser(spec, langSpec) {
		return fastCFamilyEntities(path, content, langSpec.language), langSpec.language, ParseStatus{}
	}
	return parser.ParseWithStatus(path, content)
}

func sourceLineCount(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}

func topLevelBlockFromLines(lines []string, symbols []SymbolRecord) string {
	if len(lines) == 0 {
		return ""
	}
	masked := append([]string(nil), lines...)
	for _, symbol := range symbols {
		start := maxInt(1, symbol.StartLine)
		end := maxInt(start, symbol.EndLine)
		if start > len(masked) {
			continue
		}
		if end > len(masked) {
			end = len(masked)
		}
		for i := start - 1; i <= end-1; i++ {
			masked[i] = ""
		}
	}
	return strings.Join(masked, "\n")
}

var jsDeclarationOnlyCallSignaturePattern = regexp.MustCompile(`^\s*(?:export\s+)?(?:(?:public|private|protected|readonly|static|abstract|override|declare|async)\s+)*[A-Za-z_$][A-Za-z0-9_$]*\s*(?:<[^>{}\n]*>)?\([^{};]*\)\s*:\s*[^{};]+;?\s*$`)

// pythonEllipsisStubSignaturePattern matches a declaration-only Python def whose
// body is `...` on the same line (`def describe(x: int) -> str: ...`). These are
// @typing.overload / Protocol stubs: they are intentionally not emitted as
// symbols, so their lines survive top-level masking, and the bare `def f(`
// token would otherwise be mis-scanned as a call to `f`. The real
// implementation (a `def f(...):` with a body on following lines) is part of an
// emitted symbol and is already masked, so it is not matched here.
var pythonEllipsisStubSignaturePattern = regexp.MustCompile(`^\s*(?:async\s+)?def\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^{}]*\)\s*(?:->[^:]+)?:\s*\.\.\.\s*$`)

func stripDeclarationOnlyCallSignatures(language, content string) string {
	var pattern *regexp.Regexp
	switch language {
	case "JavaScript", "TypeScript":
		pattern = jsDeclarationOnlyCallSignaturePattern
	case "Python":
		pattern = pythonEllipsisStubSignaturePattern
	default:
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if pattern.MatchString(line) {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func skipFastCFamilyCallScan(spec profileSpec, language string) bool {
	return spec.name == ProfileFast && (language == "C" || language == "C++")
}

func skipFastProfilePerSymbolScan(spec profileSpec, language string) bool {
	if spec.name != ProfileFast {
		return false
	}
	switch language {
	case "C#", "Dockerfile", "Go", "HCL", "Java", "JavaScript", "JSON", "JSON5", "Kustomize", "PHP", "Protocol Buffers", "Python", "Ruby", "TOML", "TypeScript", "XML", "YAML":
		return false
	default:
		return true
	}
}

func routeScanLanguage(language string) bool {
	switch language {
	case "C#", "Go", "Java", "JavaScript", "PHP", "Python", "Ruby", "TypeScript":
		return true
	default:
		return false
	}
}

func httpScanLanguage(language string) bool {
	switch language {
	case "C#", "Go", "Java", "JavaScript", "PHP", "Python", "Ruby", "TypeScript":
		return true
	default:
		return false
	}
}

func serviceScanLanguage(language string) bool {
	switch language {
	case "C#", "Go", "GraphQL", "Java", "JavaScript", "PHP", "Protocol Buffers", "Python", "Ruby", "TypeScript":
		return true
	default:
		return false
	}
}

func channelScanLanguage(language string) bool {
	switch language {
	case "C#", "Go", "Java", "JavaScript", "PHP", "Python", "Ruby", "TypeScript":
		return true
	default:
		return false
	}
}

func recordsByRelationSupport(recordsByFile map[string][]SymbolRecord, relationType string) map[string][]SymbolRecord {
	filtered := map[string][]SymbolRecord{}
	for path, records := range recordsByFile {
		var kept []SymbolRecord
		for _, record := range records {
			if languageSupportsRelation(record.Language, relationType) {
				kept = append(kept, record)
			}
		}
		if len(kept) > 0 {
			filtered[path] = kept
		}
	}
	return filtered
}

func languageSupportsRelation(language, relationType string) bool {
	for _, supported := range ooRelationSupport[language] {
		if supported == relationType {
			return true
		}
	}
	return false
}

func useFastCFamilyParser(spec profileSpec, langSpec languageSpec) bool {
	if spec.name == ProfileFull {
		return false
	}
	return langSpec.language == "C" || langSpec.language == "C++"
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
	LanguageTiers   map[string]string  `json:"language_tiers,omitempty"`
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
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)

	// The provider is local-only. NoNetwork is accepted to make that contract
	// explicit for callers that enforce no-egress provider execution.
	_ = options.NoNetwork
	committedRevision := ""
	if !options.Worktree && headErr == nil {
		committedRevision = commit
	}
	paths, read, readPrefix, closeSource, err := openSource(ctx, absRepo, committedRevision, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return sourceContext{}, err
	}
	if len(options.OnlyFiles) > 0 {
		allowed := make(map[string]bool, len(options.OnlyFiles))
		for _, filePath := range options.OnlyFiles {
			allowed[filepath.ToSlash(filepath.Clean(filePath))] = true
		}
		filtered := paths[:0]
		for _, filePath := range paths {
			if allowed[filepath.ToSlash(filePath)] {
				filtered = append(filtered, filePath)
			}
		}
		paths = filtered
	}

	var warnings []ProviderWarning
	if options.Worktree {
		warnings = append(warnings, ProviderWarning{
			Code:                 "W_WORKTREE_SNAPSHOT",
			Severity:             "warning",
			EffectOnCompleteness: "snapshot records are read from the working tree because --worktree was requested",
		})
	} else if headErr != nil {
		warnings = append(warnings, ProviderWarning{
			Code:                 "E_NO_GIT_HEAD",
			Severity:             "warning",
			EffectOnCompleteness: "snapshot records are read from the working tree because no HEAD tree is available",
			Detail:               headErr.Error(),
		})
	}
	return sourceContext{
		absRepo:    absRepo,
		key:        key,
		commit:     commit,
		tree:       tree,
		paths:      paths,
		read:       read,
		readPrefix: readPrefix,
		close:      closeSource,
		warnings:   warnings,
	}, nil
}

// resolveCommittedHEAD binds a repository view to one immutable commit. The
// tree is resolved from that exact object rather than from a second moving
// HEAD expression, preventing mixed commit/tree provenance if HEAD advances
// between subprocesses.
func resolveCommittedHEAD(ctx context.Context, repo string) (string, string, error) {
	commit, err := gitutil.RevParse(ctx, repo, "HEAD")
	if err != nil || commit == "" {
		if err == nil {
			err = errors.New("HEAD resolved to an empty commit")
		}
		return "", "", err
	}
	tree, err := gitutil.RevParse(ctx, repo, commit+"^{tree}")
	if err != nil || tree == "" {
		if err == nil {
			err = errors.New("committed HEAD resolved to an empty tree")
		}
		return "", "", err
	}
	return commit, tree, nil
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
			Local:           entity.Local,
		}
		symbols = append(symbols, symbol)
		byName[qualified] = id
	}
	return symbols
}

func syntheticBoundarySymbols(repoKey, path, language, content string, fileSymbols []SymbolRecord) []SymbolRecord {
	var symbols []SymbolRecord
	lines := strings.Split(content, "\n")
	if route := webRouteBoundary(path); route != "" {
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

func callableTargetKind(kind string) bool {
	return kind == "function" || kind == "method" || typeLikeKind(kind)
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

// localReachable enforces lexical nesting in name-match call resolution: a
// function-local (nested/closure) callable is only reachable from within its
// enclosing function — i.e. when `from` lexically contains it in the same file.
// Non-local callables are always reachable. This kills the dominant cross-scope
// false edges (e.g. every decorator factory's nested `decorator`/`new_func`
// name-matching each other) without a full scope resolver.
func localReachable(from, to SymbolRecord) bool {
	if !to.Local {
		return true
	}
	return to.FilePath == from.FilePath && from.StartLine <= to.StartLine && to.StartLine <= from.EndLine
}

// implicitReceiverLanguage reports whether a bare `name()` call can mean
// `this.name()` (an implicit-receiver method call). In such languages a bare
// call legitimately targets a method, so it must not be excluded from name-match
// resolution; in Go/Python/JS/TS a method call always carries an explicit
// receiver and is handled by receiverCallRelations instead.
func implicitReceiverLanguage(lang string) bool {
	switch lang {
	case "Java", "C#", "C++", "Dart", "Elixir", "Groovy", "Kotlin", "Scala", "Ruby", "Swift":
		return true
	}
	return false
}

// allowMethodTargets relaxes the "a bare name() never resolves to a method"
// rule. It must stay false for CALLS resolution (a receiver-less call is a
// function, not a class method), but the DATA_FLOWS argument-forward path
// resolves the *called* symbol of `obj.method(arg)` — which legitimately is a
// method — so it passes true. Folding that distinction into one resolver kept
// the data-flow path from silently dropping the `arg -> method` flow.
// nameCallMayTargetMethod reports whether a name-resolved call (a bare `name()`
// or a path-qualified `Type::name()`) can legitimately bind to a *method*, so
// methods must NOT be excluded from name-match resolution for that language:
//   - implicit-receiver languages: bare `name()` means `this.name()`;
//   - Rust: `Type::name()` / `Trait::name()` path calls and associated
//     functions are written explicitly and resolve to the method by name.
//
// In Go/Python/JS/TS a method call always carries an explicit `.`-receiver and
// is handled by receiverCallRelations instead, so methods stay excluded there.
// (Ported from fix/rust-call-resolution ad6ca4d onto the current gate shape.)
func nameCallMayTargetMethod(lang string) bool {
	return implicitReceiverLanguage(lang) || lang == "Rust"
}

func resolveCallTargets(name string, from SymbolRecord, candidates, sameFile []SymbolRecord, importsByName map[string][]string, allowMethodTargets bool) []resolvedCallTarget {
	var local []resolvedCallTarget
	for _, to := range sameFile {
		// A bare `name()` call resolves to a function, not a class method (methods
		// require a receiver and are resolved by receiverCallRelations) — matching
		// a same-named method here is a false edge.
		if to.ID == from.ID || to.Name != name || !callableTargetKind(to.Kind) || (to.Kind == "method" && !nameCallMayTargetMethod(from.Language) && !allowMethodTargets) || !localReachable(from, to) {
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
	if len(local) == 1 {
		return appendSQLDerivedDuplicateTargets(local, from, candidates)
	}
	if len(local) > 1 {
		// Ambiguous bare name with multiple same-file definitions: emit the
		// lexically nearest one (closest declaration line to the call site) rather
		// than fanning out an edge to every candidate. A single best guess keeps
		// recall while cutting the over-emission that dominates the false edges.
		best := local[0]
		for _, c := range local[1:] {
			if absInt(c.StartLine-from.StartLine) < absInt(best.StartLine-from.StartLine) {
				best = c
			}
		}
		return []resolvedCallTarget{best}
	}

	if imported := resolveImportedCallTargets(name, from, candidates, importsByName, allowMethodTargets); len(imported) > 0 {
		return imported
	}

	// Same-package resolution: in Go every file in a directory is the same
	// package, so a bare call resolves within that package even when the name is
	// not globally unique. Strict uniqueness-within-directory (function symbols
	// only) keeps this high-precision while recovering cross-file same-package
	// calls that the globally-unique gate below would otherwise drop.
	if from.Language == "Go" {
		fromDir := filepath.ToSlash(filepath.Dir(from.FilePath))
		var samePkg []SymbolRecord
		for _, to := range candidates {
			if to.ID == from.ID || to.Kind != "function" || !localReachable(from, to) {
				continue
			}
			if filepath.ToSlash(filepath.Dir(to.FilePath)) == fromDir {
				samePkg = append(samePkg, to)
			}
		}
		if len(samePkg) == 1 {
			return []resolvedCallTarget{{
				SymbolRecord: samePkg[0],
				Confidence:   0.80,
				Reason:       "direct call expression resolved to same-package symbol",
				Resolution:   "package",
				Scope:        "module",
			}}
		}
	}

	// Same-directory resolution for Dart: a Dart file imports siblings by bare
	// relative path (`import 'request.dart';`) without binding a local name, so
	// the import-map branch above can never disambiguate. A unique function or
	// type (constructor call) in the caller's directory wins over the
	// globally-unique gate below, which drops the call whenever another package
	// in a monorepo reuses the name (e.g. two `Request` classes in dart-lang/http).
	// Methods stay excluded: a Dart class is single-file, so an implicit-receiver
	// method call is already resolved by the same-file branch.
	if from.Language == "Dart" {
		fromDir := filepath.ToSlash(filepath.Dir(from.FilePath))
		var sameDir []SymbolRecord
		for _, to := range candidates {
			if to.ID == from.ID || (to.Kind != "function" && !typeLikeKind(to.Kind)) || !localReachable(from, to) {
				continue
			}
			if filepath.ToSlash(filepath.Dir(to.FilePath)) == fromDir {
				sameDir = append(sameDir, to)
			}
		}
		if len(sameDir) == 1 {
			return []resolvedCallTarget{{
				SymbolRecord: sameDir[0],
				Confidence:   0.80,
				Reason:       "direct call expression resolved to same-directory symbol",
				Resolution:   "package",
				Scope:        "module",
			}}
		}
	}

	var remaining []SymbolRecord
	for _, to := range candidates {
		if to.ID != from.ID && callableTargetKind(to.Kind) && (to.Kind != "method" || nameCallMayTargetMethod(from.Language) || allowMethodTargets) && localReachable(from, to) {
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
	if from.Language == "PHP" && len(remaining) > 1 {
		// PHP repos commonly re-declare bare functions (WordPress ships
		// apply_filters in plugin.php plus compat/noop stubs), and PHP has
		// no import statements for functions to disambiguate through.
		// Ambiguity must not mean silence: emit candidate edges to the
		// same-name declarations, largest declaration first (the canonical
		// implementation dwarfs its stubs), capped to keep noise bounded.
		ranked := append([]SymbolRecord(nil), remaining...)
		sort.SliceStable(ranked, func(i, j int) bool {
			return ranked[i].EndLine-ranked[i].StartLine > ranked[j].EndLine-ranked[j].StartLine
		})
		if len(ranked) > 4 {
			ranked = ranked[:4]
		}
		out := make([]resolvedCallTarget, 0, len(ranked))
		for _, cand := range ranked {
			out = append(out, resolvedCallTarget{
				SymbolRecord: cand,
				Confidence:   0.55,
				Reason:       fmt.Sprintf("ambiguous bare call: candidate among %d same-name declarations", len(remaining)),
				Resolution:   "name_only",
				Scope:        "workspace",
			})
		}
		return out
	}
	if cFamilyOverloadResolutionEnabled(from.Language) {
		if overloads, ok := sameFileOverloadSet(remaining); ok {
			out := make([]resolvedCallTarget, 0, len(overloads))
			for _, overload := range overloads {
				out = append(out, resolvedCallTarget{
					SymbolRecord: overload,
					Confidence:   0.62,
					Reason:       "direct call expression matched C/C++ overload set in one file",
					Resolution:   "name_only",
					Scope:        "workspace",
				})
			}
			return out
		}
	}
	return nil
}

func resolveImportedCallTargets(name string, from SymbolRecord, candidates []SymbolRecord, importsByName map[string][]string, allowMethodTargets bool) []resolvedCallTarget {
	var imported []resolvedCallTarget
	for _, to := range candidates {
		if to.ID == from.ID || !callableTargetKind(to.Kind) || (to.Kind == "method" && !nameCallMayTargetMethod(from.Language) && !allowMethodTargets) || !localReachable(from, to) {
			continue
		}
		if importedNameMatchesFile(importsByName[name], from.FilePath, to.FilePath) {
			imported = append(imported, resolvedCallTarget{
				SymbolRecord: to,
				Confidence:   0.86,
				Reason:       "direct call expression resolved through import path",
				Resolution:   "import_resolved",
				Scope:        "module",
			})
		}
	}
	if len(imported) == 0 {
		imported = jsExportedImportFallbackTargets(name, from, candidates, importsByName[name], allowMethodTargets)
	}
	return imported
}

func jsExportedImportFallbackTargets(name string, from SymbolRecord, candidates []SymbolRecord, modules []string, allowMethodTargets bool) []resolvedCallTarget {
	if len(modules) == 0 || (from.Language != "JavaScript" && from.Language != "TypeScript") {
		return nil
	}
	var exported []SymbolRecord
	var imported []SymbolRecord
	for _, to := range candidates {
		if to.ID == from.ID || !callableTargetKind(to.Kind) || (to.Kind == "method" && !nameCallMayTargetMethod(from.Language) && !allowMethodTargets) || !localReachable(from, to) {
			continue
		}
		if importedNameMatchesFile(modules, from.FilePath, to.FilePath) {
			imported = append(imported, to)
		}
		if jsExportedSymbol(name, to) {
			exported = append(exported, to)
		}
	}
	if len(exported) != 1 {
		exported = preferExportedSymbols(name, imported)
		if len(exported) != 1 {
			return nil
		}
	}
	return []resolvedCallTarget{{
		SymbolRecord: exported[0],
		Confidence:   0.64,
		Reason:       "JS/TS imported name matched unique exported workspace symbol",
		Resolution:   "import_resolved",
		Scope:        "module",
	}}
}

func preferExportedSymbols(name string, candidates []SymbolRecord) []SymbolRecord {
	var exported []SymbolRecord
	for _, candidate := range candidates {
		if jsExportedSymbol(name, candidate) {
			exported = append(exported, candidate)
		}
	}
	if len(exported) == 1 {
		return exported
	}
	if len(candidates) == 1 {
		return candidates
	}
	return nil
}

func jsExportedSymbol(name string, symbol SymbolRecord) bool {
	signature := strings.TrimSpace(symbol.Signature)
	for _, prefix := range []string{
		"export function " + name,
		"export async function " + name,
		"export const " + name,
		"export let " + name,
		"export var " + name,
		"export class " + name,
	} {
		if strings.HasPrefix(signature, prefix) {
			return true
		}
	}
	return false
}

func appendSQLDerivedDuplicateTargets(targets []resolvedCallTarget, from SymbolRecord, candidates []SymbolRecord) []resolvedCallTarget {
	if from.Language != "SQL" || len(targets) == 0 {
		return targets
	}
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.ID] = true
	}
	var extras []resolvedCallTarget
	for _, target := range targets {
		if !sqlDerivedScriptPath(target.FilePath) {
			continue
		}
		for _, candidate := range candidates {
			if candidate.ID == from.ID || seen[candidate.ID] || candidate.QualifiedName != target.QualifiedName || candidate.Kind != target.Kind || sqlDerivedScriptPath(candidate.FilePath) || !localReachable(from, candidate) {
				continue
			}
			seen[candidate.ID] = true
			extras = append(extras, resolvedCallTarget{
				SymbolRecord: candidate,
				Confidence:   0.54,
				Reason:       "SQL migration-script call also linked to canonical same-qualified routine",
				Resolution:   "name_only",
				Scope:        "workspace",
			})
		}
	}
	if len(extras) == 0 {
		return targets
	}
	out := append([]resolvedCallTarget(nil), targets...)
	out = append(out, extras...)
	return out
}

func sqlDerivedScriptPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(path)
	if strings.Contains(base, "--") {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "migration", "migrations", "update", "updates", "upgrade", "upgrades", "downgrade", "downgrades":
			return true
		}
	}
	return false
}

func cFamilyOverloadResolutionEnabled(language string) bool {
	return language == "C" || language == "C++"
}

func sameFileOverloadSet(candidates []SymbolRecord) ([]SymbolRecord, bool) {
	if len(candidates) < 2 {
		return nil, false
	}
	filePath := candidates[0].FilePath
	language := candidates[0].Language
	name := candidates[0].Name
	if filePath == "" || name == "" {
		return nil, false
	}
	for _, candidate := range candidates {
		if candidate.FilePath != filePath || candidate.Language != language || candidate.Name != name {
			return nil, false
		}
	}
	return candidates, true
}

// buildRelations collects every relation, deduplicates (first occurrence wins,
// in emission order), and sorts for stable output. Used by the in-memory
// snapshot path; the streaming path uses forEachRelation directly.
func buildRelations(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	var relations []RelationRecord
	forEachRelation(repoKey, files, recordsByFile, readContent, nil, resolveProfile(ProfileFull), nil, func(r RelationRecord) {
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
func forEachRelation(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, precomputedImports map[string][]string, spec profileSpec, shouldStop func() bool, emit func(RelationRecord)) {
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
	needsGlobalSymbolsByShortName := spec.callResolution == "full" || needsReceiverCalls || needsFields || needsTypes || needsOverrides || needsAsyncCalls || needsDataFlow || spec.emits("USES_TYPE") || spec.emits("PARAM_TYPE") || spec.emits("RETURNS_TYPE") || spec.emits("TESTS")
	needsGlobalSymbolsByFile := needsGlobalSymbolsByShortName
	symbolsByShortName := map[string][]SymbolRecord{}
	symbolsByFile := map[string][]SymbolRecord{}
	childNamesByContainer := map[string]map[string]bool{}
	methodsByContainer := map[string]map[string]SymbolRecord{}
	fieldsByContainer := map[string]map[string]SymbolRecord{}
	returnTypesBySymbolNameAndFile := map[string]map[string][]string{}
	returnTypesBySymbolNameAndDir := map[string]map[string][]string{}
	var inheritanceEdges []RelationRecord // captured for OVERRIDES derivation
	routeHandlers := map[string][]SymbolRecord{}
	httpCallsByRoute := map[string][]RelationRecord{}
	graphqlSchemaFields := map[string][]SymbolRecord{}
	graphqlResolvers := map[string][]SymbolRecord{}
	graphqlOperationRootAliases := map[string]string{}
	for _, file := range files {
		for _, symbol := range recordsByFile[file.Path] {
			if symbol.Kind != "graphql_schema_field" {
				continue
			}
			rootName := graphqlRootNameFromSignature(symbol)
			if rootName == "" || !graphqlOperationRoot(rootName) {
				continue
			}
			if typeName, _, ok := strings.Cut(symbol.QualifiedName, "."); ok {
				graphqlOperationRootAliases[strings.ToLower(typeName)] = rootName
			}
		}
	}
	// Cross-file container index: entitySymbols links a member to its container
	// only within one file, but some containers span files — a Go receiver type
	// commonly lives in a different file of the same package than its methods
	// (C# partial classes and reopened Ruby classes behave the same) — leaving
	// such members with an empty ContainerID and therefore invisible to
	// receiver-typed call resolution. Resolve those containers from the
	// member's qualified-name prefix against the workspace's type-like symbols.
	crossFileContainers := needsCallScan || needsReceiverCalls || needsFields || needsOverrides
	typeLikeByShortName := map[string][]SymbolRecord{}
	if crossFileContainers {
		for _, file := range files {
			for _, symbol := range recordsByFile[file.Path] {
				if typeLikeKind(symbol.Kind) {
					typeLikeByShortName[symbol.Name] = append(typeLikeByShortName[symbol.Name], symbol)
				}
			}
		}
	}
	// Iterate files in their (stable) slice order, not the recordsByFile map, so
	// structural relations stream deterministically.
	for _, file := range files {
		if shouldStop != nil && shouldStop() {
			return
		}
		records := recordsByFile[file.Path]
		for si, symbol := range records {
			if shouldStop != nil && shouldStop() {
				return
			}
			containsScope := "file"
			if crossFileContainers && symbol.ContainerID == "" && (symbol.Kind == "method" || symbol.Kind == "field") {
				if parent := containerName(symbol.QualifiedName); parent != "" {
					if container, ok := resolveContainerAcrossFiles(parent, symbol.FilePath, typeLikeByShortName); ok && container.ID != symbol.ID {
						symbol.ContainerID = container.ID
						// Write through so later passes (receiver-call scans read
						// recordsByFile again) see the resolved container too.
						records[si].ContainerID = container.ID
						if container.FilePath != symbol.FilePath {
							containsScope = "module"
						}
					}
				}
			}
			if symbol.Kind == "route" {
				routeHandlers[symbol.Name] = append(routeHandlers[symbol.Name], symbol)
			}
			if endpoint := graphqlBoundaryEndpoint(symbol, graphqlOperationRootAliases); endpoint != "" {
				switch symbol.Kind {
				case "graphql_schema_field":
					graphqlSchemaFields[endpoint] = append(graphqlSchemaFields[endpoint], symbol)
				case "graphql_resolver":
					graphqlResolvers[endpoint] = append(graphqlResolvers[endpoint], symbol)
				}
			}
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
					RelationScope: containsScope,
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
			if needsGlobalSymbolsByShortName {
				symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
			}
			if needsGlobalSymbolsByFile {
				symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
			}
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
				// C# receiver-call resolution also reads the field index:
				// class-level `Type Name { get; }` members type `Name.Method()`
				// receivers (csharpMemberType), so the map is built whenever
				// receiver calls are resolved, not only for field relations.
				if (needsFields || (needsReceiverCalls && symbol.Language == "C#")) && symbol.Kind == "field" {
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
					if symbol.Language == "Go" {
						// A Go package spans every file in a directory, so a factory
						// is commonly declared in a sibling file of its callers; key
						// return types by directory too for the package-level lookup.
						dir := filepath.ToSlash(filepath.Dir(symbol.FilePath))
						if returnTypesBySymbolNameAndDir[symbol.Name] == nil {
							returnTypesBySymbolNameAndDir[symbol.Name] = map[string][]string{}
						}
						returnTypesBySymbolNameAndDir[symbol.Name][dir] = append(returnTypesBySymbolNameAndDir[symbol.Name][dir], typeName)
					}
				}
			}
		}
	}
	// PHP declares return types in docblocks far more often than in native
	// hints (WordPress uses docblocks exclusively), and the signature pass
	// above cannot see them. Harvest '@return Type' from the docblock
	// directly above each PHP function/method so factory-return and
	// chained-call receiver inference work (rest_get_server()->dispatch()
	// must resolve through '@return WP_REST_Server').
	if needsReceiverCalls {
		for _, file := range files {
			if file.Language != "PHP" {
				continue
			}
			content, ok := readContent(file.Path)
			if !ok {
				continue
			}
			for name, typeName := range phpDocblockReturnTypes(content) {
				if returnTypesBySymbolNameAndFile[name] == nil {
					returnTypesBySymbolNameAndFile[name] = map[string][]string{}
				}
				returnTypesBySymbolNameAndFile[name][file.Path] = append(returnTypesBySymbolNameAndFile[name][file.Path], typeName)
			}
		}
	}
	// superContainerByID maps a class container to its (direct) superclass
	// container, so receiver-call resolution can follow inheritance: a
	// `this.method()` whose method is declared on a base class resolves up the
	// chain. Built after the symbol pass so the global short-name index is
	// complete; same-file resolution is preferred (a subclass and its base
	// usually share a file — e.g. zod's ZodType hierarchy), falling back to the
	// global index. Single-inheritance only (first EXTENDS edge), which matches
	// class semantics in every language that reaches here.
	superContainerByID := map[string]string{}
	implementersByContainer := map[string][]string{}
	if needsReceiverCalls {
		for _, file := range files {
			for _, symbol := range recordsByFile[file.Path] {
				if !typeLikeKind(symbol.Kind) {
					continue
				}
				for _, edge := range supertypesFromSignature(symbol.Language, symbol.Signature) {
					sup, ok := firstTypeLikeNamed(symbolsByFile[symbol.FilePath], edge.Super)
					if !ok || sup.ID == symbol.ID {
						sup, ok = firstTypeLikeNamed(symbolsByShortName[edge.Super], edge.Super)
					}
					if !ok || sup.ID == symbol.ID {
						continue
					}
					implementersByContainer[sup.ID] = append(implementersByContainer[sup.ID], symbol.ID)
					if edge.Relation == "EXTENDS" {
						if _, exists := superContainerByID[symbol.ID]; !exists {
							superContainerByID[symbol.ID] = sup.ID
						}
					}
				}
			}
		}
	}
	handledRoutes := map[string]struct{}{}
	knownFiles := map[string]bool{}
	for _, file := range files {
		if shouldStop != nil && shouldStop() {
			return
		}
		knownFiles[file.Path] = true
		if route := webRouteBoundary(file.Path); route != "" {
			handledRoutes[route] = struct{}{}
		}
	}
	if spec.emits("HANDLES_ROUTE") {
		for _, r := range goHTTPRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range djangoRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range pythonTornadoRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range pythonDirectRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range csharpMinimalAPIRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range laravelRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range railsRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
		for _, r := range jsDirectRouteRelations(files, recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
			handledRoutes[r.Route] = struct{}{}
		}
	}
	manifestImports := buildManifestImportResolver(files, readContent)

	// pythonModuleExists reports whether the repo contains a Python file that the
	// strict dotted-module matcher resolves `module` to (the module's own source
	// file or that package's __init__). It disambiguates a `from pkg import sub`
	// submodule import from an `import pkg as sub` alias rename — both record the
	// identical importsByName entry — by asking whether the submodule actually
	// exists. Results are memoized; the scan is restricted to Python files so a
	// same-stem file in another language is never mistaken for a module.
	pythonModuleFileExists := map[string]bool{}
	pythonModuleExists := func(module string) bool {
		if v, ok := pythonModuleFileExists[module]; ok {
			return v
		}
		found := false
		for p := range knownFiles {
			if ext := path.Ext(p); ext != ".py" && ext != ".pyi" {
				continue
			}
			if dottedModuleMatchesFileStrict(module, p) {
				found = true
				break
			}
		}
		pythonModuleFileExists[module] = found
		return found
	}

	// Package-level vars of package-qualified types, keyed by directory. A Go
	// package spans every file in a directory and a var is commonly declared in
	// one file but called in another, so this must be built before the per-file
	// loop. Used to resolve method calls on such vars to the right imported type.
	pkgVarTypesByDir := map[string]map[string]pkgQualType{}
	if needsReceiverCalls {
		for _, file := range files {
			if file.Language != "Go" {
				continue
			}
			content, ok := readContent(file.Path)
			if !ok {
				continue
			}
			vars := collectPackageVarTypes(content)
			if len(vars) == 0 {
				continue
			}
			dir := filepath.ToSlash(filepath.Dir(file.Path))
			if pkgVarTypesByDir[dir] == nil {
				pkgVarTypesByDir[dir] = map[string]pkgQualType{}
			}
			for n, qt := range vars {
				pkgVarTypesByDir[dir][n] = qt
			}
		}
	}

	for _, file := range files {
		if shouldStop != nil && shouldStop() {
			return
		}
		if !profileNeedsPerFileScan(spec) {
			break // syntax-only: no content-derived relations
		}
		imports, havePrecomputedImports := precomputedImports[file.Path]
		content := ""
		if !havePrecomputedImports || !skipFastProfilePerSymbolScan(spec, file.Language) {
			var ok bool
			content, ok = readContent(file.Path)
			if !ok {
				continue
			}
			if !havePrecomputedImports {
				imports = importsFor(file.Path, content)
			}
		}
		fromID := fileID(repoKey, file.Path)
		for _, imported := range imports {
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
		importsByName = resolvedImportedNameModules(file.Path, importsByName, manifestImports, knownFiles, readContent)
		// The Python dotted-call composer resolves `alias.<tail>.fn()` from the
		// syntactic form each import binding was recorded with (plain, alias
		// rename, or from-import), keyed by [local name][module]. resolvedImported-
		// NameModules only appends resolved file paths, so the original dotted
		// modules the form map keys on survive in importsByName.
		var pythonImportForms map[string]map[string]pythonImportForm
		if file.Language == "Python" {
			pythonImportForms = importedPythonImportForms(content)
		}
		if skipFastProfilePerSymbolScan(spec, file.Language) {
			continue
		}
		lines := strings.Split(content, "\n")
		currentFileSymbols := recordsByFile[file.Path]
		fileNeedsCallScan := needsCallScan && !skipFastCFamilyCallScan(spec, file.Language)
		fileNeedsRouteScan := spec.emits("HANDLES_ROUTE") && routeScanLanguage(file.Language)
		fileNeedsHTTPScan := spec.emits("HTTP_CALLS") && httpScanLanguage(file.Language)
		fileNeedsServiceScan := needsServiceRelations && serviceScanLanguage(file.Language)
		fileNeedsChannelScan := channelScanLanguage(file.Language)
		var fileStringConstants map[string]string
		if fileNeedsRouteScan || fileNeedsHTTPScan {
			fileStringConstants = staticStringConstants(content)
		}
		var routeSymbolsByID map[string]SymbolRecord
		if fileNeedsRouteScan {
			routeSymbolsByID = map[string]SymbolRecord{}
			for _, symbol := range currentFileSymbols {
				routeSymbolsByID[symbol.ID] = symbol
			}
		}
		var phpPropTypes map[string]string
		if file.Language == "PHP" && spec.callResolution == "full" {
			// Property types are declared at the class level, outside any
			// method's block: collect them once per file so property-receiver
			// calls (`$this->prop->method()`) can resolve.
			phpPropTypes = phpPropertyTypes(content)
		}
		var hsImports haskellFileImports
		if file.Language == "Haskell" && fileNeedsCallScan {
			// Import declarations live at the top of the file, outside any
			// binding's block: parse them once per file so qualified
			// applications (`Utils.fn` via `import qualified ... as Utils`)
			// and imported bare names can resolve.
			hsImports = haskellImports(content)
		}
		var ocamlOpenedModules []string
		var ocamlOpenedCallables map[string]bool
		var ocamlOpenedReferences map[string]bool
		if file.Language == "OCaml" && fileNeedsCallScan {
			ocamlOpenedModules = ocamlOpenModules(content)
			ocamlOpenedCallables = ocamlOpenedCallableNames(ocamlOpenedModules, symbolsByShortName)
			ocamlOpenedReferences = ocamlOpenedCallableReferenceNames(ocamlOpenedModules, symbolsByShortName)
		}
		var kotlinPropTypes map[string]string
		if file.Language == "Kotlin" && spec.callResolution == "full" {
			// Kotlin property types live at the class level too (primary
			// constructor val/var parameters, modifier-prefixed declarations
			// and factory initializers): collect them once per file so
			// property-receiver calls (`taskQueue.execute(...)`) can resolve.
			kotlinPropTypes = kotlinPropertyTypes(content, returnTypesBySymbolNameAndFile)
			for name, typeName := range kotlinFieldInitializerTypes(content, recordsByFile[file.Path]) {
				if existing, ok := kotlinPropTypes[name]; !ok || existing == typeName {
					kotlinPropTypes[name] = typeName
				} else {
					delete(kotlinPropTypes, name)
				}
			}
		}
		var typeScriptPropTypes map[string]string
		if file.Language == "TypeScript" && spec.callResolution == "full" {
			// TypeScript class-property receivers are commonly typed by Angular
			// DI initializers (`private router = inject(Router)`) or field
			// annotations outside the method body. Collect those once per file
			// and let receiverCallRelations apply the usual shadowing rules.
			typeScriptPropTypes = typeScriptPropertyTypes(content, recordsByFile[file.Path])
		}
		var swiftTypes swiftFileTypes
		if file.Language == "Swift" && spec.callResolution == "full" {
			// Swift stored-property types and enum-case payload types live at
			// the type level, outside any method's block: collect them once per
			// file so property receivers (`self._buffer!.discardReadBytes()`)
			// and enum-case pattern bindings (`case .available(var buffer):`)
			// can resolve.
			swiftTypes = swiftFileTypeInfo(content)
		}
		for _, from := range currentFileSymbols {
			if shouldStop != nil && shouldStop() {
				return
			}
			block := symbolBlockFromLines(lines, from)
			if fileNeedsCallScan && !typeLikeKind(from.Kind) && file.Language == "Erlang" {
				// Erlang call sites carry information the generic scanner cannot
				// use: a bare call is module-local by language rule, and a remote
				// `mod:fun(...)` call names the target module explicitly (module
				// `mod` lives in `mod.erl` by convention). A dedicated scanner
				// also keeps Erlang's variables (capitalized), `?MACRO(...)`
				// expansions, and `%` comments out of the call-name set.
				for _, r := range erlangCallRelations(from, block, currentFileSymbols, symbolsByShortName) {
					emit(r)
				}
			} else if fileNeedsCallScan && !typeLikeKind(from.Kind) && file.Language == "OCaml" {
				// OCaml applies functions by juxtaposition (`Mod.fn arg`), so
				// the generic `name(` scanner sees almost no OCaml call sites;
				// a dedicated scanner reads applications directly and uses the
				// module qualifier (module `Mod` lives in `mod.ml` by language
				// convention) to resolve targets. Interfaces (`.mli`) contain
				// only type mentions, and a nested module's block spans its
				// members' bodies, so both are skipped rather than misread.
				if from.Kind != "module" && ocamlCallScanFile(file.Path) {
					for _, r := range ocamlCallRelations(from, block, currentFileSymbols, symbolsByShortName, ocamlOpenedModules, ocamlOpenedCallables, ocamlOpenedReferences) {
						emit(r)
					}
				}
			} else if fileNeedsCallScan && !typeLikeKind(from.Kind) && file.Language == "Haskell" {
				// Haskell applies functions by whitespace (`fn arg`, `fn $
				// arg`, `Alias.fn arg`), so the generic `name(` scanner sees
				// almost no Haskell call sites; a dedicated scanner reads
				// applications directly, resolves qualified names through the
				// file's import aliases, and resolves bare names against
				// same-file bindings and the import section.
				for _, r := range haskellCallRelations(from, block, currentFileSymbols, symbolsByShortName, hsImports) {
					emit(r)
				}
			} else if fileNeedsCallScan && !typeLikeKind(from.Kind) {
				callBlock := block
				if file.Language == "Rust" {
					callBlock = stripRustCodegenMacroBodies(block)
				}
				if file.Language == "C#" {
					// Multi-line verbatim/raw string bodies (SQL blocks and the
					// like) survive the generic stripper's line-scoped string
					// masking and would register as call sites.
					callBlock = maskCSharpTextBlocks(block)
				}
				if file.Language == "Swift" {
					// Multiline string bodies ("""...""") likewise survive the
					// line-scoped masking and would register as call sites.
					callBlock = maskSwiftMultilineStrings(block)
				}
				if file.Language == "Groovy" {
					// Triple-quoted, slashy, and interpolated string bodies
					// survive the generic line-scoped masking and would
					// register as call sites.
					callBlock = maskGroovyLiteralsAndComments(block)
				}
				callNames := callLikeIdentifiers(callBlock, file.Language)
				jsCallableArgumentOnly := map[string]bool{}
				if file.Language == "Julia" {
					callNames = juliaCallIdentifiers(callBlock)
				}
				if file.Language == "Rust" {
					for name := range rustTurbofishCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Ruby" {
					// Ruby method names may end in `!`/`?` and are commonly called
					// without parentheses; the generic scanner misses both.
					for name := range rubySuffixedCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Kotlin" {
					// Kotlin trailing-lambda calls (`runTask { ... }`) carry no
					// parentheses, so the generic `name(` scanner never sees
					// them.
					for name := range kotlinBareLambdaCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Groovy" {
					// Groovy command expressions (`visitType p.type`,
					// `print 'x'`) likewise carry no parentheses. callBlock is
					// already Groovy-masked above.
					for name := range groovyCommandCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "JavaScript" || file.Language == "TypeScript" {
					// Same-file namespaces (`Parser.parseSourceFile(...)`) need their
					// terminal call resolved as a local symbol. Imported receivers are
					// handled path-aware below; arbitrary object receivers must not be
					// collapsed into workspace-global bare calls.
					for name := range jsNamespaceCallIdentifiers(callBlock, content) {
						callNames[name] = struct{}{}
					}
					for name := range jsCallableArgumentIdentifiers(callBlock) {
						if _, directCall := callNames[name]; !directCall {
							jsCallableArgumentOnly[name] = true
						}
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "F#" {
					// F# module-qualified calls (`UpdateProcess.SmartInstall(...)`,
					// `LoadingScripts.ScriptGeneration.constructScriptsFromData(...)`)
					// hide the target behind a dotted path.
					for name := range fsharpDottedCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Lua" {
					// Lua table/module calls (`vim.split(...)`, `M.joinpath(...)`)
					// are dotted or colon-qualified; the generic scanner drops the
					// final segment because it follows a selector.
					for name := range luaDottedCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Zig" {
					// Zig code frequently calls imported analyzer helpers through
					// module aliases (`analysis.resolveVarDeclAlias(...)`).
					for name := range zigDottedCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if shellCallLanguage(file.Language) {
					// Shell functions are invoked as bare commands with no
					// parentheses; the generic scanner sees no call sites at all.
					for name := range shellCommandCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "Clojure" || file.Language == "ClojureScript" {
					// Clojure calls are list heads, usually `(name ...)` or
					// `(alias/name ...)`, not `name(...)`.
					for name := range clojureCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				if file.Language == "SQL" {
					// PostgreSQL function/procedure bodies call routines as
					// `name(...)` or `schema.name(...)`. The generic scanner
					// drops schema-qualified calls because of the preceding dot.
					for name := range sqlCallIdentifiers(callBlock) {
						callNames[name] = struct{}{}
					}
				}
				callImportsByName := importsByName
				// Python module-qualified dotted calls (`pkg.mod.fn()`) are resolved
				// by a dedicated, strictly module-scoped path (emitted after the
				// generic loop below): the generic resolver ignores the module
				// qualifier and would mis-resolve to an unrelated same-named symbol.
				var pythonDottedRelations []RelationRecord
				if file.Language == "Python" {
					allDotted, externalDotted := pythonDottedCallImportedNames(callBlock, importsByName, pythonImportForms, pythonModuleExists)
					if len(allDotted) > 0 {
						pythonDottedRelations = pythonDottedCallRelations(from, allDotted, externalDotted, importsByName, symbolsByShortName, childNamesByContainer[from.ID])
					}
				}
				for _, name := range sortedKeysOf(callNames) {
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
					targets := resolveCallTargets(name, from, symbolsByShortName[name], currentFileSymbols, callImportsByName, false)
					for _, to := range targets {
						// A bare identifier passed as an argument may be a callback, but
						// it is not a construction. Keep callback discovery additive while
						// preventing `fn(input)` from linking the argument `input` to an
						// unrelated same-named type alias.
						if jsCallableArgumentOnly[name] && typeLikeKind(to.Kind) {
							continue
						}
						// Call to a type (class/struct/...) is a constructor/conversion, not a
						// callable->callable call: keep it as CONSTRUCTS for agents, out of CALLS.
						relType := "CALLS"
						if typeLikeKind(to.Kind) {
							relType = "CONSTRUCTS"
						}
						emit(RelationRecord{
							RecordType:    "relation",
							FromID:        from.ID,
							ToID:          to.ID,
							Type:          relType,
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
					if len(targets) == 0 && file.Language == "Kotlin" && spec.callResolution == "full" {
						// A bare call inside a Kotlin extension-function body
						// (`fun ApplicationCall.respondResource(...)` calling
						// `resolveResource(...)`) dispatches on the implicit
						// extension receiver: resolve it against the receiver
						// type's members and matching extension functions.
						if to, reason, ok := kotlinImplicitReceiverCallTarget(from, name, methodsByContainer, superContainerByID, symbolsByShortName); ok {
							scope := "file"
							if to.FilePath != from.FilePath {
								scope = "module"
							}
							emit(RelationRecord{
								RecordType:    "relation",
								FromID:        from.ID,
								ToID:          to.ID,
								Type:          "CALLS",
								Confidence:    0.8,
								Reason:        reason,
								RelationScope: scope,
								Resolution:    "type_inferred",
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
							continue
						}
					}
					if len(targets) == 0 {
						for _, relation := range importedExternalCallRelationsForName(from, name, callImportsByName[name]) {
							emit(relation)
						}
					}
				}
				for _, relation := range pythonDottedRelations {
					emit(relation)
				}
			}
			callableSymbol := !typeLikeKind(from.Kind)
			if needsAsyncCalls && callableSymbol {
				for _, name := range asyncCallNames(block) {
					if name == from.Name {
						continue
					}
					for _, to := range resolveCallTargets(name, from, symbolsByShortName[name], currentFileSymbols, importsByName, false) {
						if typeLikeKind(to.Kind) {
							continue // construction, not an async call
						}
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
					for _, to := range resolveCallTargets(flow.Name, from, symbolsByShortName[flow.Name], currentFileSymbols, importsByName, true) {
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
			if fileNeedsServiceScan {
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
			if fileNeedsRouteScan {
				for _, route := range routeLiteralsForSymbol(file.Path, content, block, from, routeSymbolsByID, fileStringConstants) {
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
			}
			if fileNeedsHTTPScan && callableSymbol {
				for _, call := range httpCallsWithConstants(block, fileStringConstants) {
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
			if fileNeedsChannelScan {
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
				// NestJS microservice handlers declare their transport channel via
				// an @MessagePattern/@EventPattern decorator rather than a runtime
				// .on()/.subscribe() call, so the generic channelEvents scan misses
				// them. They are the consumer side of the transport, so emit them as
				// LISTENS_ON; the emitter side (client.emit/client.send) is picked up
				// by the generic emit scan and matched on the shared channel name.
				if spec.emits("LISTENS_ON") && !typeLikeKind(from.Kind) && jsLikeExtension(filepath.Ext(file.Path)) {
					for _, channel := range nestJSMessagePatternChannelsAroundSymbol(content, from) {
						emit(RelationRecord{
							RecordType:    "relation",
							FromID:        from.ID,
							ToID:          externalID("channel", channel),
							Type:          "LISTENS_ON",
							Confidence:    0.7,
							Reason:        "NestJS @MessagePattern/@EventPattern handler",
							RelationScope: "external",
							Resolution:    "pattern",
							TargetKind:    "channel",
							Evidence: []Evidence{{
								Kind:      "message_pattern_decorator",
								FilePath:  from.FilePath,
								StartLine: from.StartLine,
								EndLine:   from.EndLine,
								Detail:    channel,
							}},
							WarningCodes: []string{},
						})
					}
				}
			}
			if spec.callResolution == "full" {
				for _, r := range receiverCallRelations(from, block, methodsByContainer, superContainerByID, implementersByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile, returnTypesBySymbolNameAndDir, importsByName, manifestImports.goModule, pkgVarTypesByDir[filepath.ToSlash(filepath.Dir(file.Path))], phpPropTypes, kotlinPropTypes, typeScriptPropTypes, fieldsByContainer, swiftTypes) {
					emit(r)
				}
				for _, r := range importedReceiverCallRelations(from, block, importsByName, symbolsByShortName) {
					emit(r)
				}
			}
			if needsFields {
				for _, r := range fieldAccessRelations(from, block, fieldsByContainer, symbolsByShortName) {
					emit(r)
				}
			}
		}

		// Erlang has no top-level call expressions: everything outside function
		// bodies is a module attribute, and attribute payloads like
		// `-spec name(...)` or `-export([...])` are declarations that would
		// register as bogus file->function call sites. OCaml is skipped for
		// the mirror-image reason: expression code lives in `let` bindings
		// (extracted as symbols and scanned by the dedicated OCaml pass), so
		// what remains at top level is declarations — opens, type definitions,
		// signatures — whose `name (` sequences are type syntax, not calls.
		// Haskell matches the same shape: all expressions live in top-level
		// bindings (extracted as symbols and scanned by the dedicated Haskell
		// pass), and what remains at top level — module/export lists, imports
		// like `hiding (doesFileExist)`, class/instance heads — would only
		// register bogus file->symbol call sites.
		if fileNeedsCallScan && file.Language != "Erlang" && file.Language != "OCaml" && file.Language != "Haskell" {
			topLevel := stripDeclarationOnlyCallSignatures(file.Language, topLevelBlockFromLines(lines, currentFileSymbols))
			if file.Language == "Rust" {
				topLevel = stripRustCodegenMacroBodies(topLevel)
			}
			if file.Language == "Groovy" {
				// Multi-line string bodies at script level would otherwise
				// register as top-level call sites.
				topLevel = maskGroovyLiteralsAndComments(topLevel)
			}
			if strings.TrimSpace(topLevel) != "" {
				fileSource := SymbolRecord{
					ID:       fileID(repoKey, file.Path),
					Kind:     "file",
					Name:     filepath.Base(file.Path),
					FilePath: file.Path,
					Language: file.Language,
				}
				topLevelNames := callLikeIdentifiers(topLevel, file.Language)
				jsCallableArgumentOnly := map[string]bool{}
				if file.Language == "Julia" {
					topLevelNames = juliaCallIdentifiers(topLevel)
				}
				if file.Language == "Rust" {
					for name := range rustTurbofishCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "JavaScript" || file.Language == "TypeScript" {
					for name := range jsNamespaceCallIdentifiers(topLevel, content) {
						topLevelNames[name] = struct{}{}
					}
					for name := range jsCallableArgumentIdentifiers(topLevel) {
						if _, directCall := topLevelNames[name]; !directCall {
							jsCallableArgumentOnly[name] = true
						}
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "F#" {
					for name := range fsharpDottedCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "Lua" {
					for name := range luaDottedCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "Zig" {
					for name := range zigDottedCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "Clojure" || file.Language == "ClojureScript" {
					for name := range clojureCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "SQL" {
					for name := range sqlCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				if file.Language == "Ruby" {
					for name := range rubySuffixedCallIdentifiers(topLevel) {
						topLevelNames[name] = struct{}{}
					}
				}
				for _, name := range sortedKeysOf(topLevelNames) {
					for _, to := range resolveCallTargets(name, fileSource, symbolsByShortName[name], currentFileSymbols, importsByName, false) {
						if jsCallableArgumentOnly[name] && typeLikeKind(to.Kind) {
							continue
						}
						relType := "CALLS"
						if typeLikeKind(to.Kind) {
							relType = "CONSTRUCTS"
						}
						emit(RelationRecord{
							RecordType:    "relation",
							FromID:        fileSource.ID,
							ToID:          to.ID,
							Type:          relType,
							Confidence:    minFloat(to.Confidence, 0.8),
							Reason:        "top-level call expression resolved to symbol",
							RelationScope: to.Scope,
							Resolution:    to.Resolution,
							TargetKind:    "symbol",
							Evidence: []Evidence{{
								Kind:     "top_level_call_site",
								FilePath: file.Path,
								Detail:   name,
							}},
							WarningCodes: []string{},
						})
					}
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
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("HANDLES_ROUTE") {
		for _, r := range crossFileExpressRouterRelations(files, recordsByFile, readContent, knownFiles) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
		}
		for _, r := range pythonIncludeRouterRelations(files, recordsByFile, readContent, knownFiles) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r.Relation)
			routeHandlers[r.Route] = append(routeHandlers[r.Route], r.Handler)
		}
	}
	if spec.emits("CALLS") {
		for _, r := range routeBridgeRelations(routeHandlers, httpCallsByRoute) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
		for _, r := range graphqlSchemaResolverRelations(graphqlSchemaFields, graphqlResolvers) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("USES_TYPE") {
		for _, r := range usesTypeRelations(recordsByFile, symbolsByFile, symbolsByShortName) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("PARAM_TYPE") || spec.emits("RETURNS_TYPE") {
		for _, r := range signatureTypeRelations(recordsByFile, symbolsByFile, symbolsByShortName, spec) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("TESTS") {
		for _, r := range testRelations(recordsByFile, symbolsByShortName) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("RESOURCE_DEPENDS_ON") {
		for _, r := range resourceDependsOnRelations(recordsByRelationSupport(recordsByFile, "RESOURCE_DEPENDS_ON"), readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("CONFIGURES") {
		for _, r := range configuresRelations(recordsByRelationSupport(recordsByFile, "CONFIGURES"), readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
			emit(r)
		}
	}
	if spec.emits("SIMILAR_TO") {
		for _, r := range similarityRelations(recordsByFile, readContent) {
			if shouldStop != nil && shouldStop() {
				return
			}
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

func graphqlSchemaResolverRelations(schemaFields, resolvers map[string][]SymbolRecord) []RelationRecord {
	var endpoints []string
	for endpoint := range schemaFields {
		if len(resolvers[endpoint]) > 0 {
			endpoints = append(endpoints, endpoint)
		}
	}
	sort.Strings(endpoints)
	var relations []RelationRecord
	for _, endpoint := range endpoints {
		fields := append([]SymbolRecord(nil), schemaFields[endpoint]...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
		targets := append([]SymbolRecord(nil), resolvers[endpoint]...)
		sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
		for _, field := range fields {
			for _, resolver := range targets {
				if field.ID == resolver.ID {
					continue
				}
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        field.ID,
					ToID:          resolver.ID,
					Type:          "CALLS",
					Confidence:    0.9,
					Reason:        "GraphQL schema field resolved to local resolver map field",
					RelationScope: "workspace",
					Resolution:    "exact",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:     "graphql_schema_resolver_match",
						FilePath: resolver.FilePath,
						Detail:   endpoint,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

func graphqlBoundaryEndpoint(symbol SymbolRecord, operationRootAliases map[string]string) string {
	switch symbol.Kind {
	case "graphql_schema_field", "graphql_resolver":
	default:
		return ""
	}
	fields := strings.Fields(symbol.Signature)
	if len(fields) < 4 {
		return ""
	}
	if fields[0] != "GraphQL" {
		return ""
	}
	switch symbol.Kind {
	case "graphql_schema_field":
		if fields[1] != "schema" {
			return ""
		}
	case "graphql_resolver":
		if fields[1] != "resolver" {
			return ""
		}
	}
	typeName := strings.ToLower(fields[2])
	if typeName == "" {
		return ""
	}
	if operationRootAliases != nil {
		if alias := operationRootAliases[typeName]; alias != "" {
			typeName = alias
		}
	}
	name := strings.TrimSpace(fields[3])
	if name == "" {
		return ""
	}
	return typeName + " " + name
}

func graphqlRootNameFromSignature(symbol SymbolRecord) string {
	fields := strings.Fields(symbol.Signature)
	if len(fields) < 4 || fields[0] != "GraphQL" {
		return ""
	}
	return strings.ToLower(fields[2])
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
// body to the target method symbol by inferring the receiver's type. Supported
// cases are this/self receivers, typed parameters, constructor assignments, and
// same-file variables assigned from a factory with an explicit local return
// type. These calls are otherwise dropped (a name preceded by '.'/'->' is not a
// plain call), so this is purely additive. Type containers are skipped as
// callers — calls live in methods/functions, not in the class declaration.
// lookupMethodUpChain finds a method by name starting in the given container and,
// failing that, walking up the superclass chain (superContainerByID). It returns
// the method, whether it was found on an ancestor rather than the start container,
// and ok. A visited set guards against inheritance cycles from misparsed headers;
// the chain is single-inheritance so the first hit is the resolution-order target.
func lookupMethodUpChain(start, name string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string) (SymbolRecord, bool, bool) {
	seen := map[string]bool{}
	for c, hops := start, 0; c != "" && !seen[c] && hops < 32; c, hops = superContainerByID[c], hops+1 {
		seen[c] = true
		if m, ok := methodsByContainer[c][name]; ok {
			return m, hops > 0, true
		}
	}
	return SymbolRecord{}, false, false
}

func uniqueImplementedMethod(start, name string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string, implementersByContainer map[string][]string) (SymbolRecord, bool) {
	seenContainers := map[string]bool{}
	seenMethods := map[string]bool{}
	var out SymbolRecord
	var walk func(string, int) bool
	walk = func(container string, depth int) bool {
		if container == "" || depth > 16 || seenContainers[container] {
			return true
		}
		seenContainers[container] = true
		for _, impl := range implementersByContainer[container] {
			if method, _, ok := lookupMethodUpChain(impl, name, methodsByContainer, superContainerByID); ok {
				if !seenMethods[method.ID] {
					if out.ID != "" {
						return false
					}
					out = method
					seenMethods[method.ID] = true
				}
			}
			if !walk(impl, depth+1) {
				return false
			}
		}
		return true
	}
	if !walk(start, 0) || out.ID == "" {
		return SymbolRecord{}, false
	}
	return out, true
}

// resolveContainerAcrossFiles resolves a member's container name to a
// type-like symbol when per-file linking left the member orphaned. Preference
// order keeps it conservative: a unique same-file type (per-file linking can
// miss a container declared after its member), then a unique same-directory
// type (a Go package — where methods routinely live in sibling files of their
// receiver type — is a directory), then a workspace-unique type. Ambiguity at
// the first populated tier resolves to nothing rather than wrongly.
func resolveContainerAcrossFiles(name, file string, typesByShortName map[string][]SymbolRecord) (SymbolRecord, bool) {
	candidates := typesByShortName[name]
	if len(candidates) == 0 {
		return SymbolRecord{}, false
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	var sameFile, sameDir []SymbolRecord
	for _, candidate := range candidates {
		switch {
		case candidate.FilePath == file:
			sameFile = append(sameFile, candidate)
		case filepath.ToSlash(filepath.Dir(candidate.FilePath)) == dir:
			sameDir = append(sameDir, candidate)
		}
	}
	for _, tier := range [][]SymbolRecord{sameFile, sameDir, candidates} {
		if len(tier) == 1 {
			return tier[0], true
		}
		if len(tier) > 1 {
			return SymbolRecord{}, false
		}
	}
	return SymbolRecord{}, false
}

// pkgQualType is a package-qualified type reference (alias.TypeName), used to
// resolve method calls on package-level vars whose bare type name is ambiguous
// across packages (e.g. zerolog's `enc = json.Encoder{}` where both internal/json
// and internal/cbor define an Encoder with the same methods).
type pkgQualType struct {
	alias    string
	typeName string
}

// pkgQualVarRe matches a package-level composite-literal var assignment:
//
//	enc = json.Encoder{...}   /   var enc = &json.Encoder{...}
var pkgQualVarRe = regexp.MustCompile(`^[ \t]*(?:var\s+)?([A-Za-z_]\w*)\s*=\s*&?([a-z]\w[\w]*)\.([A-Z]\w*)\s*\{`)

// collectPackageVarTypes scans a Go file's package-level (brace-depth 0)
// declarations for package-qualified composite literals and returns
// name -> {alias, type}. Brace depth skips function bodies; the legacy
// `var ( ... )` block uses parens, so its entries stay at depth 0.
func collectPackageVarTypes(content string) map[string]pkgQualType {
	stripped := stripCodeLiteralsAndComments(content)
	out := map[string]pkgQualType{}
	depth := 0
	for _, line := range strings.Split(stripped, "\n") {
		if depth == 0 {
			if m := pkgQualVarRe.FindStringSubmatch(line); m != nil {
				out[m[1]] = pkgQualType{alias: m[2], typeName: m[3]}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return out
}

// resolveQualifiedType picks the type-like symbol for a package-qualified
// reference by the Go convention that a package's import alias equals its
// directory basename (json.Encoder -> the Encoder in .../json/). Requires a
// unique match so an ambiguous alias resolves to nothing rather than wrongly.
func resolveQualifiedType(qt pkgQualType, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, bool) {
	var match SymbolRecord
	found := 0
	for _, cand := range symbolsByShortName[qt.typeName] {
		if !typeLikeKind(cand.Kind) {
			continue
		}
		if filepath.Base(filepath.Dir(filepath.ToSlash(cand.FilePath))) == qt.alias {
			match = cand
			found++
		}
	}
	if found == 1 {
		return match, true
	}
	return SymbolRecord{}, false
}

func receiverCallRelations(from SymbolRecord, block string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string, implementersByContainer map[string][]string, symbolsByShortName map[string][]SymbolRecord, returnTypesBySymbolNameAndFile, returnTypesBySymbolNameAndDir map[string]map[string][]string, importsByName map[string][]string, goModule string, pkgVarTypes map[string]pkgQualType, phpPropTypes, kotlinPropTypes, typeScriptPropTypes map[string]string, fieldsByContainer map[string]map[string]SymbolRecord, swiftTypes swiftFileTypes) []RelationRecord {
	if typeLikeKind(from.Kind) {
		return nil
	}
	if from.Language == "C#" {
		// Multi-line verbatim (@"...") and raw ("""...""") string bodies pass
		// through the generic stripper (which contains string masking to one
		// line), so SQL/text blocks would feed every extractor below.
		block = maskCSharpTextBlocks(block)
	}
	if from.Language == "Swift" {
		// Multiline string bodies ("""...""") likewise span lines and would
		// feed every extractor below through the line-scoped generic stripper.
		block = maskSwiftMultilineStrings(block)
	}
	calls := receiverCalls(block)
	allReceiverCalls := calls
	if from.Language == "Dart" {
		// Dart property assignment invokes a setter method when one exists
		// (`request.bodyFields = bodyFields` calls `set bodyFields(...)`), but
		// the generic receiver scanner only sees parenthesized calls.
		calls = mergeReceiverCalls(calls, dartSetterAssignmentCalls(block))
	}
	if from.Language == "C#" {
		// Chain tails (`Dependencies.MigrationsSqlGenerator.Generate(`) look
		// like `MigrationsSqlGenerator.Generate(` to the generic scanner, and
		// the typed tiers misresolve them — EF Core names properties after
		// their concrete type, so the static-call tier hits that class instead
		// of the property's interface. The typed tiers get only clean-receiver
		// calls; the chain loop below resolves the tails, and the extension
		// fallback — receiver-agnostic by construction — sees every pair.
		calls = csharpNonTailReceiverCalls(block)
	}
	chainedCalls := chainedConstructorCalls(block)
	returnedCalls := returnedReceiverCalls(block)
	chainedReturnCalls := chainedConstructorReturnCalls(block)
	deepChainedReturnCalls := chainedConstructorDeepReturnCalls(block)
	returnedChainCalls := returnedReceiverChainCalls(block)
	returnedDeepChainCalls := returnedReceiverDeepChainCalls(block)
	var rubyBareCalls []string
	if from.Language == "Ruby" {
		// Ruby call sites often omit parentheses and method names may end in
		// `!`/`?`; the generic extractors above miss both, and constructor
		// chains are spelled `Klass.new(...).method` rather than `new Klass()`.
		calls = mergeReceiverCalls(calls, rubyReceiverCalls(block))
		chainedCalls = append(chainedCalls, rubyChainedConstructorCalls(block)...)
		rubyBareCalls = rubyBareCallNames(block, from.Signature)
	}
	var kotlinChains []kotlinChainedCall
	if from.Language == "Kotlin" {
		// Kotlin safe calls (`socket?.closeQuietly()`) and trailing-lambda
		// invocations (`taskQueue.execute { ... }`) never match the generic
		// receiverCallRe, which requires a literal `.` and a literal `(`.
		// Chained receivers (`call.application.resolveResource(...)`) carry a
		// property hop the flat scanner misreads as an unknown receiver.
		calls = mergeReceiverCalls(calls, kotlinReceiverCalls(block))
		kotlinChains = kotlinChainedReceiverCalls(block)
	}
	var swiftChains []swiftChainedCall
	if from.Language == "Swift" {
		// Force-unwrapped (`self._buffer!.discardReadBytes()`) and
		// optional-chained (`delegate?.retry(...)`) receivers never match the
		// generic receiverCallRe, which requires a literal `.` directly after
		// the receiver name. Property-chain receivers
		// (`request.fileio.streamFile(...)`, often spelled across lines) carry
		// a property hop the flat scanner misreads as an unknown receiver.
		calls = mergeReceiverCalls(calls, swiftReceiverCalls(block))
		swiftChains = swiftChainedReceiverCalls(block)
	}
	var javaChains []javaCtorChainCall
	if from.Language == "Java" {
		// Nested-type fluent constructor chains
		// (`new Retrofit.Builder().baseUrl(...).build()`): the generic chain
		// regexes handle neither the dotted type nor arbitrary chain depth with
		// nested call arguments.
		javaChains = javaConstructorChainCalls(block)
	}
	if from.Language == "Objective-C" {
		// Objective-C message sends use bracket syntax (`[self setState:...]`),
		// not `receiver.method(...)`, so the generic receiver scanner never sees
		// intra-class selector calls.
		objcCalls := objectiveCMessageReceiverCalls(block)
		calls = mergeReceiverCalls(calls, objcCalls)
		allReceiverCalls = mergeReceiverCalls(allReceiverCalls, objcCalls)
	}
	var phpStatics []phpStaticCall
	var phpPropCalls []receiverCall
	var phpFactoryChains []phpPropertyFactoryChainCall
	var rustPathCalls []rustPathCall
	if from.Language == "PHP" {
		// PHP static calls use `::` (never matched by receiverCallRe) and
		// constructor chains are spelled `(new Klass(...))->m(` rather than
		// `new Klass().m(`; property receivers resolve through the class-level
		// property types the caller collected from this file.
		phpStatics = phpStaticCalls(block)
		phpPropCalls = phpPropertyReceiverCalls(block)
		phpFactoryChains = phpPropertyFactoryChainCalls(block)
		chainedCalls = append(chainedCalls, phpChainedConstructorCalls(block)...)
	}
	if from.Language == "Rust" {
		// Rust module calls (`strings::index_of(...)`) are not receiver calls:
		// the left side is a `use`-bound module alias, not a value. Resolve them
		// through Rust import bindings and Cargo/module alias resolution.
		rustPathCalls = rustModulePathCalls(block)
	}
	if from.Language == "Perl" {
		// Perl method calls commonly omit parentheses (`$self->stash`,
		// `$base->protocol`), which the generic receiver scanner never sees.
		calls = mergeReceiverCalls(calls, perlReceiverCalls(block))
	}
	var csChainCalls []csharpChainCall
	if from.Language == "C#" {
		// One-hop member chains (`Dependencies.ModelDiffer.GetDifferences(`)
		// are matched by receiverCallRe as `ModelDiffer.GetDifferences(`, an
		// untypeable receiver; the dedicated extractor keeps the full chain so
		// class-level member types can resolve it hop by hop.
		csChainCalls = csharpMemberChainCalls(block)
	}
	if len(calls) == 0 && len(allReceiverCalls) == 0 && len(chainedCalls) == 0 && len(returnedCalls) == 0 && len(chainedReturnCalls) == 0 && len(deepChainedReturnCalls) == 0 && len(returnedChainCalls) == 0 && len(returnedDeepChainCalls) == 0 && len(rubyBareCalls) == 0 && len(phpStatics) == 0 && len(phpPropCalls) == 0 && len(phpFactoryChains) == 0 && len(rustPathCalls) == 0 && len(csChainCalls) == 0 && len(kotlinChains) == 0 && len(javaChains) == 0 && len(swiftChains) == 0 {
		return nil
	}
	varTypes := parameterVarTypes(from.Signature)
	if from.Language == "Swift" {
		// Swift parameters are `label name: inout Type` (`func f(remainder
		// buffer: inout ByteBuffer)`, `_ buffer: ByteBuffer`): the generic
		// colon branch only understands the bare `name: Type` form.
		for name, typeName := range swiftParameterVarTypes(from.Signature, from.Name) {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
			}
		}
	}
	localTypes := localVarTypes(block)
	// perlVarTypes holds Perl-inferred receiver types. They are kept OUT of the
	// shared localTypes/varTypes maps so a Perl package name can never resolve a
	// receiver call in another language's context (and vice versa); only the
	// language-gated Perl resolution loop below (perlCallableForType) reads them.
	perlVarTypes := map[string]string{}
	if from.Language == "Ruby" {
		for name, typeName := range rubyLocalVarTypes(block) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	if from.Language == "PHP" {
		// The PHP-aware constructor scan understands namespace-qualified
		// `new \Foo\Bar(...)`; its terminal segment wins over the generic
		// scanner's first-segment misread.
		for name, typeName := range phpLocalVarTypes(block) {
			localTypes[name] = typeName
		}
	}
	if from.Language == "Perl" {
		// A hop resolves within a package when some Perl sub of that name lives in
		// the package's own file (perlSymbolFileMatchesType), which is how the
		// fluent gate distinguishes a same-object chain from getter navigation.
		hopResolvable := func(hop, pkgType string) bool {
			for _, candidate := range symbolsByShortName[hop] {
				if candidate.Language != "Perl" {
					continue
				}
				if candidate.Kind != "function" && candidate.Kind != "method" {
					continue
				}
				if perlSymbolFileMatchesType(candidate.FilePath, pkgType) {
					return true
				}
			}
			return false
		}
		for name, typeName := range perlLocalVarTypes(block, hopResolvable) {
			perlVarTypes[name] = typeName
		}
	}
	if from.Language == "Kotlin" {
		// Declared-type locals (`val writerToClose: WebSocketWriter?`), which
		// the generic constructor-assignment scan cannot type.
		for name, typeName := range kotlinLocalVarTypes(block) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
		// Trailing-lambda parameters typed by the callee's declared
		// function-type parameter (`status(*code) { call, status -> ... }`
		// against `fun status(..., handler: suspend (ApplicationCall,
		// HttpStatusCode) -> Unit)`) or by an explicit annotation.
		for name, typeName := range kotlinLambdaParamVarTypes(block, from, symbolsByShortName) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	if from.Language == "TypeScript" {
		// Declared-type and Angular-DI locals (`const router: Router = ...`,
		// `const ref = inject(ViewContainerRef)`), which the generic
		// constructor-assignment scan cannot type.
		for name, typeName := range typeScriptLocalVarTypes(block) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	if from.Language == "Rust" {
		// Locals assigned from associated constructors (`let id =
		// task::Id::next()`) carry their receiver type in the path, not a
		// declaration annotation or constructor call shape the generic scanner
		// understands.
		for name, typeName := range rustAssociatedPathVarTypes(block) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	if from.Language == "Java" {
		// Declared-type locals (`BuiltInFactories builtInFactories =
		// Platform.builtInFactories;`), which the generic
		// constructor-assignment scan cannot type.
		for name, typeName := range javaLocalVarTypes(block) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	if from.Language == "Swift" {
		// Declared-type locals (`let decoded: ByteBuffer? = nil`) and
		// enum-case pattern bindings (`case .available(var buffer):` typed by
		// the file's `case available(ByteBuffer)` declaration), which the
		// generic constructor-assignment scan cannot type.
		for name, typeName := range swiftLocalVarTypes(block, swiftTypes.enumPayloads) {
			if _, exists := localTypes[name]; !exists {
				localTypes[name] = typeName
			}
		}
	}
	for name, typeName := range localTypes {
		varTypes[name] = typeName
	}
	if from.Language == "Go" {
		// Named result parameters (`func (c *Command) ExecuteC() (cmd
		// *Command, err error)`) declare typed locals exactly like
		// parameters, but the generic parameter scan reads the signature's
		// first paren group — a method's receiver — so `cmd.execute()`
		// receivers stayed untyped. Parameters and constructor-typed locals
		// win; named results fill the gaps.
		for name, typeName := range goNamedResultVarTypes(from.Signature) {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
			}
		}
	}
	factoryTypes := factoryReturnVarTypes(block, from.FilePath, returnTypesBySymbolNameAndFile)
	if from.Language == "Go" {
		// Multi-value assignments (`cmd, flags, err := c.Find(args)`) type
		// their first variable from the callee's declared first result; the
		// generic factory-assignment scan matches only single-variable,
		// unqualified factories.
		for name, typeName := range goMultiAssignReturnVarTypes(block, from, symbolsByShortName) {
			if _, exists := factoryTypes[name]; !exists {
				factoryTypes[name] = typeName
			}
		}
	}
	if from.Language == "PHP" {
		// `$v = $this->factory()` receivers typed by the factory's declared
		// return type, the PHP spelling of the factory-assignment tier above.
		for name, typeName := range phpThisFactoryVarTypes(block, from.FilePath, returnTypesBySymbolNameAndFile) {
			if _, exists := factoryTypes[name]; !exists {
				factoryTypes[name] = typeName
			}
		}
	}
	if from.Language == "Rust" {
		// Rust locals often come from a typed receiver method, e.g.
		// `let c = ctx.c();` where `ctx: GenerateChunkCtx` and `c()` returns
		// `&mut LinkerContext`. Type the local through the receiver method's
		// declared return type before resolving `c.method(...)`.
		for name, typeName := range rustReceiverFactoryVarTypes(block, varTypes, methodsByContainer, superContainerByID, symbolsByShortName, returnTypesBySymbolNameAndFile, from.FilePath) {
			if _, exists := factoryTypes[name]; !exists {
				factoryTypes[name] = typeName
			}
		}
	}
	for name, typeName := range factoryTypes {
		if _, exists := varTypes[name]; !exists {
			varTypes[name] = typeName
		}
	}
	// Kotlin class-property receivers (`taskQueue.execute(...)` where
	// `taskQueue` is a typed property or constructor val/var parameter). Lowest
	// tier: a same-named parameter or local shadows the property.
	kotlinPropReceivers := map[string]bool{}
	if from.Language == "Kotlin" {
		for name, typeName := range kotlinPropTypes {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
				kotlinPropReceivers[name] = true
			}
		}
		// Properties of the enclosing extension receiver type: inside
		// `fun ApplicationCall.respondStaticResource(...)` a bare receiver
		// `application` is `this.application`, typed by the workspace field
		// symbol `ApplicationCall.application: Application` — a cross-file
		// source kotlinPropTypes (same-file class properties) cannot see.
		if receiverType := kotlinExtensionReceiver(from.Signature, from.Name); receiverType != "" {
			for _, call := range calls {
				if call.Receiver == "this" || call.Receiver == "self" || call.Receiver == "super" {
					continue
				}
				if _, exists := varTypes[call.Receiver]; exists {
					continue
				}
				if typeName := kotlinFieldTypeOnType(receiverType, call.Receiver, from, symbolsByShortName); typeName != "" {
					varTypes[call.Receiver] = typeName
					kotlinPropReceivers[call.Receiver] = true
				}
			}
		}
	}
	typeScriptPropReceivers := map[string]bool{}
	if from.Language == "TypeScript" {
		for name, typeName := range typeScriptPropTypes {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
				typeScriptPropReceivers[name] = true
			}
		}
	}
	// Swift stored-property receivers (`_buffer.discardReadBytes()` extracted
	// from `self._buffer!....`, bare `delegate?.retry(...)`). Lowest tier: a
	// same-named parameter or local shadows the property.
	swiftPropReceivers := map[string]bool{}
	swiftChainLocalReceivers := map[string]bool{}
	swiftNestedTypeLocals := map[string]string{}
	if from.Language == "Swift" {
		for name, typeName := range swiftTypes.props {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
				swiftPropReceivers[name] = true
			}
		}
		// Locals bound from property chains (`let contentRange =
		// request.headers.range`), typed by hopping declared property types
		// through the workspace's field symbols. Locals of dotted nested
		// types (`if let firstRange = contentRange.ranges.first` ->
		// HTTPFields.Range.Value) resolve by qualified method name in a
		// dedicated pass below: their type symbols keep the short name, so
		// the flat tiers cannot look methods up on them.
		plainChainLocals, dottedChainLocals := swiftChainBoundLocalTypes(block, varTypes, symbolsByShortName)
		for name, typeName := range plainChainLocals {
			if _, exists := varTypes[name]; !exists {
				varTypes[name] = typeName
				swiftChainLocalReceivers[name] = true
			}
		}
		for name, dotted := range dottedChainLocals {
			if _, exists := varTypes[name]; !exists {
				swiftNestedTypeLocals[name] = dotted
			}
		}
	}
	importedReceiverVars := importedReceiverVarTypes(from.Signature, block, importsByName, goModule)
	deepReturnedCallSuffixes := receiverDeepChainSuffixes(deepChainedReturnCalls, returnedDeepChainCalls)
	paramTypes := parameterVarTypes(from.Signature)
	if from.Language == "Swift" {
		for name, typeName := range swiftParameterVarTypes(from.Signature, from.Name) {
			if _, exists := paramTypes[name]; !exists {
				paramTypes[name] = typeName
			}
		}
	}
	for name := range localTypes {
		delete(paramTypes, name)
	}
	var relations []RelationRecord
	methodResolved := map[string]bool{}
	for _, call := range calls {
		method, confidence, reason, resolution, scope, ok := receiverQualifiedMethodTarget(from, call, symbolsByShortName[call.Method], returnTypesBySymbolNameAndFile)
		if !ok {
			continue
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from.ID,
			ToID:          method.ID,
			Type:          "CALLS",
			Confidence:    confidence,
			Reason:        reason,
			RelationScope: scope,
			Resolution:    resolution,
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
		methodResolved[call.Receiver+"."+call.Method] = true
	}
	if from.Language == "Perl" {
		for _, call := range calls {
			if methodResolved[call.Receiver+"."+call.Method] {
				continue
			}
			// Type-inferred resolution attributes call.Method to the package of
			// call.Receiver's inferred type, which is only sound when Method is
			// invoked directly on Receiver. perlReceiverCalls flattens multi-hop
			// chains (`$url->path->to_string`) to {head-receiver, terminal-method}
			// with Hops>=2, where the terminal method actually runs on the
			// intermediate object; resolving those here would emit a wrong CALLS
			// edge to a same-named sub in the head receiver's package. Restrict to
			// genuine single-hop calls (Hops<=1; the generic scanner's structurally
			// single-hop calls carry the zero default).
			if call.Hops > 1 {
				continue
			}
			// Read ONLY the Perl-isolated map: the shared varTypes never carries
			// Perl entries, so a same-named local of another language cannot leak in.
			typeName := perlVarTypes[call.Receiver]
			if typeName == "" {
				continue
			}
			target, ok := perlCallableForType(typeName, call.Method, symbolsByShortName[call.Method])
			if !ok || target.ID == from.ID {
				continue
			}
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    0.76,
				Reason:        "Perl receiver call resolved via inferred package receiver type",
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
			methodResolved[call.Receiver+"."+call.Method] = true
		}
	}
	for _, call := range calls {
		var targetID string
		confidence := 0.85
		reason := "method call resolved via inferred receiver type"
		resolution := "type_inferred"
		receiverTypeKind := ""
		switch call.Receiver {
		case "this", "self":
			if from.ContainerID == "" {
				continue
			}
			targetID = from.ContainerID
			confidence = 0.9
			reason = "method call on this/self resolved to the enclosing type"
		default:
			if typeName, ok := varTypes[call.Receiver]; ok {
				if _, ok := paramTypes[call.Receiver]; ok {
					confidence = 0.83
					reason = "method call resolved via typed parameter receiver"
				}
				if _, ok := factoryTypes[call.Receiver]; ok {
					confidence = 0.77
					reason = "method call resolved via assigned returned receiver type"
				}
				if kotlinPropReceivers[call.Receiver] || swiftPropReceivers[call.Receiver] || typeScriptPropReceivers[call.Receiver] {
					confidence = 0.8
					reason = "method call resolved via typed property receiver"
				}
				if swiftChainLocalReceivers[call.Receiver] {
					confidence = 0.75
					reason = "method call resolved via property-chain-typed local receiver"
				}
				sym, ok := typeLikeNamedWithMethod(symbolsByShortName[typeName], typeName, from.FilePath, call.Method, methodsByContainer, superContainerByID)
				if !ok {
					continue
				}
				targetID = sym.ID
				receiverTypeKind = sym.Kind
			} else if cls, ok := typeLikeNamedWithMethod(symbolsByShortName[call.Receiver], call.Receiver, from.FilePath, call.Method, methodsByContainer, superContainerByID); ok {
				// ClassName.method(): the receiver is itself a type name, not a
				// variable, so this is a static (class-qualified) call and the
				// target is that class's own method.
				confidence = 0.82
				reason = "static method call resolved to the named type"
				targetID = cls.ID
			} else if qt, ok := pkgVarTypes[call.Receiver]; ok {
				// Package-level var of a package-qualified type (alias.Type). Resolve
				// the specific imported type so an ambiguous bare name (Encoder in
				// both json and cbor) maps to the right one.
				sym, ok := resolveQualifiedType(qt, symbolsByShortName)
				if !ok {
					continue
				}
				targetID = sym.ID
				confidence = 0.78
				reason = "method call on package var resolved via qualified imported type"
			} else if typeName, ok := csharpReceiverMemberType(from, call.Receiver, fieldsByContainer, superContainerByID); ok {
				// C# class-level member receiver: `Dependencies.Method(...)`
				// where `Dependencies` is a typed property or field of the
				// enclosing class (or a base class).
				sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[typeName], typeName, from.FilePath)
				if !ok {
					continue
				}
				targetID = sym.ID
				confidence = 0.8
				reason = "method call resolved via typed property receiver"
			} else if from.ContainerID != "" && strings.EqualFold(call.Receiver, enclosingTypeShortName(from)) {
				// An untyped receiver named after the enclosing type resolves
				// to it: laravel/framework's Container.getClosure returns a
				// closure whose $container parameter is (by convention) the
				// container itself, so $container->resolve(...) is a call on
				// the enclosing class. The method-existence check below keeps
				// this honest — no edge unless the type defines the method.
				targetID = from.ContainerID
				confidence = 0.62
				reason = "method call receiver named after the enclosing type"
			} else {
				continue
			}
		}
		method, inherited, ok := lookupMethodUpChain(targetID, call.Method, methodsByContainer, superContainerByID)
		if ok && call.SetterAssign && from.Language == "Dart" {
			// A property-assignment call invokes the SETTER. For a read-write
			// property the container's short-name method index holds only the
			// last-declared accessor (getter or setter), so resolve the setter
			// explicitly and prefer it — otherwise the edge lands on the getter
			// about half the time (declaration-order dependent).
			if setter, setterInherited, found := dartSetterAccessor(targetID, call.Method, symbolsByShortName[call.Method], superContainerByID); found {
				method, inherited = setter, setterInherited
			}
		}
		if !ok && from.Language == "Ruby" && call.Method == "new" {
			// Ruby spells construction `Klass.new`; the constructor that runs is
			// the class's initialize method.
			method, inherited, ok = lookupMethodUpChain(targetID, "initialize", methodsByContainer, superContainerByID)
			if ok {
				reason = "Ruby Class.new call resolved to the class initialize constructor"
			}
		}
		if !ok && from.Language == "Swift" && receiverTypeKind == "protocol" {
			// A Swift protocol requirement without a default implementation
			// produces no method symbol (only `extension Proto` defaults do), so
			// member lookup on a protocol-typed receiver cannot find it. When
			// exactly one method in the workspace carries the name, resolve to
			// that sole implementation — the same unique-name stance as the Go
			// interface fallback.
			method, ok = uniqueMethodByShortName(symbolsByShortName[call.Method])
			if ok {
				confidence = minFloat(confidence, 0.7)
				reason = "protocol-typed receiver call resolved to the unique implementing method"
				resolution = "name_only"
			}
		}
		if !ok && from.Language == "TypeScript" && targetID != "" {
			if impl, implOK := uniqueImplementedMethod(targetID, call.Method, methodsByContainer, superContainerByID, implementersByContainer); implOK {
				method, ok = impl, true
				confidence = minFloat(confidence, 0.72)
				reason = "interface-typed receiver call resolved to the unique TypeScript implementation"
				resolution = "type_inferred"
			}
		}
		if !ok || method.ID == from.ID {
			continue
		}
		if inherited {
			// Resolved on a base class, not the receiver's own type. Still a real
			// call (single inheritance makes the target unambiguous), but one
			// resolution hop removed, so cap the confidence and mark it.
			confidence = minFloat(confidence, 0.82)
			reason += " (inherited from a base type)"
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
			Resolution:    resolution,
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
		if from.Language == "TypeScript" && strings.HasPrefix(strings.TrimSpace(method.Signature), "abstract ") {
			if impl, implOK := uniqueImplementedMethod(targetID, call.Method, methodsByContainer, superContainerByID, implementersByContainer); implOK && impl.ID != method.ID {
				implScope := "file"
				if impl.FilePath != from.FilePath {
					implScope = "module"
				}
				relations = append(relations, RelationRecord{
					RecordType: "relation", FromID: from.ID, ToID: impl.ID, Type: "CALLS",
					Confidence:    minFloat(confidence, 0.72),
					Reason:        "interface-typed receiver call resolved to the unique TypeScript implementation",
					RelationScope: implScope, Resolution: "type_inferred", TargetKind: "symbol",
					Evidence:     []Evidence{{Kind: "call_site", FilePath: from.FilePath, StartLine: from.StartLine, EndLine: from.EndLine, Detail: call.Receiver + "." + call.Method}},
					WarningCodes: []string{},
				})
			}
		}
		methodResolved[call.Receiver+"."+call.Method] = true
	}
	// TypeScript receiver expressions frequently carry their useful type one or
	// more property/collection hops away (`def.type._parseSync()`,
	// `option._parseSync()`). When ordinary type inference cannot recover that
	// receiver, accept a same-file method only if it is unique by short name and
	// belongs to the caller's own inheritance chain. This restores polymorphic
	// base-method calls without reviving the old arbitrary `obj.add()` -> bare
	// function `add` false edge or linking unrelated classes.
	if from.Language == "TypeScript" && from.ContainerID != "" {
		for _, call := range calls {
			key := call.Receiver + "." + call.Method
			if methodResolved[key] || len(importsByName[call.Receiver]) > 0 {
				continue
			}
			if typeName := varTypes[call.Receiver]; typeName != "" && len(importsByName[typeName]) > 0 {
				continue
			}
			method, _, ok := lookupMethodUpChain(from.ContainerID, call.Method, methodsByContainer, superContainerByID)
			if !ok || method.ID == from.ID || method.FilePath != from.FilePath {
				continue
			}
			unique := 0
			for _, candidate := range symbolsByShortName[call.Method] {
				if candidate.Language == from.Language && candidate.Kind == "method" && candidate.FilePath == from.FilePath {
					unique++
				}
			}
			if unique != 1 {
				continue
			}
			methodResolved[key] = true
			relations = append(relations, RelationRecord{
				RecordType: "relation", FromID: from.ID, ToID: method.ID, Type: "CALLS",
				Confidence:    0.64,
				Reason:        "TypeScript receiver call matched unique same-file method on caller inheritance chain",
				RelationScope: "file", Resolution: "name_only", TargetKind: "symbol",
				Evidence:     []Evidence{{Kind: "call_site", FilePath: from.FilePath, StartLine: from.StartLine, EndLine: from.EndLine, Detail: key}},
				WarningCodes: []string{},
			})
		}
	}
	// Globally-unique method-name fallback: when receiver-type resolution did not
	// resolve a Go receiver.method() call and exactly one method with this short
	// name exists in the workspace, resolve to it. This is conflict-free for Go
	// once package selectors and external receiver variables have been excluded:
	// a unique method name means type-based and name-based resolution agree.
	for _, call := range calls {
		if call.Receiver == "this" || call.Receiver == "self" {
			continue
		}
		if methodResolved[call.Receiver+"."+call.Method] {
			continue
		}
		if from.Language != "Go" {
			continue
		}
		if len(importsByName[call.Receiver]) > 0 {
			continue
		}
		// Skip receivers that are values of an external/imported type: the call
		// targets that package's method, not a same-named local one. The receiver
		// is detected from a package-qualified binding (`buf *bytes.Buffer`,
		// `w := json.NewEncoder()`) whose import path is external to this module
		// (importedReceiverVarTypes excludes in-module packages). The fallback
		// still fires for local-typed and fully-unknown receivers.
		if _, external := importedReceiverVars[call.Receiver]; external {
			continue
		}
		m, ok := uniqueMethodByShortName(symbolsByShortName[call.Method])
		if !ok || m.ID == from.ID {
			continue
		}
		methodResolved[call.Receiver+"."+call.Method] = true
		scope := "file"
		if m.FilePath != from.FilePath {
			scope = "module"
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from.ID,
			ToID:          m.ID,
			Type:          "CALLS",
			Confidence:    0.68,
			Reason:        "method call matched globally unique method name",
			RelationScope: scope,
			Resolution:    "name_only",
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
	for _, call := range calls {
		if from.Language != "Zig" || call.Receiver == "this" || call.Receiver == "self" {
			continue
		}
		if methodResolved[call.Receiver+"."+call.Method] {
			continue
		}
		m, ok := uniqueMethodByShortName(symbolsByShortName[call.Method])
		if !ok || m.ID == from.ID || m.FilePath != from.FilePath {
			continue
		}
		methodResolved[call.Receiver+"."+call.Method] = true
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from.ID,
			ToID:          m.ID,
			Type:          "CALLS",
			Confidence:    0.62,
			Reason:        "Zig namespace-style receiver call matched same-file unique callable",
			RelationScope: "file",
			Resolution:    "name_only",
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
	if from.Language == "Perl" {
		for _, call := range calls {
			if methodResolved[call.Receiver+"."+call.Method] {
				continue
			}
			target, ok := uniqueCallableByShortName(symbolsByShortName[call.Method], from.Language)
			if !ok || target.ID == from.ID {
				continue
			}
			methodResolved[call.Receiver+"."+call.Method] = true
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    0.68,
				Reason:        "Perl receiver call matched globally unique subroutine name",
				RelationScope: scope,
				Resolution:    "name_only",
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
	}
	// Kotlin extension functions (`fun Closeable.closeQuietly()`) are top-level
	// functions, not members of the receiver's type, so the container lookups
	// above can never resolve `writerToClose.closeQuietly()`. When member
	// resolution failed, match the call against workspace extension functions:
	// by declared receiver type (the receiver's inferred type or one of its
	// spelled supertypes) when the receiver is typed, or by workspace-unique
	// name when it is not.
	if from.Language == "Kotlin" {
		for _, call := range calls {
			if call.Receiver == "this" || call.Receiver == "self" || call.Receiver == "super" {
				continue
			}
			if methodResolved[call.Receiver+"."+call.Method] {
				continue
			}
			receiverType := varTypes[call.Receiver]
			if receiverType == "" && isCapitalized(call.Receiver) {
				// A capitalized untyped receiver is a type/object reference
				// (`Companion.foo()`), not a value an extension is called on.
				continue
			}
			target, typeDirected, ok := kotlinExtensionFunctionTarget(call, receiverType, from, symbolsByShortName)
			if !ok || target.ID == from.ID {
				continue
			}
			confidence, reason, resolution := 0.78, "call resolved to Kotlin extension function matching the receiver type", "type_inferred"
			if !typeDirected {
				confidence, reason, resolution = 0.68, "call matched workspace-unique Kotlin extension function", "name_only"
			}
			methodResolved[call.Receiver+"."+call.Method] = true
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    confidence,
				Reason:        reason,
				RelationScope: scope,
				Resolution:    resolution,
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
	}
	// Kotlin chained receivers (`call.application.resolveResource(...)`): the
	// base is a typed variable, the middle hop a declared property of that type
	// (workspace field symbols), and the terminal call resolves against the
	// property's type — as a member first, then as a type-directed extension
	// function.
	if from.Language == "Kotlin" {
		for _, chain := range kotlinChains {
			key := chain.Property + "." + chain.Method
			if methodResolved[key] {
				continue
			}
			baseType, ok := varTypes[chain.Base]
			if !ok {
				continue
			}
			propType := kotlinFieldTypeOnType(baseType, chain.Property, from, symbolsByShortName)
			if propType == "" {
				continue
			}
			var target SymbolRecord
			resolved := false
			if sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[propType], propType, from.FilePath); ok {
				if method, _, ok := lookupMethodUpChain(sym.ID, chain.Method, methodsByContainer, superContainerByID); ok {
					target, resolved = method, true
				}
			}
			if !resolved {
				if ext, typeDirected, ok := kotlinExtensionFunctionTarget(receiverCall{Method: chain.Method}, propType, from, symbolsByShortName); ok && typeDirected {
					target, resolved = ext, true
				}
			}
			if !resolved || target.ID == from.ID {
				continue
			}
			methodResolved[key] = true
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    0.75,
				Reason:        "method call resolved via typed property of chained receiver",
				RelationScope: scope,
				Resolution:    "type_inferred",
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    chain.Detail,
				}},
				WarningCodes: []string{},
			})
		}
	}
	// Swift property-chain receivers (`request.fileio.streamFile(...)`): the
	// base is a typed variable, the middle hop a declared (stored or computed)
	// property — including properties added in `extension` blocks, whose field
	// symbols carry the extended type in their qualified name even when the
	// extension lives away from the type's file — and the terminal call
	// resolves against the property's type. An untyped base (route-handler
	// closure parameters like `app.get { req ... }` carry no annotation) falls
	// back to the workspace-unique property name, still gated by the tail
	// method existing on the property's declared type.
	if from.Language == "Swift" {
		for _, chain := range swiftChains {
			key := chain.Property + "." + chain.Method
			if methodResolved[key] {
				continue
			}
			if chain.Base == "self" || chain.Base == "super" {
				// The flat scanner already reads `self.prop.method(` as
				// `prop.method(`, which the stored-property tier types.
				continue
			}
			baseType, baseTyped := varTypes[chain.Base]
			propType := ""
			nameOnly := false
			if baseTyped {
				propType = swiftFieldTypeOnType(baseType, chain.Property, symbolsByShortName)
			} else {
				propType = swiftUniqueFieldType(chain.Property, symbolsByShortName)
				nameOnly = propType != ""
			}
			if propType == "" {
				continue
			}
			var target SymbolRecord
			resolved := false
			if sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[propType], propType, from.FilePath); ok {
				if method, _, ok := lookupMethodUpChain(sym.ID, chain.Method, methodsByContainer, superContainerByID); ok {
					target, resolved = method, true
				}
			}
			if !resolved {
				// Extension members declared in a different file than their
				// type are scoped under the extended type's name but never
				// linked to its container; resolve them by qualified name.
				target, resolved = swiftUniqueQualifiedMethod(propType, chain.Method, symbolsByShortName)
			}
			if !resolved || target.ID == from.ID {
				continue
			}
			confidence, reason, resolution := 0.75, "method call resolved via typed property of chained receiver", "type_inferred"
			if nameOnly {
				confidence, reason, resolution = 0.68, "chained receiver typed via workspace-unique Swift property", "name_only"
			}
			methodResolved[key] = true
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    confidence,
				Reason:        reason,
				RelationScope: scope,
				Resolution:    resolution,
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    chain.Detail,
				}},
				WarningCodes: []string{},
			})
		}
		// Locals chain-bound to dotted nested types (`if let firstRange =
		// contentRange.ranges.first` -> HTTPFields.Range.Value): the nested
		// type's symbol keeps its short name (often ambiguous, e.g. two
		// `Value` enums per header type), so calls on these receivers resolve
		// by the method's unique dotted qualified name instead.
		for _, call := range calls {
			key := call.Receiver + "." + call.Method
			if methodResolved[key] {
				continue
			}
			dottedType := swiftNestedTypeLocals[call.Receiver]
			if dottedType == "" {
				continue
			}
			method, ok := swiftUniqueQualifiedMethod(dottedType, call.Method, symbolsByShortName)
			if !ok || method.ID == from.ID {
				continue
			}
			methodResolved[key] = true
			scope := "file"
			if method.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          method.ID,
				Type:          "CALLS",
				Confidence:    0.75,
				Reason:        "method call resolved via nested-type property-chain local receiver",
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
	}
	if from.Language == "Swift" {
		swiftChainTails := swiftReceiverCallTailsInLongerChains(block)
		for _, chain := range swiftChains {
			swiftChainTails[chain.Property+"."+chain.Method] = true
		}
		for _, call := range calls {
			key := call.Receiver + "." + call.Method
			if methodResolved[key] || swiftChainTails[key] {
				continue
			}
			target, ok := swiftReceiverMethodByArgumentLabels(call, symbolsByShortName[call.Method])
			if !ok || target.ID == from.ID {
				continue
			}
			methodResolved[key] = true
			scope := "file"
			if target.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          target.ID,
				Type:          "CALLS",
				Confidence:    0.66,
				Reason:        "Swift receiver call matched unique argument-label signature",
				RelationScope: scope,
				Resolution:    "signature",
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
	}
	// Java fluent constructor chains (`new Retrofit.Builder().baseUrl(...)
	// .build()`): every chained method resolves against the constructed type
	// when the whole chain is defined on it (fluent builders return `this`);
	// otherwise only the first call — dispatched on the constructed value
	// itself — is trusted.
	if from.Language == "Java" {
		for _, chain := range javaChains {
			sym, ok := javaResolveConstructedType(chain, from, symbolsByShortName)
			if !ok {
				continue
			}
			targets := make([]SymbolRecord, 0, len(chain.Methods))
			for _, name := range chain.Methods {
				method, _, ok := lookupMethodUpChain(sym.ID, name, methodsByContainer, superContainerByID)
				if !ok {
					break
				}
				targets = append(targets, method)
			}
			if len(targets) == 0 {
				continue
			}
			if len(targets) < len(chain.Methods) {
				targets = targets[:1]
			}
			for _, method := range targets {
				if method.ID == from.ID {
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
					Reason:        "method call resolved via fluent constructor chain type",
					RelationScope: scope,
					Resolution:    "type_inferred",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      "call_site",
						FilePath:  from.FilePath,
						StartLine: from.StartLine,
						EndLine:   from.EndLine,
						Detail:    chain.Detail,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	// Ruby receiver-less calls target implicit self: emit an edge when the bare
	// word resolves to a method of the enclosing class (or an ancestor). The
	// candidate list already excludes keywords, locals, parameters, and hash
	// keys; requiring a same-class method match keeps this precise.
	for _, name := range rubyBareCalls {
		if name == from.Name || from.ContainerID == "" {
			continue
		}
		method, inherited, ok := lookupMethodUpChain(from.ContainerID, name, methodsByContainer, superContainerByID)
		if !ok || method.ID == from.ID {
			continue
		}
		confidence := 0.85
		reason := "receiver-less Ruby call resolved to a method of the enclosing class"
		if inherited {
			confidence = 0.8
			reason += " (inherited from a base type)"
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
				Detail:    name,
			}},
			WarningCodes: []string{},
		})
	}
	// PHP `Class::method()` / `self::` / `static::` / `parent::` static calls:
	// resolve the class reference (terminal segment against the workspace type
	// index, or the enclosing class / its superclass for the keywords), then
	// look the method up the inheritance chain like every other receiver call.
	for _, call := range phpStatics {
		var targetID string
		confidence := 0.82
		reason := "PHP static method call resolved to the named class"
		switch call.Class {
		case "self", "static":
			targetID = from.ContainerID
			confidence = 0.9
			reason = "PHP self/static call resolved to the enclosing class"
		case "parent":
			targetID = superContainerByID[from.ContainerID]
			confidence = 0.85
			reason = "PHP parent:: call resolved to the superclass"
		default:
			cls, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[call.Class], call.Class, from.FilePath)
			if !ok {
				continue
			}
			targetID = cls.ID
		}
		if targetID == "" {
			continue
		}
		method, inherited, ok := lookupMethodUpChain(targetID, call.Method, methodsByContainer, superContainerByID)
		if !ok || method.ID == from.ID {
			continue
		}
		if inherited && call.Class != "parent" {
			confidence = minFloat(confidence, 0.82)
			reason += " (inherited from a base type)"
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
				Detail:    call.Detail,
			}},
			WarningCodes: []string{},
		})
	}
	// PHP `$this->prop->method()` property receivers: the property's type comes
	// from this file's typed declarations, @var docblocks, or constructor
	// assignments (phpPropertyTypes, computed once per file by the caller).
	for _, call := range phpPropCalls {
		typeName, ok := phpPropTypes[call.Receiver]
		if !ok {
			continue
		}
		sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[typeName], typeName, from.FilePath)
		if !ok {
			continue
		}
		method, inherited, ok := lookupMethodUpChain(sym.ID, call.Method, methodsByContainer, superContainerByID)
		if !ok || method.ID == from.ID {
			continue
		}
		confidence := 0.8
		reason := "method call resolved via typed property receiver"
		if inherited {
			confidence = 0.78
			reason += " (inherited from a base type)"
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
				Detail:    "this->" + call.Receiver + "->" + call.Method,
			}},
			WarningCodes: []string{},
		})
	}
	// Generated factory convention: a property typed `CustomerFactory` exposes
	// `create()`, whose returned object is the generated product type
	// (`Customer`). Resolve the terminal chain call on that product.
	for _, call := range phpFactoryChains {
		factoryType, ok := phpPropTypes[call.Property]
		if !ok || call.FactoryMethod != "create" || !strings.HasSuffix(factoryType, "Factory") {
			continue
		}
		typeName := strings.TrimSuffix(factoryType, "Factory")
		if typeName == "" {
			continue
		}
		sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[typeName], typeName, from.FilePath)
		if !ok {
			continue
		}
		method, inherited, ok := lookupMethodUpChain(sym.ID, call.Method, methodsByContainer, superContainerByID)
		if !ok || method.ID == from.ID {
			continue
		}
		confidence := 0.72
		reason := "PHP factory property chain resolved via generated Factory::create product type"
		if inherited {
			confidence = 0.7
			reason += " (inherited from a base type)"
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
				Detail:    call.Detail,
			}},
			WarningCodes: []string{},
		})
	}
	relations = append(relations, rustModulePathCallRelations(from, rustPathCalls, importsByName, symbolsByShortName)...)
	// C# one-hop member chains `A.B.Method(...)`: A is a typed member of the
	// enclosing class (or a typed parameter/local), B a typed property on A's
	// type — declared in that type's own file — and Method resolves up B's
	// type's inheritance chain. Interface-typed members resolve to the
	// interface's method symbol, matching how the call is dispatched.
	for _, call := range csChainCalls {
		if methodResolved[call.Property+"."+call.Method] {
			// The generic scanner read this site as `B.Method(` and a typed
			// tier above already resolved that pair (e.g. the enclosing class
			// itself has a member named B of the same type); don't double-emit.
			continue
		}
		// The property is looked up on A's type — or on the enclosing class
		// itself when the chain is spelled `this.B.Method(` / `base.B.Method(`
		// (the member walk already covers inherited members for both).
		propContainer, propFile := "", from.FilePath
		if call.Receiver == "this" || call.Receiver == "base" {
			propContainer = from.ContainerID
		} else {
			receiverType, ok := csharpReceiverMemberType(from, call.Receiver, fieldsByContainer, superContainerByID)
			if !ok {
				receiverType, ok = varTypes[call.Receiver]
			}
			if !ok {
				continue
			}
			receiverSym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[receiverType], receiverType, from.FilePath)
			if !ok {
				continue
			}
			propContainer, propFile = receiverSym.ID, receiverSym.FilePath
		}
		if propContainer == "" {
			continue
		}
		propType, ok := csharpMemberType(fieldsByContainer, superContainerByID, propContainer, call.Property)
		if !ok {
			continue
		}
		propSym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[propType], propType, propFile)
		if !ok {
			continue
		}
		method, inherited, ok := lookupMethodUpChain(propSym.ID, call.Method, methodsByContainer, superContainerByID)
		if !ok || method.ID == from.ID {
			continue
		}
		confidence := 0.75
		reason := "method call resolved via typed property chain receiver"
		if inherited {
			confidence = 0.73
			reason += " (inherited from a base type)"
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
				Detail:    call.Detail,
			}},
			WarningCodes: []string{},
		})
		// The generic scanner saw this site as `B.Method(`; mark that pair
		// resolved so the fallback tiers below do not re-emit for it.
		methodResolved[call.Property+"."+call.Method] = true
	}
	// C# extension methods: `x.Ext(...)` never resolves through the receiver's
	// type (the method is a static on an unrelated static class), so a receiver
	// call left unresolved by the typed tiers falls back to the workspace's
	// unique extension-shaped method with that name (static + `this` first
	// parameter). Uniqueness keeps this conflict-free, as in the Go fallback.
	if from.Language == "C#" {
		for _, call := range allReceiverCalls {
			if call.Receiver == "this" || call.Receiver == "self" || call.Receiver == "base" {
				continue
			}
			if methodResolved[call.Receiver+"."+call.Method] {
				continue
			}
			method, ok := csharpUniqueExtensionMethod(symbolsByShortName[call.Method])
			if !ok || method.ID == from.ID {
				continue
			}
			methodResolved[call.Receiver+"."+call.Method] = true
			scope := "file"
			if method.FilePath != from.FilePath {
				scope = "module"
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          method.ID,
				Type:          "CALLS",
				Confidence:    0.7,
				Reason:        "method call resolved to the unique C# extension method",
				RelationScope: scope,
				Resolution:    "name_only",
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
	}
	for _, call := range chainedCalls {
		sym, ok := firstTypeLikeNamed(symbolsByShortName[call.TypeName], call.TypeName)
		if !ok {
			continue
		}
		confidence := 0.8
		reason := "method call resolved via chained constructor type"
		resolution := "type_inferred"
		method, ok := methodsByContainer[sym.ID][call.Method]
		if !ok && interfaceSignatureDeclaresMethod(sym.Signature, call.Method) {
			// The "constructor" here is really an interface-typed accessor: a Go
			// getter is conventionally named after the interface it returns
			// (s.AuthStore().IsAdminPermitted()), and interface methods are
			// declared inside the type declaration rather than as method
			// symbols. When the interface names this method and exactly one
			// method in the workspace carries the name, resolve to that sole
			// implementation — the same unique-name tier as the receiver-call
			// fallback.
			method, ok = uniqueMethodByShortName(symbolsByShortName[call.Method])
			confidence = 0.7
			reason = "interface method call resolved to the unique implementing method"
			resolution = "name_only"
		}
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
			Resolution:    resolution,
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
		if deepReturnedCallSuffixes[call.Factory+"."+call.Method] {
			continue
		}
		returnTypes := returnTypesBySymbolNameAndFile[call.Factory][from.FilePath]
		if len(returnTypes) == 0 && from.Language == "Go" {
			// A Go factory is commonly declared in a sibling file of the same
			// package (one package spans a directory), so fall back to the
			// package-level return types when this file declares no such factory.
			returnTypes = returnTypesBySymbolNameAndDir[call.Factory][filepath.ToSlash(filepath.Dir(from.FilePath))]
		}
		if len(returnTypes) == 0 && from.Language == "Rust" {
			method, ok := uniqueMethodByShortName(symbolsByShortName[call.Method])
			if !ok || method.ID == from.ID || method.Language != "Rust" {
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
				Confidence:    0.66,
				Reason:        "Rust returned receiver call matched globally unique method name",
				RelationScope: scope,
				Resolution:    "name_only",
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
			continue
		}
		for _, typeName := range returnTypes {
			sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
			if !ok {
				continue
			}
			confidence := 0.78
			reason := "method call resolved via returned receiver type"
			resolution := "type_inferred"
			method, ok := methodsByContainer[sym.ID][call.Method]
			if !ok && interfaceSignatureDeclaresMethod(sym.Signature, call.Method) {
				// Interface-typed return: a Go interface declares its methods
				// inside the type declaration itself (they are not separate
				// method symbols), so the container lookup above cannot succeed.
				// When the locally-known interface names this method and exactly
				// one method in the workspace carries the name, resolve to that
				// sole implementation — the same unique-name tier as the
				// receiver-call fallback.
				method, ok = uniqueMethodByShortName(symbolsByShortName[call.Method])
				confidence = 0.7
				reason = "interface-typed return resolved to the unique implementing method"
				resolution = "name_only"
			}
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
				Resolution:    resolution,
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
	for _, call := range chainedReturnCalls {
		for _, typeName := range methodReturnChainTypes(call.TypeName, []string{call.FirstMethod}, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
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
				Confidence:    0.74,
				Reason:        "method call resolved via chained constructor-return type",
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
	for _, call := range deepChainedReturnCalls {
		if len(call.Methods) < 2 {
			continue
		}
		intermediateMethods := call.Methods[:len(call.Methods)-1]
		finalMethod := call.Methods[len(call.Methods)-1]
		for _, typeName := range methodReturnChainTypes(call.TypeName, intermediateMethods, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
			sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
			if !ok {
				continue
			}
			method, ok := methodsByContainer[sym.ID][finalMethod]
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
				Confidence:    0.71,
				Reason:        "method call resolved via deep chained constructor-return type",
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
	for _, call := range returnedChainCalls {
		for _, factoryTypeName := range returnTypesBySymbolNameAndFile[call.Factory][from.FilePath] {
			for _, typeName := range methodReturnChainTypes(factoryTypeName, []string{call.FirstMethod}, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
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
					Confidence:    0.73,
					Reason:        "method call resolved via chained returned receiver type",
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
			break
		}
	}
	for _, call := range returnedDeepChainCalls {
		if len(call.Methods) < 2 {
			continue
		}
		intermediateMethods := call.Methods[:len(call.Methods)-1]
		finalMethod := call.Methods[len(call.Methods)-1]
		for _, factoryTypeName := range returnTypesBySymbolNameAndFile[call.Factory][from.FilePath] {
			for _, typeName := range methodReturnChainTypes(factoryTypeName, intermediateMethods, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
				sym, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
				if !ok {
					continue
				}
				method, ok := methodsByContainer[sym.ID][finalMethod]
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
					Confidence:    0.7,
					Reason:        "method call resolved via deep chained returned receiver type",
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
			break
		}
	}
	return relations
}

func receiverQualifiedMethodTarget(from SymbolRecord, call receiverCall, candidates []SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string) (SymbolRecord, float64, string, string, string, bool) {
	qualified := call.Receiver + "." + call.Method
	var matches []SymbolRecord
	var sameFile []SymbolRecord
	for _, candidate := range candidates {
		if candidate.ID == from.ID || candidate.Kind != "method" {
			continue
		}
		if candidate.QualifiedName != qualified && candidate.Name != qualified {
			continue
		}
		matches = append(matches, candidate)
		if candidate.FilePath == from.FilePath {
			sameFile = append(sameFile, candidate)
		}
	}
	if len(sameFile) == 1 {
		return sameFile[0], 0.82, "receiver call resolved to same-file qualified method symbol", "exact", "file", true
	}
	if target, ok := receiverQualifiedOverloadByArgReturnType(from, call, sameFile, returnTypesBySymbolNameAndFile); ok {
		return target, 0.8, "receiver call overload resolved from nested argument return type", "type_inferred", "file", true
	}
	if len(matches) == 1 {
		scope := "workspace"
		if matches[0].FilePath != from.FilePath {
			scope = "module"
		}
		return matches[0], 0.66, "receiver call matched globally unique qualified method symbol", "name_only", scope, true
	}
	if target, ok := receiverQualifiedOverloadByArgReturnType(from, call, matches, returnTypesBySymbolNameAndFile); ok {
		scope := "workspace"
		if target.FilePath != from.FilePath {
			scope = "module"
		}
		return target, 0.78, "receiver call overload resolved from nested argument return type", "type_inferred", scope, true
	}
	return SymbolRecord{}, 0, "", "", "", false
}

// uniqueMethodByShortName returns the sole method whose short name matches, if
// exactly one method (across the workspace) carries that name. Used as a
// last-resort receiver.method() resolver when the receiver type is unknown.
func uniqueMethodByShortName(candidates []SymbolRecord) (SymbolRecord, bool) {
	var methods []SymbolRecord
	for _, c := range candidates {
		if c.Kind == "method" {
			methods = append(methods, c)
		}
	}
	if len(methods) == 1 {
		return methods[0], true
	}
	return SymbolRecord{}, false
}

func uniqueCallableByShortName(candidates []SymbolRecord, language string) (SymbolRecord, bool) {
	var callables []SymbolRecord
	for _, c := range candidates {
		if language != "" && c.Language != language {
			continue
		}
		if c.Kind == "function" || c.Kind == "method" {
			callables = append(callables, c)
		}
	}
	if len(callables) == 1 {
		return callables[0], true
	}
	return SymbolRecord{}, false
}

func receiverQualifiedOverloadByArgReturnType(from SymbolRecord, call receiverCall, candidates []SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string) (SymbolRecord, bool) {
	if len(candidates) < 2 || strings.TrimSpace(call.Args) == "" {
		return SymbolRecord{}, false
	}
	argTypes := receiverCallArgumentReturnTypes(call.Args, from.FilePath, returnTypesBySymbolNameAndFile)
	if len(argTypes) == 0 {
		return SymbolRecord{}, false
	}
	var matches []SymbolRecord
	for _, candidate := range candidates {
		params := signatureTypeReferences(candidate.Language, candidate.Signature)["PARAM_TYPE"]
		if typeListIntersects(params, argTypes) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return SymbolRecord{}, false
}

func receiverCallArgumentReturnTypes(args, filePath string, returnTypesBySymbolNameAndFile map[string]map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, arg := range splitTopLevelStaticComma(args) {
		arg = strings.TrimSpace(arg)
		m := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\(`).FindStringSubmatch(arg)
		if len(m) != 2 {
			continue
		}
		for _, typeName := range returnTypesBySymbolNameAndFile[m[1]][filePath] {
			if typeName == "" || seen[typeName] {
				continue
			}
			seen[typeName] = true
			out = append(out, typeName)
		}
	}
	return out
}

func typeListIntersects(left, right []string) bool {
	set := map[string]bool{}
	for _, value := range left {
		set[value] = true
	}
	for _, value := range right {
		if set[value] {
			return true
		}
	}
	return false
}

func receiverDeepChainSuffixes(chained []typedMethodDeepChainCall, returned []returnedMethodDeepChainCall) map[string]bool {
	suffixes := map[string]bool{}
	add := func(methods []string) {
		if len(methods) < 2 {
			return
		}
		receiver := methods[len(methods)-2]
		method := methods[len(methods)-1]
		if receiver != "" && method != "" {
			suffixes[receiver+"."+method] = true
		}
	}
	for _, call := range chained {
		add(call.Methods)
	}
	for _, call := range returned {
		add(call.Methods)
	}
	return suffixes
}

func methodReturnChainTypes(typeName string, methodNames []string, methodsByContainer map[string]map[string]SymbolRecord, symbolsByShortName map[string][]SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string) []string {
	if typeName == "" {
		return nil
	}
	types := []string{typeName}
	for _, methodName := range methodNames {
		if methodName == "" || len(types) == 0 {
			return nil
		}
		var next []string
		seen := map[string]bool{}
		for _, currentType := range types {
			for _, returnedType := range methodReturnTypes(currentType, methodName, methodsByContainer, symbolsByShortName, returnTypesBySymbolNameAndFile) {
				if returnedType == "" || seen[returnedType] {
					continue
				}
				seen[returnedType] = true
				next = append(next, returnedType)
			}
		}
		types = next
	}
	return types
}

func methodReturnTypes(typeName, methodName string, methodsByContainer map[string]map[string]SymbolRecord, symbolsByShortName map[string][]SymbolRecord, returnTypesBySymbolNameAndFile map[string]map[string][]string) []string {
	typeSymbol, ok := firstTypeLikeNamed(symbolsByShortName[typeName], typeName)
	if !ok {
		return nil
	}
	method, ok := methodsByContainer[typeSymbol.ID][methodName]
	if !ok {
		return nil
	}
	return returnTypesBySymbolNameAndFile[method.Name][method.FilePath]
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

func importedReceiverCallRelations(from SymbolRecord, block string, importsByName map[string][]string, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	if typeLikeKind(from.Kind) {
		return nil
	}
	var relations []RelationRecord
	seen := map[string]bool{}
	for _, call := range receiverCalls(block) {
		var localTargets []resolvedCallTarget
		for _, to := range symbolsByShortName[call.Method] {
			if to.ID == from.ID || to.Kind == "field" {
				continue
			}
			if importedNameMatchesFile(importsByName[call.Receiver], from.FilePath, to.FilePath) {
				localTargets = append(localTargets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.84,
					Reason:       "receiver call resolved through imported module path",
					Resolution:   "import_resolved",
					Scope:        "module",
				})
			}
		}
		if len(localTargets) > 0 {
			for _, target := range localTargets {
				key := target.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        from.ID,
					ToID:          target.ID,
					Type:          "CALLS",
					Confidence:    target.Confidence,
					Reason:        target.Reason,
					RelationScope: target.Scope,
					Resolution:    target.Resolution,
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
			continue
		}
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
	relations = append(relations, kubernetesSelectorExpressionResourceRelations(recordsByFile, readContent)...)
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
			targetName := dep.ExternalName
			if targetName == "" {
				targetName = dep.Name
			}
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        source.ID,
				ToID:          externalID("config", "kubernetes/"+dep.Kind+"/"+targetName),
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
					Detail:    dep.Kind + "/" + targetName,
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
	ExternalName string
	EvidenceKind string
	Confidence   float64
}

func kubernetesResourceReferences(content string) []resourceReference {
	var refs []resourceReference
	metadata := yamlMapAtPath(content, "metadata")
	resourceNamespace := strings.TrimSpace(metadata["namespace"])
	add := func(kind, name, evidence string, confidence float64) {
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if name == "" {
			return
		}
		refs = append(refs, resourceReference{Kind: strings.ToLower(kind), Name: name, EvidenceKind: evidence, Confidence: confidence})
		if resourceNamespace != "" && kubernetesNamespacedReferenceKind(kind) {
			refs = append(refs, resourceReference{Kind: strings.ToLower(kind), Name: name, ExternalName: resourceNamespace + "/" + name, EvidenceKind: evidence, Confidence: confidence})
		}
	}
	if len(metadata) > 0 {
		if namespace := metadata["namespace"]; namespace != "" {
			add("namespace", namespace, "kubernetes_metadata_namespace", 0.84)
		}
	}
	for _, match := range regexp.MustCompile(`(?is)\bconfigMapRef:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("configmap", match[1], "kubernetes_configmap_ref", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bconfigMapKeyRef:[ \t]*\n(?:[ \t]+[A-Za-z0-9_-]+:[^\n]*\n)*[ \t]+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("configmap", match[1], "kubernetes_configmap_key_ref", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*(?:-\s*)?configMap:\s*\n\s+name:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("configmap", match[1], "kubernetes_configmap_volume", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bsecretRef:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_secret_ref", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?is)\bsecretKeyRef:[ \t]*\n(?:[ \t]+[A-Za-z0-9_-]+:[^\n]*\n)*[ \t]+name:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
		add("secret", match[1], "kubernetes_secret_key_ref", 0.8)
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
	for _, match := range regexp.MustCompile(`(?im)^\s*storageClassName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("storageclass", match[1], "kubernetes_storage_class", 0.78)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*volumeName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("persistentvolume", match[1], "kubernetes_persistent_volume", 0.78)
	}
	if kubernetesManifestHasAnyKind(content, "PersistentVolumeClaim") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "dataSource", "kubernetes_pvc_data_source_ref", 0.82, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "dataSourceRef", "kubernetes_pvc_data_source_ref", 0.82, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "VolumeSnapshot") {
		for _, match := range regexp.MustCompile(`(?im)^\s*persistentVolumeClaimName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
			add("persistentvolumeclaim", match[1], "kubernetes_volume_snapshot_pvc_ref", 0.82)
		}
	}
	if kubernetesManifestHasAnyKind(content, "VolumeSnapshotContent") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "volumeSnapshotRef", "kubernetes_volume_snapshot_content_ref", 0.82, kubernetesDefaultReferenceKind("volumesnapshot")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*ingressClassName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("ingressclass", match[1], "kubernetes_ingress_class", 0.8)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*runtimeClassName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("runtimeclass", match[1], "kubernetes_runtime_class", 0.78)
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*priorityClassName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		add("priorityclass", match[1], "kubernetes_priority_class", 0.78)
	}
	for _, ref := range kubernetesKindNameBlockReferences(content, "roleRef", "kubernetes_rbac_role_ref", 0.82) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesKindNameBlockReferences(content, "scaleTargetRef", "kubernetes_hpa_scale_target", 0.84) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	if kubernetesManifestHasAnyKind(content, "VerticalPodAutoscaler") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "targetRef", "kubernetes_vpa_target_ref", 0.84, kubernetesDefaultReferenceKind("deployment")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	for _, ref := range kubernetesScaledObjectScaleTargetReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	if kubernetesManifestHasAnyKind(content, "ScaledObject", "ScaledJob") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "authenticationRef", "kubernetes_keda_authentication_ref", 0.84, kubernetesDefaultReferenceKind("triggerauthentication")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "Certificate", "CertificateRequest") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "issuerRef", "kubernetes_cert_manager_issuer_ref", 0.84, kubernetesDefaultReferenceKind("issuer")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	for _, ref := range kubernetesCertManagerIssuerSecretReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	if kubernetesManifestHasAnyKind(content, "ExternalSecret", "ClusterExternalSecret", "PushSecret") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "secretStoreRef", "kubernetes_external_secret_store_ref", 0.84, kubernetesDefaultReferenceKind("secretstore")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "ExternalSecret", "ClusterExternalSecret") {
		for _, ref := range kubernetesExternalSecretTargetReferences(content) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "SealedSecret") {
		for _, ref := range kubernetesSealedSecretTargetReferences(content) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "Workflow", "CronWorkflow", "WorkflowTemplate") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "workflowTemplateRef", "kubernetes_argo_workflow_template_ref", 0.84, kubernetesWorkflowTemplateReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "templateRef", "kubernetes_argo_template_ref", 0.8, kubernetesWorkflowTemplateReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	for _, ref := range kubernetesRolloutsAnalysisTemplateReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesArgoEventsReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesArgoCDApplicationProjectReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	if kubernetesManifestHasAnyKind(content, "PipelineRun", "TaskRun", "Pipeline") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "pipelineRef", "kubernetes_tekton_pipeline_ref", 0.84, kubernetesDefaultReferenceKind("pipeline")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "taskRef", "kubernetes_tekton_task_ref", 0.82, kubernetesDefaultReferenceKind("task")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "ServiceBinding") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "service", "kubernetes_service_binding_service_ref", 0.84, kubernetesDefaultReferenceKind("service")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "workload", "kubernetes_service_binding_workload_ref", 0.84, kubernetesDefaultReferenceKind("deployment")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "Trigger") {
		for _, ref := range kubernetesKnativeTriggerBrokerReferences(content) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "ref", "kubernetes_knative_subscriber_ref", 0.82, kubernetesDefaultReferenceKind("service")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "Subscription") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "channel", "kubernetes_knative_subscription_channel_ref", 0.84, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "subscriber", "kubernetes_knative_subscription_subscriber_ref", 0.82, kubernetesDefaultReferenceKind("service")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "reply", "kubernetes_knative_subscription_reply_ref", 0.82, kubernetesDefaultReferenceKind("service")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	for _, ref := range kubernetesKnativeServingTrafficReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesPrometheusMonitorSecretReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesReloaderAnnotationReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	if kubernetesManifestHasAnyKind(content, "HelmRelease", "HelmChart", "Kustomization", "ImageRepository", "ImagePolicy", "ImageUpdateAutomation") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "sourceRef", "kubernetes_flux_source_ref", 0.84, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "HelmRelease") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "chartRef", "kubernetes_flux_chart_ref", 0.84, kubernetesDefaultReferenceKind("helmchart")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesFluxHelmReleaseValuesFromReferences(content) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kind := kubernetesFluxDependsOnKind(content); kind != "" {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "dependsOn", "kubernetes_flux_depends_on", 0.82, kubernetesDefaultReferenceKind(kind)) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasAnyKind(content, "Gateway") {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "certificateRefs", "kubernetes_gateway_certificate_ref", 0.84, kubernetesGatewayCertificateReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
	}
	if kubernetesManifestHasCrossplaneReferences(content) {
		for _, ref := range kubernetesNamedRefBlockReferences(content, "providerConfigRef", "kubernetes_crossplane_provider_config_ref", 0.84, kubernetesDefaultReferenceKind("providerconfig")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "compositionRef", "kubernetes_crossplane_composition_ref", 0.84, kubernetesDefaultReferenceKind("composition")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "compositionRevisionRef", "kubernetes_crossplane_composition_revision_ref", 0.82, kubernetesDefaultReferenceKind("compositionrevision")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "resourceRef", "kubernetes_crossplane_resource_ref", 0.82, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "resourceRefs", "kubernetes_crossplane_resource_ref", 0.82, kubernetesExplicitReferenceKind) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
		for _, ref := range kubernetesNamedRefBlockReferences(content, "writeConnectionSecretToRef", "kubernetes_crossplane_connection_secret_ref", 0.82, kubernetesDefaultReferenceKind("secret")) {
			add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
		}
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
	for _, ref := range kubernetesGatewayBackendReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesGatewayParentReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesGatewayPolicyTargetReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
	}
	for _, ref := range kubernetesIstioServiceMeshReferences(content) {
		add(ref.Kind, ref.Name, ref.EvidenceKind, ref.Confidence)
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

func kubernetesNamespacedReferenceKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "namespace", "node", "persistentvolume", "storageclass", "runtimeclass", "priorityclass",
		"clusterissuer", "clustersecretstore", "clustertriggerauthentication", "clusterworkflowtemplate",
		"clusteranalysistemplate", "providerconfig", "composition":
		return false
	default:
		return true
	}
}

func kubernetesNamedRefBlockReferences(content, blockKey, evidence string, confidence float64, kindFor func(map[string]string) string) []resourceReference {
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != blockKey {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		fields := map[string]string{}
		flush := func() {
			name := strings.Trim(strings.TrimSpace(fields["name"]), `"'`)
			if name == "" {
				fields = map[string]string{}
				return
			}
			kind := strings.Trim(strings.TrimSpace(kindFor(fields)), `"'`)
			if kind == "" {
				fields = map[string]string{}
				return
			}
			refs = append(refs, resourceReference{
				Kind:         strings.ToLower(kind),
				Name:         name,
				EvidenceKind: evidence,
				Confidence:   confidence,
			})
			fields = map[string]string{}
		}
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				flush()
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			}
			childKey, ok := yamlLineKey(trimmed)
			if !ok {
				continue
			}
			fields[childKey] = yamlLineValue(trimmed)
		}
		flush()
	}
	return refs
}

func kubernetesDefaultReferenceKind(defaultKind string) func(map[string]string) string {
	return func(fields map[string]string) string {
		if kind := fields["kind"]; kind != "" {
			return kind
		}
		return defaultKind
	}
}

func kubernetesExplicitReferenceKind(fields map[string]string) string {
	return fields["kind"]
}

func kubernetesFluxHelmReleaseValuesFromReferences(content string) []resourceReference {
	var refs []resourceReference
	for _, ref := range kubernetesNamedRefBlockReferences(content, "valuesFrom", "kubernetes_flux_values_from", 0.82, kubernetesExplicitReferenceKind) {
		switch strings.ToLower(ref.Kind) {
		case "configmap", "secret":
			refs = append(refs, ref)
		}
	}
	return refs
}

func kubernetesExternalSecretTargetReferences(content string) []resourceReference {
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != "target" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			childKey, ok := yamlLineKey(line)
			if !ok || childKey != "name" {
				continue
			}
			name := strings.Trim(strings.TrimSpace(yamlLineValue(line)), `"'`)
			if name == "" {
				continue
			}
			refs = append(refs, resourceReference{
				Kind:         "secret",
				Name:         name,
				EvidenceKind: "kubernetes_external_secret_target",
				Confidence:   0.82,
			})
			break
		}
	}
	return refs
}

func kubernetesCertManagerIssuerSecretReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `cert-manager\.io/`) || !kubernetesManifestHasAnyKind(content, "Issuer", "ClusterIssuer") {
		return nil
	}
	var refs []resourceReference
	for _, blockKey := range []string{
		"privateKeySecretRef",
		"apiTokenSecretRef",
		"apiKeySecretRef",
		"secretAccessKeySecretRef",
		"clientSecretSecretRef",
		"serviceAccountSecretRef",
	} {
		refs = append(refs, kubernetesNamedRefBlockReferences(content, blockKey, "kubernetes_cert_manager_issuer_secret_ref", 0.82, kubernetesDefaultReferenceKind("secret"))...)
	}
	return dedupeResourceReferences(refs)
}

func kubernetesSealedSecretTargetReferences(content string) []resourceReference {
	name := strings.Trim(strings.TrimSpace(yamlMapAtPath(content, "spec", "template", "metadata")["name"]), `"'`)
	evidence := "kubernetes_sealed_secret_template"
	if name == "" {
		name = strings.Trim(strings.TrimSpace(yamlMapAtPath(content, "metadata")["name"]), `"'`)
		evidence = "kubernetes_sealed_secret_name"
	}
	if name == "" {
		return nil
	}
	return []resourceReference{{
		Kind:         "secret",
		Name:         name,
		EvidenceKind: evidence,
		Confidence:   0.84,
	}}
}

func kubernetesKnativeTriggerBrokerReferences(content string) []resourceReference {
	var refs []resourceReference
	re := regexp.MustCompile(`(?im)^\s*broker:\s*([A-Za-z0-9_.-]+)\s*$`)
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			refs = append(refs, resourceReference{
				Kind:         "broker",
				Name:         match[1],
				EvidenceKind: "kubernetes_knative_broker_ref",
				Confidence:   0.82,
			})
		}
	}
	return refs
}

func kubernetesKnativeServingTrafficReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `serving\.knative\.dev/`) || !kubernetesManifestHasAnyKind(content, "Service", "Route") {
		return nil
	}
	var refs []resourceReference
	for _, match := range regexp.MustCompile(`(?im)^\s*(?:-\s*)?revisionName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		refs = append(refs, resourceReference{
			Kind:         "revision",
			Name:         match[1],
			EvidenceKind: "kubernetes_knative_traffic_revision_ref",
			Confidence:   0.82,
		})
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*(?:-\s*)?configurationName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		refs = append(refs, resourceReference{
			Kind:         "configuration",
			Name:         match[1],
			EvidenceKind: "kubernetes_knative_traffic_configuration_ref",
			Confidence:   0.82,
		})
	}
	return refs
}

func kubernetesPrometheusMonitorSecretReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `monitoring\.coreos\.com/`) || !kubernetesManifestHasAnyKind(content, "ServiceMonitor", "PodMonitor", "Probe", "ScrapeConfig") {
		return nil
	}
	var refs []resourceReference
	for _, blockKey := range []string{"bearerTokenSecret", "keySecret", "username", "password", "clientSecret", "credentials"} {
		refs = append(refs, kubernetesNamedRefBlockReferences(content, blockKey, "kubernetes_prometheus_monitor_secret_ref", 0.82, kubernetesDefaultReferenceKind("secret"))...)
	}
	return refs
}

func kubernetesReloaderAnnotationReferences(content string) []resourceReference {
	var refs []resourceReference
	for _, path := range [][]string{
		{"metadata", "annotations"},
		{"spec", "template", "metadata", "annotations"},
		{"spec", "jobTemplate", "spec", "template", "metadata", "annotations"},
	} {
		for key, value := range yamlMapAtPath(content, path...) {
			kind := ""
			evidence := ""
			switch key {
			case "configmap.reloader.stakater.com/reload":
				kind = "configmap"
				evidence = "kubernetes_reloader_configmap_ref"
			case "secret.reloader.stakater.com/reload":
				kind = "secret"
				evidence = "kubernetes_reloader_secret_ref"
			default:
				continue
			}
			for _, name := range splitKubernetesNamedList(value) {
				refs = append(refs, resourceReference{
					Kind:         kind,
					Name:         name,
					EvidenceKind: evidence,
					Confidence:   0.82,
				})
			}
		}
	}
	return dedupeResourceReferences(refs)
}

func splitKubernetesNamedList(value string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		name := strings.Trim(strings.TrimSpace(part), `"'`)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func kubernetesManifestAPIMatches(content, pattern string) bool {
	return regexp.MustCompile(`(?im)^\s*apiVersion:\s*` + pattern).MatchString(content)
}

func kubernetesFluxDependsOnKind(content string) string {
	switch {
	case kubernetesManifestHasAnyKind(content, "HelmRelease"):
		return "helmrelease"
	case kubernetesManifestHasAnyKind(content, "Kustomization"):
		return "kustomization"
	default:
		return ""
	}
}

func kubernetesManifestHasCrossplaneReferences(content string) bool {
	return strings.Contains(content, "providerConfigRef:") ||
		strings.Contains(content, "compositionRef:") ||
		strings.Contains(content, "compositionRevisionRef:") ||
		strings.Contains(content, "resourceRef:") ||
		strings.Contains(content, "resourceRefs:") ||
		strings.Contains(content, "writeConnectionSecretToRef:")
}

func kubernetesWorkflowTemplateReferenceKind(fields map[string]string) string {
	if kind := fields["kind"]; kind != "" {
		return kind
	}
	if strings.EqualFold(fields["clusterScope"], "true") {
		return "clusterworkflowtemplate"
	}
	return "workflowtemplate"
}

func kubernetesGatewayCertificateReferenceKind(fields map[string]string) string {
	kind := strings.Trim(strings.TrimSpace(fields["kind"]), `"'`)
	if kind == "" || strings.EqualFold(kind, "Secret") {
		return "secret"
	}
	return ""
}

func kubernetesRolloutsAnalysisTemplateReferences(content string) []resourceReference {
	if !kubernetesManifestHasAnyKind(content, "Rollout", "AnalysisRun", "Experiment") {
		return nil
	}
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != "templates" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		fields := map[string]string{}
		flush := func() {
			name := strings.Trim(strings.TrimSpace(fields["templateName"]), `"'`)
			if name == "" {
				fields = map[string]string{}
				return
			}
			kind := "analysistemplate"
			if strings.EqualFold(strings.Trim(strings.TrimSpace(fields["clusterScope"]), `"'`), "true") {
				kind = "clusteranalysistemplate"
			}
			refs = append(refs, resourceReference{
				Kind:         kind,
				Name:         name,
				EvidenceKind: "kubernetes_argo_rollouts_analysis_template_ref",
				Confidence:   0.84,
			})
			fields = map[string]string{}
		}
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				flush()
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			}
			childKey, ok := yamlLineKey(trimmed)
			if !ok {
				continue
			}
			fields[childKey] = yamlLineValue(trimmed)
		}
		flush()
	}
	return refs
}

func kubernetesArgoEventsReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `argoproj\.io/`) || !kubernetesManifestHasAnyKind(content, "Sensor", "EventSource") {
		return nil
	}
	var refs []resourceReference
	if kubernetesManifestHasAnyKind(content, "Sensor") {
		for _, match := range regexp.MustCompile(`(?im)^\s*eventSourceName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
			if len(match) == 2 && match[1] != "" {
				refs = append(refs, resourceReference{
					Kind:         "eventsource",
					Name:         match[1],
					EvidenceKind: "kubernetes_argo_events_event_source_ref",
					Confidence:   0.84,
				})
			}
		}
	}
	for _, match := range regexp.MustCompile(`(?im)^\s*eventBusName:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		if len(match) == 2 && match[1] != "" {
			refs = append(refs, resourceReference{
				Kind:         "eventbus",
				Name:         match[1],
				EvidenceKind: "kubernetes_argo_events_event_bus_ref",
				Confidence:   0.82,
			})
		}
	}
	return refs
}

func kubernetesArgoCDApplicationProjectReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `argoproj\.io/`) || !kubernetesManifestHasAnyKind(content, "Application", "ApplicationSet") {
		return nil
	}
	var refs []resourceReference
	for _, match := range regexp.MustCompile(`(?im)^\s*project:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
		if len(match) != 2 || match[1] == "" {
			continue
		}
		refs = append(refs, resourceReference{
			Kind:         "appproject",
			Name:         match[1],
			EvidenceKind: "kubernetes_argocd_project_ref",
			Confidence:   0.82,
		})
	}
	return refs
}

func kubernetesManifestHasAnyKind(content string, kinds ...string) bool {
	for _, kind := range kinds {
		re := regexp.MustCompile(`(?im)^\s*kind:\s*` + regexp.QuoteMeta(kind) + `\s*$`)
		if re.MatchString(content) {
			return true
		}
	}
	return false
}

func kubernetesScaledObjectScaleTargetReferences(content string) []resourceReference {
	if !regexp.MustCompile(`(?im)^\s*kind:\s*ScaledObject\s*$`).MatchString(content) {
		return nil
	}
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != "scaleTargetRef" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		var name, kind string
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			childKey, ok := yamlLineKey(line)
			if !ok {
				continue
			}
			switch childKey {
			case "name":
				name = yamlLineValue(line)
			case "kind":
				kind = yamlLineValue(line)
			}
		}
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		kind = strings.Trim(strings.TrimSpace(kind), `"'`)
		if name == "" || kind != "" {
			continue
		}
		refs = append(refs, resourceReference{
			Kind:         "deployment",
			Name:         name,
			EvidenceKind: "kubernetes_keda_scale_target",
			Confidence:   0.8,
		})
	}
	return refs
}

func kubernetesGatewayBackendReferences(content string) []resourceReference {
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "- ")))
		if !ok || key != "backendRefs" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		var name, kind string
		flush := func() {
			name = strings.Trim(strings.TrimSpace(name), `"'`)
			kind = strings.Trim(strings.TrimSpace(kind), `"'`)
			if name == "" {
				name, kind = "", ""
				return
			}
			if kind == "" || strings.EqualFold(kind, "Service") {
				refs = append(refs, resourceReference{
					Kind:         "service",
					Name:         name,
					EvidenceKind: "kubernetes_gateway_backend_ref",
					Confidence:   0.82,
				})
			}
			name, kind = "", ""
		}
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				flush()
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			}
			childKey, ok := yamlLineKey(trimmed)
			if !ok {
				continue
			}
			switch childKey {
			case "name":
				name = yamlLineValue(trimmed)
			case "kind":
				kind = yamlLineValue(trimmed)
			}
		}
		flush()
	}
	return refs
}

func kubernetesGatewayParentReferences(content string) []resourceReference {
	lines := strings.Split(content, "\n")
	var refs []resourceReference
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != "parentRefs" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		var name, kind string
		flush := func() {
			if name == "" {
				return
			}
			if kind == "" || strings.EqualFold(kind, "Gateway") {
				refs = append(refs, resourceReference{
					Kind:         "gateway",
					Name:         name,
					EvidenceKind: "kubernetes_gateway_parent_ref",
					Confidence:   0.82,
				})
			}
			name, kind = "", ""
		}
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				flush()
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			}
			childKey, ok := yamlLineKey(trimmed)
			if !ok {
				continue
			}
			switch childKey {
			case "name":
				name = yamlLineValue(trimmed)
			case "kind":
				kind = yamlLineValue(trimmed)
			}
		}
		flush()
	}
	return refs
}

func kubernetesGatewayPolicyTargetReferences(content string) []resourceReference {
	if !kubernetesManifestAPIMatches(content, `gateway\.networking\.k8s\.io/`) || !regexp.MustCompile(`(?im)^\s*kind:\s*[A-Za-z0-9_.-]*Policy\s*$`).MatchString(content) {
		return nil
	}
	var refs []resourceReference
	for _, blockKey := range []string{"targetRef", "targetRefs"} {
		for _, ref := range kubernetesNamedRefBlockReferences(content, blockKey, "kubernetes_gateway_policy_target_ref", 0.84, kubernetesExplicitReferenceKind) {
			switch strings.ToLower(ref.Kind) {
			case "service", "gateway", "httproute", "grpcroute", "tlsroute", "tcproute", "udproute":
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func kubernetesIstioServiceMeshReferences(content string) []resourceReference {
	var refs []resourceReference
	if kubernetesManifestHasAnyKind(content, "VirtualService", "DestinationRule") {
		for _, match := range regexp.MustCompile(`(?is)\bdestination:\s*\n(?:\s+[A-Za-z0-9_-]+:\s*[^\n]*\n)*\s+host:\s*([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(content, -1) {
			service := kubernetesServiceNameFromHost(match[1])
			if service == "" {
				continue
			}
			refs = append(refs, resourceReference{
				Kind:         "service",
				Name:         service,
				EvidenceKind: "kubernetes_istio_destination_host",
				Confidence:   0.8,
			})
		}
	}
	if kubernetesManifestHasAnyKind(content, "DestinationRule") {
		for _, match := range regexp.MustCompile(`(?im)^\s*host:\s*([A-Za-z0-9_.-]+)\s*$`).FindAllStringSubmatch(content, -1) {
			service := kubernetesServiceNameFromHost(match[1])
			if service == "" {
				continue
			}
			refs = append(refs, resourceReference{
				Kind:         "service",
				Name:         service,
				EvidenceKind: "kubernetes_istio_host",
				Confidence:   0.78,
			})
		}
	}
	if !kubernetesManifestHasAnyKind(content, "VirtualService") {
		return refs
	}
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		key, ok := yamlLineKey(lines[i])
		if !ok || key != "gateways" {
			continue
		}
		parentIndent := yamlIndent(lines[i])
		for j := i + 1; j < len(lines); j++ {
			line := lines[j]
			if yamlIgnoreLine(line) {
				continue
			}
			indent := yamlIndent(line)
			if indent <= parentIndent {
				break
			}
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- ") {
				continue
			}
			gateway := kubernetesServiceMeshName(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if gateway == "" || strings.EqualFold(gateway, "mesh") {
				continue
			}
			refs = append(refs, resourceReference{
				Kind:         "gateway",
				Name:         gateway,
				EvidenceKind: "kubernetes_istio_gateway_ref",
				Confidence:   0.78,
			})
		}
	}
	return refs
}

func kubernetesServiceNameFromHost(host string) string {
	name := kubernetesServiceMeshName(host)
	if name == "" || strings.Contains(name, "*") {
		return ""
	}
	if dot := strings.Index(name, "."); dot >= 0 {
		name = name[:dot]
	}
	return name
}

func kubernetesServiceMeshName(value string) string {
	name := strings.Trim(strings.TrimSpace(value), `"'`)
	if name == "" {
		return ""
	}
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	return strings.TrimSpace(name)
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
	SelectorExpressions []kubernetesSelectorExpression
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
			labels := kubernetesWorkloadLabels(strings.ToLower(kind), content)
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
		"rollout":               true,
	}
}

func kubernetesWorkloadLabels(kind, content string) map[string]string {
	if strings.EqualFold(kind, "cronjob") {
		if labels := yamlMapAtPath(content, "spec", "jobTemplate", "spec", "template", "metadata", "labels"); len(labels) > 0 {
			return labels
		}
	}
	if labels := yamlMapAtPath(content, "spec", "template", "metadata", "labels"); len(labels) > 0 {
		return labels
	}
	return yamlMapAtPath(content, "metadata", "labels")
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

type kubernetesSelectorExpression struct {
	Key      string
	Operator string
	Values   []string
}

func kubernetesSelectorExpressionResourceRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
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
			labels := kubernetesWorkloadLabels(strings.ToLower(kind), content)
			selector, expressions, targetKinds, evidence, reason, confidence := kubernetesExpressionSelectorForResource(strings.ToLower(kind), content)
			resources = append(resources, kubernetesResourceInfo{
				Symbol:              symbol,
				Kind:                strings.ToLower(kind),
				Name:                name,
				Labels:              labels,
				Selector:            selector,
				SelectorExpressions: expressions,
				SelectorTargetKinds: targetKinds,
				SelectorEvidence:    evidence,
				SelectorReason:      reason,
				SelectorConfidence:  confidence,
			})
		}
	}
	var relations []RelationRecord
	for _, source := range resources {
		if len(source.SelectorExpressions) == 0 {
			continue
		}
		for _, target := range resources {
			if target.Symbol.ID == source.Symbol.ID || len(target.Labels) == 0 {
				continue
			}
			if len(source.SelectorTargetKinds) > 0 && !source.SelectorTargetKinds[target.Kind] {
				continue
			}
			if !kubernetesSelectorMatches(source.Selector, target.Labels) || !kubernetesSelectorExpressionsMatch(source.SelectorExpressions, target.Labels) {
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

func kubernetesExpressionSelectorForResource(kind, content string) (map[string]string, []kubernetesSelectorExpression, map[string]bool, string, string, float64) {
	switch kind {
	case "poddisruptionbudget":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			yamlMatchExpressionsAtPath(content, "spec", "selector"),
			kubernetesWorkloadKinds(),
			"kubernetes_pdb_match_expression_selector",
			"Kubernetes PodDisruptionBudget matchExpressions selector matches workload labels",
			0.82
	case "networkpolicy":
		return yamlMapAtPath(content, "spec", "podSelector", "matchLabels"),
			yamlMatchExpressionsAtPath(content, "spec", "podSelector"),
			kubernetesWorkloadKinds(),
			"kubernetes_network_policy_match_expression_selector",
			"Kubernetes NetworkPolicy podSelector matchExpressions matches workload labels",
			0.8
	case "servicemonitor":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			yamlMatchExpressionsAtPath(content, "spec", "selector"),
			map[string]bool{"service": true},
			"kubernetes_service_monitor_match_expression_selector",
			"Kubernetes ServiceMonitor matchExpressions selector matches Service labels",
			0.78
	case "podmonitor":
		return yamlMapAtPath(content, "spec", "selector", "matchLabels"),
			yamlMatchExpressionsAtPath(content, "spec", "selector"),
			kubernetesWorkloadKinds(),
			"kubernetes_pod_monitor_match_expression_selector",
			"Kubernetes PodMonitor matchExpressions selector matches workload labels",
			0.78
	default:
		return nil, nil, nil, "", "", 0
	}
}

func yamlMatchExpressionsAtPath(content string, path ...string) []kubernetesSelectorExpression {
	var stack []yamlPathFrame
	var out []kubernetesSelectorExpression
	var current kubernetesSelectorExpression
	inValues := false
	flush := func() {
		if current.Key != "" && current.Operator != "" {
			out = append(out, current)
		}
		current = kubernetesSelectorExpression{}
		inValues = false
	}
	targetPath := append(append([]string{}, path...), "matchExpressions")
	for _, line := range strings.Split(content, "\n") {
		if yamlIgnoreLine(line) {
			continue
		}
		indent := yamlIndent(line)
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			if yamlPathMatches(stack, targetPath) {
				flush()
			}
			stack = stack[:len(stack)-1]
		}
		insideExpressions := len(stack) == len(targetPath) && yamlPathMatches(stack, targetPath) && indent > stack[len(stack)-1].indent
		trimmed := strings.TrimSpace(line)
		if insideExpressions {
			switch {
			case strings.HasPrefix(trimmed, "- key:"):
				flush()
				current.Key = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- key:")), `"'`)
			case strings.HasPrefix(trimmed, "key:"):
				current.Key = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "key:")), `"'`)
			case strings.HasPrefix(trimmed, "operator:"):
				current.Operator = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "operator:")), `"'`)
				inValues = false
			case strings.HasPrefix(trimmed, "values:"):
				inValues = true
				current.Values = append(current.Values, yamlInlineListValues(strings.TrimSpace(strings.TrimPrefix(trimmed, "values:")))...)
			case inValues && strings.HasPrefix(trimmed, "- "):
				current.Values = append(current.Values, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), `"'`))
			case strings.HasPrefix(trimmed, "- "):
				flush()
			}
			continue
		}
		key, ok := yamlLineKey(line)
		if !ok {
			continue
		}
		stack = append(stack, yamlPathFrame{indent: indent, key: key})
	}
	flush()
	return out
}

func yamlInlineListValues(value string) []string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil
	}
	value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func kubernetesSelectorExpressionsMatch(expressions []kubernetesSelectorExpression, labels map[string]string) bool {
	for _, expression := range expressions {
		value, exists := labels[expression.Key]
		switch strings.ToLower(expression.Operator) {
		case "in":
			if !exists || !stringInSlice(value, expression.Values) {
				return false
			}
		case "notin":
			if exists && stringInSlice(value, expression.Values) {
				return false
			}
		case "exists":
			if !exists {
				return false
			}
		case "doesnotexist":
			if exists {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
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
				target, ok := services[dep.Name]
				if !ok || target.ID == symbol.ID {
					continue
				}
				relations = append(relations, RelationRecord{
					RecordType:    "relation",
					FromID:        symbol.ID,
					ToID:          target.ID,
					Type:          "RESOURCE_DEPENDS_ON",
					Confidence:    dep.Confidence,
					Reason:        dep.Reason,
					RelationScope: "file",
					Resolution:    "exact",
					TargetKind:    "symbol",
					Evidence: []Evidence{{
						Kind:      dep.EvidenceKind,
						FilePath:  symbol.FilePath,
						StartLine: symbol.StartLine,
						EndLine:   symbol.EndLine,
						Detail:    composeServiceName(symbol) + " -> " + dep.Name,
					}},
					WarningCodes: []string{},
				})
			}
		}
	}
	return relations
}

type composeServiceDependencyRef struct {
	Name         string
	Reason       string
	EvidenceKind string
	Confidence   float64
}

func composeServiceDependencyRefs(block string) []composeServiceDependencyRef {
	var refs []composeServiceDependencyRef
	add := func(name, reason, evidenceKind string, confidence float64) {
		name = normalizeComposeServiceReference(name)
		if name == "" {
			return
		}
		refs = append(refs, composeServiceDependencyRef{
			Name:         name,
			Reason:       reason,
			EvidenceKind: evidenceKind,
			Confidence:   confidence,
		})
	}
	for _, value := range composeBlockListValues(block, "depends_on") {
		add(value, "Docker Compose service depends_on references another service", "compose_depends_on", 0.9)
	}
	for _, value := range composeBlockMapKeys(block, "depends_on") {
		add(value, "Docker Compose service depends_on references another service", "compose_depends_on", 0.9)
	}
	for _, value := range composeBlockListValues(block, "links") {
		add(value, "Docker Compose service link references another service", "compose_link", 0.78)
	}
	if value := composeNestedMapScalarValue(block, "extends", "service"); value != "" {
		add(value, "Docker Compose service extends another service", "compose_extends", 0.82)
	}
	if value := composeBlockScalarValue(block, "network_mode"); strings.HasPrefix(strings.TrimSpace(value), "service:") {
		add(strings.TrimPrefix(strings.TrimSpace(value), "service:"), "Docker Compose service network_mode references another service", "compose_network_mode", 0.76)
	}
	return dedupeComposeServiceDependencyRefs(refs)
}

func normalizeComposeServiceReference(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	value = strings.TrimPrefix(value, "service:")
	if before, _, ok := strings.Cut(value, ":"); ok {
		value = before
	}
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func dedupeComposeServiceDependencyRefs(refs []composeServiceDependencyRef) []composeServiceDependencyRef {
	seen := map[string]bool{}
	var out []composeServiceDependencyRef
	for _, ref := range refs {
		key := ref.Name + "\x00" + ref.EvidenceKind
		if ref.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].EvidenceKind < out[j].EvidenceKind
	})
	return out
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
		key := ref.Kind + "/" + ref.Name + "/" + ref.ExternalName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			if out[i].Name == out[j].Name {
				return out[i].ExternalName < out[j].ExternalName
			}
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

func composeBlockScalarValue(block, key string) string {
	for _, line := range strings.Split(block, "\n") {
		if yamlIgnoreLine(line) {
			continue
		}
		lineKey, hasKey := yamlLineKey(line)
		if !hasKey || lineKey != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(yamlLineValue(line)), `"'`)
	}
	return ""
}

func composeNestedMapScalarValue(block, key, childKey string) string {
	lines := strings.Split(block, "\n")
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
			if value != "" && childKey == "" {
				return value
			}
			inBlock = true
			blockIndent = indent
			continue
		}
		if !inBlock || !hasKey || indent <= blockIndent || lineKey != childKey {
			continue
		}
		return strings.Trim(strings.TrimSpace(yamlLineValue(line)), `"'`)
	}
	return ""
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

func fileChangesWithRelations(ctx context.Context, repo, revision, repoKey string, files []FileRecord) []RelationRecord {
	if revision == "" {
		return nil
	}
	cochanges, err := gitutil.FileCochanges(ctx, repo, revision, 256)
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

// firstTypeLikeNamedPreferFile resolves a type name to a symbol, preferring a
// declaration in the given file before falling back to the first global match.
// Same-file preference matters when a repo vendors a mirror copy of its sources
// (e.g. a Deno build alongside src/): without it a class-qualified call could
// bind to the twin in the wrong file.
// enclosingTypeShortName extracts the short name of a symbol's enclosing
// type from its container ID (".../class:Outer.Inner" -> "Inner").
func enclosingTypeShortName(from SymbolRecord) string {
	id := from.ContainerID
	if idx := strings.LastIndex(id, ":"); idx >= 0 {
		id = id[idx+1:]
	}
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		id = id[idx+1:]
	}
	return id
}

// typeLikeNamedWithMethod resolves a type name to the declaration that
// actually defines the called method. C# partial classes declare the same
// type in several files, and the lexically-first declaration may not hold
// the method (roslyn's Contract.InterpolatedStringHandlers.cs sorts before
// Contract.cs, which defines ThrowIfFalse); probing only the first
// candidate dropped every such static call. Preference order: a same-file
// declaration defining the method, any declaration defining it (directly
// or up its supertype chain), then the plain prefer-file lookup.
func typeLikeNamedWithMethod(records []SymbolRecord, name, file, method string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string) (SymbolRecord, bool) {
	var withMethod []SymbolRecord
	for _, symbol := range records {
		if symbol.Name != name || !typeLikeKind(symbol.Kind) {
			continue
		}
		if _, _, ok := lookupMethodUpChain(symbol.ID, method, methodsByContainer, superContainerByID); ok {
			withMethod = append(withMethod, symbol)
		}
	}
	if len(withMethod) > 0 {
		for _, symbol := range withMethod {
			if symbol.FilePath == file {
				return symbol, true
			}
		}
		return withMethod[0], true
	}
	return firstTypeLikeNamedPreferFile(records, name, file)
}

func firstTypeLikeNamedPreferFile(records []SymbolRecord, name, file string) (SymbolRecord, bool) {
	for _, symbol := range records {
		if symbol.Name == name && symbol.FilePath == file && typeLikeKind(symbol.Kind) {
			return symbol, true
		}
	}
	return firstTypeLikeNamed(records, name)
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
// that fetches one file at a time from an exact committed revision (when
// non-empty) or the working tree, so the snapshot never holds all source
// content in memory.
func openSource(ctx context.Context, repo, committedRevision string, ignoreFiles, includeFiles []string) ([]string, contentReader, prefixReader, func() error, error) {
	if committedRevision != "" {
		ignores, err := loadExplicitIgnoreMatcher(repo, ignoreFiles, includeFiles)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		paths, err := gitutil.ListFiles(ctx, repo, committedRevision)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		paths = filterVendoredPaths(paths, headIgnoreMatcher(ctx, repo, committedRevision))
		paths = filterIgnoredPaths(paths, ignores)
		batch, err := gitutil.NewBatchFileReader(ctx, repo, committedRevision)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		read := func(path string) (string, bool) {
			if strings.Contains(path, "\n") {
				content, ok, err := gitutil.ShowFile(ctx, repo, committedRevision, path)
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
		// Git blob reads are all-or-nothing, so the HEAD-tree prefix reader
		// fetches the blob and truncates.
		readPrefix := func(path string, limit int) (string, bool) {
			content, ok := read(path)
			if !ok {
				return "", false
			}
			if limit >= 0 && len(content) > limit {
				content = content[:limit]
			}
			return content, true
		}
		return paths, read, readPrefix, batch.Close, nil
	}
	ignores, err := loadWorktreeIgnoreMatcher(repo, ignoreFiles, includeFiles)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	paths, err := workingTreeFiles(repo, ignores, trackedDirSet(ctx, repo))
	if err != nil {
		return nil, nil, nil, nil, err
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
	readPrefix := func(path string, limit int) (string, bool) {
		full := filepath.Join(repo, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", false
		}
		file, err := os.Open(full)
		if err != nil {
			return "", false
		}
		defer file.Close()
		buf := make([]byte, limit)
		n, err := io.ReadFull(file, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", false
		}
		return string(buf[:n]), true
	}
	return paths, read, readPrefix, nil, nil
}

// bundledRuntimeDirNames are directory basenames of third-party C/C++ runtime,
// libc, libc++, and sanitizer source trees that projects vendor to build
// against (e.g. Zig's lib/libc and lib/libcxx). They are matched only when
// nested (not the repository's own top-level directory) so a project that *is*
// one of these (e.g. the LLVM libcxx repo) is not excluded.
var bundledRuntimeDirNames = map[string]struct{}{
	"libc": {}, "libcxx": {}, "libcxxabi": {}, "libunwind": {}, "libtsan": {},
	"libubsan": {}, "libasan": {}, "compiler-rt": {}, "musl": {}, "glibc": {},
	"mingw": {}, "mingw-w64": {}, "wasi-libc": {},
}

// isVendoredScanDir reports whether a directory is unambiguously vendored
// third-party (or generated) and must be skipped during the scan regardless of
// git tracked-ness: these names mean third-party even when committed (Go
// vendor/, checked-in node_modules, third_party trees) plus nested bundled
// C/C++ runtime trees. Ambiguous names like build/ or deps/ are handled
// separately by skipVendoredDir, which consults git tracked-ness.
func isVendoredScanDir(rel, name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".next",
		"third_party", "third-party", "thirdparty", "3rdparty",
		"Pods", "Carthage":
		return true
	}
	// Nested bundled-runtime trees: require a parent segment so the project's own
	// top-level directory of the same name is preserved.
	if strings.Contains(rel, "/") {
		if _, ok := bundledRuntimeDirNames[name]; ok {
			return true
		}
	}
	return false
}

// isAmbiguousVendoredDirName reports whether a directory name usually denotes
// generated or fetched content (build output, dist bundles, fetched deps) but
// is also a common first-party source directory name (next.js keeps its
// compiler in packages/next/src/build; rabbitmq keeps its applications in
// deps/). Generated output is virtually never committed, so these names are
// skipped only when the directory is not tracked in git — a tracked directory
// of this name is first-party source.
func isAmbiguousVendoredDirName(name string) bool {
	switch name {
	case "build", "dist", "external", "deps":
		return true
	}
	return false
}

// skipVendoredDir is the single vendored-directory decision for both the
// working-tree walk and the HEAD-tree listing: skip unambiguous vendored names
// always, and ambiguous generated-output names only when the directory is not
// tracked in git (dirTracked). Gitignore negations that re-include a
// descendant (see ReincludesDescendant) keep the tree walked in either case;
// the ignore rules themselves then filter its contents.
func skipVendoredDir(rel, name string, ignores ignoreMatcher, dirTracked func(string) bool) bool {
	vendored := isVendoredScanDir(rel, name) ||
		(isAmbiguousVendoredDirName(name) && !dirTracked(rel))
	return vendored && !ignores.ReincludesDescendant(rel)
}

// trackedDirSet returns every repo-relative directory (slash-separated) that
// contains a git-tracked file, built from one `git ls-files -z` subprocess for
// the whole snapshot; the walk then answers "is this directory tracked?" with
// a map lookup instead of per-directory git calls. A non-git directory yields
// nil, so every directory reads as untracked there.
func trackedDirSet(ctx context.Context, repo string) map[string]struct{} {
	files, err := gitutil.ListIndexFiles(ctx, repo)
	if err != nil {
		return nil
	}
	dirs := make(map[string]struct{})
	for _, file := range files {
		for dir := path.Dir(file); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if _, seen := dirs[dir]; seen {
				break
			}
			dirs[dir] = struct{}{}
		}
	}
	return dirs
}

func isVendoredScanFile(rel, name string) bool {
	switch name {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
		"bun.lock", "bun.lockb", "poetry.lock", "Pipfile.lock", "composer.lock",
		"Gemfile.lock", "Cargo.lock":
		return true
	}
	return strings.HasSuffix(rel, ".map")
}

func workingTreeFiles(repo string, ignores ignoreMatcher, trackedDirs map[string]struct{}) ([]string, error) {
	dirTracked := func(rel string) bool {
		_, ok := trackedDirs[rel]
		return ok
	}
	var paths []string
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != repo {
				rel, err := filepath.Rel(repo, path)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if skipVendoredDir(rel, name, ignores, dirTracked) {
					return filepath.SkipDir
				}
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
		if isVendoredScanFile(rel, name) {
			return nil
		}
		if ignores.Ignored(rel, false) {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func filterVendoredPaths(paths []string, ignores ignoreMatcher) []string {
	filtered := paths[:0]
	for _, rel := range paths {
		if vendoredPath(rel, ignores) {
			continue
		}
		filtered = append(filtered, rel)
	}
	return filtered
}

func filterIgnoredPaths(paths []string, ignores ignoreMatcher) []string {
	filtered := paths[:0]
	for _, rel := range paths {
		rel = filepath.ToSlash(rel)
		if ignores.Ignored(rel, false) {
			continue
		}
		filtered = append(filtered, rel)
	}
	return filtered
}

// headIgnoreMatcher parses the repository's root .gitignore at the same exact
// committed revision used for listing and content reads, so the vendored-
// directory heuristic cannot observe a newer HEAD.
func headIgnoreMatcher(ctx context.Context, repo, committedRevision string) ignoreMatcher {
	content, ok, err := gitutil.ShowFile(ctx, repo, committedRevision, ".gitignore")
	if err != nil || !ok {
		return ignoreMatcher{}
	}
	var matcher ignoreMatcher
	if err := matcher.loadContent(content, false); err != nil {
		return ignoreMatcher{}
	}
	return matcher
}

// vendoredPath filters a HEAD-tree path. Every path in the HEAD listing is
// tracked by construction, so ambiguous generated-output directory names
// (build, dist, external, deps) are never vendored here — headTracked reports
// every directory as tracked; only the unambiguous vendored names are skipped.
func vendoredPath(rel string, ignores ignoreMatcher) bool {
	headTracked := func(string) bool { return true }
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) == 0 {
		return false
	}
	if isVendoredScanFile(rel, parts[len(parts)-1]) {
		return true
	}
	for i, part := range parts[:len(parts)-1] {
		dirRel := strings.Join(parts[:i+1], "/")
		if skipVendoredDir(dirRel, part, ignores, headTracked) {
			return true
		}
	}
	return false
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
		".cs", ".cue", ".ex", ".exs", ".go", ".gradle", ".groovy", ".gvy",
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
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx":
		return resolveCFamilyLocalInclude(importingPath, spec, knownFiles)
	case ".js", ".jsx", ".ts", ".tsx":
		for _, candidate := range jsLocalImportCandidates(importingPath, spec) {
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

func resolveReadableLocalImport(importingPath, spec string, readContent contentReader) (string, bool) {
	switch strings.ToLower(filepath.Ext(importingPath)) {
	case ".js", ".jsx", ".ts", ".tsx":
		for _, candidate := range jsLocalImportCandidates(importingPath, spec) {
			if _, ok := readContent(candidate); ok {
				return candidate, true
			}
		}
	}
	return "", false
}

func jsLocalImportCandidates(importingPath, spec string) []string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return nil
	}
	base := filepath.ToSlash(filepath.Join(filepath.Dir(importingPath), spec))
	exts := []string{".ts", ".tsx", ".js", ".jsx"}
	var candidates []string
	if ext := strings.ToLower(filepath.Ext(base)); ext != "" {
		candidates = append(candidates, base)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		switch ext {
		case ".js":
			candidates = append(candidates, stem+".ts", stem+".tsx")
		case ".jsx":
			candidates = append(candidates, stem+".tsx")
		}
		return candidates
	}
	for _, ext := range exts {
		candidates = append(candidates, base+ext)
	}
	for _, ext := range exts {
		candidates = append(candidates, filepath.ToSlash(filepath.Join(base, "index"+ext)))
	}
	return candidates
}

func resolveCFamilyLocalInclude(importingPath, spec string, knownFiles map[string]bool) (string, bool) {
	spec = strings.TrimSpace(filepath.ToSlash(spec))
	if spec == "" {
		return "", false
	}
	dir := filepath.Dir(importingPath)
	ext := filepath.Ext(spec)
	if ext == "" {
		// Avoid resolving standard headers such as <string>, <map>, or
		// <system_error> by basename coincidence.
		return "", false
	}
	candidates := []string{
		filepath.ToSlash(filepath.Join(dir, spec)),
		spec,
		filepath.ToSlash(filepath.Join("include", spec)),
	}
	if !strings.Contains(spec, "/") {
		candidates = append(candidates,
			filepath.ToSlash(filepath.Join("include", filepath.Base(spec))),
			filepath.ToSlash(filepath.Join("test", filepath.Base(spec))),
		)
	}
	for _, candidate := range candidates {
		if knownFiles[candidate] {
			return candidate, true
		}
	}
	if strings.Contains(spec, "/") {
		var matches []string
		suffix := "/" + spec
		for path := range knownFiles {
			if strings.HasSuffix(path, suffix) {
				matches = append(matches, path)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
	}
	return "", false
}

type manifestImportResolver struct {
	goModule            string
	goPackages          map[string]string
	jsPackageName       string
	jsPackageExports    map[string]string
	jsPackageImports    map[string]string
	jsWorkspacePackages []jsPackageRoot
	jsImportMap         map[string]string
	jsImportMapScopes   []jsImportMapScope
	jsModuleFiles       map[string]string
	tsPathMappings      []tsPathMapping
	tsBaseURLDirs       []string
	pythonPackages      []string
	pythonSourceRoots   []string
	pythonPackageDirs   []pythonPackageDirMapping
	pythonModules       map[string]string
	pythonNamespaces    map[string]bool
	jvmTypes            map[string]string
	jvmTypeEvidence     map[string]string
	jvmPackagePrefixes  []string
	csharpNamespaces    map[string]string
	csharpEvidence      map[string]string
	csharpAmbiguous     map[string]bool
	csharpPrefixes      []string
	phpTypes            map[string]string
	phpTypeEvidence     map[string]string
	phpTypeAmbiguous    map[string]bool
	phpPSR4Prefixes     []phpPSR4Prefix
	rustCrateName       string
	rustCrateNames      map[string]bool
	rustSrcRoots        []rustSrcRoot
	rustModules         map[string]string
	rustAliases         map[string]string
}

type rustSrcRoot struct {
	dir   string
	crate string
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

type jsPackageRoot struct {
	Name    string
	Root    string
	Exports map[string]string
}

type jsImportMapScope struct {
	Prefix  string
	Targets map[string]string
}

type phpPSR4Prefix struct {
	Prefix string
	Dirs   []string
}

type pythonPackageDirMapping struct {
	Package string
	Dir     string
}

func buildManifestImportResolver(files []FileRecord, readContent contentReader) manifestImportResolver {
	resolver := manifestImportResolver{goPackages: map[string]string{}, jsPackageExports: map[string]string{}, jsPackageImports: map[string]string{}, jsImportMap: map[string]string{}, jsModuleFiles: map[string]string{}, pythonSourceRoots: []string{"src"}, pythonModules: map[string]string{}, pythonNamespaces: map[string]bool{}, jvmTypes: map[string]string{}, jvmTypeEvidence: map[string]string{}, csharpNamespaces: map[string]string{}, csharpEvidence: map[string]string{}, csharpAmbiguous: map[string]bool{}, phpTypes: map[string]string{}, phpTypeEvidence: map[string]string{}, phpTypeAmbiguous: map[string]bool{}, rustModules: map[string]string{}, rustAliases: map[string]string{}, rustCrateNames: map[string]bool{}}
	if content, ok := readContent("go.mod"); ok {
		resolver.goModule = parseGoModulePath(content)
	}
	if content, ok := readContent("package.json"); ok {
		resolver.jsPackageName = parsePackageJSONName(content)
		resolver.jsPackageExports = parsePackageJSONTargets(content, "exports")
		resolver.jsPackageImports = parsePackageJSONTargets(content, "imports")
	}
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		if path == "package.json" || filepath.Base(path) != "package.json" {
			continue
		}
		content, ok := readContent(path)
		if !ok {
			continue
		}
		name := parsePackageJSONName(content)
		if name == "" {
			continue
		}
		resolver.jsWorkspacePackages = append(resolver.jsWorkspacePackages, jsPackageRoot{
			Name:    name,
			Root:    filepath.ToSlash(filepath.Dir(path)),
			Exports: parsePackageJSONTargets(content, "exports"),
		})
	}
	sort.Slice(resolver.jsWorkspacePackages, func(i, j int) bool {
		if resolver.jsWorkspacePackages[i].Name == resolver.jsWorkspacePackages[j].Name {
			return resolver.jsWorkspacePackages[i].Root < resolver.jsWorkspacePackages[j].Root
		}
		return resolver.jsWorkspacePackages[i].Name < resolver.jsWorkspacePackages[j].Name
	})
	if _, ok := readContent("tsconfig.json"); ok {
		resolver.tsPathMappings = tsConfigPathMappings("tsconfig.json", readContent, nil)
		if baseURL, ok := tsConfigBaseURL("tsconfig.json", readContent, nil); ok {
			resolver.tsBaseURLDirs = []string{baseURL}
		}
	}
	for _, importMapPath := range []string{"import-map.json", "importmap.json"} {
		if content, ok := readContent(importMapPath); ok {
			resolver.jsImportMap = parseJSImportMapTargets(content)
			resolver.jsImportMapScopes = parseJSImportMapScopes(content)
			break
		}
	}
	if content, ok := readContent("pyproject.toml"); ok {
		resolver.pythonPackages = append(resolver.pythonPackages, parsePyProjectName(content))
		resolver.pythonSourceRoots = append(resolver.pythonSourceRoots, parsePyProjectPythonSourceRoots(content)...)
		resolver.pythonPackageDirs = append(resolver.pythonPackageDirs, parsePyProjectPythonPackageDirs(content)...)
	}
	if content, ok := readContent("setup.cfg"); ok {
		resolver.pythonPackages = append(resolver.pythonPackages, parseSetupCFGName(content))
		resolver.pythonSourceRoots = append(resolver.pythonSourceRoots, parseSetupCFGPythonSourceRoots(content)...)
		resolver.pythonPackageDirs = append(resolver.pythonPackageDirs, parseSetupCFGPythonPackageDirs(content)...)
	}
	resolver.pythonPackages = normalizePythonPackageNames(resolver.pythonPackages)
	if content, ok := readContent("Cargo.toml"); ok {
		resolver.rustCrateName = normalizeRustCrateName(parseCargoPackageName(content))
	}
	if content, ok := readContent("composer.json"); ok {
		resolver.phpPSR4Prefixes = parseComposerPSR4Prefixes(content)
	}
	if content, ok := readContent("pom.xml"); ok {
		resolver.jvmPackagePrefixes = append(resolver.jvmPackagePrefixes, parsePOMJVMPackagePrefixes(content)...)
	}
	var cargoPaths []string
	var gradleArtifactName string
	for _, path := range []string{"settings.gradle", "settings.gradle.kts"} {
		if content, ok := readContent(path); ok {
			gradleArtifactName = parseGradleRootProjectName(content)
			if gradleArtifactName != "" {
				break
			}
		}
	}
	for _, path := range []string{"build.gradle", "build.gradle.kts"} {
		if content, ok := readContent(path); ok {
			resolver.jvmPackagePrefixes = append(resolver.jvmPackagePrefixes, parseGradleJVMPackagePrefixes(content, gradleArtifactName)...)
		}
	}
	resolver.jvmPackagePrefixes = normalizeJVMPackagePrefixes(resolver.jvmPackagePrefixes)
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".csproj") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		resolver.csharpPrefixes = append(resolver.csharpPrefixes, parseCSharpProjectNamespacePrefixes(file.Path, content)...)
	}
	resolver.csharpPrefixes = normalizeCSharpNamespaces(resolver.csharpPrefixes)
	var goPaths []string
	var jsPaths []string
	var pyPaths []string
	var jvmPaths []string
	var csharpPaths []string
	var phpPaths []string
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
		if strings.EqualFold(filepath.Ext(file.Path), ".cs") {
			csharpPaths = append(csharpPaths, filepath.ToSlash(file.Path))
		}
		if strings.EqualFold(filepath.Ext(file.Path), ".php") {
			phpPaths = append(phpPaths, filepath.ToSlash(file.Path))
		}
		if strings.EqualFold(filepath.Ext(file.Path), ".rs") {
			rustPaths = append(rustPaths, filepath.ToSlash(file.Path))
		}
		if filepath.Base(file.Path) == "Cargo.toml" {
			cargoPaths = append(cargoPaths, filepath.ToSlash(file.Path))
		}
	}
	cargoPaths = append(cargoPaths, inferCargoManifestsFromRustPaths(rustPaths, readContent)...)
	cargoPaths = uniqueStrings(cargoPaths)
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
		for _, key := range pythonPackageDirModuleKeysForPath(path, resolver.pythonPackageDirs, pyFileSet) {
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
			resolver.addJVMType(qualifiedName, path, "jvm_package_import")
			for _, prefix := range resolver.jvmPackagePrefixes {
				if qualifiedName == prefix || strings.HasPrefix(qualifiedName, prefix+".") {
					continue
				}
				resolver.addJVMType(prefix+"."+qualifiedName, path, "jvm_manifest_package_import")
			}
		}
	}
	sort.Strings(csharpPaths)
	for _, path := range csharpPaths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		namespace := csharpNamespaceName(content)
		if namespace == "" {
			continue
		}
		resolver.addCSharpNamespace(namespace, path, "csharp_namespace_import")
		for _, prefix := range resolver.csharpPrefixes {
			if namespace == prefix || strings.HasPrefix(namespace, prefix+".") {
				continue
			}
			resolver.addCSharpNamespace(prefix+"."+namespace, path, "csharp_csproj_namespace_import")
		}
	}
	sort.Strings(phpPaths)
	for _, path := range phpPaths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		for _, qualifiedName := range phpQualifiedTypeNames(content) {
			resolver.addPHPType(qualifiedName, path, "php_namespace_import")
			for _, prefix := range resolver.phpPSR4Prefixes {
				if !phpPathUnderAnyDir(path, prefix.Dirs) {
					continue
				}
				if phpClassNameHasPrefix(qualifiedName, prefix.Prefix) {
					continue
				}
				resolver.addPHPType(prefix.Prefix+qualifiedName, path, "composer_psr4_import")
			}
		}
	}
	// Build per-crate src roots from every Cargo.toml [package] (Cargo workspaces
	// put crates under <crate>/src/, not ./src/). Longest dir first for nearest-match.
	for _, cp := range cargoPaths {
		content, ok := readContent(cp)
		if !ok {
			continue
		}
		name := normalizeRustCrateName(parseCargoPackageName(content))
		if name == "" {
			continue
		}
		srcDir := cargoRustSourceRootDir(cp, content)
		resolver.rustSrcRoots = append(resolver.rustSrcRoots, rustSrcRoot{dir: srcDir, crate: name})
		resolver.rustCrateNames[name] = true
	}
	sort.Slice(resolver.rustSrcRoots, func(i, j int) bool {
		return len(resolver.rustSrcRoots[i].dir) > len(resolver.rustSrcRoots[j].dir)
	})
	if resolver.rustCrateName != "" {
		resolver.rustCrateNames[resolver.rustCrateName] = true
	}
	for _, sr := range resolver.rustSrcRoots {
		for _, rootFile := range []string{sr.dir + "/lib.rs", sr.dir + "/main.rs", sr.dir + "/mod.rs"} {
			if _, ok := readContent(rootFile); ok {
				rustPaths = append(rustPaths, rootFile)
			}
		}
	}
	rustPaths = uniqueStrings(rustPaths)
	sort.Strings(rustPaths)
	for _, path := range rustPaths {
		for _, module := range resolver.rustModuleKeysForPath(path) {
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
		for _, alias := range resolver.rustAliasesForFile(path, content) {
			if alias.From != "" && alias.To != "" {
				resolver.rustAliases[alias.From] = alias.To
			}
		}
	}
	return resolver
}

func (resolver manifestImportResolver) addJVMType(qualifiedName, path, evidenceKind string) {
	if _, exists := resolver.jvmTypes[qualifiedName]; exists {
		return
	}
	resolver.jvmTypes[qualifiedName] = path
	resolver.jvmTypeEvidence[qualifiedName] = evidenceKind
}

func (resolver manifestImportResolver) addCSharpNamespace(namespace, path, evidenceKind string) {
	namespace = normalizeCSharpNamespace(namespace)
	if namespace == "" {
		return
	}
	if existing, exists := resolver.csharpNamespaces[namespace]; exists {
		if existing != path {
			resolver.csharpAmbiguous[namespace] = true
		}
		return
	}
	resolver.csharpNamespaces[namespace] = path
	resolver.csharpEvidence[namespace] = evidenceKind
}

func (resolver manifestImportResolver) addPHPType(qualifiedName, path, evidenceKind string) {
	qualifiedName = normalizePHPClassName(qualifiedName)
	if qualifiedName == "" {
		return
	}
	if existing, exists := resolver.phpTypes[qualifiedName]; exists {
		if existing != path {
			resolver.phpTypeAmbiguous[qualifiedName] = true
		}
		return
	}
	resolver.phpTypes[qualifiedName] = path
	resolver.phpTypeEvidence[qualifiedName] = evidenceKind
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

func parseJSImportMapScopes(content string) []jsImportMapScope {
	var data struct {
		Scopes map[string]map[string]any `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil {
		return nil
	}
	var scopes []jsImportMapScope
	for rawPrefix, imports := range data.Scopes {
		prefix := normalizeJSImportMapScope(rawPrefix)
		if prefix == "" {
			continue
		}
		targets := map[string]string{}
		for key, value := range imports {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if target := jsManifestTarget(value); target != "" {
				targets[key] = target
			}
		}
		if len(targets) == 0 {
			continue
		}
		scopes = append(scopes, jsImportMapScope{Prefix: prefix, Targets: targets})
	}
	sort.Slice(scopes, func(i, j int) bool {
		if len(scopes[i].Prefix) == len(scopes[j].Prefix) {
			return scopes[i].Prefix < scopes[j].Prefix
		}
		return len(scopes[i].Prefix) > len(scopes[j].Prefix)
	})
	return scopes
}

func normalizeJSImportMapScope(scope string) string {
	scope = strings.TrimSpace(filepath.ToSlash(scope))
	scope = strings.TrimPrefix(scope, "./")
	scope = strings.TrimPrefix(scope, "/")
	scope = strings.Trim(scope, "/")
	if scope == "" {
		return ""
	}
	return scope + "/"
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
	return parseTSConfigPathsAt(content, "")
}

func tsConfigPathMappings(path string, readContent contentReader, seen map[string]bool) []tsPathMapping {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return nil
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[path] {
		return nil
	}
	seen[path] = true
	content, ok := readContent(path)
	if !ok {
		return nil
	}
	var mappings []tsPathMapping
	if extendsPath, ok := resolveTSConfigExtendsPath(path, parseTSConfigExtends(content)); ok {
		mappings = append(mappings, tsConfigPathMappings(extendsPath, readContent, seen)...)
	}
	mappings = append(mappings, parseTSConfigPathsAt(content, filepath.Dir(path))...)
	return mergeTSPathMappings(mappings)
}

func parseTSConfigExtends(content string) string {
	var data struct {
		Extends string `json:"extends"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.Extends)
}

func tsConfigBaseURL(path string, readContent contentReader, seen map[string]bool) (string, bool) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return "", false
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[path] {
		return "", false
	}
	seen[path] = true
	content, ok := readContent(path)
	if !ok {
		return "", false
	}
	baseURL := ""
	found := false
	if extendsPath, ok := resolveTSConfigExtendsPath(path, parseTSConfigExtends(content)); ok {
		baseURL, found = tsConfigBaseURL(extendsPath, readContent, seen)
	}
	if current, ok := parseTSConfigBaseURLAt(content, filepath.Dir(path)); ok {
		baseURL = current
		found = true
	}
	return baseURL, found
}

func parseTSConfigBaseURLAt(content, configDir string) (string, bool) {
	var data struct {
		CompilerOptions struct {
			BaseURL *string `json:"baseUrl"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil || data.CompilerOptions.BaseURL == nil {
		return "", false
	}
	baseURL := normalizeTSConfigRelativePath(*data.CompilerOptions.BaseURL, configDir)
	return baseURL, true
}

func resolveTSConfigExtendsPath(currentPath, spec string) (string, bool) {
	spec = strings.Trim(strings.TrimSpace(spec), `"'`)
	if spec == "" || !(strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")) {
		return "", false
	}
	baseDir := filepath.Dir(filepath.ToSlash(currentPath))
	if baseDir == "." {
		baseDir = ""
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(baseDir, spec)))
	if filepath.Ext(resolved) == "" {
		resolved += ".json"
	}
	if strings.HasPrefix(resolved, "../") || resolved == ".." {
		return "", false
	}
	return strings.TrimPrefix(resolved, "./"), true
}

func mergeTSPathMappings(mappings []tsPathMapping) []tsPathMapping {
	byPattern := map[string]tsPathMapping{}
	for _, mapping := range mappings {
		if mapping.Pattern == "" || len(mapping.Targets) == 0 {
			continue
		}
		byPattern[mapping.Pattern] = mapping
	}
	out := make([]tsPathMapping, 0, len(byPattern))
	for _, pattern := range sortedKeysOf(byPattern) {
		out = append(out, byPattern[pattern])
	}
	return out
}

func parseTSConfigPathsAt(content, configDir string) []tsPathMapping {
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
			target = normalizeTSConfigPathTarget(target, configDir)
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

func normalizeTSConfigPathTarget(target, configDir string) string {
	return normalizeTSConfigRelativePath(target, configDir)
}

func normalizeTSConfigRelativePath(target, configDir string) string {
	target = strings.TrimSpace(filepath.ToSlash(target))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	configDir = strings.TrimSpace(filepath.ToSlash(configDir))
	if configDir == "." {
		configDir = ""
	}
	configDir = strings.Trim(configDir, "/")
	if configDir == "" {
		return strings.TrimPrefix(target, "./")
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(configDir, target)))
	if resolved == "." {
		return ""
	}
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return ""
	}
	return strings.TrimPrefix(resolved, "./")
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
	inSetuptools := false
	inSetuptoolsFind := false
	inSetuptoolsPackageDir := false
	var roots []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSetuptools = line == "[tool.setuptools]"
			inSetuptoolsFind = line == "[tool.setuptools.packages.find]"
			inSetuptoolsPackageDir = line == "[tool.setuptools.package-dir]" || line == "[tool.setuptools.package_dir]"
			continue
		}
		if inSetuptoolsFind && strings.HasPrefix(line, "where") {
			roots = append(roots, parsePythonSourceRootValues(line)...)
		}
		if inSetuptools && (strings.HasPrefix(line, "package-dir") || strings.HasPrefix(line, "package_dir")) {
			roots = append(roots, parsePyProjectPackageDirRoots(line)...)
		}
		if inSetuptoolsPackageDir {
			roots = append(roots, parsePythonPackageDirRootMapping(line)...)
		}
	}
	return normalizePythonSourceRoots(roots)
}

func parsePyProjectPythonPackageDirs(content string) []pythonPackageDirMapping {
	inSetuptools := false
	inSetuptoolsPackageDir := false
	var mappings []pythonPackageDirMapping
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSetuptools = line == "[tool.setuptools]"
			inSetuptoolsPackageDir = line == "[tool.setuptools.package-dir]" || line == "[tool.setuptools.package_dir]"
			continue
		}
		if inSetuptools && (strings.HasPrefix(line, "package-dir") || strings.HasPrefix(line, "package_dir")) {
			mappings = append(mappings, parsePyProjectPackageDirMappings(line)...)
		}
		if inSetuptoolsPackageDir {
			mappings = append(mappings, parsePythonPackageDirMapping(line)...)
		}
	}
	return normalizePythonPackageDirMappings(mappings)
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
	inOptions := false
	inFind := false
	inPackageDirSection := false
	inPackageDirContinuation := false
	var roots []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inOptions = strings.EqualFold(line, "[options]")
			inFind = strings.EqualFold(line, "[options.packages.find]")
			inPackageDirSection = strings.EqualFold(line, "[options.package_dir]") || strings.EqualFold(line, "[options.package-dir]")
			inPackageDirContinuation = false
			continue
		}
		if inFind && strings.HasPrefix(strings.ToLower(line), "where") {
			roots = append(roots, parsePythonSourceRootValues(line)...)
		}
		if inOptions && strings.HasPrefix(strings.ToLower(line), "package_dir") {
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[len("package_dir"):]), "="))
			if value != "" {
				roots = append(roots, strings.Trim(value, `"'`))
			}
			inPackageDirContinuation = value == ""
			continue
		}
		if inPackageDirSection || inPackageDirContinuation {
			roots = append(roots, parsePythonPackageDirRootMapping(line)...)
		}
	}
	return normalizePythonSourceRoots(roots)
}

func parseSetupCFGPythonPackageDirs(content string) []pythonPackageDirMapping {
	inOptions := false
	inPackageDirSection := false
	inPackageDirContinuation := false
	var mappings []pythonPackageDirMapping
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inOptions = strings.EqualFold(line, "[options]")
			inPackageDirSection = strings.EqualFold(line, "[options.package_dir]") || strings.EqualFold(line, "[options.package-dir]")
			inPackageDirContinuation = false
			continue
		}
		if inOptions && strings.HasPrefix(strings.ToLower(line), "package_dir") {
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[len("package_dir"):]), "="))
			if value != "" {
				mappings = append(mappings, pythonPackageDirMapping{Dir: strings.Trim(value, `"'`)})
			}
			inPackageDirContinuation = value == ""
			continue
		}
		if inPackageDirSection || inPackageDirContinuation {
			mappings = append(mappings, parsePythonPackageDirMapping(line)...)
		}
	}
	return normalizePythonPackageDirMappings(mappings)
}

func parsePyProjectPackageDirRoots(line string) []string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return nil
	}
	value := strings.TrimSpace(parts[1])
	if strings.HasPrefix(value, "{") {
		return regexpPackageDirRootValues(value)
	}
	value = strings.Trim(value, `"'`)
	if value == "" {
		return nil
	}
	return []string{value}
}

func parsePyProjectPackageDirMappings(line string) []pythonPackageDirMapping {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return nil
	}
	value := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(value, "{") {
		return []pythonPackageDirMapping{{Dir: strings.Trim(value, `"'`)}}
	}
	var mappings []pythonPackageDirMapping
	re := regexp.MustCompile(`(?m)["']([^"']*)["']\s*=\s*["']([^"']+)["']`)
	for _, match := range re.FindAllStringSubmatch(value, -1) {
		if len(match) == 3 {
			mappings = append(mappings, pythonPackageDirMapping{Package: match[1], Dir: match[2]})
		}
	}
	return mappings
}

func regexpPackageDirRootValues(value string) []string {
	var roots []string
	re := regexp.MustCompile(`(?m)(?:""|'')\s*=\s*["']([^"']+)["']`)
	for _, match := range re.FindAllStringSubmatch(value, -1) {
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			roots = append(roots, match[1])
		}
	}
	return roots
}

func parsePythonPackageDirRootMapping(line string) []string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) != "" {
		return nil
	}
	value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	if value == "" {
		return nil
	}
	return []string{value}
}

func parsePythonPackageDirMapping(line string) []pythonPackageDirMapping {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return nil
	}
	pkg := strings.Trim(strings.TrimSpace(parts[0]), `"'`)
	dir := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	if dir == "" {
		return nil
	}
	return []pythonPackageDirMapping{{Package: pkg, Dir: dir}}
}

func normalizePythonPackageDirMappings(mappings []pythonPackageDirMapping) []pythonPackageDirMapping {
	seen := map[string]bool{}
	var out []pythonPackageDirMapping
	for _, mapping := range mappings {
		pkg := strings.Trim(strings.ReplaceAll(strings.TrimSpace(mapping.Package), "/", "."), ".")
		dir := strings.Trim(filepath.ToSlash(strings.TrimSpace(mapping.Dir)), "/")
		if dir == "" {
			continue
		}
		key := pkg + "\x00" + dir
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pythonPackageDirMapping{Package: pkg, Dir: dir})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Dir) == len(out[j].Dir) {
			if out[i].Dir == out[j].Dir {
				return out[i].Package < out[j].Package
			}
			return out[i].Dir < out[j].Dir
		}
		return len(out[i].Dir) > len(out[j].Dir)
	})
	return out
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

func parseCargoLibPath(content string) string {
	inLib := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inLib = line == "[lib]"
			continue
		}
		if inLib && strings.HasPrefix(strings.ToLower(line), "path") {
			if path, ok := parseSimpleConfigKeyValue(line, "path"); ok {
				return filepath.ToSlash(path)
			}
		}
	}
	return ""
}

func cargoRustSourceRootDir(cargoPath, content string) string {
	dir := filepath.ToSlash(filepath.Dir(cargoPath))
	if libPath := parseCargoLibPath(content); libPath != "" {
		root := filepath.ToSlash(filepath.Dir(filepath.Join(dir, libPath)))
		if root == "." {
			return ""
		}
		return root
	}
	if dir == "." || dir == "" {
		return "src"
	}
	return dir + "/src"
}

func inferCargoManifestsFromRustPaths(rustPaths []string, readContent contentReader) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range rustPaths {
		dir := filepath.ToSlash(filepath.Dir(path))
		for {
			candidate := "Cargo.toml"
			if dir != "." && dir != "" {
				candidate = dir + "/Cargo.toml"
			}
			if !seen[candidate] {
				if _, ok := readContent(candidate); ok {
					seen[candidate] = true
					out = append(out, candidate)
				}
			}
			if dir == "." || dir == "" {
				break
			}
			parent := filepath.ToSlash(filepath.Dir(dir))
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	sort.Strings(out)
	return out
}

func parseSimpleConfigValue(line string) (string, bool) {
	return parseSimpleConfigKeyValue(line, "name")
}

func parseSimpleConfigKeyValue(line, key string) (string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 || strings.ToLower(strings.TrimSpace(parts[0])) != strings.ToLower(key) {
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
	if ext == ".cs" {
		return resolver.resolveCSharpImport(importingPath, spec)
	}
	if ext == ".php" {
		return resolver.resolvePHPImport(importingPath, spec)
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
	if resolution, ok := resolver.resolveJSWorkspacePackageImport(importingPath, spec); ok {
		return resolution, true
	}
	if targetModule, ok := resolver.resolveScopedJSImportMap(importingPath, spec); ok {
		if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
			return manifestImportResolution{
				Path:         path,
				Confidence:   0.9,
				Scope:        "module",
				Reason:       "JS/TS import resolved through scoped import map",
				EvidenceKind: "import_map_scoped_import",
			}, true
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
	for _, baseURL := range resolver.tsBaseURLDirs {
		targetModule := filepath.ToSlash(filepath.Join(baseURL, spec))
		if path, resolved := resolver.resolveJSModulePath(targetModule); resolved && path != filepath.ToSlash(importingPath) {
			return manifestImportResolution{
				Path:         path,
				Confidence:   0.88,
				Scope:        "module",
				Reason:       "JS/TS bare module import resolved through tsconfig.json baseUrl",
				EvidenceKind: "tsconfig_baseurl_import",
			}, true
		}
	}
	return manifestImportResolution{}, false
}

func (resolver manifestImportResolver) resolveScopedJSImportMap(importingPath, spec string) (string, bool) {
	if len(resolver.jsImportMapScopes) == 0 {
		return "", false
	}
	importingPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(importingPath)), "/")
	for _, scope := range resolver.jsImportMapScopes {
		if strings.HasPrefix(importingPath, scope.Prefix) {
			if target, ok := resolver.resolveJSTargetMap(scope.Targets, spec); ok {
				return target, true
			}
		}
	}
	return "", false
}

func (resolver manifestImportResolver) resolveJSWorkspacePackageImport(importingPath, spec string) (manifestImportResolution, bool) {
	if len(resolver.jsWorkspacePackages) == 0 {
		return manifestImportResolution{}, false
	}
	importingPath = filepath.ToSlash(importingPath)
	for _, pkg := range resolver.jsWorkspacePackages {
		if spec != pkg.Name && !strings.HasPrefix(spec, pkg.Name+"/") {
			continue
		}
		exportKey := "."
		if spec != pkg.Name {
			exportKey = "./" + strings.TrimPrefix(strings.TrimPrefix(spec, pkg.Name), "/")
		}
		if targetModule, ok := resolver.resolveJSTargetMap(pkg.Exports, exportKey); ok {
			if path, resolved := resolver.resolveJSWorkspacePackageModule(pkg, targetModule); resolved && path != importingPath {
				return manifestImportResolution{
					Path:         path,
					Confidence:   0.91,
					Scope:        "module",
					Reason:       "JS/TS workspace package export resolved through nested package.json",
					EvidenceKind: "package_workspace_exports_import",
				}, true
			}
		}
		module := strings.TrimPrefix(exportKey, "./")
		if module == "." || module == "" {
			module = "index"
		}
		if path, ok := resolver.resolveJSWorkspacePackageModule(pkg, module); ok && path != importingPath {
			return manifestImportResolution{
				Path:         path,
				Confidence:   0.89,
				Scope:        "module",
				Reason:       "JS/TS workspace package import resolved through nested package.json",
				EvidenceKind: "package_workspace_import",
			}, true
		}
	}
	return manifestImportResolution{}, false
}

func (resolver manifestImportResolver) resolveJSWorkspacePackageModule(pkg jsPackageRoot, module string) (string, bool) {
	module = strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(module)), "./")
	if module == "" {
		return "", false
	}
	if path, ok := resolver.resolveJSModulePath(filepath.ToSlash(filepath.Join(pkg.Root, module))); ok {
		return path, true
	}
	return resolver.resolveJSModulePath(filepath.ToSlash(filepath.Join(pkg.Root, "src", module)))
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

func pythonPackageDirModuleKeysForPath(path string, mappings []pythonPackageDirMapping, pyFileSet map[string]bool) []pythonModuleKey {
	path = filepath.ToSlash(path)
	if !strings.HasSuffix(path, ".py") || len(mappings) == 0 {
		return nil
	}
	withoutExt := strings.TrimSuffix(path, ".py")
	if strings.HasSuffix(withoutExt, "/__init__") {
		withoutExt = strings.TrimSuffix(withoutExt, "/__init__")
	}
	var keys []pythonModuleKey
	seen := map[string]bool{}
	for _, mapping := range mappings {
		dir := strings.Trim(filepath.ToSlash(mapping.Dir), "/")
		if dir == "" || (withoutExt != dir && !strings.HasPrefix(withoutExt, dir+"/")) {
			continue
		}
		rel := strings.Trim(strings.TrimPrefix(withoutExt, dir), "/")
		module := strings.Trim(strings.ReplaceAll(filepath.ToSlash(rel), "/", "."), ".")
		if mapping.Package != "" {
			module = strings.Trim(mapping.Package+"."+module, ".")
		}
		if module == "" || seen[module] {
			continue
		}
		seen[module] = true
		keys = append(keys, pythonModuleKey{
			Module:    module,
			Namespace: pythonPackageDirUnderNamespaceRoot(path, mapping, pyFileSet),
		})
	}
	return keys
}

func pythonPackageDirUnderNamespaceRoot(path string, mapping pythonPackageDirMapping, pyFileSet map[string]bool) bool {
	dir := strings.Trim(filepath.ToSlash(mapping.Dir), "/")
	if dir == "" || mapping.Package == "" {
		return false
	}
	return !pyFileSet[dir+"/__init__.py"]
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

func (resolver manifestImportResolver) resolveCSharpImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = normalizeCSharpNamespace(spec)
	if spec == "" || resolver.csharpAmbiguous[spec] {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.csharpNamespaces[spec]
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	evidenceKind := resolver.csharpEvidence[spec]
	if evidenceKind == "" {
		evidenceKind = "csharp_namespace_import"
	}
	confidence := 0.86
	reason := "C# namespace import resolved through local namespace"
	if evidenceKind == "csharp_csproj_namespace_import" {
		confidence = 0.87
		reason = "C# namespace import resolved through .csproj root namespace"
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   confidence,
		Scope:        "module",
		Reason:       reason,
		EvidenceKind: evidenceKind,
	}, true
}

func (resolver manifestImportResolver) resolveJVMImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasSuffix(spec, ".*") {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.jvmTypes[spec]
	matchedSpec := spec
	if !ok {
		probe := spec
		for strings.Contains(probe, ".") {
			probe = probe[:strings.LastIndex(probe, ".")]
			if path, ok = resolver.jvmTypes[probe]; ok {
				matchedSpec = probe
				break
			}
		}
	}
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	evidenceKind := resolver.jvmTypeEvidence[matchedSpec]
	if evidenceKind == "" {
		evidenceKind = "jvm_package_import"
	}
	reason := "JVM package import resolved through package declaration"
	if evidenceKind == "jvm_manifest_package_import" {
		reason = "JVM package import resolved through Maven/Gradle package identity"
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   0.9,
		Scope:        "module",
		Reason:       reason,
		EvidenceKind: evidenceKind,
	}, true
}

func parsePOMJVMPackagePrefixes(content string) []string {
	groupID := firstXMLTagValue(content, "groupId")
	artifactID := firstXMLTagValue(content, "artifactId")
	return jvmPackagePrefixesFromGroupArtifact(groupID, artifactID)
}

func parseCSharpProjectNamespacePrefixes(path, content string) []string {
	var prefixes []string
	if root := firstXMLTagValue(content, "RootNamespace"); root != "" {
		prefixes = append(prefixes, root)
	}
	if assembly := firstXMLTagValue(content, "AssemblyName"); assembly != "" {
		prefixes = append(prefixes, assembly)
	}
	if len(prefixes) == 0 {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if base != "" {
			prefixes = append(prefixes, base)
		}
	}
	return normalizeCSharpNamespaces(prefixes)
}

func firstXMLTagValue(content, tag string) string {
	re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(tag) + `>\s*([^<]+?)\s*</` + regexp.QuoteMeta(tag) + `>`)
	if match := re.FindStringSubmatch(content); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func parseGradleJVMPackagePrefixes(content, fallbackArtifact string) []string {
	group := firstGradleStringAssignment(content, "group")
	artifact := firstGradleStringAssignment(content, "archivesBaseName")
	if artifact == "" {
		artifact = firstGradleStringAssignment(content, "archivesName")
	}
	if artifact == "" {
		artifact = fallbackArtifact
	}
	return jvmPackagePrefixesFromGroupArtifact(group, artifact)
}

func parseGradleRootProjectName(content string) string {
	return firstGradleStringAssignment(content, "rootProject.name")
}

func firstGradleStringAssignment(content, key string) string {
	pattern := `(?m)^\s*` + regexp.QuoteMeta(key) + `\s*(?:=|\(\s*)\s*["']([^"']+)["']`
	re := regexp.MustCompile(pattern)
	if match := re.FindStringSubmatch(content); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func jvmPackagePrefixesFromGroupArtifact(groupID, artifactID string) []string {
	groupID = normalizeJVMPackagePrefix(groupID)
	artifactID = normalizeJVMPackagePrefix(artifactID)
	if groupID == "" || artifactID == "" {
		return nil
	}
	return []string{groupID + "." + artifactID}
}

func normalizeJVMPackagePrefixes(prefixes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, prefix := range prefixes {
		prefix = normalizeJVMPackagePrefix(prefix)
		if prefix == "" || seen[prefix] {
			continue
		}
		seen[prefix] = true
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func normalizeJVMPackagePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.ReplaceAll(prefix, "-", ".")
	prefix = strings.ReplaceAll(prefix, "_", ".")
	prefix = regexp.MustCompile(`[^A-Za-z0-9.]+`).ReplaceAllString(prefix, ".")
	prefix = regexp.MustCompile(`\.+`).ReplaceAllString(prefix, ".")
	return strings.Trim(prefix, ".")
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

func csharpNamespaceName(content string) string {
	re := regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_\.]*)\s*(?:[;{]|$)`)
	if match := re.FindStringSubmatch(content); len(match) == 2 {
		return normalizeCSharpNamespace(match[1])
	}
	return ""
}

func normalizeCSharpNamespaces(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		name = normalizeCSharpNamespace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalizeCSharpNamespace(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "-", ".")
	name = regexp.MustCompile(`[^A-Za-z0-9_.]+`).ReplaceAllString(name, ".")
	name = regexp.MustCompile(`\.+`).ReplaceAllString(name, ".")
	return strings.Trim(name, ".")
}

func (resolver manifestImportResolver) resolvePHPImport(importingPath, spec string) (manifestImportResolution, bool) {
	spec = normalizePHPClassName(spec)
	if spec == "" || resolver.phpTypeAmbiguous[spec] {
		return manifestImportResolution{}, false
	}
	path, ok := resolver.phpTypes[spec]
	if !ok || path == filepath.ToSlash(importingPath) {
		return manifestImportResolution{}, false
	}
	evidenceKind := resolver.phpTypeEvidence[spec]
	if evidenceKind == "" {
		evidenceKind = "php_namespace_import"
	}
	confidence := 0.87
	reason := "PHP namespace import resolved through local type declaration"
	if evidenceKind == "composer_psr4_import" {
		confidence = 0.88
		reason = "PHP namespace import resolved through composer PSR-4 autoload"
	}
	return manifestImportResolution{
		Path:         path,
		Confidence:   confidence,
		Scope:        "module",
		Reason:       reason,
		EvidenceKind: evidenceKind,
	}, true
}

func parseComposerPSR4Prefixes(content string) []phpPSR4Prefix {
	var data struct {
		Autoload struct {
			PSR4 map[string]any `json:"psr-4"`
		} `json:"autoload"`
		AutoloadDev struct {
			PSR4 map[string]any `json:"psr-4"`
		} `json:"autoload-dev"`
	}
	if err := json.Unmarshal([]byte(stripJSONLineComments(content)), &data); err != nil {
		return nil
	}
	byPrefix := map[string]map[string]bool{}
	add := func(rawPrefix string, rawDirs any) {
		prefix := normalizePHPClassPrefix(rawPrefix)
		if prefix == "" {
			return
		}
		dirs := composerPSR4Dirs(rawDirs)
		if len(dirs) == 0 {
			return
		}
		if byPrefix[prefix] == nil {
			byPrefix[prefix] = map[string]bool{}
		}
		for _, dir := range dirs {
			byPrefix[prefix][dir] = true
		}
	}
	for prefix, dirs := range data.Autoload.PSR4 {
		add(prefix, dirs)
	}
	for prefix, dirs := range data.AutoloadDev.PSR4 {
		add(prefix, dirs)
	}
	var prefixes []phpPSR4Prefix
	for prefix, dirs := range byPrefix {
		values := make([]string, 0, len(dirs))
		for dir := range dirs {
			values = append(values, dir)
		}
		sort.Strings(values)
		prefixes = append(prefixes, phpPSR4Prefix{Prefix: prefix, Dirs: values})
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i].Prefix) == len(prefixes[j].Prefix) {
			return prefixes[i].Prefix < prefixes[j].Prefix
		}
		return len(prefixes[i].Prefix) > len(prefixes[j].Prefix)
	})
	return prefixes
}

func composerPSR4Dirs(raw any) []string {
	var dirs []string
	add := func(value string) {
		value = strings.Trim(filepath.ToSlash(strings.TrimSpace(value)), "/")
		if value == "" || value == "." {
			value = ""
		}
		dirs = append(dirs, value)
	}
	switch value := raw.(type) {
	case string:
		add(value)
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				add(text)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) == len(out[j]) {
			return out[i] < out[j]
		}
		return len(out[i]) > len(out[j])
	})
	return out
}

func phpQualifiedTypeNames(content string) []string {
	namespace := phpNamespaceName(content)
	re := regexp.MustCompile(`(?im)^\s*(?:abstract\s+|final\s+)?(?:class|interface|trait|enum)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	var names []string
	seen := map[string]bool{}
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		name := match[1]
		if namespace != "" {
			name = namespace + `\` + name
		}
		name = normalizePHPClassName(name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func phpNamespaceName(content string) string {
	re := regexp.MustCompile(`(?im)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_\\]*)\s*(?:[;{]|$)`)
	if match := re.FindStringSubmatch(content); len(match) == 2 {
		return normalizePHPClassName(match[1])
	}
	return ""
}

func phpPathUnderAnyDir(path string, dirs []string) bool {
	path = strings.Trim(filepath.ToSlash(path), "/")
	for _, dir := range dirs {
		dir = strings.Trim(filepath.ToSlash(dir), "/")
		if dir == "" || path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

func phpClassNameHasPrefix(name, prefix string) bool {
	name = normalizePHPClassName(name)
	prefix = normalizePHPClassPrefix(prefix)
	return prefix != "" && (name == strings.TrimSuffix(prefix, `\`) || strings.HasPrefix(name, prefix))
}

func normalizePHPClassPrefix(name string) string {
	name = normalizePHPClassName(name)
	if name == "" {
		return ""
	}
	return strings.TrimSuffix(name, `\`) + `\`
}

func normalizePHPClassName(name string) string {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	for _, prefix := range []string{"function ", "const "} {
		if strings.HasPrefix(lower, prefix) {
			name = strings.TrimSpace(name[len(prefix):])
			lower = strings.ToLower(name)
			break
		}
	}
	if idx := regexp.MustCompile(`(?i)\s+as\s+`).FindStringIndex(name); len(idx) == 2 {
		name = strings.TrimSpace(name[:idx[0]])
	}
	name = strings.Trim(name, `\`)
	name = strings.ReplaceAll(name, "/", `\`)
	name = regexp.MustCompile(`[^A-Za-z0-9_\\]+`).ReplaceAllString(name, `\`)
	name = regexp.MustCompile(`\\+`).ReplaceAllString(name, `\`)
	return strings.Trim(name, `\`)
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
	// Module keys are crate-qualified (e.g. "tokio::runtime::handle"), so import
	// specs are resolved relative to the importing file's crate. crate::/self::/
	// super:: are intra-crate; a bare path whose head is a known workspace crate is
	// a cross-crate import; everything else (std, third-party deps) is external.
	importCrate := resolver.crateForPath(importingPath)
	evidenceKind := "rust_crate_import"
	var qualified string
	switch {
	case module == "crate" || strings.HasPrefix(module, "crate::"):
		qualified = rustJoinCrateRest(importCrate, strings.TrimPrefix(strings.TrimPrefix(module, "crate"), "::"))
	case strings.HasPrefix(module, "self::"):
		qualified = rustJoinCrateRest(resolver.rustCurrentModuleForPath(importingPath), strings.TrimPrefix(module, "self::"))
	case strings.HasPrefix(module, "super::"):
		current := resolver.rustCurrentModuleForPath(importingPath)
		rest := module
		for strings.HasPrefix(rest, "super::") {
			rest = strings.TrimPrefix(rest, "super::")
			if idx := strings.LastIndex(current, "::"); idx >= 0 {
				current = current[:idx]
			} else {
				current = ""
			}
		}
		qualified = rustJoinCrateRest(current, rest)
	default:
		head := module
		if idx := strings.Index(module, "::"); idx >= 0 {
			head = module[:idx]
		}
		if !resolver.rustCrateNames[head] {
			return manifestImportResolution{}, false
		}
		qualified = module
		evidenceKind = "cargo_package_import"
	}
	path, ok := resolver.resolveRustModulePath(qualified)
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
	// No indexed modules (e.g. a Cargo workspace where crates live under
	// <crate>/src/ rather than ./src/, so rustModuleKeysForPath matched nothing):
	// resolution can never succeed, so skip the O(depth^2) alias-expansion walk
	// that would otherwise run — and fail — for every import on large repos.
	if len(resolver.rustModules) == 0 {
		return "", false
	}
	seen := map[string]bool{}
	for iters := 0; module != "" && iters < 256; iters++ {
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

func rustModuleFromRel(rel string) string {
	rel = strings.TrimSuffix(rel, ".rs")
	if rel == "lib" || rel == "main" {
		return ""
	}
	rel = strings.TrimSuffix(rel, "/mod")
	return strings.ReplaceAll(rel, "/", "::")
}

func rustJoinCrateRest(base, rest string) string {
	base, rest = strings.Trim(base, ":"), strings.Trim(rest, ":")
	if base == "" {
		return rest
	}
	if rest == "" {
		return base
	}
	return base + "::" + rest
}

// rustModuleKeysForPath maps a .rs file to its crate-qualified module key
// (e.g. tokio/src/runtime/handle.rs -> "tokio::runtime::handle") using the
// per-crate src roots discovered from every Cargo.toml. Falls back to a bare
// key for a root-level src/ when no crate metadata is available.
func (resolver manifestImportResolver) rustModuleKeysForPath(path string) []string {
	path = filepath.ToSlash(path)
	if !strings.HasSuffix(path, ".rs") {
		return nil
	}
	for _, sr := range resolver.rustSrcRoots {
		if strings.HasPrefix(path, sr.dir+"/") {
			mod := rustModuleFromRel(strings.TrimPrefix(path, sr.dir+"/"))
			if mod == "" {
				return []string{sr.crate}
			}
			return []string{sr.crate + "::" + mod}
		}
	}
	if strings.HasPrefix(path, "src/") {
		mod := rustModuleFromRel(strings.TrimPrefix(path, "src/"))
		if mod == "" {
			return nil
		}
		return []string{mod}
	}
	return nil
}

func (resolver manifestImportResolver) crateForPath(path string) string {
	path = filepath.ToSlash(path)
	for _, sr := range resolver.rustSrcRoots {
		if strings.HasPrefix(path, sr.dir+"/") {
			return sr.crate
		}
	}
	return resolver.rustCrateName
}

type rustAlias struct {
	From string
	To   string
}

func (resolver manifestImportResolver) rustAliasesForFile(path, content string) []rustAlias {
	current := resolver.rustCurrentModuleForPath(path)
	crate := resolver.crateForPath(path)
	var aliases []rustAlias
	aliases = append(aliases, resolver.rustPathModuleAliases(path, content, current)...)
	aliases = append(aliases, rustPubUseAliases(content, current, crate)...)
	return aliases
}

func (resolver manifestImportResolver) rustPathModuleAliases(path, content, current string) []rustAlias {
	re := regexp.MustCompile(`(?m)#\s*\[\s*path\s*=\s*"([^"]+)"\s*\]\s*(?:pub\s+)?mod\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)
	var aliases []rustAlias
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		targetPath := filepath.ToSlash(filepath.Join(filepath.Dir(path), match[1]))
		keys := resolver.rustModuleKeysForPath(targetPath)
		if len(keys) == 0 {
			continue
		}
		from := rustJoinModule(current, match[2])
		aliases = append(aliases, rustAlias{From: from, To: keys[0]})
	}
	return aliases
}

func rustPubUseAliases(content, current, crate string) []rustAlias {
	re := regexp.MustCompile(`(?m)^\s*pub\s+use\s+([^;]+);`)
	var aliases []rustAlias
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		source, exported, ok := parseRustPubUseAlias(match[1], current, crate)
		if ok {
			aliases = append(aliases, rustAlias{From: exported, To: source})
		}
	}
	return aliases
}

func parseRustPubUseAlias(expr, current, crate string) (string, string, bool) {
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
	target := rustNormalizeUsePath(expr, current, crate)
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

func rustNormalizeUsePath(path, current, crate string) string {
	path = strings.Trim(path, ": ")
	switch {
	case strings.HasPrefix(path, "crate::"):
		return rustJoinModule(crate, strings.TrimPrefix(path, "crate::"))
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

func (resolver manifestImportResolver) rustCurrentModuleForPath(path string) string {
	keys := resolver.rustModuleKeysForPath(path)
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
	case ".gradle", ".groovy", ".gvy":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z0-9_\.]+)`))
	case ".hcl", ".tf", ".tfvars":
		return nil
	case ".java", ".kt", ".kts", ".scala", ".sc", ".sbt":
		return scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z0-9_\.\*]+)`))
	case ".py":
		return scanPythonImports(content)
	case ".js", ".jsx", ".ts", ".tsx":
		return scanJSImports(content)
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

func scanPythonImports(content string) []string {
	seen := map[string]struct{}{}
	add := func(module string) {
		module = strings.TrimSpace(module)
		if module != "" {
			seen[module] = struct{}{}
		}
	}
	for _, module := range scanImports(content, regexp.MustCompile(`(?m)^\s*(?:from\s+(\.*[A-Za-z0-9_\.]+)\s+import|import\s+([A-Za-z0-9_\.]+))`)) {
		if strings.HasPrefix(module, ".") && strings.Trim(module, ".") == "" {
			continue
		}
		add(module)
	}
	runtimeImportCalls := []string{`importlib\s*\.\s*import_module`, `__import__`}
	for _, match := range regexp.MustCompile(`(?m)^\s*import\s+([^\n#]+)`).FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		for _, imported := range strings.Split(match[1], ",") {
			fields := strings.Fields(strings.TrimSpace(imported))
			if len(fields) == 3 && fields[0] == "importlib" && fields[1] == "as" && isSimpleIdentifier(fields[2]) {
				runtimeImportCalls = append(runtimeImportCalls, regexp.QuoteMeta(fields[2])+`\s*\.\s*import_module`)
			}
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^\s*from\s+importlib\s+import\s+([^\n#]+)`).FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		for _, imported := range strings.Split(match[1], ",") {
			fields := strings.Fields(strings.TrimSpace(imported))
			if len(fields) == 1 && fields[0] == "import_module" {
				runtimeImportCalls = append(runtimeImportCalls, regexp.QuoteMeta(fields[0]))
			}
			if len(fields) == 3 && fields[0] == "import_module" && fields[1] == "as" && isSimpleIdentifier(fields[2]) {
				runtimeImportCalls = append(runtimeImportCalls, regexp.QuoteMeta(fields[2]))
			}
		}
	}
	runtimeImportCallRe := strings.Join(runtimeImportCalls, "|")
	for _, match := range regexp.MustCompile(`\b(?:`+runtimeImportCallRe+`)\s*\(\s*["']([^"']+)["']`).FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			add(match[1])
		}
	}
	constants := staticStringConstants(content)
	for _, match := range regexp.MustCompile(`\b(?:`+runtimeImportCallRe+`)\s*\(\s*([^,\n)]+)`).FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		if module, ok := staticStringExpressionValue(match[1], constants); ok {
			add(module)
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^\s*from\s+(\.*(?:[A-Za-z_][A-Za-z0-9_\.]*)?)\s+import\s+([^\n#]+)`).FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		module := strings.TrimSpace(match[1])
		if module == "" {
			continue
		}
		for _, item := range strings.Split(match[2], ",") {
			name, _ := parsePythonImportItem(item)
			if name == "" || name == "*" {
				continue
			}
			switch {
			case strings.HasPrefix(module, ".") && strings.Trim(module, ".") == "":
				add(pythonFromImportModuleSpec(module, name))
			case !strings.HasPrefix(module, ".") && !strings.Contains(module, "."):
				add(strings.TrimRight(module, ".") + "." + name)
			}
		}
	}
	return sortedKeys(seen)
}

func isSimpleIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func scanJSImports(content string) []string {
	seen := map[string]struct{}{}
	add := func(module string) {
		module = strings.TrimSpace(module)
		if module != "" {
			seen[module] = struct{}{}
		}
	}
	for _, module := range scanImports(content, regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]|^\s*import\s+['"]([^'"]+)['"]|require\s*\(\s*['"]([^'"]+)['"]\s*\)|import\s*\(\s*['"]([^'"]+)['"]\s*\)`)) {
		add(module)
	}
	constants := staticJSStringConstants(content)
	for _, expr := range scanJSDynamicImportExpressions(content) {
		if module, ok := staticStringExpressionValue(expr, constants); ok {
			add(module)
		}
	}
	return sortedKeys(seen)
}

func scanJSDynamicImportExpressions(content string) []string {
	callRe := regexp.MustCompile(`\b(?:require|import)\s*\(`)
	var expressions []string
	for _, loc := range callRe.FindAllStringIndex(content, -1) {
		if len(loc) != 2 {
			continue
		}
		if loc[0] > 0 {
			prev := content[loc[0]-1]
			if prev == '.' || isASCIIIdentifierByte(prev) {
				continue
			}
		}
		open := strings.LastIndex(content[loc[0]:loc[1]], "(")
		if open < 0 {
			continue
		}
		open += loc[0]
		close := findMatchingStaticDelimiter(content, open, '(', ')')
		if close < 0 {
			continue
		}
		args := splitTopLevelStaticComma(content[open+1 : close])
		if len(args) != 1 {
			continue
		}
		expressions = append(expressions, args[0])
	}
	return expressions
}

func isASCIIIdentifierByte(ch byte) bool {
	return ch == '_' || ch == '$' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
}

func staticJSStringConstants(content string) map[string]string {
	constants := map[string]string{}
	for i := 0; i < 5; i++ {
		changed := false
		for _, match := range staticStringAssignRe.FindAllStringSubmatch(content, -1) {
			if len(match) != 3 {
				continue
			}
			value, ok := staticStringExpressionValue(match[2], constants)
			if !ok || constants[match[1]] == value {
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
	case ".kt", ".kts":
		return importedKotlinNames(content)
	case ".py":
		return importedPythonNames(content)
	case ".rs":
		return importedRustNames(content)
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
	requireNamedExpr := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+\{([^}]+)\}\s*=\s*require\s*\(\s*([^\n)]+)\s*\)`)
	requireDefaultExpr := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:await\s+)?(?:require|import)\s*\(\s*([^\n)]+)\s*\)`)
	constants := staticJSStringConstants(content)
	add := func(local, module string) {
		local = strings.TrimSpace(local)
		module = strings.TrimSpace(module)
		if local != "" && module != "" {
			imports[local] = append(imports[local], module)
		}
	}
	for _, match := range namedImport.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			add(javascriptImportedLocalName(item), match[2])
		}
	}
	for _, match := range defaultImport.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2])
	}
	for _, match := range namespaceImport.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2])
	}
	for _, match := range requireNamed.FindAllStringSubmatch(content, -1) {
		for _, item := range strings.Split(match[1], ",") {
			add(javascriptImportedLocalName(item), match[2])
		}
	}
	for _, match := range requireDefault.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2])
	}
	for _, match := range requireNamedExpr.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		module, ok := staticStringExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		for _, item := range strings.Split(match[1], ",") {
			add(javascriptImportedLocalName(item), module)
		}
	}
	for _, match := range requireDefaultExpr.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		if module, ok := staticStringExpressionValue(match[2], constants); ok {
			add(match[1], module)
		}
	}
	return imports
}

func importedKotlinNames(content string) map[string][]string {
	imports := map[string][]string{}
	importRe := regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z_][A-Za-z0-9_.]*(?:\.\*)?)(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?`)
	for _, match := range importRe.FindAllStringSubmatch(content, -1) {
		module := strings.TrimSpace(match[1])
		if module == "" || strings.HasSuffix(module, ".*") {
			continue
		}
		local := strings.TrimSpace(match[2])
		if local == "" {
			parts := strings.Split(module, ".")
			local = parts[len(parts)-1]
		}
		if local != "" {
			imports[local] = append(imports[local], module)
		}
	}
	return imports
}

func resolvedImportedNameModules(importingPath string, imports map[string][]string, manifestImports manifestImportResolver, knownFiles map[string]bool, readContent contentReader) map[string][]string {
	if len(imports) == 0 {
		return imports
	}
	readCached := cachingContentReader(readContent)
	reexportCache := map[string][]string{}
	resolved := make(map[string][]string, len(imports))
	for name, modules := range imports {
		resolved[name] = append(resolved[name], modules...)
		for _, module := range modules {
			targetPath, _ := resolveImportSpecPath(importingPath, module, manifestImports, knownFiles, readCached)
			if targetPath == "" {
				continue
			}
			resolved[name] = append(resolved[name], targetPath)
			resolved[name] = append(resolved[name], resolvedJavaScriptReexportTargets(targetPath, name, manifestImports, knownFiles, readCached, map[string]bool{}, reexportCache, 0)...)
		}
		resolved[name] = uniqueStrings(resolved[name])
	}
	return resolved
}

func cachingContentReader(readContent contentReader) contentReader {
	type cached struct {
		content string
		ok      bool
	}
	cache := map[string]cached{}
	return func(path string) (string, bool) {
		if hit, ok := cache[path]; ok {
			return hit.content, hit.ok
		}
		content, ok := readContent(path)
		cache[path] = cached{content: content, ok: ok}
		return content, ok
	}
}

func resolveImportSpecPath(importingPath, module string, manifestImports manifestImportResolver, knownFiles map[string]bool, readContent contentReader) (string, bool) {
	switch {
	case isRelativeImportSpec(importingPath, module):
		if path, ok := resolveLocalImport(importingPath, module, knownFiles); ok {
			return path, true
		}
		return resolveReadableLocalImport(importingPath, module, readContent)
	default:
		if resolution, ok := manifestImports.resolve(importingPath, module); ok {
			return resolution.Path, true
		}
	}
	return "", false
}

func resolvedJavaScriptReexportTargets(path, name string, manifestImports manifestImportResolver, knownFiles map[string]bool, readContent contentReader, seen map[string]bool, cache map[string][]string, depth int) []string {
	if path == "" || depth > 8 || seen[path] {
		return nil
	}
	cacheKey := path + "\x00" + name
	if cached, ok := cache[cacheKey]; ok {
		return cached
	}
	seen[path] = true
	content, ok := readContent(path)
	if !ok {
		return nil
	}
	var out []string
	for _, reexport := range javascriptNamedReexportModulesForName(content, name) {
		targetPath, ok := resolveImportSpecPath(path, reexport, manifestImports, knownFiles, readContent)
		if !ok || targetPath == "" {
			continue
		}
		out = append(out, targetPath)
		out = append(out, resolvedJavaScriptReexportTargets(targetPath, name, manifestImports, knownFiles, readContent, seen, cache, depth+1)...)
	}
	out = append(out, resolvedJavaScriptStarReexportClosure(path, manifestImports, knownFiles, readContent, map[string]bool{}, cache, 0)...)
	out = uniqueStrings(out)
	cache[cacheKey] = out
	return out
}

func resolvedJavaScriptStarReexportClosure(path string, manifestImports manifestImportResolver, knownFiles map[string]bool, readContent contentReader, seen map[string]bool, cache map[string][]string, depth int) []string {
	if path == "" || depth > 8 || seen[path] {
		return nil
	}
	cacheKey := "\x00star\x00" + path
	if cached, ok := cache[cacheKey]; ok {
		return cached
	}
	seen[path] = true
	content, ok := readContent(path)
	if !ok {
		return nil
	}
	var out []string
	for _, reexport := range javascriptStarReexportModules(content) {
		targetPath, ok := resolveImportSpecPath(path, reexport, manifestImports, knownFiles, readContent)
		if !ok || targetPath == "" {
			continue
		}
		out = append(out, targetPath)
		out = append(out, resolvedJavaScriptStarReexportClosure(targetPath, manifestImports, knownFiles, readContent, seen, cache, depth+1)...)
	}
	out = uniqueStrings(out)
	cache[cacheKey] = out
	return out
}

var jsNamedReexportPattern = regexp.MustCompile(`(?ms)^\s*export\s+(?:type\s+)?\{([^}]+)\}\s+from\s+['"]([^'"]+)['"]`)
var jsStarReexportPattern = regexp.MustCompile(`(?m)^\s*export\s+(?:type\s+)?\*\s+from\s+['"]([^'"]+)['"]`)

func javascriptNamedReexportModulesForName(content, name string) []string {
	var modules []string
	for _, match := range jsNamedReexportPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		for _, item := range strings.Split(stripJSLineComments(match[1]), ",") {
			imported, local := javascriptImportNames(item)
			if imported == name || local == name {
				modules = append(modules, match[2])
				break
			}
		}
	}
	return uniqueStrings(modules)
}

func javascriptStarReexportModules(content string) []string {
	var modules []string
	for _, match := range jsStarReexportPattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			modules = append(modules, match[1])
		}
	}
	return uniqueStrings(modules)
}

func stripJSLineComments(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	return sortedKeys(seen)
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
	requireNamedExpr := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+\{([^}]+)\}\s*=\s*require\s*\(\s*([^\n)]+)\s*\)`)
	requireDefaultExpr := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:await\s+)?(?:require|import)\s*\(\s*([^\n)]+)\s*\)`)
	constants := staticJSStringConstants(content)
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
	for _, match := range requireNamedExpr.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		module, ok := staticStringExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		for _, item := range strings.Split(match[1], ",") {
			imported, local := javascriptImportNames(item)
			add(local, jsImportBinding{Module: module, Imported: imported})
		}
	}
	for _, match := range requireDefaultExpr.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		if module, ok := staticStringExpressionValue(match[2], constants); ok {
			add(match[1], jsImportBinding{Module: module})
		}
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

// pythonImportForm records the syntactic shape of a Python import binding so the
// dotted-call composer can resolve `alias.<tail>.fn()` from the form the binding
// was written with instead of guessing it back from the recorded module string.
// `from pkg import name` and `import pkg as name` record string-identical
// importsByName entries but mean different things; the form disambiguates them.
type pythonImportForm int

const (
	// pythonPlainImport: `import a.b` — the local name is the module's leading
	// segment, so the literal dotted call path `local.<tail>` names the module.
	// This is the zero value, so a nil/absent form map composes as a plain import
	// (the pure-string unit tests rely on that default).
	pythonPlainImport pythonImportForm = iota
	// pythonAliasRename: `import a.b as c` — the local name IS the module a.b, so
	// the terminal lives in module.<tail>.
	pythonAliasRename
	// pythonFromImport: `from pkg import name` — name is a member of pkg (a class,
	// function, or module-level value) unless pkg.name is a real submodule.
	pythonFromImport
)

func importedPythonNames(content string) map[string][]string {
	names, _ := importedPythonNamesAndForms(content)
	return names
}

// importedPythonImportForms returns the syntactic form of each Python import
// binding, keyed by the local binding name and then by the recorded module
// string (the same module strings importedPythonNames records), so the
// dotted-call composer can look up forms[local][module].
func importedPythonImportForms(content string) map[string]map[string]pythonImportForm {
	_, forms := importedPythonNamesAndForms(content)
	return forms
}

// importedPythonNamesAndForms is the single parser shared by importedPythonNames
// and importedPythonImportForms, so the module strings and their recorded forms
// can never drift apart.
func importedPythonNamesAndForms(content string) (map[string][]string, map[string]map[string]pythonImportForm) {
	imports := map[string][]string{}
	forms := map[string]map[string]pythonImportForm{}
	recordForm := func(local, module string, form pythonImportForm) {
		if forms[local] == nil {
			forms[local] = map[string]pythonImportForm{}
		}
		forms[local][module] = form
	}
	importRe := regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromRe := regexp.MustCompile(`^\s*from\s+(\.*(?:[A-Za-z_][A-Za-z0-9_\.]*)?)\s+import\s+(.+)$`)
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
				form := pythonAliasRename
				if local == "" {
					local = strings.Split(module, ".")[0]
					form = pythonPlainImport
				}
				imports[local] = append(imports[local], module)
				recordForm(local, module, form)
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
				importedModule := pythonFromImportModuleSpec(module, name)
				imports[local] = append(imports[local], importedModule)
				recordForm(local, importedModule, pythonFromImport)
			}
		}
	}
	return imports, forms
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

func pythonFromImportModuleSpec(module, imported string) string {
	module = strings.TrimSpace(module)
	imported = strings.TrimSpace(imported)
	if strings.HasPrefix(module, ".") && strings.Trim(module, ".") == "" && imported != "" {
		return module + imported
	}
	return module
}

func importedNameMatchesFile(modules []string, importingPath, targetPath string) bool {
	for _, module := range modules {
		if importModuleMatchesFile(module, importingPath, targetPath) {
			return true
		}
	}
	return false
}

func importModuleMatchesFile(module, importingPath, targetPath string) bool {
	module = strings.TrimSpace(module)
	if module == "" {
		return false
	}
	targetPath = filepath.ToSlash(targetPath)
	// resolvedImportedNameModules adds exact repository-relative JS/TS target
	// files alongside the authored import spec. Once an entry carries a source
	// extension, match it exactly; suffix matching would also select generated
	// mirrors such as `deno/lib/helpers/x.ts` for resolved `src/helpers/x.ts`.
	if !strings.HasPrefix(module, ".") {
		switch strings.ToLower(filepath.Ext(module)) {
		case ".js", ".jsx", ".ts", ".tsx":
			return filepath.ToSlash(module) == targetPath
		}
	}
	if strings.Contains(module, ".") && !strings.Contains(module, "/") {
		dottedPath := strings.ReplaceAll(module, ".", "/")
		target := strings.TrimSuffix(targetPath, filepath.Ext(targetPath))
		if target == dottedPath || strings.HasSuffix(target, "/"+dottedPath) {
			return true
		}
		if parent := filepath.ToSlash(filepath.Dir(dottedPath)); parent != "." && parent != "" {
			targetDir := filepath.ToSlash(filepath.Dir(targetPath))
			if targetDir == parent || strings.HasSuffix(targetDir, "/"+parent) {
				return true
			}
		}
	}
	if strings.HasPrefix(module, ".") {
		if dottedRelativeModuleStemMatchesFile(module, targetPath) {
			return true
		}
		base := filepath.ToSlash(filepath.Join(filepath.Dir(importingPath), module))
		target := strings.TrimSuffix(targetPath, filepath.Ext(targetPath))
		return target == base || (strings.HasSuffix(target, "/index") && strings.TrimSuffix(target, "/index") == base)
	}
	if strings.HasSuffix(module, ".go") && strings.HasSuffix(targetPath, ".go") {
		// A Go import names a package — every non-test .go file in a
		// directory — but resolveGoImport records one representative file per
		// package, so symbols declared in sibling files of the imported
		// package never matched (hugo's `loggers.TimeTrackfn` resolved only
		// when the callee happened to live in the representative file). When
		// the resolved module entry is a .go file, any non-test sibling in
		// its directory belongs to the imported package; test files are not
		// importable.
		return !strings.HasSuffix(targetPath, "_test.go") &&
			filepath.ToSlash(filepath.Dir(module)) == filepath.ToSlash(filepath.Dir(targetPath))
	}
	module = strings.TrimPrefix(module, "@/")
	module = strings.TrimPrefix(module, "src/")
	module = strings.TrimSuffix(filepath.ToSlash(module), filepath.Ext(module))
	target := strings.TrimSuffix(targetPath, filepath.Ext(targetPath))
	return strings.HasSuffix(target, module) || strings.HasSuffix(target, "/"+module) || target == module
}

func dottedRelativeModuleStemMatchesFile(module, targetPath string) bool {
	stem := jsModuleSpecifierStem(module)
	if !strings.Contains(stem, ".") {
		return false
	}
	parts := strings.Split(stem, ".")
	if len(parts) < 2 {
		return false
	}
	exportedModule := strings.TrimSpace(parts[len(parts)-1])
	targetBase := strings.TrimSuffix(filepath.Base(targetPath), filepath.Ext(targetPath))
	return exportedModule != "" && targetBase == exportedModule
}

func jsModuleSpecifierStem(module string) string {
	base := filepath.Base(filepath.ToSlash(module))
	for _, suffix := range []string{".d.ts", ".d.tsx", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// rustCodegenMacroStart matches the opening of a Rust code-generation macro
// invocation (`quote! { ... }`, `quote_block! { ... }`, etc.). The bodies of
// these macros are token templates for generated code, not calls executed by
// the surrounding function, so identifiers inside them must not produce CALLS
// edges (e.g. serde_derive's `_serde::Serializer::serialize_struct(...)` inside
// quote! wrongly resolved to the local `fn serialize_struct`).
var rustCodegenMacroStart = regexp.MustCompile(`\b(?:quote|quote_block|quote_spanned|parse_quote|parse_quote_spanned)\s*!\s*[\[({]`)

// stripRustCodegenMacroBodies blanks the delimited body of each Rust
// code-generation macro invocation while preserving the surrounding code, so
// real calls outside the macro are still scanned. Length is preserved.
func stripRustCodegenMacroBodies(content string) string {
	out := []byte(content)
	// Operate on a literal/comment-masked copy so delimiters inside strings or
	// comments do not throw off nesting. Indices line up because masking
	// preserves length.
	masked := []byte(stripCodeLiteralsAndComments(content))
	for _, loc := range rustCodegenMacroStart.FindAllIndex(masked, -1) {
		open := loc[1] - 1
		close := matchDelimiter(masked, open)
		if close < 0 {
			continue
		}
		for j := open + 1; j < close; j++ {
			out[j] = ' '
			masked[j] = ' '
		}
	}
	return string(out)
}

// matchDelimiter returns the index of the delimiter that closes the opening
// bracket at openIdx, tracking nesting of the same bracket family, or -1.
func matchDelimiter(b []byte, openIdx int) int {
	open := b[openIdx]
	var close byte
	switch open {
	case '(':
		close = ')'
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return -1
	}
	depth := 0
	for i := openIdx; i < len(b); i++ {
		switch b[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func callLikeIdentifiers(content, language string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(content)
	if language == "Python" {
		// Python uses # line comments and triple-quoted strings. The generic
		// C-family masker intentionally does not consume either, which allowed
		// examples in comments and docstrings (for example `Path("-")`) to
		// become real CALLS/CONSTRUCTS edges when a same-named repository symbol
		// happened to exist. Reuse the Python-aware, length-preserving masker
		// before scanning bare call expressions.
		stripped = stripPythonLiteralsAndComments(content)
	}
	identifiers := map[string]struct{}{}
	call := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*(?:<[^>\n;{}()]*>)?\(`)
	for _, match := range call.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) < 4 {
			continue
		}
		start := match[2]
		name := stripped[match[2]:match[3]]
		if callNameIgnored(stripped, start, name, language) {
			continue
		}
		identifiers[name] = struct{}{}
	}
	return identifiers
}

func callNameIgnored(content string, start int, name, language string) bool {
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
		// JS/browser globals; a call to these names is almost never a call to a
		// repo symbol in a JS-family file. Dart is different: names like Request
		// and Response are ordinary user classes (dart:core has no such globals),
		// and resolution only fires when a repo symbol actually matches — so the
		// blanket skip would silently drop Dart constructor calls.
		return language != "Dart"
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
	routeLiteralRe            = regexp.MustCompile(`["'](/[A-Za-z0-9_\-/{}\[\]:.]*)["']`)
	staticRouteConcatRe       = regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\+\s*["']([^"']*)["']`)
	staticStringAssignRe      = regexp.MustCompile(`(?m)\b(?:(?:const|let|var)\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s*(?::[^=\n]+)?=\s*([^\n;]+)`)
	staticTemplateHoleRe      = regexp.MustCompile(`\$\{\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\}`)
	staticPythonFStringHoleRe = regexp.MustCompile(`\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}`)
	routeCallExpressionRe     = regexp.MustCompile(`(?i)\b(?:get|post|put|patch|delete|head|options|route|handle|handlefunc|group|mapping|getmapping|postmapping|putmapping|deletemapping|patchmapping|requestmapping)\s*\(\s*([^,\n)]+)`)
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
		for _, match := range routeCallExpressionRe.FindAllStringSubmatch(line, -1) {
			if len(match) == 2 {
				if route, ok := staticRouteExpressionValue(match[1], constants); ok {
					seen[route] = struct{}{}
				}
			}
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
				seen[normalizeRouteParamSyntax(line[match[2]:match[3]])] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

func routeLiteralsForSymbol(path, content, block string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord, constants map[string]string) []string {
	seen := map[string]struct{}{}
	ext := filepath.Ext(path)
	if strings.EqualFold(ext, ".cs") {
		if typeLikeKind(symbol.Kind) {
			return nil
		}
		for _, route := range csharpAnnotationRouteLiterals(content, symbol, symbolsByID) {
			seen[route] = struct{}{}
		}
		if len(seen) > 0 {
			return sortedKeys(seen)
		}
	}
	if jvmLikeExtension(ext) {
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
	if strings.EqualFold(ext, ".php") {
		if typeLikeKind(symbol.Kind) {
			return nil
		}
		for _, route := range phpAttributeRouteLiterals(content, symbol, symbolsByID) {
			seen[route] = struct{}{}
		}
		if len(seen) > 0 {
			return sortedKeys(seen)
		}
	}
	if jsLikeExtension(ext) {
		if typeLikeKind(symbol.Kind) && len(nestJSControllerRouteLiteralsAroundSymbol(content, symbol)) > 0 {
			return nil
		}
		for _, route := range nestJSAnnotationRouteLiterals(content, symbol, symbolsByID) {
			seen[route] = struct{}{}
		}
		if len(seen) > 0 {
			return sortedKeys(seen)
		}
	}
	for _, route := range routeLiteralsWithConstants(block, constants) {
		seen[route] = struct{}{}
	}
	if jsLikeExtension(ext) {
		for _, route := range jsRouterComposedRouteLiterals(block, constants) {
			seen[route] = struct{}{}
		}
	}
	if strings.EqualFold(ext, ".py") {
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
			if symbol.QualifiedName != "" {
				if _, exists := handlers[symbol.QualifiedName]; !exists {
					handlers[symbol.QualifiedName] = symbol
				}
			}
		}
		for _, registration := range goHTTPRouteRegistrations(content) {
			handler, ok := resolveRouteHandlerSymbol(handlers, registration.Handler)
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
	groupPrefixes := map[string]string{}
	groupRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*([A-Za-z_][A-Za-z0-9_]*)\.Group\s*\(\s*([^,\n)]+)\s*\)`)
	groupMatches := groupRe.FindAllStringSubmatch(content, -1)
	changed := true
	for changed {
		changed = false
		for _, match := range groupMatches {
			if len(match) != 4 {
				continue
			}
			prefix, ok := staticRouteExpressionValue(match[3], constants)
			if !ok {
				continue
			}
			if parentPrefix := groupPrefixes[match[2]]; parentPrefix != "" {
				prefix = joinRoutePaths(parentPrefix, prefix)
			}
			if groupPrefixes[match[1]] == prefix {
				continue
			}
			groupPrefixes[match[1]] = prefix
			changed = true
		}
	}
	for _, match := range groupMatches {
		if len(match) != 4 {
			continue
		}
		if _, exists := groupPrefixes[match[1]]; exists {
			continue
		}
		if prefix, ok := staticRouteExpressionValue(match[3], constants); ok {
			groupPrefixes[match[1]] = prefix
		}
	}
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
	goHandlerExpr := `[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?`
	handleFuncRe := regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?HandleFunc\s*\(\s*([^,\n]+)\s*,\s*(` + goHandlerExpr + `)\s*\)`)
	handleFuncWrapperRe := regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?Handle\s*\(\s*([^,\n]+)\s*,\s*(?:http\.)?HandlerFunc\s*\(\s*(` + goHandlerExpr + `)\s*\)\s*\)`)
	routerMethodRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Get|Post|Put|Patch|Delete|Head|Options)\s*\(\s*([^,\n]+)\s*,\s*(` + goHandlerExpr + `)\s*\)`)
	chainedGroupMethodRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.Group\s*\(\s*([^,\n)]+)\s*\)\.(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Get|Post|Put|Patch|Delete|Head|Options)\s*\(\s*([^,\n]+)\s*,\s*(` + goHandlerExpr + `)\s*\)`)
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
	for _, match := range routerMethodRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 4 {
			routeExpr := match[2]
			if prefix := groupPrefixes[match[1]]; prefix != "" {
				if route, ok := staticRouteExpressionValue(routeExpr, constants); ok {
					routeExpr = strconv.Quote(joinRoutePaths(prefix, route))
				}
			}
			add(routeExpr, match[3], "go_router_method")
		}
	}
	for _, match := range chainedGroupMethodRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 5 {
			continue
		}
		prefix, ok := staticRouteExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		if parentPrefix := groupPrefixes[match[1]]; parentPrefix != "" {
			prefix = joinRoutePaths(parentPrefix, prefix)
		}
		route, ok := staticRouteExpressionValue(match[3], constants)
		if !ok {
			continue
		}
		add(strconv.Quote(joinRoutePaths(prefix, route)), match[4], "go_router_group_method")
	}
	return registrations
}

func resolveRouteHandlerSymbol(handlers map[string]SymbolRecord, expr string) (SymbolRecord, bool) {
	expr = strings.TrimSpace(expr)
	if handler, ok := handlers[expr]; ok {
		return handler, true
	}
	if !strings.Contains(expr, ".") {
		return SymbolRecord{}, false
	}
	_, member, ok := strings.Cut(expr, ".")
	if !ok || member == "" || strings.Contains(member, ".") {
		return SymbolRecord{}, false
	}
	var found SymbolRecord
	seen := map[string]bool{}
	for name, handler := range handlers {
		if seen[handler.ID] {
			continue
		}
		if name == member || strings.HasSuffix(name, "."+member) {
			found = handler
			seen[handler.ID] = true
		}
	}
	if len(seen) == 1 {
		return found, true
	}
	return SymbolRecord{}, false
}

func djangoRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	knownFiles := map[string]bool{}
	for _, file := range files {
		knownFiles[file.Path] = true
	}
	moduleFiles := pythonModuleFiles(files)
	importsByFile := map[string]map[string][]pythonImportBinding{}
	symbolsByFileAndName := map[string]map[string]SymbolRecord{}
	typeSymbolsByFileAndName := map[string]map[string]SymbolRecord{}
	for _, file := range files {
		symbolsByFileAndName[file.Path] = map[string]SymbolRecord{}
		typeSymbolsByFileAndName[file.Path] = map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				if _, exists := typeSymbolsByFileAndName[file.Path][symbol.Name]; !exists {
					typeSymbolsByFileAndName[file.Path][symbol.Name] = symbol
				}
				if symbol.QualifiedName != "" {
					if _, exists := typeSymbolsByFileAndName[file.Path][symbol.QualifiedName]; !exists {
						typeSymbolsByFileAndName[file.Path][symbol.QualifiedName] = symbol
					}
				}
				continue
			}
			if _, exists := symbolsByFileAndName[file.Path][symbol.Name]; !exists {
				symbolsByFileAndName[file.Path][symbol.Name] = symbol
			}
			if symbol.QualifiedName != "" {
				if _, exists := symbolsByFileAndName[file.Path][symbol.QualifiedName]; !exists {
					symbolsByFileAndName[file.Path][symbol.QualifiedName] = symbol
				}
			}
		}
	}
	includedTargets := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".py") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		importsByFile[file.Path] = importedPythonBindings(content)
		for _, mount := range djangoIncludeMounts(content) {
			if target, ok := djangoResolveIncludeTarget(file.Path, mount, importsByFile[file.Path], moduleFiles, knownFiles); ok {
				includedTargets[target] = true
			}
		}
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	addRelation := func(route, evidence, detail string, handler SymbolRecord) {
		key := handler.ID + "\x00" + route
		if seen[key] {
			return
		}
		seen[key] = true
		relations = append(relations, expressRouteRelation{
			Route:   route,
			Handler: handler,
			Relation: RelationRecord{
				RecordType:    "relation",
				FromID:        handler.ID,
				ToID:          externalID("route", route),
				Type:          "HANDLES_ROUTE",
				Confidence:    0.84,
				Reason:        "Django URL pattern resolved to local handler",
				RelationScope: "external",
				Resolution:    "exact",
				TargetKind:    "route",
				Evidence: []Evidence{{
					Kind:      evidence,
					FilePath:  handler.FilePath,
					StartLine: handler.StartLine,
					EndLine:   handler.EndLine,
					Detail:    detail,
				}},
				WarningCodes: []string{},
			},
		})
	}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".py") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		if !includedTargets[file.Path] {
			for _, registration := range djangoRouteRegistrations(content) {
				symbols := symbolsByFileAndName
				if registration.AllowTypeHandler {
					symbols = typeSymbolsByFileAndName
				}
				handler, ok := djangoResolveRouteHandler(registration.Handler, file.Path, content, symbols, knownFiles)
				if !ok {
					continue
				}
				addRelation(registration.Route, registration.EvidenceKind, registration.Detail, handler)
			}
		}
		for _, mount := range djangoIncludeMounts(content) {
			targetFile, ok := djangoResolveIncludeTarget(file.Path, mount, importsByFile[file.Path], moduleFiles, knownFiles)
			if !ok {
				continue
			}
			targetContent, ok := readContent(targetFile)
			if !ok {
				continue
			}
			for _, registration := range djangoRouteRegistrations(targetContent) {
				symbols := symbolsByFileAndName
				if registration.AllowTypeHandler {
					symbols = typeSymbolsByFileAndName
				}
				handler, ok := djangoResolveRouteHandler(registration.Handler, targetFile, targetContent, symbols, knownFiles)
				if !ok {
					continue
				}
				fullRoute := joinRoutePaths(mount.Prefix, registration.Route)
				addRelation(fullRoute, "django_include", mount.Prefix+" + "+registration.Detail, handler)
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

func djangoRouteRegistrations(content string) []djangoRouteRegistration {
	var registrations []djangoRouteRegistration
	add := func(pattern, handler, evidence string) {
		route := djangoRoutePatternValue(pattern, evidence == "django_re_path")
		if route == "" || handler == "" {
			return
		}
		allowTypeHandler := false
		if className, ok := pythonAsViewClassName(handler); ok {
			handler = className
			allowTypeHandler = true
		}
		registrations = append(registrations, djangoRouteRegistration{
			Route:            route,
			Handler:          handler,
			EvidenceKind:     evidence,
			Detail:           route + " -> " + handler,
			AllowTypeHandler: allowTypeHandler,
		})
	}
	handlerExpr := `[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?`
	handlerOrAsViewExpr := `(?:` + handlerExpr + `\.as_view\s*\([^)\n]*\)|` + handlerExpr + `)`
	pathRe := regexp.MustCompile(`\bpath\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*(` + handlerOrAsViewExpr + `)`)
	rePathRe := regexp.MustCompile(`\bre_path\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*(` + handlerOrAsViewExpr + `)`)
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

type djangoIncludeMount struct {
	Prefix string
	Module string
	Target string
}

func pythonModuleFiles(files []FileRecord) map[string]string {
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	candidates := map[string][]string{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".py") {
			continue
		}
		for _, key := range pythonModuleKeysForPath(file.Path, nil, paths) {
			parts := strings.Split(key.Module, ".")
			for i := 0; i < len(parts)-1; i++ {
				if suffix := strings.Join(parts[i:], "."); suffix != "" {
					candidates[suffix] = append(candidates[suffix], file.Path)
				}
			}
			candidates[key.Module] = append(candidates[key.Module], file.Path)
		}
	}
	out := map[string]string{}
	for module, paths := range candidates {
		paths = dedupeStrings(paths)
		if len(paths) == 1 {
			out[module] = paths[0]
		}
	}
	return out
}

func djangoIncludeMounts(content string) []djangoIncludeMount {
	stringRe := regexp.MustCompile(`\bpath\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*include\s*\(\s*([rRuUbB]*["'][^"']*["'])`)
	targetRe := regexp.MustCompile(`\bpath\s*\(\s*([rRuUbB]*["'][^"']*["'])\s*,\s*include\s*\(\s*([A-Za-z_][A-Za-z0-9_]*(?:\.urlpatterns)?)\s*[\),]`)
	var mounts []djangoIncludeMount
	for _, match := range stringRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		prefix := djangoRoutePatternValue(match[1], false)
		module, ok := pythonStringLiteralValue(match[2])
		if prefix == "" || !ok || module == "" {
			continue
		}
		mounts = append(mounts, djangoIncludeMount{Prefix: prefix, Module: module})
	}
	for _, match := range targetRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		prefix := djangoRoutePatternValue(match[1], false)
		target := strings.TrimSpace(match[2])
		if prefix == "" || target == "" {
			continue
		}
		mounts = append(mounts, djangoIncludeMount{Prefix: prefix, Target: target})
	}
	return mounts
}

func djangoResolveIncludeTarget(importingPath string, mount djangoIncludeMount, imports map[string][]pythonImportBinding, moduleFiles map[string]string, knownFiles map[string]bool) (string, bool) {
	if mount.Module != "" {
		return djangoResolveIncludeModule(importingPath, mount.Module, moduleFiles, knownFiles)
	}
	target := strings.TrimSuffix(strings.TrimSpace(mount.Target), ".urlpatterns")
	if target == "" {
		return "", false
	}
	for _, binding := range imports[target] {
		module := strings.TrimSpace(binding.Module)
		if binding.Imported != "" {
			module = pythonDjangoIncludeModuleSpec(module, binding.Imported)
		}
		if resolved, ok := djangoResolveIncludeModule(importingPath, module, moduleFiles, knownFiles); ok {
			return resolved, true
		}
	}
	return "", false
}

func djangoResolveIncludeModule(importingPath, module string, moduleFiles map[string]string, knownFiles map[string]bool) (string, bool) {
	if resolved, ok := resolveLocalImport(importingPath, module, knownFiles); ok {
		return resolved, true
	}
	module = strings.Trim(strings.TrimSpace(module), ".")
	if module == "" {
		return "", false
	}
	target, ok := moduleFiles[module]
	return target, ok
}

func pythonDjangoIncludeModuleSpec(module, imported string) string {
	module = strings.TrimSpace(module)
	imported = strings.TrimSpace(imported)
	if imported == "" {
		return module
	}
	if strings.HasPrefix(module, ".") {
		if strings.Trim(module, ".") == "" {
			return module + imported
		}
		return module
	}
	return strings.TrimRight(module, ".") + "." + imported
}

func djangoResolveRouteHandler(handler, filePath, content string, symbolsByFileAndName map[string]map[string]SymbolRecord, knownFiles map[string]bool) (SymbolRecord, bool) {
	if !strings.Contains(handler, ".") {
		symbol, ok := symbolsByFileAndName[filePath][handler]
		return symbol, ok
	}
	alias, member, ok := strings.Cut(handler, ".")
	if !ok || alias == "" || member == "" {
		return SymbolRecord{}, false
	}
	for _, module := range importedPythonNames(content)[alias] {
		spec := module
		if strings.HasPrefix(module, ".") && strings.Trim(module, ".") == "" {
			spec = strings.TrimRight(module, ".") + "." + alias
		}
		targetFile, ok := resolveLocalImport(filePath, spec, knownFiles)
		if !ok {
			continue
		}
		if symbol, ok := symbolsByFileAndName[targetFile][member]; ok {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

func pythonTornadoRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
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
		symbolsByName := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if _, exists := symbolsByName[symbol.Name]; !exists {
				symbolsByName[symbol.Name] = symbol
			}
		}
		for _, registration := range pythonTornadoRouteRegistrations(content) {
			handler, ok := symbolsByName[registration.Handler]
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
					Confidence:    0.78,
					Reason:        "Python Tornado route tuple resolved to local handler",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      "python_tornado_route",
						FilePath:  file.Path,
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

type pythonTornadoRouteRegistration struct {
	Route   string
	Handler string
	Detail  string
}

func pythonTornadoRouteRegistrations(content string) []pythonTornadoRouteRegistration {
	routeTupleRe := regexp.MustCompile(`\(\s*((?:[rRuUbB]*)?["'][^"']+["'])\s*,\s*([A-Za-z_][A-Za-z0-9_]*)\b`)
	var registrations []pythonTornadoRouteRegistration
	seen := map[string]bool{}
	for _, match := range routeTupleRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		route := pythonTornadoRoutePatternValue(match[1])
		if route == "" {
			continue
		}
		key := route + "\x00" + match[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		registrations = append(registrations, pythonTornadoRouteRegistration{
			Route:   route,
			Handler: match[2],
			Detail:  route + " -> " + match[2],
		})
	}
	sort.Slice(registrations, func(i, j int) bool {
		if registrations[i].Route != registrations[j].Route {
			return registrations[i].Route < registrations[j].Route
		}
		return registrations[i].Handler < registrations[j].Handler
	})
	return registrations
}

func pythonTornadoRoutePatternValue(pattern string) string {
	route := djangoRoutePatternValue(pattern, true)
	if route == "" {
		return ""
	}
	namedCaptureRe := regexp.MustCompile(`\(\?P<([A-Za-z_][A-Za-z0-9_]*)>[^)]*\)`)
	route = namedCaptureRe.ReplaceAllString(route, `{$1}`)
	unnamedCaptureRe := regexp.MustCompile(`\([^)]*\)`)
	route = unnamedCaptureRe.ReplaceAllString(route, `{param}`)
	route = strings.ReplaceAll(route, `\/`, `/`)
	route = strings.ReplaceAll(route, `\d+`, `{param}`)
	return normalizeRouteParamSyntax(route)
}

func pythonStringLiteralValue(expr string) (string, bool) {
	expr = strings.TrimSpace(expr)
	for expr != "" {
		first := expr[0]
		if first == 'r' || first == 'R' || first == 'u' || first == 'U' || first == 'b' || first == 'B' {
			expr = strings.TrimSpace(expr[1:])
			continue
		}
		break
	}
	return staticStringExpressionValue(expr, nil)
}

func laravelRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	handlers := map[string]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			if symbol.Kind != "method" || symbol.ContainerID == "" {
				continue
			}
			className := ""
			for _, candidate := range recordsByFile[symbol.FilePath] {
				if candidate.ID == symbol.ContainerID {
					className = candidate.Name
					break
				}
			}
			if className == "" {
				continue
			}
			handlers[className+"."+symbol.Name] = symbol
		}
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".php") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		for _, registration := range laravelRouteRegistrations(content) {
			handler, ok := handlers[registration.Controller+"."+registration.Method]
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
					Confidence:    0.82,
					Reason:        "Laravel route declaration resolved to controller method",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:     registration.EvidenceKind,
						FilePath: file.Path,
						Detail:   registration.Detail,
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

func laravelRouteRegistrations(content string) []laravelRouteRegistration {
	var registrations []laravelRouteRegistration
	add := func(route, controller, method, evidence string) {
		route = normalizeSlashRoute(route)
		controller = shortQualifiedName(strings.TrimSpace(controller))
		if route == "" || controller == "" || method == "" {
			return
		}
		registrations = append(registrations, laravelRouteRegistration{
			Route:        route,
			Controller:   controller,
			Method:       method,
			EvidenceKind: evidence,
			Detail:       route + " -> " + controller + "." + method,
		})
	}
	arrayRe := regexp.MustCompile(`(?is)\bRoute::(?:get|post|put|patch|delete|options|any)\s*\(\s*["']([^"']+)["']\s*,\s*\[\s*([A-Za-z_\\][A-Za-z0-9_\\]*)::class\s*,\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]`)
	stringRe := regexp.MustCompile(`(?is)\bRoute::(?:get|post|put|patch|delete|options|any)\s*\(\s*["']([^"']+)["']\s*,\s*["']([A-Za-z_\\][A-Za-z0-9_\\]*)@([A-Za-z_][A-Za-z0-9_]*)["']`)
	scan := func(body, prefix, evidenceSuffix string) {
		for _, match := range arrayRe.FindAllStringSubmatch(body, -1) {
			if len(match) == 4 {
				add(joinRoutePaths(prefix, match[1]), match[2], match[3], "laravel_route_controller_array"+evidenceSuffix)
			}
		}
		for _, match := range stringRe.FindAllStringSubmatch(body, -1) {
			if len(match) == 4 {
				add(joinRoutePaths(prefix, match[1]), match[2], match[3], "laravel_route_controller_string"+evidenceSuffix)
			}
		}
	}
	topLevel, groups := laravelPrefixGroups(content)
	scan(topLevel, "", "")
	for _, group := range groups {
		scan(group.Body, group.Prefix, "_prefix_group")
	}
	for _, group := range laravelControllerGroups(content) {
		for _, match := range laravelControllerGroupMethodRouteRe().FindAllStringSubmatch(group.Body, -1) {
			if len(match) == 3 {
				add(joinRoutePaths(group.Prefix, match[1]), group.Controller, match[2], "laravel_route_controller_group")
			}
		}
	}
	return registrations
}

type laravelPrefixGroup struct {
	Prefix string
	Body   string
}

type laravelControllerGroup struct {
	Prefix     string
	Controller string
	Body       string
}

func laravelPrefixGroups(content string) (string, []laravelPrefixGroup) {
	re := regexp.MustCompile(`(?is)Route::prefix\s*\(\s*["']([^"']+)["']\s*\)\s*->\s*group\s*\(\s*function\s*\(\)\s*\{(.*?)\}\s*\);`)
	var groups []laravelPrefixGroup
	top := re.ReplaceAllStringFunc(content, func(block string) string {
		match := re.FindStringSubmatch(block)
		if len(match) == 3 {
			groups = append(groups, laravelPrefixGroup{Prefix: normalizeSlashRoute(match[1]), Body: match[2]})
		}
		return ""
	})
	return top, groups
}

func laravelControllerGroups(content string) []laravelControllerGroup {
	re := regexp.MustCompile(`(?is)\bRoute::((?:(?:prefix|controller)\s*\([^)]*\)\s*->\s*)+)group\s*\(\s*function\s*\(\)\s*\{(.*?)\}\s*\);`)
	prefixRe := regexp.MustCompile(`(?is)\bprefix\s*\(\s*["']([^"']+)["']\s*\)`)
	controllerRe := regexp.MustCompile(`(?is)\bcontroller\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)::class\s*\)`)
	var groups []laravelControllerGroup
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		chain, body := match[1], match[2]
		controllerMatch := controllerRe.FindStringSubmatch(chain)
		if len(controllerMatch) != 2 {
			continue
		}
		prefix := "/"
		if prefixMatch := prefixRe.FindStringSubmatch(chain); len(prefixMatch) == 2 {
			prefix = normalizeSlashRoute(prefixMatch[1])
		}
		groups = append(groups, laravelControllerGroup{
			Prefix:     prefix,
			Controller: controllerMatch[1],
			Body:       body,
		})
	}
	return groups
}

func laravelControllerGroupMethodRouteRe() *regexp.Regexp {
	return regexp.MustCompile(`(?is)\bRoute::(?:get|post|put|patch|delete|options|any)\s*\(\s*["']([^"']+)["']\s*,\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`)
}

func railsRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	handlers := map[string]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			if symbol.Kind != "method" || symbol.ContainerID == "" {
				continue
			}
			className := ""
			for _, candidate := range recordsByFile[symbol.FilePath] {
				if candidate.ID == symbol.ContainerID {
					className = candidate.Name
					break
				}
			}
			if className == "" {
				continue
			}
			handlers[className+"."+symbol.Name] = symbol
		}
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".rb") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok || !railsRoutesFile(file.Path, content) {
			continue
		}
		for _, registration := range railsRouteRegistrations(content) {
			var handler SymbolRecord
			found := false
			for _, className := range railsControllerClassCandidates(registration.Controller) {
				if candidate, ok := handlers[className+"."+registration.Method]; ok {
					handler = candidate
					found = true
					break
				}
			}
			if !found {
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
					Confidence:    0.82,
					Reason:        "Rails route declaration resolved to controller action",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:     registration.EvidenceKind,
						FilePath: file.Path,
						Detail:   registration.Detail,
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

func railsRoutesFile(path, content string) bool {
	slashPath := filepath.ToSlash(path)
	return strings.HasSuffix(slashPath, "config/routes.rb") || strings.Contains(content, ".routes.draw")
}

func railsRouteRegistrations(content string) []railsRouteRegistration {
	var registrations []railsRouteRegistration
	add := func(route, controller, action, evidence string) {
		route = normalizeSlashRoute(route)
		controller = strings.TrimSpace(controller)
		action = strings.TrimSpace(action)
		if route == "" || controller == "" || action == "" {
			return
		}
		registrations = append(registrations, railsRouteRegistration{
			Route:        route,
			Controller:   controller,
			Method:       action,
			EvidenceKind: evidence,
			Detail:       route + " -> " + controller + "#" + action,
		})
	}
	toRe := regexp.MustCompile(`(?is)\b(?:get|post|put|patch|delete|match)\s+["']([^"']+)["']\s*,\s*to:\s*["']([A-Za-z0-9_/]+)#([A-Za-z_][A-Za-z0-9_]*)["']`)
	hashRocketRe := regexp.MustCompile(`(?is)\b(?:get|post|put|patch|delete|match)\s+["']([^"']+)["']\s*=>\s*["']([A-Za-z0-9_/]+)#([A-Za-z_][A-Za-z0-9_]*)["']`)
	scan := func(body, routePrefix, controllerPrefix, evidenceSuffix string) {
		for _, match := range toRe.FindAllStringSubmatch(body, -1) {
			if len(match) == 4 {
				add(joinRoutePaths(routePrefix, match[1]), railsJoinControllerPrefix(controllerPrefix, match[2]), match[3], "rails_route_to"+evidenceSuffix)
			}
		}
		for _, match := range hashRocketRe.FindAllStringSubmatch(body, -1) {
			if len(match) == 4 {
				add(joinRoutePaths(routePrefix, match[1]), railsJoinControllerPrefix(controllerPrefix, match[2]), match[3], "rails_route_hash_rocket"+evidenceSuffix)
			}
		}
		for _, resource := range railsResourceDeclarations(body) {
			for _, route := range railsResourceRoutes(resource.Name, resource.Actions) {
				add(joinRoutePaths(routePrefix, route.Path), railsJoinControllerPrefix(controllerPrefix, resource.Name), route.Action, resource.EvidenceKind+evidenceSuffix)
			}
		}
	}
	topLevel, nested := railsNestedResourceBlocks(content)
	for _, block := range nested {
		parentActions := railsDefaultResourceActions()
		evidence := "rails_resources_nested_parent"
		if only := railsResourceOptionActions(block.ParentOptions, "only"); len(only) > 0 {
			parentActions = only
			evidence = "rails_resources_only_nested_parent"
		} else if except := railsResourceOptionActions(block.ParentOptions, "except"); len(except) > 0 {
			parentActions = railsResourceActionsExcept(parentActions, except)
			evidence = "rails_resources_except_nested_parent"
		}
		for _, route := range railsResourceRoutes(block.Parent, parentActions) {
			add(route.Path, block.Parent, route.Action, evidence)
		}
		parentPrefix := joinRoutePaths("/"+block.Parent, ":"+railsSingularResourceName(block.Parent)+"_id")
		scan(block.Body, parentPrefix, "", "_nested_resources")
	}
	topLevel, scoped := railsScopedRouteBlocks(topLevel)
	scan(topLevel, "", "", "")
	for _, block := range scoped {
		scan(block.Body, block.RoutePrefix, block.ControllerPrefix, block.EvidenceSuffix)
	}
	return registrations
}

type railsScopedRouteBlock struct {
	RoutePrefix      string
	ControllerPrefix string
	Body             string
	EvidenceSuffix   string
}

type railsNestedResourceBlock struct {
	Parent        string
	ParentOptions string
	Body          string
}

func railsNestedResourceBlocks(content string) (string, []railsNestedResourceBlock) {
	re := regexp.MustCompile(`(?ims)^\s*resources\s+:([A-Za-z_][A-Za-z0-9_]*)([^\n]*)\s+do\s*(.*?)^\s*end\b`)
	var blocks []railsNestedResourceBlock
	top := re.ReplaceAllStringFunc(content, func(block string) string {
		match := re.FindStringSubmatch(block)
		if len(match) != 4 {
			return block
		}
		blocks = append(blocks, railsNestedResourceBlock{
			Parent:        match[1],
			ParentOptions: strings.TrimSpace(match[2]),
			Body:          match[3],
		})
		return ""
	})
	return top, blocks
}

func railsScopedRouteBlocks(content string) (string, []railsScopedRouteBlock) {
	re := regexp.MustCompile(`(?ims)^\s*(scope\s+["']([^"']+)["']|namespace\s+:([A-Za-z_][A-Za-z0-9_]*))\s+do\s*(.*?)^\s*end\b`)
	var blocks []railsScopedRouteBlock
	top := re.ReplaceAllStringFunc(content, func(block string) string {
		match := re.FindStringSubmatch(block)
		if len(match) != 5 {
			return block
		}
		if match[2] != "" {
			blocks = append(blocks, railsScopedRouteBlock{
				RoutePrefix:    normalizeSlashRoute(match[2]),
				Body:           match[4],
				EvidenceSuffix: "_scope",
			})
			return ""
		}
		if match[3] != "" {
			ns := strings.TrimSpace(match[3])
			blocks = append(blocks, railsScopedRouteBlock{
				RoutePrefix:      normalizeSlashRoute(ns),
				ControllerPrefix: ns,
				Body:             match[4],
				EvidenceSuffix:   "_namespace",
			})
			return ""
		}
		return block
	})
	return top, blocks
}

func railsSingularResourceName(resource string) string {
	resource = strings.TrimSpace(resource)
	if strings.HasSuffix(resource, "ies") && len(resource) > 3 {
		return strings.TrimSuffix(resource, "ies") + "y"
	}
	if strings.HasSuffix(resource, "s") && len(resource) > 1 {
		return strings.TrimSuffix(resource, "s")
	}
	return resource
}

func railsJoinControllerPrefix(prefix, controller string) string {
	prefix = strings.Trim(strings.TrimSpace(filepath.ToSlash(prefix)), "/")
	controller = strings.Trim(strings.TrimSpace(filepath.ToSlash(controller)), "/")
	if prefix == "" {
		return controller
	}
	if controller == "" || strings.HasPrefix(controller, prefix+"/") {
		return controller
	}
	return prefix + "/" + controller
}

type railsResourceDeclaration struct {
	Name         string
	Actions      []string
	EvidenceKind string
}

type railsResourceRoute struct {
	Path   string
	Action string
}

func railsResourceRoutes(resource string, actions []string) []railsResourceRoute {
	resource = strings.Trim(strings.TrimSpace(resource), "/")
	if resource == "" {
		return nil
	}
	base := "/" + resource
	byAction := map[string][]string{
		"index":   {base},
		"create":  {base},
		"new":     {base + "/new"},
		"show":    {base + "/:id"},
		"edit":    {base + "/:id/edit"},
		"update":  {base + "/:id"},
		"destroy": {base + "/:id"},
	}
	var routes []railsResourceRoute
	for _, action := range actions {
		for _, path := range byAction[action] {
			routes = append(routes, railsResourceRoute{Path: path, Action: action})
		}
	}
	return routes
}

func railsResourceDeclarations(content string) []railsResourceDeclaration {
	re := regexp.MustCompile(`(?m)^\s*resources\s+:([A-Za-z_][A-Za-z0-9_]*)(?:\s*,\s*(.*))?$`)
	var declarations []railsResourceDeclaration
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		options := strings.TrimSpace(match[2])
		actions := railsDefaultResourceActions()
		evidence := "rails_resources"
		if only := railsResourceOptionActions(options, "only"); len(only) > 0 {
			actions = only
			evidence = "rails_resources_only"
		} else if except := railsResourceOptionActions(options, "except"); len(except) > 0 {
			actions = railsResourceActionsExcept(actions, except)
			evidence = "rails_resources_except"
		}
		declarations = append(declarations, railsResourceDeclaration{Name: match[1], Actions: actions, EvidenceKind: evidence})
	}
	return declarations
}

func railsDefaultResourceActions() []string {
	return []string{"index", "create", "new", "show", "edit", "update", "destroy"}
}

func railsResourceActions(value string) []string {
	seen := map[string]bool{}
	var actions []string
	known := map[string]bool{}
	for _, action := range railsDefaultResourceActions() {
		known[action] = true
	}
	normalized := regexp.MustCompile(`[:'",\[\]]`).ReplaceAllString(value, " ")
	for _, action := range strings.Fields(normalized) {
		if !known[action] {
			continue
		}
		if seen[action] {
			continue
		}
		seen[action] = true
		actions = append(actions, action)
	}
	return actions
}

func railsResourceOptionActions(options, key string) []string {
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(key) + `:\s*(?:\[(.*?)\]|%i\[(.*?)\]|:([A-Za-z_][A-Za-z0-9_]*))`)
	match := re.FindStringSubmatch(options)
	if len(match) == 0 {
		return nil
	}
	for _, group := range match[1:] {
		if strings.TrimSpace(group) != "" {
			return railsResourceActions(group)
		}
	}
	return nil
}

func railsResourceActionsExcept(actions, except []string) []string {
	excluded := map[string]bool{}
	for _, action := range except {
		excluded[action] = true
	}
	var out []string
	for _, action := range actions {
		if !excluded[action] {
			out = append(out, action)
		}
	}
	return out
}

func railsControllerClassCandidates(controller string) []string {
	controller = strings.Trim(strings.TrimSpace(filepath.ToSlash(controller)), "/")
	if controller == "" {
		return nil
	}
	parts := strings.Split(controller, "/")
	for i, part := range parts {
		parts[i] = railsCamelize(part)
	}
	compact := parts[:0]
	for _, part := range parts {
		if part != "" {
			compact = append(compact, part)
		}
	}
	parts = compact
	if len(parts) == 0 {
		return nil
	}
	className := strings.Join(parts, ".") + "Controller"
	lastClassName := parts[len(parts)-1] + "Controller"
	if className == lastClassName {
		return []string{className}
	}
	return []string{className, lastClassName}
}

func railsCamelize(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == '-' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
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
	route := "/" + value
	if !regex {
		route = normalizeRouteParamSyntax(route)
	}
	return route
}

func routeLiteralPartOfConcat(line string, start, end int) bool {
	before := strings.TrimSpace(line[:start])
	after := strings.TrimSpace(line[end:])
	return strings.HasSuffix(before, "+") || strings.HasPrefix(after, "+")
}

func staticStringConstants(content string) map[string]string {
	constants := map[string]string{}
	for i := 0; i < 5; i++ {
		changed := false
		for _, match := range staticStringAssignRe.FindAllStringSubmatch(content, -1) {
			if len(match) != 3 {
				continue
			}
			value, ok := staticStringExpressionValue(match[2], constants)
			if !ok {
				continue
			}
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

type jsFastifyPluginMount struct {
	Prefix string
	Target string
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
	Route            string
	Handler          string
	EvidenceKind     string
	Detail           string
	AllowTypeHandler bool
}

type djangoRouteRegistration struct {
	Route            string
	Handler          string
	EvidenceKind     string
	Detail           string
	AllowTypeHandler bool
}

type laravelRouteRegistration struct {
	Route        string
	Controller   string
	Method       string
	EvidenceKind string
	Detail       string
}

type railsRouteRegistration struct {
	Route        string
	Controller   string
	Method       string
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

type pythonRouterTarget struct {
	File     string
	Receiver string
}

type pythonImportBinding struct {
	Module   string
	Imported string
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
	re := regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.(?:use|route)\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)`)
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
	koaMountRe := regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.use\s*\(\s*mount\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)\.routes\s*\(\s*\)\s*\)\s*\)`)
	for _, match := range koaMountRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 4 {
			continue
		}
		prefix, ok := staticRouteExpressionValue(match[2], constants)
		if ok {
			mounts = append(mounts, jsRouterMount{Receiver: match[1], Prefix: prefix, Target: match[3]})
		}
	}
	koaUseRe := regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.use\s*\(\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)\.routes\s*\(\s*\)\s*\)`)
	for _, match := range koaUseRe.FindAllStringSubmatch(block, -1) {
		if len(match) == 3 {
			mounts = append(mounts, jsRouterMount{Receiver: match[1], Prefix: "/", Target: match[2]})
		}
	}
	return mounts
}

func jsFastifyPluginMounts(block string, constants map[string]string) []jsFastifyPluginMount {
	re := regexp.MustCompile(`(?is)\b[A-Za-z_$][\w$]*\.register\s*\(\s*([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?)\s*,\s*\{([^}]*)\}`)
	var mounts []jsFastifyPluginMount
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		options := match[2]
		prefixMatch := regexp.MustCompile(`(?is)\bprefix\s*:\s*([^,\n}]+)`).FindStringSubmatch(options)
		if len(prefixMatch) != 2 {
			continue
		}
		prefix, ok := staticRouteExpressionValue(prefixMatch[1], constants)
		if ok {
			mounts = append(mounts, jsFastifyPluginMount{Prefix: prefix, Target: match[1]})
		}
	}
	return mounts
}

func jsFastifyPluginRoutes(content string, constants map[string]string, symbols []SymbolRecord) map[string][]jsRouterRoute {
	lines := strings.Split(content, "\n")
	routes := map[string][]jsRouterRoute{}
	for _, symbol := range symbols {
		if typeLikeKind(symbol.Kind) {
			continue
		}
		block := symbolBlockFromLines(lines, symbol)
		for _, route := range jsRouterRoutes(block, constants) {
			if route.Handler != "" {
				routes[symbol.Name] = append(routes[symbol.Name], route)
			}
		}
	}
	return routes
}

func jsRouterRoutes(block string, constants map[string]string) []jsRouterRoute {
	jsHandlerExpr := `[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)?`
	re := regexp.MustCompile(`(?i)\b([A-Za-z_$][\w$]*)\.(get|post|put|patch|delete|head|options)\s*\(\s*([^,\n)]+)(?:\s*,\s*(` + jsHandlerExpr + `))?`)
	prefixes := jsRouterConstructorPrefixes(block, constants)
	var routes []jsRouterRoute
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) != 5 {
			continue
		}
		route, ok := staticRouteExpressionValue(match[3], constants)
		if ok {
			if prefix := prefixes[match[1]]; prefix != "" {
				route = joinRoutePaths(prefix, route)
			}
			routes = append(routes, jsRouterRoute{Receiver: match[1], Route: route, Handler: match[4]})
		}
	}
	return routes
}

func jsRouterConstructorPrefixes(block string, constants map[string]string) map[string]string {
	re := regexp.MustCompile(`(?is)\b(?:const|let|var)?\s*([A-Za-z_$][\w$]*)\s*=\s*new\s+(?:[A-Za-z_$][\w$]*\.)?(?:Router|KoaRouter)\s*\(\s*\{([^}]*)\}\s*\)`)
	prefixes := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		prefixMatch := regexp.MustCompile(`(?is)\bprefix\s*:\s*([^,\n}]+)`).FindStringSubmatch(match[2])
		if len(prefixMatch) == 2 {
			prefix, ok := staticRouteExpressionValue(prefixMatch[1], constants)
			if !ok {
				continue
			}
			prefixes[match[1]] = prefix
			continue
		}
		if regexp.MustCompile(`(?:^|,)\s*prefix\s*(?:,|$)`).MatchString(strings.TrimSpace(match[2])) {
			if prefix, ok := staticRouteExpressionValue("prefix", constants); ok {
				prefixes[match[1]] = prefix
			}
		}
	}
	return prefixes
}

func jsDirectRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !jsLikeExtension(filepath.Ext(file.Path)) {
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
			if symbol.QualifiedName != "" {
				if _, exists := handlers[symbol.QualifiedName]; !exists {
					handlers[symbol.QualifiedName] = symbol
				}
			}
		}
		for _, route := range jsRouterRoutes(content, staticStringConstants(content)) {
			if !jsDirectRouteReceiver(route.Receiver) || route.Handler == "" {
				continue
			}
			handler, ok := resolveRouteHandlerSymbol(handlers, route.Handler)
			if !ok {
				continue
			}
			key := handler.ID + "\x00" + route.Route
			if seen[key] {
				continue
			}
			seen[key] = true
			relations = append(relations, expressRouteRelation{
				Route:   route.Route,
				Handler: handler,
				Relation: RelationRecord{
					RecordType:    "relation",
					FromID:        handler.ID,
					ToID:          externalID("route", route.Route),
					Type:          "HANDLES_ROUTE",
					Confidence:    0.82,
					Reason:        "JavaScript route registration resolved to local handler",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      "js_direct_route",
						FilePath:  handler.FilePath,
						StartLine: handler.StartLine,
						EndLine:   handler.EndLine,
						Detail:    route.Receiver + "." + route.Route + " -> " + route.Handler,
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

func jsDirectRouteReceiver(receiver string) bool {
	switch strings.ToLower(receiver) {
	case "app", "server", "fastify":
		return true
	default:
		return false
	}
}

func staticRouteExpressionValue(expr string, constants map[string]string) (string, bool) {
	value, ok := staticStringExpressionValue(expr, constants)
	if !ok || !strings.HasPrefix(value, "/") {
		return "", false
	}
	return normalizeRouteParamSyntax(value), true
}

func staticStringExpressionValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ";"))
	if expr == "" {
		return "", false
	}
	if value, ok := staticPythonFStringValue(expr, constants); ok {
		return value, true
	}
	if (strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`)) || (strings.HasPrefix(expr, `'`) && strings.HasSuffix(expr, `'`)) {
		if parts := splitStaticConcatExpression(expr); len(parts) > 1 {
			return staticConcatStringValue(parts, constants)
		}
		return strings.Trim(expr, `"'`), true
	}
	if strings.HasPrefix(expr, "`") && strings.HasSuffix(expr, "`") {
		return staticTemplateStringValue(expr, constants)
	}
	if value, ok := staticTaggedTemplateStringValue(expr, constants); ok {
		return value, true
	}
	if value, ok := staticArrayJoinStringValue(expr, constants); ok {
		return value, true
	}
	if value, ok := staticPathJoinStringValue(expr, constants); ok {
		return value, true
	}
	if value, ok := staticURLPathnameStringValue(expr, constants); ok {
		return value, true
	}
	if parts := splitStaticConcatExpression(expr); len(parts) > 1 {
		return staticConcatStringValue(parts, constants)
	}
	if value := constants[expr]; value != "" {
		return value, true
	}
	return "", false
}

func staticTemplateStringValue(expr string, constants map[string]string) (string, bool) {
	body := strings.TrimSuffix(strings.TrimPrefix(expr, "`"), "`")
	ok := true
	value := staticTemplateHoleRe.ReplaceAllStringFunc(body, func(hole string) string {
		match := staticTemplateHoleRe.FindStringSubmatch(hole)
		if len(match) != 2 || constants[match[1]] == "" {
			ok = false
			return ""
		}
		return constants[match[1]]
	})
	if !ok || strings.Contains(value, "${") {
		return "", false
	}
	return value, true
}

func staticPythonFStringValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(expr)
	lower := strings.ToLower(expr)
	prefixLen := 0
	switch {
	case strings.HasPrefix(lower, "fr\"") || strings.HasPrefix(lower, "rf\"") || strings.HasPrefix(lower, "fr'") || strings.HasPrefix(lower, "rf'"):
		prefixLen = 2
	case strings.HasPrefix(lower, "f\"") || strings.HasPrefix(lower, "f'"):
		prefixLen = 1
	default:
		return "", false
	}
	if len(expr) <= prefixLen+1 {
		return "", false
	}
	quote := expr[prefixLen]
	if (quote != '"' && quote != '\'') || expr[len(expr)-1] != quote {
		return "", false
	}
	body := expr[prefixLen+1 : len(expr)-1]
	ok := true
	value := staticPythonFStringHoleRe.ReplaceAllStringFunc(body, func(hole string) string {
		match := staticPythonFStringHoleRe.FindStringSubmatch(hole)
		if len(match) != 2 || constants[match[1]] == "" {
			ok = false
			return ""
		}
		return constants[match[1]]
	})
	if !ok || strings.ContainsAny(value, "{}") {
		return "", false
	}
	return value, true
}

func staticTaggedTemplateStringValue(expr string, constants map[string]string) (string, bool) {
	const rawTag = "String.raw"
	if !strings.HasPrefix(expr, rawTag+"`") || !strings.HasSuffix(expr, "`") {
		return "", false
	}
	return staticTemplateStringValue(strings.TrimPrefix(expr, rawTag), constants)
}

func staticArrayJoinStringValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "[") {
		return "", false
	}
	arrayEnd := findMatchingStaticDelimiter(expr, 0, '[', ']')
	if arrayEnd < 0 {
		return "", false
	}
	rest := strings.TrimSpace(expr[arrayEnd+1:])
	if !strings.HasPrefix(rest, ".join") {
		return "", false
	}
	call := strings.TrimSpace(strings.TrimPrefix(rest, ".join"))
	if !strings.HasPrefix(call, "(") {
		return "", false
	}
	callEnd := findMatchingStaticDelimiter(call, 0, '(', ')')
	if callEnd < 0 || strings.TrimSpace(call[callEnd+1:]) != "" {
		return "", false
	}
	separator := ","
	arg := strings.TrimSpace(call[1:callEnd])
	if arg != "" {
		value, ok := staticStringExpressionValue(arg, constants)
		if !ok {
			return "", false
		}
		separator = value
	}
	body := strings.TrimSpace(expr[1:arrayEnd])
	if body == "" {
		return "", true
	}
	items := splitTopLevelStaticComma(body)
	if len(items) == 0 {
		return "", false
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := staticStringExpressionValue(item, constants)
		if !ok {
			return "", false
		}
		values = append(values, value)
	}
	return strings.Join(values, separator), true
}

func staticPathJoinStringValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(expr)
	call := ""
	switch {
	case strings.HasPrefix(expr, "path.join"):
		call = strings.TrimSpace(strings.TrimPrefix(expr, "path.join"))
	case strings.HasPrefix(expr, "path.posix.join"):
		call = strings.TrimSpace(strings.TrimPrefix(expr, "path.posix.join"))
	default:
		return "", false
	}
	if !strings.HasPrefix(call, "(") {
		return "", false
	}
	callEnd := findMatchingStaticDelimiter(call, 0, '(', ')')
	if callEnd < 0 || strings.TrimSpace(call[callEnd+1:]) != "" {
		return "", false
	}
	args := splitTopLevelStaticComma(call[1:callEnd])
	if len(args) == 0 {
		return "", false
	}
	values := make([]string, 0, len(args))
	for _, arg := range args {
		value, ok := staticStringExpressionValue(arg, constants)
		if !ok {
			return "", false
		}
		values = append(values, value)
	}
	return joinStaticPathSegments(values), true
}

func joinStaticPathSegments(values []string) string {
	var out string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if out == "" {
			out = strings.TrimRight(value, "/")
			if out == "" && strings.HasPrefix(value, "/") {
				out = "/"
			}
			continue
		}
		segment := strings.Trim(value, "/")
		if segment == "" {
			continue
		}
		out = strings.TrimRight(out, "/") + "/" + segment
	}
	if out == "" {
		return "."
	}
	return out
}

func staticURLPathnameStringValue(expr string, constants map[string]string) (string, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "new URL") {
		return "", false
	}
	call := strings.TrimSpace(strings.TrimPrefix(expr, "new URL"))
	if !strings.HasPrefix(call, "(") {
		return "", false
	}
	callEnd := findMatchingStaticDelimiter(call, 0, '(', ')')
	if callEnd < 0 || strings.TrimSpace(call[callEnd+1:]) != ".pathname" {
		return "", false
	}
	args := splitTopLevelStaticComma(call[1:callEnd])
	if len(args) == 0 {
		return "", false
	}
	value, ok := staticStringExpressionValue(args[0], constants)
	if !ok || !strings.HasPrefix(value, "/") {
		return "", false
	}
	return value, true
}

func staticConcatStringValue(parts []string, constants map[string]string) (string, bool) {
	var value string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		var piece string
		switch {
		case (strings.HasPrefix(part, `"`) && strings.HasSuffix(part, `"`)) || (strings.HasPrefix(part, `'`) && strings.HasSuffix(part, `'`)):
			piece = strings.Trim(part, `"'`)
		case strings.HasPrefix(part, "`") && strings.HasSuffix(part, "`"):
			resolved, ok := staticTemplateStringValue(part, constants)
			if !ok {
				return "", false
			}
			piece = resolved
		default:
			if constants[part] == "" {
				return "", false
			}
			piece = constants[part]
		}
		if value == "" {
			value = piece
			continue
		}
		value += piece
	}
	return value, true
}

func splitStaticConcatExpression(expr string) []string {
	var parts []string
	var b strings.Builder
	quote := rune(0)
	templateDepth := 0
	escaped := false
	for _, r := range expr {
		if quote != 0 {
			b.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if quote == '`' {
				switch r {
				case '{':
					if strings.HasSuffix(b.String(), "${") {
						templateDepth++
					}
				case '}':
					if templateDepth > 0 {
						templateDepth--
					}
				}
			}
			if r == quote && templateDepth == 0 {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
			b.WriteRune(r)
		case '+':
			part := strings.TrimSpace(b.String())
			if part == "" {
				return nil
			}
			parts = append(parts, part)
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	part := strings.TrimSpace(b.String())
	if part == "" {
		return nil
	}
	parts = append(parts, part)
	return parts
}

func splitTopLevelStaticComma(expr string) []string {
	var parts []string
	var b strings.Builder
	quote := rune(0)
	escaped := false
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	templateDepth := 0
	for _, r := range expr {
		if quote != 0 {
			b.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if quote == '`' {
				switch r {
				case '{':
					if strings.HasSuffix(b.String(), "${") {
						templateDepth++
					}
				case '}':
					if templateDepth > 0 {
						templateDepth--
					}
				}
			}
			if r == quote && templateDepth == 0 {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
			b.WriteRune(r)
		case '(':
			parenDepth++
			b.WriteRune(r)
		case ')':
			if parenDepth == 0 {
				return nil
			}
			parenDepth--
			b.WriteRune(r)
		case '[':
			bracketDepth++
			b.WriteRune(r)
		case ']':
			if bracketDepth == 0 {
				return nil
			}
			bracketDepth--
			b.WriteRune(r)
		case '{':
			braceDepth++
			b.WriteRune(r)
		case '}':
			if braceDepth == 0 {
				return nil
			}
			braceDepth--
			b.WriteRune(r)
		case ',':
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				part := strings.TrimSpace(b.String())
				if part == "" {
					return nil
				}
				parts = append(parts, part)
				b.Reset()
				continue
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	if quote != 0 || parenDepth != 0 || bracketDepth != 0 || braceDepth != 0 {
		return nil
	}
	part := strings.TrimSpace(b.String())
	if part == "" {
		return nil
	}
	parts = append(parts, part)
	return parts
}

func findMatchingStaticDelimiter(expr string, start int, open, close rune) int {
	if start < 0 || start >= len(expr) || rune(expr[start]) != open {
		return -1
	}
	depth := 0
	quote := rune(0)
	escaped := false
	templateDepth := 0
	for i, r := range expr {
		if i < start {
			continue
		}
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if quote == '`' {
				switch r {
				case '{':
					prefix := expr[:i]
					if strings.HasSuffix(prefix, "${") {
						templateDepth++
					}
				case '}':
					if templateDepth > 0 {
						templateDepth--
					}
				}
			}
			if r == quote && templateDepth == 0 {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
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
	pluginRoutesByFile := map[string]map[string][]jsRouterRoute{}
	mountsByFile := map[string][]jsRouterMount{}
	pluginMountsByFile := map[string][]jsFastifyPluginMount{}
	importBindingsByFile := map[string]map[string][]jsImportBinding{}
	defaultExportsByFile := map[string]string{}
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
			if symbol.QualifiedName != "" {
				if _, exists := symbolsByFileAndName[file.Path][symbol.QualifiedName]; !exists {
					symbolsByFileAndName[file.Path][symbol.QualifiedName] = symbol
				}
			}
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		constants := staticStringConstants(content)
		routesByFile[file.Path] = jsRouterRoutes(content, constants)
		pluginRoutesByFile[file.Path] = jsFastifyPluginRoutes(content, constants, recordsByFile[file.Path])
		mountsByFile[file.Path] = jsRouterMounts(content, constants)
		pluginMountsByFile[file.Path] = jsFastifyPluginMounts(content, constants)
		importBindingsByFile[file.Path] = importedJavaScriptBindings(content)
		defaultExportsByFile[file.Path] = javascriptDefaultExportName(content)
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		for _, mount := range mountsByFile[file.Path] {
			targetLocal, targetMember := splitJavaScriptMember(mount.Target)
			localReceiver := targetLocal
			if targetMember != "" {
				localReceiver = targetMember
			}
			for _, route := range routesByFile[file.Path] {
				if route.Receiver != localReceiver || route.Handler == "" {
					continue
				}
				handler, ok := resolveRouteHandlerSymbol(symbolsByFileAndName[file.Path], route.Handler)
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
						Reason:        "JavaScript router route resolved through local router mount",
						RelationScope: "external",
						Resolution:    "exact",
						TargetKind:    "route",
						Evidence: []Evidence{{
							Kind:      "js_router_mount",
							FilePath:  handler.FilePath,
							StartLine: handler.StartLine,
							EndLine:   handler.EndLine,
							Detail:    mount.Prefix + " + " + route.Receiver + "." + route.Route,
						}},
						WarningCodes: []string{},
					},
				})
			}
			for _, binding := range importBindingsByFile[file.Path][targetLocal] {
				routeFile, ok := resolveLocalImport(file.Path, binding.Module, knownFiles)
				if !ok || routeFile == file.Path {
					continue
				}
				routeReceiver := binding.Imported
				if binding.Namespace {
					routeReceiver = targetMember
				}
				if routeReceiver == "default" || routeReceiver == "" {
					if exported := defaultExportsByFile[routeFile]; exported != "" {
						routeReceiver = exported
					}
				}
				if routeReceiver == "" {
					routeReceiver = targetLocal
				}
				for _, route := range routesByFile[routeFile] {
					if route.Receiver != routeReceiver || route.Handler == "" {
						continue
					}
					handler, ok := resolveRouteHandlerSymbol(symbolsByFileAndName[routeFile], route.Handler)
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
							Reason:        "JavaScript router route resolved through local imported router mount",
							RelationScope: "external",
							Resolution:    "import_resolved",
							TargetKind:    "route",
							Evidence: []Evidence{{
								Kind:      "js_router_mount",
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
		for _, mount := range pluginMountsByFile[file.Path] {
			targetLocal, targetMember := splitJavaScriptMember(mount.Target)
			for _, binding := range importBindingsByFile[file.Path][targetLocal] {
				routeFile, ok := resolveLocalImport(file.Path, binding.Module, knownFiles)
				if !ok || routeFile == file.Path {
					continue
				}
				pluginName := binding.Imported
				if binding.Namespace {
					pluginName = targetMember
				}
				if pluginName == "default" || pluginName == "" {
					if exported := defaultExportsByFile[routeFile]; exported != "" {
						pluginName = exported
					}
				}
				if pluginName == "" {
					pluginName = targetLocal
				}
				for _, route := range pluginRoutesByFile[routeFile][pluginName] {
					if route.Handler == "" {
						continue
					}
					handler, ok := resolveRouteHandlerSymbol(symbolsByFileAndName[routeFile], route.Handler)
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
							Reason:        "Fastify plugin route resolved through local imported register prefix",
							RelationScope: "external",
							Resolution:    "import_resolved",
							TargetKind:    "route",
							Evidence: []Evidence{{
								Kind:      "fastify_plugin_register",
								FilePath:  handler.FilePath,
								StartLine: handler.StartLine,
								EndLine:   handler.EndLine,
								Detail:    mount.Prefix + " + " + pluginName + "." + route.Route,
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

func javascriptDefaultExportName(content string) string {
	for _, match := range regexp.MustCompile(`(?m)^\s*export\s+default\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*;?\s*$`).FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			return match[1]
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^\s*export\s*\{([^}]+)\}`).FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		for _, item := range strings.Split(match[1], ",") {
			imported, local := javascriptImportNames(item)
			if imported == "default" && local != "" {
				return local
			}
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^\s*module\.exports\s*=\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*;?\s*$`).FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			return match[1]
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^\s*exports\.default\s*=\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*;?\s*$`).FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func pythonIncludeRouterRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader, knownFiles map[string]bool) []expressRouteRelation {
	routesByFile := map[string][]pythonRouterRoute{}
	mountsByFile := map[string][]pythonRouterMount{}
	importsByFile := map[string]map[string][]pythonImportBinding{}
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
		importsByFile[file.Path] = importedPythonBindings(content)
	}
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if len(mountsByFile[file.Path]) == 0 {
			continue
		}
		for _, mount := range mountsByFile[file.Path] {
			for _, target := range pythonRouterTargetFiles(file.Path, mount.Target, importsByFile[file.Path], knownFiles) {
				for _, route := range routesByFile[target.File] {
					if route.Receiver != target.Receiver {
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

func pythonDirectRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
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
		typeHandlers := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				if _, exists := typeHandlers[symbol.Name]; !exists {
					typeHandlers[symbol.Name] = symbol
				}
				if symbol.QualifiedName != "" {
					if _, exists := typeHandlers[symbol.QualifiedName]; !exists {
						typeHandlers[symbol.QualifiedName] = symbol
					}
				}
				continue
			}
			if _, exists := handlers[symbol.Name]; !exists {
				handlers[symbol.Name] = symbol
			}
			if symbol.QualifiedName != "" {
				if _, exists := handlers[symbol.QualifiedName]; !exists {
					handlers[symbol.QualifiedName] = symbol
				}
			}
		}
		for _, registration := range pythonDirectRouteRegistrations(content) {
			handler, ok := resolveRouteHandlerSymbol(handlers, registration.Handler)
			if !ok && registration.AllowTypeHandler {
				handler, ok = resolveRouteHandlerSymbol(typeHandlers, registration.Handler)
			}
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
					Confidence:    0.82,
					Reason:        "Python direct route registration resolved to local handler",
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

func pythonDirectRouteRegistrations(content string) []goHTTPRouteRegistration {
	constants := staticStringConstants(content)
	handlerExpr := `[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?`
	handlerOrAsViewExpr := `(?:` + handlerExpr + `\.as_view\s*\([^)\n]*\)|` + handlerExpr + `)`
	addAPIRe := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.add_api_route\s*\(\s*([^,\n]+)\s*,\s*(` + handlerExpr + `)`)
	addURLRuleKeywordRe := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.add_url_rule\s*\(\s*([^,\n]+)[^\n)]*\bview_func\s*=\s*(` + handlerOrAsViewExpr + `)`)
	addURLRulePositionalRe := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.add_url_rule\s*\(\s*([^,\n]+)\s*,\s*[^,\n)]+\s*,\s*(` + handlerOrAsViewExpr + `)`)
	var registrations []goHTTPRouteRegistration
	add := func(routeExpr, handler, evidence string) {
		route, ok := staticRouteExpressionValue(routeExpr, constants)
		if !ok {
			return
		}
		handler = strings.TrimSpace(handler)
		if handler == "" {
			return
		}
		allowTypeHandler := false
		if className, ok := pythonAsViewClassName(handler); ok {
			handler = className
			allowTypeHandler = true
		}
		registrations = append(registrations, goHTTPRouteRegistration{
			Route:            route,
			Handler:          handler,
			EvidenceKind:     evidence,
			Detail:           route + " -> " + handler,
			AllowTypeHandler: allowTypeHandler,
		})
	}
	for _, match := range addAPIRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		add(match[1], match[2], "python_add_api_route")
	}
	for _, match := range addURLRuleKeywordRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		add(match[1], match[2], "python_flask_add_url_rule")
	}
	for _, match := range addURLRulePositionalRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		add(match[1], match[2], "python_flask_add_url_rule")
	}
	return registrations
}

func pythonAsViewClassName(handler string) (string, bool) {
	match := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\.as_view\s*\(`).FindStringSubmatch(handler)
	if len(match) != 2 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func pythonRouterTargetFiles(importingPath, target string, importsByName map[string][]pythonImportBinding, knownFiles map[string]bool) []pythonRouterTarget {
	seen := map[string]bool{importingPath: true}
	files := []pythonRouterTarget{{File: importingPath, Receiver: target}}
	for _, binding := range importsByName[target] {
		resolved, ok := resolveLocalImport(importingPath, binding.Module, knownFiles)
		if !ok || seen[resolved] {
			continue
		}
		seen[resolved] = true
		receiver := strings.TrimSpace(binding.Imported)
		if receiver == "" {
			receiver = target
		}
		files = append(files, pythonRouterTarget{File: resolved, Receiver: receiver})
	}
	return files
}

func importedPythonBindings(content string) map[string][]pythonImportBinding {
	imports := map[string][]pythonImportBinding{}
	add := func(local, module, imported string) {
		local = strings.TrimSpace(local)
		module = strings.TrimSpace(module)
		if local == "" || module == "" {
			return
		}
		imports[local] = append(imports[local], pythonImportBinding{
			Module:   module,
			Imported: strings.TrimSpace(imported),
		})
	}
	importRe := regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromRe := regexp.MustCompile(`^\s*from\s+(\.*(?:[A-Za-z_][A-Za-z0-9_\.]*)?)\s+import\s+(.+)$`)
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
				add(local, module, "")
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
				add(local, module, name)
			}
		}
	}
	return imports
}

func pythonRouterMounts(content string) []pythonRouterMount {
	includeRouterRe := regexp.MustCompile(`\.include_router\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	blueprintRe := regexp.MustCompile(`\.register_blueprint\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	prefixRe := regexp.MustCompile(`\bprefix\s*=\s*["']([^"']+)["']`)
	urlPrefixRe := regexp.MustCompile(`\burl_prefix\s*=\s*["']([^"']+)["']`)
	var mounts []pythonRouterMount
	for _, line := range strings.Split(content, "\n") {
		targetMatch := includeRouterRe.FindStringSubmatch(line)
		if len(targetMatch) == 2 {
			prefixMatch := prefixRe.FindStringSubmatch(line)
			if len(prefixMatch) == 2 && strings.HasPrefix(prefixMatch[1], "/") {
				mounts = append(mounts, pythonRouterMount{Prefix: prefixMatch[1], Target: targetMatch[1]})
			}
		}
		targetMatch = blueprintRe.FindStringSubmatch(line)
		if len(targetMatch) == 2 {
			prefix := "/"
			if prefixMatch := urlPrefixRe.FindStringSubmatch(line); len(prefixMatch) == 2 && strings.HasPrefix(prefixMatch[1], "/") {
				prefix = prefixMatch[1]
			}
			mounts = append(mounts, pythonRouterMount{Prefix: prefix, Target: targetMatch[1]})
		}
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
		route := normalizeRouteParamSyntax(match[2])
		key := match[1] + "\x00" + route
		if seen[key] {
			continue
		}
		seen[key] = true
		routes = append(routes, pythonRouteDecorator{Receiver: match[1], Route: route})
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

func phpAttributeRouteLiterals(content string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord) []string {
	if symbol.Kind != "method" && symbol.Kind != "function" {
		return nil
	}
	methodRoutes := phpRouteAttributeLiteralsAroundSymbol(content, symbol)
	if len(methodRoutes) == 0 {
		return nil
	}
	var classPrefixes []string
	if symbol.ContainerID != "" {
		if container, ok := symbolsByID[symbol.ContainerID]; ok && typeLikeKind(container.Kind) {
			classPrefixes = phpRouteAttributeLiteralsAroundSymbol(content, container)
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

func csharpAnnotationRouteLiterals(content string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord) []string {
	if symbol.Kind != "method" && symbol.Kind != "function" {
		return nil
	}
	methodRoutes := csharpRouteAnnotationLiteralsAroundSymbol(content, symbol, nil)
	if len(methodRoutes) == 0 {
		return nil
	}
	var classPrefixes []string
	if symbol.ContainerID != "" {
		if container, ok := symbolsByID[symbol.ContainerID]; ok && typeLikeKind(container.Kind) {
			tokens := map[string]string{"controller": csharpControllerRouteToken(container.Name)}
			classPrefixes = csharpRouteAnnotationLiteralsAroundSymbol(content, container, tokens)
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

func csharpMinimalAPIRouteRelations(files []FileRecord, recordsByFile map[string][]SymbolRecord, readContent contentReader) []expressRouteRelation {
	var relations []expressRouteRelation
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Path), ".cs") {
			continue
		}
		content, ok := readContent(file.Path)
		if !ok {
			continue
		}
		symbolsByName := map[string]SymbolRecord{}
		for _, symbol := range recordsByFile[file.Path] {
			if typeLikeKind(symbol.Kind) {
				continue
			}
			for _, name := range []string{symbol.Name, symbol.QualifiedName} {
				if name == "" {
					continue
				}
				if _, exists := symbolsByName[name]; !exists {
					symbolsByName[name] = symbol
				}
			}
			if _, simple, ok := strings.Cut(symbol.QualifiedName, "."); ok && simple != "" {
				if _, exists := symbolsByName[simple]; !exists {
					symbolsByName[simple] = symbol
				}
			}
		}
		for _, registration := range csharpMinimalAPIRouteRegistrations(content) {
			handler, ok := symbolsByName[registration.Handler]
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
					Confidence:    0.82,
					Reason:        "C# minimal API route registration resolved to local handler",
					RelationScope: "external",
					Resolution:    "exact",
					TargetKind:    "route",
					Evidence: []Evidence{{
						Kind:      "csharp_minimal_api_route",
						FilePath:  file.Path,
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

type csharpMinimalAPIRouteRegistration struct {
	Route   string
	Handler string
	Detail  string
}

func csharpMinimalAPIRouteRegistrations(content string) []csharpMinimalAPIRouteRegistration {
	constants := staticStringConstants(content)
	groupPrefixes := csharpMinimalAPIGroupPrefixes(content, constants)
	mapRouteRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*Map(?i:Get|Post|Put|Patch|Delete|Head|Options)\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\b`)
	chainedGroupRouteRe := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\s*\.\s*MapGroup\s*\(\s*([^,\n)]+)\s*\)\s*\.\s*Map(?i:Get|Post|Put|Patch|Delete|Head|Options)\s*\(\s*([^,\n]+)\s*,\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\b`)
	var registrations []csharpMinimalAPIRouteRegistration
	seen := map[string]bool{}
	add := func(route, handler string) {
		handler = strings.TrimSpace(handler)
		key := route + "\x00" + handler
		if seen[key] {
			return
		}
		seen[key] = true
		registrations = append(registrations, csharpMinimalAPIRouteRegistration{
			Route:   route,
			Handler: handler,
			Detail:  route + " -> " + handler,
		})
	}
	for _, match := range mapRouteRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 4 {
			continue
		}
		route, ok := staticRouteExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		if prefix, ok := groupPrefixes[match[1]]; ok {
			route = joinRoutePaths(prefix, route)
		}
		add(route, match[3])
	}
	for _, match := range chainedGroupRouteRe.FindAllStringSubmatch(content, -1) {
		if len(match) != 4 {
			continue
		}
		prefix, ok := staticRouteExpressionValue(match[1], constants)
		if !ok {
			continue
		}
		route, ok := staticRouteExpressionValue(match[2], constants)
		if !ok {
			continue
		}
		add(joinRoutePaths(prefix, route), match[3])
	}
	sort.Slice(registrations, func(i, j int) bool {
		if registrations[i].Route != registrations[j].Route {
			return registrations[i].Route < registrations[j].Route
		}
		return registrations[i].Handler < registrations[j].Handler
	})
	return registrations
}

func csharpMinimalAPIGroupPrefixes(content string, constants map[string]string) map[string]string {
	groupRe := regexp.MustCompile(`\b(?:var|[A-Za-z_][A-Za-z0-9_<>,.?]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*MapGroup\s*\(\s*([^,\n)]+)\s*\)`)
	groups := map[string]string{}
	for i := 0; i < 5; i++ {
		changed := false
		for _, match := range groupRe.FindAllStringSubmatch(content, -1) {
			if len(match) != 4 {
				continue
			}
			prefix, ok := staticRouteExpressionValue(match[3], constants)
			if !ok {
				continue
			}
			if base, ok := groups[match[2]]; ok {
				prefix = joinRoutePaths(base, prefix)
			}
			if groups[match[1]] == prefix {
				continue
			}
			groups[match[1]] = prefix
			changed = true
		}
		if !changed {
			break
		}
	}
	return groups
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

func nestJSAnnotationRouteLiterals(content string, symbol SymbolRecord, symbolsByID map[string]SymbolRecord) []string {
	if symbol.Kind != "method" && symbol.Kind != "function" {
		return nil
	}
	methodRoutes := nestJSMethodRouteLiteralsAroundSymbol(content, symbol)
	if len(methodRoutes) == 0 {
		return nil
	}
	var classPrefixes []string
	if symbol.ContainerID != "" {
		if container, ok := symbolsByID[symbol.ContainerID]; ok && typeLikeKind(container.Kind) {
			classPrefixes = nestJSControllerRouteLiteralsAroundSymbol(content, container)
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

func nestJSControllerRouteLiteralsAroundSymbol(content string, symbol SymbolRecord) []string {
	return nestJSRouteDecoratorLiteralsAroundSymbol(content, symbol, true)
}

func nestJSMethodRouteLiteralsAroundSymbol(content string, symbol SymbolRecord) []string {
	return nestJSRouteDecoratorLiteralsAroundSymbol(content, symbol, false)
}

func nestJSRouteDecoratorLiteralsAroundSymbol(content string, symbol SymbolRecord, controllerOnly bool) []string {
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	collect := func(line string) {
		for _, route := range nestJSRouteDecoratorLiterals(line, controllerOnly) {
			seen[route] = struct{}{}
		}
	}
	if index >= 0 && index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "@") {
		for i := index; i < len(lines) && i-index <= 8; i++ {
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

func nestJSRouteDecoratorLiterals(line string, controllerOnly bool) []string {
	decoratorRe := regexp.MustCompile("(?i)@(?:[A-Za-z_$][A-Za-z0-9_$]*\\.)?(Controller|Get|Post|Put|Patch|Delete|Head|Options|All)\\s*(?:\\((.*)\\))?")
	stringRe := regexp.MustCompile(`^\s*(?:"([^"]*)"|'([^']*)'|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
	pathPropertyRe := regexp.MustCompile(`(?i)\bpath\s*:\s*(?:"([^"]*)"|'([^']*)'|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
	var routes []string
	for _, match := range decoratorRe.FindAllStringSubmatch(line, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.ToLower(match[1])
		if controllerOnly && name != "controller" {
			continue
		}
		if !controllerOnly && name == "controller" {
			continue
		}
		arg := strings.TrimSpace(match[2])
		route := ""
		if parts := stringRe.FindStringSubmatch(arg); len(parts) == 4 {
			route = firstNonEmpty(parts[1], parts[2], parts[3])
		} else if parts := pathPropertyRe.FindStringSubmatch(arg); len(parts) == 4 {
			route = firstNonEmpty(parts[1], parts[2], parts[3])
		}
		if normalized := normalizeSlashRoute(route); normalized != "" {
			routes = append(routes, normalized)
			continue
		}
		if name != "controller" {
			routes = append(routes, "/")
		}
	}
	sort.Strings(routes)
	return routes
}

var (
	nestMessagePatternRe     = regexp.MustCompile("(?i)@(?:[A-Za-z_$][A-Za-z0-9_$]*\\.)?(MessagePattern|EventPattern)\\s*\\((.*)\\)")
	nestMessageStringRe      = regexp.MustCompile(`^\s*(?:"([^"]*)"|'([^']*)'|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
	nestMessageCmdPropertyRe = regexp.MustCompile(`(?i)\bcmd\s*:\s*(?:"([^"]*)"|'([^']*)'|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
)

// nestJSMessagePatternChannelsAroundSymbol finds the transport channels a NestJS
// microservice handler listens on: methods decorated with @MessagePattern(...) or
// @EventPattern(...) immediately above (or at) the symbol. Both a bare string
// literal (`@MessagePattern('sum')`) and the object command form
// (`@MessagePattern({ cmd: 'sum' })`) are recognized. Non-literal patterns
// (identifiers/enums) are skipped since they cannot be resolved statically.
func nestJSMessagePatternChannelsAroundSymbol(content string, symbol SymbolRecord) []string {
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	collect := func(line string) {
		for _, channel := range nestJSMessagePatternChannels(line) {
			seen[channel] = struct{}{}
		}
	}
	if index >= 0 && index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "@") {
		for i := index; i < len(lines) && i-index <= 8; i++ {
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

func nestJSMessagePatternChannels(line string) []string {
	var channels []string
	for _, match := range nestMessagePatternRe.FindAllStringSubmatch(line, -1) {
		if len(match) != 3 {
			continue
		}
		arg := strings.TrimSpace(match[2])
		channel := ""
		if parts := nestMessageStringRe.FindStringSubmatch(arg); len(parts) == 4 {
			channel = firstNonEmpty(parts[1], parts[2], parts[3])
		} else if parts := nestMessageCmdPropertyRe.FindStringSubmatch(arg); len(parts) == 4 {
			channel = firstNonEmpty(parts[1], parts[2], parts[3])
		}
		if channel != "" {
			channels = append(channels, channel)
		}
	}
	sort.Strings(channels)
	return channels
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
	collect := func(block string) {
		if !springRouteAnnotationLine(block) {
			return
		}
		for _, route := range springRouteAnnotationLiterals(block) {
			seen[route] = struct{}{}
		}
	}
	current := ""
	if index >= 0 && index < len(lines) {
		current = strings.TrimSpace(lines[index])
	}
	if strings.HasPrefix(current, "@") {
		collect(springAnnotationBlock(lines, index, minInt(len(lines)-1, index+8)))
		for i := index + 1; i < len(lines) && i-index <= 8; i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "@") {
				break
			}
			collect(springAnnotationBlock(lines, i, minInt(len(lines)-1, index+8)))
		}
	}
	for i := index - 1; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			if springAnnotationContinuationLine(line) {
				continue
			}
			break
		}
		collect(springAnnotationBlock(lines, i, index-1))
	}
	return sortedKeys(seen)
}

func springAnnotationBlock(lines []string, start, maxEnd int) string {
	if start < 0 || start >= len(lines) {
		return ""
	}
	if maxEnd >= len(lines) {
		maxEnd = len(lines) - 1
	}
	var block []string
	depth := 0
	for i := start; i <= maxEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if i > start && strings.HasPrefix(line, "@") && depth <= 0 {
			break
		}
		block = append(block, line)
		depth += strings.Count(line, "(")
		depth -= strings.Count(line, ")")
		if i > start && depth <= 0 {
			break
		}
		if i == start && !strings.Contains(line, "(") {
			break
		}
	}
	return strings.Join(block, " ")
}

func springAnnotationContinuationLine(line string) bool {
	line = strings.TrimSpace(line)
	return line == ")" || line == "})" || strings.HasPrefix(line, "{") || strings.HasPrefix(line, "}") || strings.HasPrefix(line, `"`) || strings.HasPrefix(line, `'`)
}

func springRouteAnnotationLiterals(block string) []string {
	if !springRouteAnnotationLine(block) {
		return nil
	}
	seen := map[string]struct{}{}
	for _, match := range routeLiteralRe.FindAllStringSubmatch(block, -1) {
		if len(match) == 2 && strings.HasPrefix(match[1], "/") {
			seen[normalizeRouteParamSyntax(match[1])] = struct{}{}
		}
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

func csharpRouteAnnotationLiteralsAroundSymbol(content string, symbol SymbolRecord, tokens map[string]string) []string {
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	if index >= 0 && index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "[") {
		for i := index; i < len(lines) && i-index <= 8; i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "[") {
				break
			}
			for _, route := range csharpRouteAnnotationLiterals(line, tokens) {
				seen[route] = struct{}{}
			}
		}
	}
	for i := index - 1; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "[") {
			break
		}
		for _, route := range csharpRouteAnnotationLiterals(line, tokens) {
			seen[route] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func csharpRouteAnnotationLiterals(line string, tokens map[string]string) []string {
	attributeRe := regexp.MustCompile(`\[(?i:(Route|HttpGet|HttpPost|HttpPut|HttpPatch|HttpDelete|HttpHead|HttpOptions))\s*(?:\(\s*(?:"([^"]*)"|'([^']*)'))?`)
	var routes []string
	for _, match := range attributeRe.FindAllStringSubmatch(line, -1) {
		if len(match) < 4 {
			continue
		}
		route := match[2]
		if route == "" {
			route = match[3]
		}
		route = csharpNormalizeRouteTemplate(route, tokens)
		if route == "" {
			if strings.EqualFold(match[1], "Route") {
				continue
			}
			route = "/"
		}
		routes = append(routes, route)
	}
	sort.Strings(routes)
	return routes
}

func csharpNormalizeRouteTemplate(route string, tokens map[string]string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return ""
	}
	route = strings.Trim(route, "/")
	if tokens != nil {
		for key, value := range tokens {
			route = strings.ReplaceAll(route, "["+key+"]", value)
			route = strings.ReplaceAll(route, "["+strings.ToUpper(key)+"]", value)
			route = strings.ReplaceAll(route, "["+strings.Title(key)+"]", value)
		}
	}
	if route == "" {
		return "/"
	}
	return "/" + route
}

func csharpControllerRouteToken(name string) string {
	name = strings.TrimSuffix(name, "Controller")
	if name == "" {
		return ""
	}
	return strings.ToLower(name)
}

func phpRouteAttributeLiteralsAroundSymbol(content string, symbol SymbolRecord) []string {
	lines := strings.Split(content, "\n")
	index := symbol.StartLine - 1
	if index >= len(lines) {
		index = len(lines) - 1
	}
	seen := map[string]struct{}{}
	if index >= 0 && index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "#[") {
		for i := index; i < len(lines) && i-index <= 8; i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "#[") {
				break
			}
			for _, route := range phpRouteAttributeLiterals(line) {
				seen[route] = struct{}{}
			}
		}
	}
	for i := index - 1; i >= 0 && index-i <= 8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#[") {
			break
		}
		for _, route := range phpRouteAttributeLiterals(line) {
			seen[route] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func phpRouteAttributeLiterals(line string) []string {
	attributeRe := regexp.MustCompile(`(?i)#\[\s*(?:[A-Za-z_\\][A-Za-z0-9_\\]*\\)?Route\s*\(\s*(?:"([^"]*)"|'([^']*)')`)
	var routes []string
	for _, match := range attributeRe.FindAllStringSubmatch(line, -1) {
		if len(match) < 3 {
			continue
		}
		route := match[1]
		if route == "" {
			route = match[2]
		}
		if normalized := normalizeSlashRoute(route); normalized != "" {
			routes = append(routes, normalized)
		}
	}
	sort.Strings(routes)
	return routes
}

func normalizeSlashRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return ""
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	return route
}

func shortQualifiedName(name string) string {
	name = strings.Trim(name, "\\")
	if idx := strings.LastIndex(name, "\\"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
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

func webRouteBoundary(path string) string {
	if route := nextRouteBoundary(path); route != "" {
		return route
	}
	if route := svelteKitRouteBoundary(path); route != "" {
		return route
	}
	if route := remixRouteBoundary(path); route != "" {
		return route
	}
	return ""
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

func svelteKitRouteBoundary(path string) string {
	slashPath := filepath.ToSlash(path)
	const rootMarker = "src/routes/"
	const nestedMarker = "/src/routes/"
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
	for _, suffix := range []string{
		"/+server.ts",
		"/+server.js",
		"/+server.tsx",
		"/+server.jsx",
		"/+page.svelte",
		"/+page.ts",
		"/+page.js",
		"/+page.tsx",
		"/+page.jsx",
	} {
		if strings.HasSuffix(relative, suffix) {
			relative = strings.TrimSuffix(relative, suffix)
			return svelteKitRouteFromRelative(relative)
		}
	}
	return ""
}

func svelteKitRouteFromRelative(relative string) string {
	var segments []string
	for _, segment := range strings.Split(relative, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" || strings.HasPrefix(segment, "(") && strings.HasSuffix(segment, ")") {
			continue
		}
		if strings.HasPrefix(segment, "@") {
			continue
		}
		if strings.HasPrefix(segment, "[[") && strings.HasSuffix(segment, "]]") {
			segment = strings.TrimSuffix(strings.TrimPrefix(segment, "[["), "]]")
			if strings.HasPrefix(segment, "...") {
				segment = "{..." + strings.TrimPrefix(segment, "...") + "}"
			} else {
				segment = "{" + segment + "}"
			}
		} else {
			segment = nextRouteSegment(segment)
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

func remixRouteBoundary(path string) string {
	slashPath := filepath.ToSlash(path)
	const rootMarker = "app/routes/"
	const nestedMarker = "/app/routes/"
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
	for _, suffix := range []string{".tsx", ".ts", ".jsx", ".js"} {
		if strings.HasSuffix(relative, suffix) {
			relative = strings.TrimSuffix(relative, suffix)
			return remixRouteFromRelative(relative)
		}
	}
	return ""
}

func remixRouteFromRelative(relative string) string {
	relative = strings.Trim(relative, "/")
	if relative == "" || relative == "root" {
		return ""
	}
	relative = strings.TrimSuffix(relative, "/route")
	var segments []string
	for _, part := range strings.Split(relative, "/") {
		for _, segment := range strings.Split(part, ".") {
			segment = strings.TrimSpace(segment)
			if segment == "" || segment == "_index" || strings.HasPrefix(segment, "_") {
				continue
			}
			if strings.HasPrefix(segment, "$") {
				name := strings.TrimPrefix(segment, "$")
				if name == "" {
					continue
				}
				if name == "*" {
					segments = append(segments, "{...splat}")
				} else {
					segments = append(segments, "{"+name+"}")
				}
				continue
			}
			segments = append(segments, segment)
		}
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

func completenessLevel(failures, files, parsedFiles, symbols int) string {
	switch {
	case files == 0:
		// Genuinely empty repo/scope — nothing to parse, so "ok" (not "unsafe":
		// there is no missing coverage to warn about).
		return "ok"
	case parsedFiles*2 < files:
		// A majority of the discovered files were never parsed — likely an
		// unsupported-language or mis-scoped run; the graph is missing most of
		// the repo. Loud "unsafe" so the caller does not trust a partial graph.
		return "unsafe"
	case parsedFiles > 0 && symbols == 0:
		// Files were parsed but zero symbols came out — the graph is empty and
		// unusable even though no hard parse failure occurred.
		return "degraded"
	case failures == 0:
		return "ok"
	case failures*4 > files:
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
	case ".f90", ".for", ".fsharp":
		return "unsupported source extension " + filepath.Ext(path)
	default:
		return ""
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
