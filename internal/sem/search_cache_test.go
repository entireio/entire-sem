package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFullPreindexSelectiveSnapshotMatchesUncachedAcrossFileBoundary(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/caller.ts", `import { Helper, helperFunction } from "./helper";

export class Caller extends Helper {
  run(): string {
    function localFormatter(value: string): string { return value.trim(); }
    return localFormatter(helperFunction());
  }
}
`)
	write(t, repo, "src/helper.ts", `export class Helper {}
export function helperFunction(): string { return "helper"; }
`)
	write(t, repo, "src/unrelated.ts", `export function unrelated(): boolean { return true; }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	} else if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}

	selectiveOptions := ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"src/caller.ts"},
	}
	cached, cacheHit, err := LoadOrBuildProviderSnapshot(
		t.Context(), repo, "test-version", selectiveOptions, cacheDir, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from complete preindex")
	}
	uncached, _, err := LoadOrBuildProviderSnapshot(
		t.Context(), repo, "test-version", selectiveOptions, cacheDir, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, uncached) {
		t.Fatalf("cached selective snapshot differs from uncached OnlyFiles build:\ncached=%#v\nuncached=%#v", cached, uncached)
	}

	assertSelectiveSnapshotAccounting(t, cached)
	if !hasExternalID(cached.Externals, "external:import:./helper") {
		t.Fatalf("cross-boundary import was not externalized: %#v", cached.Externals)
	}
	if !hasExternalID(cached.Externals, "external:type:Helper") {
		t.Fatalf("cross-boundary superclass was not externalized: %#v", cached.Externals)
	}
	for _, relation := range cached.Relations {
		if strings.Contains(relation.ToID, ":src/helper.ts:") {
			t.Fatalf("selective snapshot retained a relation to an unselected symbol: %#v", relation)
		}
	}
}

func TestFullPreindexSelectiveSnapshotFiltersFailureStats(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "selected.go", "package sample\nfunc Selected() bool { return true }\n")
	write(t, repo, "too_large.go", "package sample\n// "+strings.Repeat("oversized ", 80)+"\n")
	write(t, repo, "not_selected.go", "package sample\nfunc NotSelected() bool { return false }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	baseOptions := ProviderSnapshotOptions{Profile: ProfileFull, MaxParseBytes: 128}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", baseOptions, cacheDir); err != nil {
		t.Fatal(err)
	}
	selectiveOptions := baseOptions
	selectiveOptions.OnlyFiles = []string{"selected.go", "too_large.go"}
	cached, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from complete preindex")
	}
	uncached, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, uncached) {
		t.Fatalf("cached selective failure accounting differs from uncached build:\ncached=%#v\nuncached=%#v", cached.Header, uncached.Header)
	}
	assertSelectiveSnapshotAccounting(t, cached)
	if cached.Header.Stats.Files != 2 || cached.Header.Stats.ParsedFiles != 1 || cached.Header.Stats.PartialFailures != 1 {
		t.Fatalf("unexpected selective failure stats: %#v", cached.Header.Stats)
	}
	if len(cached.Header.PartialFailures) != 1 || cached.Header.PartialFailures[0].FilePath != "too_large.go" {
		t.Fatalf("unexpected selective failures: %#v", cached.Header.PartialFailures)
	}
}

func assertSelectiveSnapshotAccounting(t *testing.T, snapshot ProviderSnapshot) {
	t.Helper()
	if snapshot.Header.Stats.Files != len(snapshot.Files) ||
		snapshot.Header.Stats.Symbols != len(snapshot.Symbols) ||
		snapshot.Header.Stats.Relations != len(snapshot.Relations) ||
		snapshot.Header.Stats.PartialFailures != len(snapshot.Header.PartialFailures) {
		t.Fatalf("header stats do not describe selective records: stats=%#v files=%d symbols=%d relations=%d failures=%d",
			snapshot.Header.Stats,
			len(snapshot.Files),
			len(snapshot.Symbols),
			len(snapshot.Relations),
			len(snapshot.Header.PartialFailures),
		)
	}
	relationCount := 0
	for _, count := range snapshot.Header.Completeness.Relations {
		relationCount += count
	}
	if relationCount != len(snapshot.Relations) {
		t.Fatalf("relation completeness total = %d, want %d: %#v", relationCount, len(snapshot.Relations), snapshot.Header.Completeness.Relations)
	}
	fileCount, symbolCount := 0, 0
	for _, completeness := range snapshot.Header.Completeness.Languages {
		fileCount += completeness.Files
		symbolCount += completeness.Symbols
	}
	if fileCount != len(snapshot.Files) || symbolCount != len(snapshot.Symbols) {
		t.Fatalf("language completeness does not describe selective records: %#v", snapshot.Header.Completeness.Languages)
	}
}

