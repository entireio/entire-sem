package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestPreindexProviderSnapshotReusesTreeAcrossCommitsWithCurrentProvenance(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	first, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}
	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	second, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("same-tree commit did not reuse preindex")
	}
	if second.Header.Tree != first.Header.Tree || second.Header.Commit == first.Header.Commit {
		t.Fatalf("cache provenance was not refreshed: first=%s/%s second=%s/%s",
			first.Header.Commit, first.Header.Tree, second.Header.Commit, second.Header.Tree,
		)
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
