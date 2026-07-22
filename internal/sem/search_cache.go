package sem

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const searchSnapshotCacheVersion = "search-snapshot-v4"

type cachedSearchSnapshot struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Commit          string           `json:"commit"`
	Tree            string           `json:"tree"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Snapshot        ProviderSnapshot `json:"snapshot"`
	// FileRecord.Lines and SymbolRecord.Local are intentionally absent from the
	// public wire format, but relation resolution consumes them. Preserve those
	// internal fields so a complete preindex can derive an exact selective view
	// without reparsing source files.
	FileLines      map[string]int `json:"file_lines,omitempty"`
	LocalSymbolIDs []string       `json:"local_symbol_ids,omitempty"`
}

// loadOrBuildSearchGraphSnapshot keeps the query's candidate file scope from
// shrinking a durable repository graph. If an exact complete committed-tree
// preindex exists, search loads it directly and performs no query-time
// relation re-resolution. Generic provider callers retain the exact OnlyFiles
// projection semantics of loadOrBuildSearchSnapshot below.
func loadOrBuildSearchGraphSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	if !disableCache && cacheDir != "" && !options.Worktree && len(options.OnlyFiles) > 0 {
		if snapshot, cacheHit, err := loadCachedCompleteSearchSnapshot(
			ctx, repo, providerVersion, options, cacheDir,
		); err != nil || cacheHit {
			return snapshot, cacheHit, err
		}
	}
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache)
}

func loadCachedCompleteSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
) (ProviderSnapshot, bool, error) {
	if cacheDir == "" || options.Worktree {
		return ProviderSnapshot{}, false, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)
	if headErr != nil {
		return ProviderSnapshot{}, false, nil
	}
	fullOptions := options
	fullOptions.OnlyFiles = nil
	if fullOptions.Profile == "" {
		fullOptions.Profile = ProfileFull
	}
	fullKey, keyErr := searchSnapshotKey(absRepo, providerVersion, tree, fullOptions)
	if keyErr != nil {
		return ProviderSnapshot{}, false, keyErr
	}
	fullPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")
	cached, readErr := readSearchSnapshot(fullPath)
	if readErr != nil || !validCachedSearchSnapshot(cached, providerVersion, tree, fullOptions) {
		return ProviderSnapshot{}, false, nil
	}
	// The cache key is tree-only: a hit may have been built for a different
	// commit that shares this tree. The parsed graph is exactly correct, but
	// commit provenance must reflect the HEAD we are serving right now.
	cached = restampCachedSearchSnapshotCommit(cached, commit)
	return cached.Snapshot, true, nil
}

func loadOrBuildSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	if disableCache || cacheDir == "" || options.Worktree {
		snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)
	if headErr != nil {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	key, err := searchSnapshotKey(absRepo, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	if cached, err := readSearchSnapshot(path); err == nil && validCachedSearchSnapshot(cached, providerVersion, tree, options) {
		// See loadCachedCompleteSearchSnapshot: tree-only keying means this hit
		// may belong to a different commit that shares the tree. Re-stamp before
		// handing it back so no caller ever reports a stale commit.
		cached = restampCachedSearchSnapshotCommit(cached, commit)
		return cached.Snapshot, true, nil
	}
	// A complete committed-tree snapshot is query independent and can serve a
	// selective search without rebuilding the same tree for every query. Keep
	// the selective view so cache presence cannot change retrieval semantics.
	if len(options.OnlyFiles) > 0 {
		fullOptions := options
		fullOptions.OnlyFiles = nil
		fullKey, keyErr := searchSnapshotKey(absRepo, providerVersion, tree, fullOptions)
		if keyErr != nil {
			return ProviderSnapshot{}, false, keyErr
		}
		fullPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")
		if cached, readErr := readSearchSnapshot(fullPath); readErr == nil && validCachedSearchSnapshot(cached, providerVersion, tree, fullOptions) {
			cached = restampCachedSearchSnapshotCommit(cached, commit)
			selective, deriveErr := selectiveSearchSnapshotFromFull(ctx, absRepo, providerVersion, options, cached.Snapshot)
			if deriveErr == nil {
				// Persisting the exact selective view makes repeated identical queries
				// a direct cache hit. As with ordinary search caching, this is best effort.
				_ = writeSearchSnapshot(path, newCachedSearchSnapshot(providerVersion, commit, tree, options, selective))
				return selective, true, nil
			}
			// Provenance or internal-metadata mismatches make the complete cache
			// unsuitable for derivation. Fall through to the ordinary selective
			// build instead of letting an optional cache break retrieval.
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if snapshot.Header.Tree != tree {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"repository HEAD changed while building search snapshot: got commit %q tree %q, started at commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	// The tree we started at is still what got built even if a same-tree
	// commit (e.g. an empty commit) landed concurrently. Re-stamp so the
	// returned snapshot reports the commit this call is serving, not whatever
	// HEAD happened to be mid-build.
	snapshot.Header.Commit = commit
	cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
	// Cache persistence is best effort. Retrieval correctness never depends on
	// a writable cache directory.
	_ = writeSearchSnapshot(path, cache)
	return snapshot, false, nil
}

// PreindexProviderSnapshot builds or loads the complete snapshot for exactly
// the repository's current HEAD tree. Unlike query-time selective indexing,
// this cache entry is query independent and can be prepared before an agent
// task begins. Worktree snapshots are deliberately rejected because dirty
// state cannot be represented by a durable tree-keyed cache safely.
func PreindexProviderSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
) (ProviderSnapshot, bool, error) {
	if options.Worktree {
		return ProviderSnapshot{}, false, errors.New("preindex requires a committed HEAD snapshot")
	}
	if cacheDir == "" {
		return ProviderSnapshot{}, false, errors.New("preindex requires a cache directory")
	}
	options.Worktree = false
	options.OnlyFiles = nil
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return ProviderSnapshot{}, false, fmt.Errorf("resolve committed HEAD for preindex: %w", err)
	}
	snapshot, cacheHit, err := loadOrBuildSearchSnapshot(ctx, absRepo, providerVersion, options, cacheDir, false)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if snapshot.Header.Tree != tree {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"preindex snapshot provenance mismatch: got commit %q tree %q, want commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	// Query-time caching is deliberately best effort, but an explicit preindex
	// command promises a durable artifact. Verify that the entry exists and, if
	// the best-effort write failed, retry while surfacing the persistence error.
	key, err := searchSnapshotKey(absRepo, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	persisted, readErr := readSearchSnapshot(path)
	if readErr != nil || !validCachedSearchSnapshot(persisted, providerVersion, tree, options) {
		cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
		if err := writeSearchSnapshot(path, cache); err != nil {
			return ProviderSnapshot{}, false, fmt.Errorf("persist preindex snapshot: %w", err)
		}
	}
	return snapshot, cacheHit, nil
}

func newCachedSearchSnapshot(providerVersion, commit, tree string, options ProviderSnapshotOptions, snapshot ProviderSnapshot) cachedSearchSnapshot {
	cache := cachedSearchSnapshot{
		CacheVersion:    searchSnapshotCacheVersion,
		ProviderVersion: providerVersion,
		Commit:          commit,
		Tree:            tree,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
		Snapshot:        snapshot,
	}
	for _, file := range snapshot.Files {
		if file.Lines == 0 {
			continue
		}
		if cache.FileLines == nil {
			cache.FileLines = make(map[string]int)
		}
		cache.FileLines[file.ID] = file.Lines
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.Local {
			cache.LocalSymbolIDs = append(cache.LocalSymbolIDs, symbol.ID)
		}
	}
	return cache
}

func restoreCachedSearchInternals(cache *cachedSearchSnapshot) {
	for index := range cache.Snapshot.Files {
		cache.Snapshot.Files[index].Lines = cache.FileLines[cache.Snapshot.Files[index].ID]
	}
	localIDs := make(map[string]bool, len(cache.LocalSymbolIDs))
	for _, id := range cache.LocalSymbolIDs {
		localIDs[id] = true
	}
	for index := range cache.Snapshot.Symbols {
		cache.Snapshot.Symbols[index].Local = localIDs[cache.Snapshot.Symbols[index].ID]
	}
}

// selectiveSearchSnapshotFromFull derives the same graph that a fresh
// OnlyFiles build would produce. It reuses cached parse output, but deliberately
// reruns relation resolution against only the selected symbols: simply dropping
// cross-boundary edges from a complete graph is wrong because an OnlyFiles build
// externalizes those targets and records different resolution metadata.
func selectiveSearchSnapshotFromFull(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	full ProviderSnapshot,
) (ProviderSnapshot, error) {
	sc, err := prepareSource(ctx, repo, options)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	if sc.close != nil {
		defer sc.close()
	}
	// Tree (not commit) determines whether the cached full snapshot is a valid
	// derivation source: two different commits sharing a tree parse identically.
	if sc.tree != full.Header.Tree || sc.key != full.Header.RepoKey {
		return ProviderSnapshot{}, fmt.Errorf(
			"cached full snapshot provenance mismatch: got repo %q commit %q tree %q, want repo %q commit %q tree %q",
			full.Header.RepoKey, full.Header.Commit, full.Header.Tree, sc.key, sc.commit, sc.tree,
		)
	}

	spec := resolveProfile(options.Profile)
	selective := ProviderSnapshot{Header: leanHeader(sc, providerVersion, spec)}
	allowedFiles := make(map[string]bool, len(sc.paths))
	for _, filePath := range sc.paths {
		allowedFiles[filepath.ToSlash(filepath.Clean(filePath))] = true
	}
	for _, file := range full.Files {
		if allowedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			selective.Files = append(selective.Files, file)
		}
	}
	for _, symbol := range full.Symbols {
		if allowedFiles[filepath.ToSlash(filepath.Clean(symbol.FilePath))] {
			selective.Symbols = append(selective.Symbols, symbol)
		}
	}

	recordsByFile := make(map[string][]SymbolRecord)
	structuralByFile := make(map[string][]structuralSymbol)
	for _, symbol := range selective.Symbols {
		recordsByFile[symbol.FilePath] = append(recordsByFile[symbol.FilePath], symbol)
	}
	if spec.name == ProfileSyntaxOnly {
		for filePath, symbols := range recordsByFile {
			structuralByFile[filePath] = compactStructuralSymbols(symbols)
		}
	} else {
		for filePath, symbols := range recordsByFile {
			recordsByFile[filePath] = retainedSymbolsForProfile(symbols, spec)
		}
	}
	precomputedImports := make(map[string][]string)
	if spec.name != ProfileSyntaxOnly {
		for _, file := range selective.Files {
			if !skipFastProfilePerSymbolScan(spec, file.Language) {
				continue
			}
			if content, ok := sc.read(file.Path); ok {
				precomputedImports[file.Path] = importsFor(file.Path, content)
			}
		}
	}

	seenRelations := make(map[uint64]struct{})
	externalsByID := make(map[string]ExternalRecord)
	relationsByType := make(map[string]int)
	var symbolsByID map[string]SymbolRecord
	var filesByID map[string]FileRecord
	if spec.includeEvidence {
		symbolsByID, filesByID = recordIndexes(selective.Files, recordsByFile)
	}
	emitRelation := func(relation RelationRecord) {
		if !spec.emits(relation.Type) {
			return
		}
		if relation.Type == "CALLS" && spec.callResolution == "shallow" && relation.Resolution != "exact" {
			return
		}
		if !spec.includeEvidence {
			relation.Evidence = nil
		}
		if relation.WarningCodes == nil {
			relation.WarningCodes = []string{}
		}
		key := relationDedupKey(relation)
		if _, seen := seenRelations[key]; seen {
			return
		}
		seenRelations[key] = struct{}{}
		for _, id := range []string{relation.FromID, relation.ToID} {
			if strings.HasPrefix(id, "external:") {
				mergeExternalRecord(externalsByID, externalRecordFor(relation, id, symbolsByID, filesByID))
			}
		}
		relationsByType[relation.Type]++
		selective.Relations = append(selective.Relations, relation)
	}
	if spec.name == ProfileSyntaxOnly {
		emitStructuralRelationsCompact(sc.key, selective.Files, structuralByFile, emitRelation)
	} else {
		forEachRelation(sc.key, selective.Files, recordsByFile, sc.read, precomputedImports, spec, func() bool {
			return ctx.Err() != nil
		}, emitRelation)
		if spec.emits("FILE_CHANGES_WITH") {
			for _, relation := range fileChangesWithRelations(ctx, sc.absRepo, sc.commit, sc.key, selective.Files) {
				if ctx.Err() != nil {
					break
				}
				emitRelation(relation)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return ProviderSnapshot{}, err
	}

	externalIDs := make([]string, 0, len(externalsByID))
	for id := range externalsByID {
		externalIDs = append(externalIDs, id)
	}
	sort.Strings(externalIDs)
	for _, id := range externalIDs {
		selective.Externals = append(selective.Externals, externalsByID[id])
	}
	sort.Slice(selective.Relations, func(i, j int) bool {
		left := selective.Relations[i].Type + selective.Relations[i].FromID + selective.Relations[i].ToID
		right := selective.Relations[j].Type + selective.Relations[j].FromID + selective.Relations[j].ToID
		return left < right
	})

	warnings := sc.warnings
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	failures := filterSearchPartialFailures(full.Header.PartialFailures, allowedFiles)
	languageSet := make(map[string]struct{})
	completenessLanguages := make(map[string]LanguageCompleteness)
	for _, file := range selective.Files {
		languageSet[file.Language] = struct{}{}
		completeness := completenessLanguages[file.Language]
		completeness.Files++
		completenessLanguages[file.Language] = completeness
	}
	for _, symbol := range selective.Symbols {
		completeness := completenessLanguages[symbol.Language]
		completeness.Symbols++
		completenessLanguages[symbol.Language] = completeness
	}
	unparsedFiles := make(map[string]bool)
	for _, failure := range failures {
		if failure.Code == "E_FILE_TOO_LARGE" || failure.Code == "E_MINIFIED" {
			unparsedFiles[filepath.ToSlash(filepath.Clean(failure.FilePath))] = true
		}
	}
	parsedFiles := 0
	for _, file := range selective.Files {
		if !unparsedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			parsedFiles++
		}
	}
	selective.Header.Languages = sortedKeys(languageSet)
	selective.Header.LanguageTiers = languageTiers(languageSet)
	selective.Header.Warnings = warnings
	selective.Header.PartialFailures = failures
	selective.Header.Stats = ProviderStats{
		Files:             len(selective.Files),
		ParsedFiles:       parsedFiles,
		Symbols:           len(selective.Symbols),
		Relations:         len(selective.Relations),
		PartialFailures:   len(failures),
		CompletenessLevel: completenessLevel(len(failures), len(selective.Files)),
	}
	selective.Header.Completeness = CompletenessReport{
		Languages: completenessLanguages,
		Relations: relationsByType,
	}
	return selective, nil
}

func filterSearchPartialFailures(failures []PartialFailure, allowedFiles map[string]bool) []PartialFailure {
	filtered := make([]PartialFailure, 0, len(failures))
	for _, failure := range failures {
		if failure.FilePath == "" || allowedFiles[filepath.ToSlash(filepath.Clean(failure.FilePath))] {
			filtered = append(filtered, failure)
		}
	}
	return filtered
}

// LoadOrBuildProviderSnapshot reuses the tree-keyed, option-keyed compressed
// provider snapshot cache shared with search. Worktree snapshots always bypass
// the cache so dirty edits cannot be hidden by committed-tree state.
func LoadOrBuildProviderSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache)
}

// searchSnapshotKey is deliberately tree-only, not commit-keyed: parsing is a
// pure function of tree content, so any commit whose tree matches an existing
// entry can reuse it (e.g. --allow-empty commits, amends, rebases that don't
// touch content). Commit is provenance metadata carried on the cached value
// and re-stamped to the serving HEAD on load; it never influences the key.
// This is scoped to the parsed graph itself: a full-profile snapshot also
// embeds FILE_CHANGES_WITH co-change relations derived by walking recent git
// history (see fileChangesWithRelations), so a same-tree hit after a rebase
// can serve co-change edges computed against the prior history. That is
// accepted because those edges are heuristic and confidence-scored, not
// exact facts about the tree.
func searchSnapshotKey(absRepo, providerVersion, tree string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(searchSnapshotCacheVersion)
	writePart(absRepo)
	writePart(providerVersion)
	writePart(tree)
	writePart(string(options.Profile))
	writePart(fmt.Sprintf("%d", options.MaxParseBytes))
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writePart("only-files")
	for _, filePath := range onlyFiles {
		writePart(filepath.ToSlash(filepath.Clean(filePath)))
	}
	for groupIndex, group := range [][]string{options.IgnoreFiles, options.IncludeFiles} {
		writePart(fmt.Sprintf("path-group-%d", groupIndex))
		// Preserve caller order: ignore matching is last-rule-wins, including
		// across repeatable ignore/include files within each group.
		for _, path := range group {
			resolved := path
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(absRepo, resolved)
			}
			writePart(filepath.Clean(resolved))
			content, err := os.ReadFile(resolved)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					writePart("missing")
					continue
				}
				return "", err
			}
			_, _ = hash.Write(content)
			writePart("")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// validCachedSearchSnapshot deliberately does not compare commit: the cache is
// tree-keyed, so an entry built at a different commit sharing this tree is a
// valid hit. Callers that serve a cached snapshot re-stamp the commit to the
// serving HEAD via restampCachedSearchSnapshotCommit before returning it;
// other call sites (e.g. PreindexProviderSnapshot's persisted-entry check)
// use this function only as a persistence check and never hand the cached
// value back to a caller, so they have no re-stamping to do.
func validCachedSearchSnapshot(cache cachedSearchSnapshot, providerVersion, tree string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == searchSnapshotCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.Tree == tree &&
		cache.Snapshot.Header.Provider == ProviderName &&
		cache.Snapshot.Header.Profile == string(options.Profile)
}

// restampCachedSearchSnapshotCommit rewrites a loaded cache entry's commit
// provenance to the commit we are actually serving. Tree determines the
// parsed graph, so a same-tree cache hit from a different (empty, amended,
// rebased) commit is exactly correct content-wise; commit is provenance
// metadata layered on top and must reflect the serving HEAD, never the
// possibly-stale commit recorded when the entry was built. This is not just
// provenance cosmetics: query time also reads Header.Commit back out as the
// git treeish for content reads (see openSearchContentReader in search.go),
// so serving a stale commit here could point those reads at a dangling or
// wrong revision.
func restampCachedSearchSnapshotCommit(cache cachedSearchSnapshot, commit string) cachedSearchSnapshot {
	cache.Commit = commit
	cache.Snapshot.Header.Commit = commit
	return cache
}

func readSearchSnapshot(path string) (cachedSearchSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer reader.Close()
	var cache cachedSearchSnapshot
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&cache); err != nil {
		return cachedSearchSnapshot{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cachedSearchSnapshot{}, errors.New("search snapshot cache has trailing data")
	}
	restoreCachedSearchInternals(&cache)
	return cache, nil
}

func writeSearchSnapshot(path string, cache cachedSearchSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".snapshot-*.json.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writer := gzip.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(cache); err != nil {
		_ = writer.Close()
		_ = temporary.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