func hasExternalID(externals []ExternalRecord, id string) bool {
	for _, external := range externals {
		if external.ID == id {
			return true
		}
	}
	return false
}

func TestPreindexProviderSnapshotServesSelectiveSearch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for index := 0; index < 12; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf(
			"package noise\nfunc Noise%d() int { return %d }\n", index, index,
		))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the query-independent preindex request.
func NeedleTarget() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	preindexed, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit the cache")
	}
	if len(preindexed.Files) != 13 {
		t.Fatalf("preindex files = %d, want 13", len(preindexed.Files))
	}

	options := SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 1,
		CacheDir:        cacheDir,
	}
	selectiveProviderOptions := ProviderSnapshotOptions{
		Profile:   ProfileSyntaxOnly,
		OnlyFiles: []string{"target/needle.go"},
	}
	selectiveKey, err := searchSnapshotKey(repo, "test-version", preindexed.Header.Tree, selectiveProviderOptions)
	if err != nil {
		t.Fatal(err)
	}
	selectivePath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, selectiveKey+".json.gz")
	cached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Stats.IndexCacheHit {
		t.Fatal("selective search did not reuse the complete preindex cache")
	}
	if cached.Stats.FilesIndexed != 1 {
		t.Fatalf("selective search indexed %d files, want 1", cached.Stats.FilesIndexed)
	}
	if cached.Stats.SymbolsConsidered != len(preindexed.Symbols) {
		t.Fatalf("search considered %d symbols, want complete cached graph with %d", cached.Stats.SymbolsConsidered, len(preindexed.Symbols))
	}
	if cached.Stats.QueryFilesRead == 0 || cached.Stats.QueryBytesRead == 0 || cached.Stats.QueryFilesRead >= len(preindexed.Files) {
		t.Fatalf("query content reads were not bounded to candidate scope: %#v", cached.Stats)
	}
	if _, statErr := os.Stat(selectivePath); !os.IsNotExist(statErr) {
		t.Fatalf("warm search materialized a query-specific graph cache at %s: %v", selectivePath, statErr)
	}
	if len(cached.Results) == 0 || cached.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("preindexed search lost target: %#v", cached.Results)
	}

	options.DisableCache = true
	uncached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached.Results, uncached.Results) {
		t.Fatalf("full-cache selective view changed retrieval: cached=%#v uncached=%#v", cached.Results, uncached.Results)
	}

	_, secondHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !secondHit {
		t.Fatal("second preindex did not reuse the complete cache")
	}
}

func TestIndexAllFilesSearchWritesCanonicalFullSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	_, err := SearchRepository(t.Context(), repo, "test-version", "validate token", SearchOptions{
		Profile:       ProfileSyntaxOnly,
		IndexAllFiles: true,
		CacheDir:      cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("index-all-files search did not populate the canonical full-snapshot cache")
	}
}

