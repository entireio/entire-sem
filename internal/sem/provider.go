package sem

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
)

var relationTypes = []string{
	"DEFINES",
	"CONTAINS",
	"IMPORTS",
	"CALLS",
	"HANDLES_ROUTE",
	"HANDLES_TOOL",
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
	Warnings         []ProviderWarning  `json:"warnings"`
	PartialFailures  []PartialFailure   `json:"partial_failures"`
	Stats            ProviderStats      `json:"stats"`
	Completeness     CompletenessReport `json:"completeness"`
	BenchmarkProfile string             `json:"benchmark_profile,omitempty"`
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
		HeuristicRelationTypes:          []string{"HANDLES_ROUTE", "HANDLES_TOOL"},
		OptionalLocalOnlyFeatures: map[string]bool{
			"stable_symbol_ids": true,
			"semantic_diff":     true,
			"ndjson_snapshot":   true,
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
		sort.Strings(types)
		support[spec.language] = types
	}
	return support
}

func BuildProviderSnapshot(ctx context.Context, repo, providerVersion string) (ProviderSnapshot, error) {
	return BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, ProviderSnapshotOptions{})
}

func BuildProviderSnapshotWithOptions(ctx context.Context, repo, providerVersion string, options ProviderSnapshotOptions) (ProviderSnapshot, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	key := repoKey(ctx, absRepo)
	var warnings []ProviderWarning
	commit, commitErr := gitutil.RevParse(ctx, absRepo, "HEAD")
	tree, treeErr := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")

	// The provider is local-only. NoNetwork is accepted to make that contract
	// explicit for callers that enforce no-egress provider execution.
	_ = options.NoNetwork
	useHead := !options.Worktree && commitErr == nil && treeErr == nil
	paths, contentByFile, err := snapshotSource(ctx, absRepo, useHead, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return ProviderSnapshot{}, err
	}
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

	parser := TreeSitterParser{}
	languageSet := map[string]struct{}{}
	var files []FileRecord
	var symbols []SymbolRecord
	var failures []PartialFailure
	recordsByFile := map[string][]SymbolRecord{}

	for _, path := range paths {
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
		content, ok := contentByFile[path]
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
		contentBytes := []byte(content)
		files = append(files, FileRecord{
			RecordType: "file",
			ID:         fileID(key, path),
			Path:       path,
			Blob:       contentHash(contentBytes),
			Language:   language,
			Bytes:      len(contentBytes),
		})
		fileSymbols := entitySymbols(key, path, language, entities)
		fileSymbols = append(fileSymbols, syntheticBoundarySymbols(key, path, language, content, fileSymbols)...)
		symbols = append(symbols, fileSymbols...)
		recordsByFile[path] = fileSymbols
	}

	relations := buildRelations(key, files, recordsByFile, contentByFile)
	externals := externalRecords(relations, files, recordsByFile)
	languages := sortedKeys(languageSet)
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	if failures == nil {
		failures = []PartialFailure{}
	}
	header := SnapshotHeader{
		SchemaVersion:    SchemaVersion,
		Provider:         ProviderName,
		ProviderVersion:  providerVersion,
		RepoRoot:         absRepo,
		RepoKey:          key,
		Commit:           commit,
		Tree:             tree,
		Languages:        languages,
		Capabilities:     []string{"ndjson", "stable-symbol-id-v1", "local-only", "partial-failures"},
		SchemaFeatures:   append([]string(nil), schemaFeatures...),
		LanguageVersions: parserVersions(),
		Warnings:         warnings,
		PartialFailures:  failures,
		Stats: ProviderStats{
			Files:             len(files),
			ParsedFiles:       len(recordsByFile),
			Symbols:           len(symbols),
			Relations:         len(relations),
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(len(failures), len(files)),
		},
		Completeness: buildCompleteness(files, symbols, relations),
	}
	return ProviderSnapshot{Header: header, Files: files, Externals: externals, Symbols: symbols, Relations: relations}, nil
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

func buildRelations(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, contentByFile map[string]string) []RelationRecord {
	var relations []RelationRecord
	symbolsByShortName := map[string][]SymbolRecord{}
	symbolsByFile := map[string][]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			relations = append(relations, RelationRecord{
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
				relations = append(relations, RelationRecord{
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
			if symbol.Kind == "tool" && symbol.ContainerID != "" {
				relations = append(relations, RelationRecord{
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
		}
	}
	handledRoutes := map[string]struct{}{}
	for _, file := range files {
		if route := nextRouteBoundary(file.Path); route != "" {
			handledRoutes[route] = struct{}{}
		}
	}

	for _, file := range files {
		content, ok := contentByFile[file.Path]
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")
		fromID := fileID(repoKey, file.Path)
		for _, imported := range importsFor(file.Path, content) {
			relations = append(relations, RelationRecord{
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
				WarningCodes: []string{},
			})
		}
		importsByName := importedNamesFor(file.Path, content)
		for _, from := range recordsByFile[file.Path] {
			block := symbolBlockFromLines(lines, from)
			for name := range callLikeIdentifiers(block) {
				if name == from.Name {
					continue
				}
				for _, to := range resolveCallTargets(name, from, symbolsByShortName[name], symbolsByFile[file.Path], importsByName) {
					relations = append(relations, RelationRecord{
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
				relations = append(relations, RelationRecord{
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
		}
	}

	sort.Slice(relations, func(i, j int) bool {
		left := relations[i].Type + relations[i].FromID + relations[i].ToID
		right := relations[j].Type + relations[j].FromID + relations[j].ToID
		return left < right
	})
	return dedupeRelations(relations)
}

func externalRecords(relations []RelationRecord, files []FileRecord, recordsByFile map[string][]SymbolRecord) []ExternalRecord {
	seen := map[string]ExternalRecord{}
	filesByID := map[string]FileRecord{}
	for _, file := range files {
		filesByID[file.ID] = file
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			symbolsByID[symbol.ID] = symbol
		}
	}
	for _, relation := range relations {
		for _, id := range []string{relation.FromID, relation.ToID} {
			if !strings.HasPrefix(id, "external:") {
				continue
			}
			kind, value := externalParts(id)
			record := ExternalRecord{
				RecordType: "external",
				ID:         id,
				Kind:       kind,
				Value:      value,
				External:   true,
			}
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
			if existing, ok := seen[id]; ok && existing.FilePath != "" {
				continue
			}
			seen[id] = record
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ExternalRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, seen[id])
	}
	return out
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

func snapshotSource(ctx context.Context, repo string, useHead bool, ignoreFiles, includeFiles []string) ([]string, map[string]string, error) {
	if useHead {
		paths, err := gitutil.ListFiles(ctx, repo, "HEAD")
		if err != nil {
			return nil, nil, err
		}
		contentByFile := map[string]string{}
		for _, path := range paths {
			if !Supported(path) {
				continue
			}
			content, ok, err := gitutil.ShowFile(ctx, repo, "HEAD", path)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				contentByFile[path] = content
			}
		}
		return paths, contentByFile, nil
	}
	ignores, err := loadWorktreeIgnoreMatcher(repo, ignoreFiles, includeFiles)
	if err != nil {
		return nil, nil, err
	}
	paths, err := workingTreeFiles(repo, ignores)
	if err != nil {
		return nil, nil, err
	}
	contentByFile := map[string]string{}
	for _, path := range paths {
		if !Supported(path) {
			continue
		}
		full := filepath.Join(repo, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		contentByFile[path] = string(content)
	}
	return paths, contentByFile, nil
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

func routeLiterals(content string) []string {
	re := regexp.MustCompile(`["'](/[A-Za-z0-9_\-/{}/:.]*)["']`)
	seen := map[string]struct{}{}
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			seen[match[1]] = struct{}{}
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

// buildCompleteness aggregates parse/index coverage by language (file and
// symbol counts) and by emitted relation type. The maps are nil-safe and
// serialize with sorted keys, keeping golden output deterministic.
func buildCompleteness(files []FileRecord, symbols []SymbolRecord, relations []RelationRecord) CompletenessReport {
	byLanguage := map[string]LanguageCompleteness{}
	for _, file := range files {
		if file.Language == "" {
			continue
		}
		entry := byLanguage[file.Language]
		entry.Files++
		byLanguage[file.Language] = entry
	}
	for _, symbol := range symbols {
		if symbol.Language == "" {
			continue
		}
		entry := byLanguage[symbol.Language]
		entry.Symbols++
		byLanguage[symbol.Language] = entry
	}
	byRelationType := map[string]int{}
	for _, relation := range relations {
		byRelationType[relation.Type]++
	}
	return CompletenessReport{Languages: byLanguage, Relations: byRelationType}
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

func unsupportedLanguageHint(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dart", ".erb", ".f90", ".for", ".fs", ".fsharp", ".m", ".mm", ".pl", ".pm", ".svelte", ".vue", ".zig":
		return "unsupported source extension " + filepath.Ext(path)
	default:
		return ""
	}
}
