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
	SchemaVersion         = "1.0"
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

type ProviderRecord struct {
	RecordType string `json:"record_type,omitempty"`
}

type SnapshotHeader struct {
	SchemaVersion   string            `json:"schema_version"`
	Provider        string            `json:"provider"`
	ProviderVersion string            `json:"provider_version"`
	RepoRoot        string            `json:"repo_root"`
	RepoKey         string            `json:"repo_key"`
	Commit          string            `json:"commit"`
	Tree            string            `json:"tree"`
	Languages       []string          `json:"languages"`
	Capabilities    []string          `json:"capabilities"`
	Warnings        []ProviderWarning `json:"warnings"`
	PartialFailures []PartialFailure  `json:"partial_failures"`
	Stats           ProviderStats     `json:"stats"`
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
	RecordType string `json:"record_type"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
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
	RecordType   string   `json:"record_type"`
	FromID       string   `json:"from_id"`
	ToID         string   `json:"to_id"`
	Type         string   `json:"type"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
	WarningCodes []string `json:"warning_codes"`
}

type CapabilityReport struct {
	SchemaVersion                   string            `json:"schema_version"`
	Provider                        string            `json:"provider"`
	SupportedFileExtensions         []string          `json:"supported_file_extensions"`
	SupportedLanguages              []string          `json:"supported_languages"`
	ParserVersions                  map[string]string `json:"parser_versions"`
	SupportedRelationTypes          []string          `json:"supported_relation_types"`
	UnsupportedButDetectedLanguages []string          `json:"unsupported_but_detected_language_hints"`
	OptionalLocalOnlyFeatures       map[string]bool   `json:"optional_local_only_features"`
	FeaturesRequiringNetworkAccess  map[string]bool   `json:"features_requiring_network_access"`
}

type ProviderSnapshot struct {
	Header    SnapshotHeader
	Files     []FileRecord
	Externals []ExternalRecord
	Symbols   []SymbolRecord
	Relations []RelationRecord
}

type ProviderSnapshotOptions struct {
	NoNetwork bool
	Worktree  bool
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
		ParserVersions: map[string]string{
			"go-tree-sitter": "github.com/smacker/go-tree-sitter",
		},
		SupportedRelationTypes: append([]string(nil), relationTypes...),
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
	paths, contentByFile, err := snapshotSource(ctx, absRepo, useHead)
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
		symbols = append(symbols, fileSymbols...)
		recordsByFile[path] = fileSymbols
	}

	relations := buildRelations(key, files, recordsByFile, contentByFile)
	externals := externalRecords(relations)
	languages := sortedKeys(languageSet)
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	if failures == nil {
		failures = []PartialFailure{}
	}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: providerVersion,
		RepoRoot:        absRepo,
		RepoKey:         key,
		Commit:          commit,
		Tree:            tree,
		Languages:       languages,
		Capabilities:    []string{"ndjson", "stable-symbol-id-v1", "local-only", "partial-failures"},
		Warnings:        warnings,
		PartialFailures: failures,
		Stats: ProviderStats{
			Files:             len(files),
			ParsedFiles:       len(recordsByFile),
			Symbols:           len(symbols),
			Relations:         len(relations),
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(len(failures), len(files)),
		},
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
	seenDuplicateIDs := map[string]int{}
	for _, entity := range entities {
		baseCounts[symbolID(repoKey, language, path, entity.Kind, entity.Name)]++
	}
	var symbols []SymbolRecord
	for _, entity := range entities {
		qualified := entity.Name
		id := symbolID(repoKey, language, path, entity.Kind, qualified)
		if baseCounts[id] > 1 {
			id = symbolID(repoKey, language, path, entity.Kind, duplicateSymbolName(qualified, entity))
			seenDuplicateIDs[id]++
			if seenDuplicateIDs[id] > 1 {
				id = fmt.Sprintf("%s#%d", id, seenDuplicateIDs[id])
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

func duplicateSymbolName(qualified string, entity Entity) string {
	return fmt.Sprintf("%s#L%d-%d", qualified, entity.StartLine, entity.EndLine)
}

func buildRelations(repoKey string, files []FileRecord, recordsByFile map[string][]SymbolRecord, contentByFile map[string]string) []RelationRecord {
	var relations []RelationRecord
	symbolsByShortName := map[string][]SymbolRecord{}
	for _, records := range recordsByFile {
		for _, symbol := range records {
			relations = append(relations, RelationRecord{
				RecordType:   "relation",
				FromID:       fileID(repoKey, symbol.FilePath),
				ToID:         symbol.ID,
				Type:         "DEFINES",
				Confidence:   1,
				Reason:       "symbol parsed from file",
				WarningCodes: []string{},
			})
			if symbol.ContainerID != "" {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       symbol.ContainerID,
					ToID:         symbol.ID,
					Type:         "CONTAINS",
					Confidence:   1,
					Reason:       "symbol qualified name is nested in container",
					WarningCodes: []string{},
				})
			}
			symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
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
				RecordType:   "relation",
				FromID:       fromID,
				ToID:         externalID("import", imported),
				Type:         "IMPORTS",
				Confidence:   0.8,
				Reason:       "import declaration matched by language-specific scanner",
				WarningCodes: []string{},
			})
		}
		for _, from := range recordsByFile[file.Path] {
			block := symbolBlockFromLines(lines, from)
			for name := range identifiersIn(block) {
				if name == from.Name {
					continue
				}
				candidates := symbolsByShortName[name]
				for _, to := range candidates {
					if to.ID == from.ID {
						continue
					}
					relations = append(relations, RelationRecord{
						RecordType:   "relation",
						FromID:       from.ID,
						ToID:         to.ID,
						Type:         "CALLS",
						Confidence:   0.62,
						Reason:       "identifier reference inside symbol body matches known symbol name",
						WarningCodes: []string{},
					})
				}
			}
			for _, route := range routeLiterals(block) {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       from.ID,
					ToID:         externalID("route", route),
					Type:         "HANDLES_ROUTE",
					Confidence:   0.7,
					Reason:       "route-like string literal found inside handler symbol",
					WarningCodes: []string{},
				})
			}
			if looksLikeToolHandler(from, block) {
				relations = append(relations, RelationRecord{
					RecordType:   "relation",
					FromID:       from.ID,
					ToID:         externalID("tool", from.QualifiedName),
					Type:         "HANDLES_TOOL",
					Confidence:   0.58,
					Reason:       "symbol name or body contains tool handler vocabulary",
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

func externalRecords(relations []RelationRecord) []ExternalRecord {
	seen := map[string]ExternalRecord{}
	for _, relation := range relations {
		for _, id := range []string{relation.FromID, relation.ToID} {
			if !strings.HasPrefix(id, "external:") {
				continue
			}
			kind, value := externalParts(id)
			seen[id] = ExternalRecord{
				RecordType: "external",
				ID:         id,
				Kind:       kind,
				Value:      value,
			}
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

func externalParts(id string) (string, string) {
	rest := strings.TrimPrefix(id, "external:")
	kind, value, ok := strings.Cut(rest, ":")
	if !ok {
		return "unknown", rest
	}
	return kind, value
}

func snapshotSource(ctx context.Context, repo string, useHead bool) ([]string, map[string]string, error) {
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
	paths, err := workingTreeFiles(repo)
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

func workingTreeFiles(repo string) ([]string, error) {
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
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
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

func unsupportedLanguageHint(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dart", ".erb", ".f90", ".for", ".fs", ".fsharp", ".m", ".mm", ".pl", ".pm", ".svelte", ".vue", ".zig":
		return "unsupported source extension " + filepath.Ext(path)
	default:
		return ""
	}
}