func TestWarmNoHitSearchPreservesCachedGraphHealth(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "healthy.go", "package sample\nfunc Healthy() bool { return true }\n")
	write(t, repo, "oversized.go", "package sample\n// "+strings.Repeat("oversized ", 80)+"\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	const maxParseBytes = 128
	preindexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(preindexed.Header.PartialFailures) != 1 {
		t.Fatalf("preindex failures = %#v", preindexed.Header.PartialFailures)
	}

	response, err := SearchRepository(t.Context(), repo, "test-version", "definitely absent retrieval phrase", SearchOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes, CacheDir: cacheDir, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 || !response.Stats.IndexCacheHit {
		t.Fatalf("warm no-hit response = %#v", response)
	}
	if !reflect.DeepEqual(response.PartialFailures, preindexed.Header.PartialFailures) ||
		!reflect.DeepEqual(response.Completeness, preindexed.Header.Completeness) {
		t.Fatalf("no-hit search lost cached graph health: response=%#v preindex=%#v", response, preindexed.Header)
	}
	if response.Stats.QueryFilesRead != 0 || response.Stats.QueryBytesRead != 0 {
		t.Fatalf("no-hit search read repository content: %#v", response.Stats)
	}
	if response.Stats.PreselectionBackend != "git-tree-grep" || response.Stats.PreselectionPasses != 1 ||
		response.Stats.PreselectionFilesExamined != response.Stats.FilesScanned {
		t.Fatalf("no-hit Git full-tree work was hidden by zero blob hydration: %#v", response.Stats)
	}
}

func TestWarmCommittedSearchMatchesExhaustiveResultsWithoutFullContentRescan(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/policy.ts", "export const durableCacheRefreshPolicy = 'eager'\n")
	write(t, repo, "src/consumer.ts", "export function applyRefreshPolicy() { install(durableCacheRefreshPolicy) }\n")
	write(t, repo, "tests/policy.test.ts", "test('refresh policy', () => expect(durableCacheRefreshPolicy).toBeTruthy())\n")
	write(t, repo, "docs/policy.md", "# Durable cache refresh policy\n")
	for index := 0; index < 80; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%03d.ts", index), fmt.Sprintf(
			"export function unrelated%d() { return %d }\n", index, index,
		))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	query := "durable cache refresh policy consumer"
	warm, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
		Profile: ProfileFull, TopK: 10, IndexAllFiles: true, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	exhaustive, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
		Worktree: true, Profile: ProfileFull, TopK: 10, IndexAllFiles: true, DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := func(results []SearchResult) []string {
		out := make([]string, len(results))
		for index, result := range results {
			out[index] = fmt.Sprintf("%s:%d:%d:%s", result.FilePath, result.StartLine, result.EndLine, result.SymbolID)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(identity(warm.Results), identity(exhaustive.Results)) {
		t.Fatalf("optimized results differ from exhaustive retrieval:\nwarm=%#v\nexhaustive=%#v", warm.Results, exhaustive.Results)
	}
	if len(warm.Results) == 0 || warm.Results[0].FilePath != "src/policy.ts" {
		t.Fatalf("query-aware artifact prior did not rank implementation first: %#v", warm.Results)
	}
	if !warm.Stats.IndexCacheHit || warm.Stats.FilesContentRead != 0 {
		t.Fatalf("warm committed search did not use the canonical cache/tree grep: %#v", warm.Stats)
	}
	if warm.Stats.QueryFilesRead >= exhaustive.Stats.QueryFilesRead || warm.Stats.QueryFilesRead > 4 {
		t.Fatalf("warm query reads were not bounded: warm=%#v exhaustive=%#v", warm.Stats, exhaustive.Stats)
	}
	if warm.Stats.UsageFilesRead != 0 || warm.Stats.UsageBytesRead != 0 {
		t.Fatalf("identifier usage cache hits were double-counted as physical reads: %#v", warm.Stats)
	}
	if warm.Stats.UsagePreselectionBackend != "git-tree-grep" || warm.Stats.UsagePreselectionPasses != 1 ||
		warm.Stats.UsagePreselectionFilesExamined != warm.Stats.FilesScanned {
		t.Fatalf("identifier-usage Git scan was not represented honestly: %#v", warm.Stats)
	}
}

func TestWarmCommittedSearchKeepsLexicalMatchesFromPartialFailureFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "large.go", "package sample\n// HiddenLargeNeedle "+strings.Repeat("oversized payload ", 40)+"\n")
	write(t, repo, "healthy.go", "package sample\nfunc Healthy() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	const maxParseBytes = 128
	preindexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(preindexed.Header.PartialFailures) != 1 || preindexed.Header.PartialFailures[0].FilePath != "large.go" {
		t.Fatalf("preindex did not record the oversized file: %#v", preindexed.Header.PartialFailures)
	}
	options := SearchOptions{Profile: ProfileFull, MaxParseBytes: maxParseBytes, TopK: 5}
	warmOptions := options
	warmOptions.CacheDir = cacheDir
	warm, err := SearchRepository(t.Context(), repo, "test-version", "HiddenLargeNeedle", warmOptions)
	if err != nil {
		t.Fatal(err)
	}
	exhaustiveOptions := options
	exhaustiveOptions.Worktree = true
	exhaustiveOptions.IndexAllFiles = true
	exhaustiveOptions.DisableCache = true
	exhaustive, err := SearchRepository(t.Context(), repo, "test-version", "HiddenLargeNeedle", exhaustiveOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(warm.Results) == 0 || warm.Results[0].FilePath != "large.go" ||
		len(exhaustive.Results) == 0 || exhaustive.Results[0].FilePath != "large.go" {
		t.Fatalf("partial-failure lexical result was dropped: warm=%#v exhaustive=%#v", warm.Results, exhaustive.Results)
	}
	if warm.Results[0].Snippet != exhaustive.Results[0].Snippet {
		t.Fatalf("optimized partial-failure result differs from exhaustive retrieval: warm=%#v exhaustive=%#v", warm.Results[0], exhaustive.Results[0])
	}
}

func TestCommittedGitPreselectionMatchesExhaustiveUnicodeLowering(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "unicode.go", "package sample\n// KernelNeedle is deliberately spelled with Unicode Kelvin sign.\nfunc KernelNeedle() {}\n// İssueNeedle uses Turkish dotted capital I.\nfunc İssueNeedle() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "unicode case-fold fixture")
	if !strings.Contains(strings.ToLower("KernelNeedle"), "kernelneedle") {
		t.Fatal("Go Unicode lowering no longer maps Kelvin sign to ASCII k")
	}
	if !strings.Contains(strings.ToLower("İssueNeedle"), "issueneedle") {
		t.Fatal("Go Unicode lowering no longer maps dotted capital I to ASCII i")
	}

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"kernelneedle", "issueneedle"} {
		warm, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
			Profile: ProfileFull, TopK: 5, CacheDir: cacheDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		exhaustive, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
			Worktree: true, Profile: ProfileFull, TopK: 5, IndexAllFiles: true, DisableCache: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(exhaustive.Results) == 0 || exhaustive.Results[0].FilePath != "unicode.go" {
			t.Fatalf("exhaustive Unicode fixture did not match %q: %#v", query, exhaustive.Results)
		}
		if !reflect.DeepEqual(warm.Results, exhaustive.Results) {
			t.Fatalf("committed Git preselection changed Unicode-fold retrieval for %q:\nwarm=%#v\nexhaustive=%#v", query, warm.Results, exhaustive.Results)
		}
	}
}

func TestPreindexProviderSnapshotRejectsWorktreeAndMissingCache(t *testing.T) {
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{Worktree: true}, t.TempDir()); err == nil {
		t.Fatal("expected worktree preindex to fail")
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{}, ""); err == nil {
		t.Fatal("expected preindex without a cache directory to fail")
	}
}

func TestPreindexProviderSnapshotSurfacesPersistenceFailure(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cacheDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err == nil {
		t.Fatal("expected an unwritable cache path to fail preindex")
	}
}

func TestPreindexProviderSnapshotReusesSameTreeAcrossCommits(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	first := make(map[Profile]ProviderSnapshot)
	for _, profile := range []Profile{ProfileFast, ProfileFull} {
		snapshot, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile: profile,
		}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if cacheHit {
			t.Fatalf("first %s preindex unexpectedly hit cache", profile)
		}
		first[profile] = snapshot
	}
	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")
	if newHead == first[ProfileFull].Header.Commit {
		t.Fatal("test setup did not advance HEAD to a new commit")
	}
	for _, profile := range []Profile{ProfileFast, ProfileFull} {
		second, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile: profile,
		}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if !cacheHit {
			t.Fatalf("same-tree but different-commit %s snapshot did not reuse cache", profile)
		}
		if second.Header.Tree != first[profile].Header.Tree {
			t.Fatalf("%s snapshot tree changed across an empty commit: first=%s second=%s",
				profile, first[profile].Header.Tree, second.Header.Tree,
			)
		}
		// The parsed graph is reused from the old commit's cache entry, but the
		// commit we report must be the one we are actually serving right now,
		// not the stale commit recorded when the cache entry was built.
		if second.Header.Commit != newHead {
			t.Fatalf("%s snapshot commit was not re-stamped to serving HEAD: got %s, want %s",
				profile, second.Header.Commit, newHead,
			)
		}
	}
}

func TestSearchReusesSameTreeCacheAcrossCommitsAndReportsCurrentHEAD(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	options := SearchOptions{Profile: ProfileFull, TopK: 5, CacheDir: cacheDir}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}

	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")

	response, err := SearchRepository(t.Context(), repo, "test-version", "ValidateToken", options)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Stats.IndexCacheHit {
		t.Fatal("search after an empty commit did not reuse the same-tree cache")
	}
	if response.Commit != newHead {
		t.Fatalf("search response commit = %q, want current HEAD %q", response.Commit, newHead)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "ValidateToken" {
		t.Fatalf("search lost target after cache reuse across commits: %#v", response.Results)
	}
}

func TestSearchSnapshotCacheKeyPreservesIgnoreFileOrder(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".ignore-target", "target.go\n")
	write(t, repo, ".reinclude-target", "!target.go\n")
	write(t, repo, "target.go", "package target\nfunc Target() bool { return true }\n")
	write(t, repo, "control.go", "package target\nfunc Control() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	includeTarget := ProviderSnapshotOptions{
		Profile:     ProfileSyntaxOnly,
		IgnoreFiles: []string{".ignore-target", ".reinclude-target"},
	}
	first, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", includeTarget, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first ordered-ignore snapshot unexpectedly hit cache")
	}
	if !snapshotHasSymbol(first, "Target") {
		t.Fatalf("later re-inclusion rule did not restore target: %#v", first.Symbols)
	}

	ignoreTarget := includeTarget
	ignoreTarget.IgnoreFiles = []string{".reinclude-target", ".ignore-target"}
	includeKey, err := searchSnapshotKey(repo, "test-version", first.Header.Tree, includeTarget)
	if err != nil {
		t.Fatal(err)
	}
	ignoreKey, err := searchSnapshotKey(repo, "test-version", first.Header.Tree, ignoreTarget)
	if err != nil {
		t.Fatal(err)
	}
	if includeKey == ignoreKey {
		t.Fatalf("reversed order-sensitive ignore files produced the same cache key %q", includeKey)
	}

	second, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", ignoreTarget, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("reversed ignore-file order reused the incompatible cached snapshot")
	}
	if snapshotHasSymbol(second, "Target") {
		t.Fatalf("later ignore rule did not exclude target: %#v", second.Symbols)
	}
	if !snapshotHasSymbol(second, "Control") {
		t.Fatalf("reversed-rule snapshot lost control symbol: %#v", second.Symbols)
	}
}

// TestOnlyFilesDerivationReStampsCommitAfterSameTreeCommit pins the re-stamp
// on the OnlyFiles-derivation branch of loadOrBuildSearchSnapshot: a selective
// snapshot derived from a complete same-tree cache entry built at an older
// commit must report the commit it is actually serving right now, not the
// stale commit recorded when the complete entry was written.
func TestOnlyFilesDerivationReStampsCommitAfterSameTreeCommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "selected.go", "package sample\nfunc Selected() bool { return true }\n")
	write(t, repo, "other.go", "package sample\nfunc Other() bool { return false }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	full, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}

	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")
	if newHead == full.Header.Commit {
		t.Fatal("test setup did not advance HEAD to a new commit")
	}

	selective, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"selected.go"},
	}, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from the complete same-tree preindex")
	}
	if selective.Header.Tree != full.Header.Tree {
		t.Fatalf("derived selective snapshot tree changed across an empty commit: got %s, want %s",
			selective.Header.Tree, full.Header.Tree,
		)
	}
	if selective.Header.Commit != newHead {
		t.Fatalf("derived selective snapshot commit was not re-stamped to serving HEAD: got %s, want %s",
			selective.Header.Commit, newHead,
		)
	}
}
