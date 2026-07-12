package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchRepositoryRanksExactSymbol(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "config/service.go", `package config

type ServiceConfig struct { Name string }

func NewServiceConfig(name string) ServiceConfig {
	return ServiceConfig{Name: name}
}
`)
	write(t, repo, "docs/example.go", `package docs

// This example discusses constructing a service configuration.
func Example() {}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "NewServiceConfig", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("search returned no results")
	}
	first := response.Results[0]
	if first.FilePath != "config/service.go" || first.SymbolName != "NewServiceConfig" {
		t.Fatalf("first result = %#v", first)
	}
	if !containsString(first.Signals, "exact-symbol") {
		t.Fatalf("exact symbol signal missing: %#v", first.Signals)
	}
}

func TestSearchRepositorySupportsPunctuatedLanguageQuery(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "native/bridge.cpp", "// C++ bridge implementation\nint bridge() { return 1; }\n")
	write(t, repo, "docs/unrelated.txt", "plain language documentation\n")

	response, err := SearchRepository(t.Context(), repo, "test", "C++", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].FilePath != "native/bridge.cpp" {
		t.Fatalf("C++ search results = %#v", response.Results)
	}
}

func TestSearchRepositoryFindsConceptualBodyText(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "encoding/serializer.go", `package encoding

// MarshalCompact emits minified serialization output for network transport.
func MarshalCompact(value any) []byte { return nil }

// MarshalIndented provides pretty printing for human-readable output.
func MarshalIndented(value any) []byte { return nil }
`)
	write(t, repo, "transport/socket.go", `package transport

// Send writes bytes to a socket.
func Send(value []byte) error { return nil }
`)

	response, err := SearchRepository(t.Context(), repo, "test", "minified and pretty printing serialization output", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              5,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("results = %#v stats = %#v", response.Results, response.Stats)
	}
	seen := map[string]bool{}
	for _, result := range response.Results {
		if result.FilePath == "encoding/serializer.go" {
			seen[result.SymbolName] = true
		}
	}
	if !seen["MarshalCompact"] || !seen["MarshalIndented"] {
		t.Fatalf("conceptual query did not preserve both relevant regions: %#v", response.Results)
	}
}

func TestSearchRepositoryPreservesDistinctRegionsInOneFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "timing/wheel.go", `package timing

// NewTimingWheel creates the production timing wheel.
func NewTimingWheel(interval int) *Wheel {
	return newTimingWheelWithClock(interval, systemClock{})
}

// newTimingWheelWithClock creates the timing wheel with an injected clock.
func newTimingWheelWithClock(interval int, clock Clock) *Wheel {
	return &Wheel{}
}

type Wheel struct{}
type Clock interface{}
type systemClock struct{}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "create timing wheel with injected clock", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              5,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]SearchResult{}
	for _, result := range response.Results {
		seen[result.SymbolName] = result
	}
	constructor, constructorOK := seen["NewTimingWheel"]
	helper, helperOK := seen["newTimingWheelWithClock"]
	if !constructorOK || !helperOK {
		t.Fatalf("missing distinct same-file regions: %#v", response.Results)
	}
	if constructor.StartLine == helper.StartLine {
		t.Fatalf("regions collapsed to one location: %#v", response.Results)
	}
}

func TestSearchRepositoryExpandsMorphologicalIssueTerms(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "reader/collection.go", `package reader

// initializeCollectionReader selects the collection implementation.
func initializeCollectionReader(kind string) *CollectionReader { return &CollectionReader{} }

// readObject reads values from the input stream.
func (r *CollectionReader) readObject(input []byte) any { return nil }

type CollectionReader struct{}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "collection reader initialization", SearchOptions{
		Worktree:          true,
		Profile:           ProfileSyntaxOnly,
		TopK:              10,
		MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seenInitializer := false
	for _, result := range response.Results {
		if result.SymbolName == "initializeCollectionReader" {
			seenInitializer = true
		}
	}
	if !seenInitializer {
		t.Fatalf("initialization did not expand to initialize: %#v", response.Results)
	}
}

func TestSearchRepositoryExpandsSemanticNeighbor(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth/auth.go", `package auth

// Authenticate creates a user session after successful login.
func Authenticate(raw string) bool {
	return checkSignature(raw)
}

func checkSignature(raw string) bool {
	return len(raw) > 4
}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "authenticate user session login", SearchOptions{
		Worktree: true,
		Profile:  ProfileFast,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var helper SearchResult
	for _, result := range response.Results {
		if result.SymbolName == "checkSignature" {
			helper = result
			break
		}
	}
	if helper.SymbolName == "" {
		t.Fatalf("graph neighbor missing: %#v", response.Results)
	}
	foundGraphSignal := false
	for _, signal := range helper.Signals {
		if strings.HasPrefix(signal, "graph:") {
			foundGraphSignal = true
		}
	}
	if !foundGraphSignal {
		t.Fatalf("helper was not identified through graph expansion: %#v", helper)
	}
}

func TestSearchRepositoryRejectsStopWordsOnly(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package main\n")
	_, err := SearchRepository(t.Context(), repo, "test", "the and with", SearchOptions{Worktree: true})
	if err == nil {
		t.Fatal("expected an error for a stop-word-only query")
	}
}

func TestSearchRepositoryPreservesHeadProvenanceWhenPreselectionIsEmpty(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	write(t, repo, "alpha.go", "package source\nfunc Alpha() {}\n")
	write(t, repo, "beta.go", "package source\nfunc Beta() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	response, err := SearchRepository(t.Context(), repo, "test", "unmatched-provenance-query", SearchOptions{
		Profile:         ProfileSyntaxOnly,
		MaxIndexedFiles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 || response.Stats.FilesIndexed != 0 {
		t.Fatalf("unexpected results: results=%#v stats=%#v", response.Results, response.Stats)
	}
	if response.Commit != rev(t, repo, "HEAD") || response.Tree != rev(t, repo, "HEAD^{tree}") {
		t.Fatalf("provenance does not identify HEAD: commit=%q tree=%q", response.Commit, response.Tree)
	}
	if response.Warnings == nil || len(response.Warnings) != 0 {
		t.Fatalf("warnings = %#v", response.Warnings)
	}
}

func TestSearchRepositoryPreservesSourceWarningsWhenPreselectionIsEmpty(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		repo := t.TempDir()
		git(t, repo, "init")
		git(t, repo, "config", "user.name", "Entire Sem Test")
		git(t, repo, "config", "user.email", "sem@example.com")
		write(t, repo, "alpha.go", "package source\nfunc Alpha() {}\n")
		write(t, repo, "beta.go", "package source\nfunc Beta() {}\n")
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "initial")

		response, err := SearchRepository(t.Context(), repo, "test", "unmatched-worktree-query", SearchOptions{
			Worktree:        true,
			Profile:         ProfileSyntaxOnly,
			MaxIndexedFiles: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.Commit == "" || response.Tree == "" {
			t.Fatalf("worktree response lost baseline provenance: %#v", response)
		}
		if len(response.Warnings) != 1 || response.Warnings[0].Code != "W_WORKTREE_SNAPSHOT" {
			t.Fatalf("warnings = %#v", response.Warnings)
		}
	})

	t.Run("no git head", func(t *testing.T) {
		repo := t.TempDir()
		write(t, repo, "alpha.go", "package source\nfunc Alpha() {}\n")
		write(t, repo, "beta.go", "package source\nfunc Beta() {}\n")

		response, err := SearchRepository(t.Context(), repo, "test", "unmatched-fallback-query", SearchOptions{
			Profile:         ProfileSyntaxOnly,
			MaxIndexedFiles: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.Commit != "" || response.Tree != "" {
			t.Fatalf("unexpected git provenance: %#v", response)
		}
		if len(response.Warnings) != 1 || response.Warnings[0].Code != "E_NO_GIT_HEAD" {
			t.Fatalf("warnings = %#v", response.Warnings)
		}
	})
}

func TestSearchRepositoryReusesCommittedIndexCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	write(t, repo, "auth.go", `package auth

// ValidateToken verifies an authentication token.
func ValidateToken(token string) bool { return token != "" }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	options := SearchOptions{
		Profile:  ProfileFast,
		TopK:     5,
		CacheDir: t.TempDir(),
	}
	first, err := SearchRepository(t.Context(), repo, "test-version", "validate authentication token", options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.IndexCacheHit {
		t.Fatal("first search unexpectedly hit the index cache")
	}
	second, err := SearchRepository(t.Context(), repo, "test-version", "validate authentication token", options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Stats.IndexCacheHit {
		t.Fatal("second search did not reuse the committed index cache")
	}
	if len(first.Results) == 0 || len(second.Results) == 0 || first.Results[0].SymbolID != second.Results[0].SymbolID {
		t.Fatalf("cache changed retrieval: first=%#v second=%#v", first.Results, second.Results)
	}
}

func TestWriteSearchSnapshotReplacesExistingEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json.gz")
	first := cachedSearchSnapshot{CacheVersion: searchSnapshotCacheVersion, Tree: "first"}
	second := cachedSearchSnapshot{CacheVersion: searchSnapshotCacheVersion, Tree: "second"}
	if err := writeSearchSnapshot(path, first); err != nil {
		t.Fatalf("write first snapshot: %v", err)
	}
	if err := writeSearchSnapshot(path, second); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}
	got, err := readSearchSnapshot(path)
	if err != nil {
		t.Fatalf("read replaced snapshot: %v", err)
	}
	if got.Tree != second.Tree {
		t.Fatalf("cached tree = %q, want %q", got.Tree, second.Tree)
	}
}

func TestSearchRepositorySelectivelyIndexesLargeRepositories(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 20; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf("package noise\nfunc Noise%d() int { return %d }\n", index, index))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the rare selective-indexing request.
func NeedleTarget() bool { return true }
`)
	response, err := SearchRepository(t.Context(), repo, "test", "NeedleTarget selective indexing", SearchOptions{
		Worktree:        true,
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.FilesScanned != 21 || response.Stats.FilesIndexed > 4 {
		t.Fatalf("unexpected selective-index stats: %#v", response.Stats)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("selective index lost the target: %#v", response.Results)
	}
}

func TestSearchRepositoryDoesNotTreatPathPriorAsEvidence(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/target.go", "package source\nfunc TargetNeedle() bool { return true }\n")
	write(t, repo, "src/unrelated.go", "package source\nfunc UnrelatedOperation() bool { return false }\n")
	response, err := SearchRepository(t.Context(), repo, "test", "TargetNeedle", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.SymbolName == "UnrelatedOperation" {
			t.Fatalf("source-directory prior created an unrelated candidate: %#v", response.Results)
		}
	}
}

func TestSearchRepositoryDropsWeakFragmentPathOnlyCandidates(t *testing.T) {
	repo := t.TempDir()
	// "config" is only a derived fragment (weight 1.1) of the compound query
	// identifier, and the file body contains no query tokens: the file must
	// not produce a candidate at all, and in particular must not produce a
	// zero-value result that fails response validation.
	write(t, repo, "config/notes.md", "# unrelated documentation\nplain prose only\n")
	write(t, repo, "svc/main.go", "package svc\nfunc NewServiceConfig() {}\n")

	response, err := SearchRepository(t.Context(), repo, "test", "NewServiceConfig", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation failed: %v (results=%#v)", err, response.Results)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "NewServiceConfig" {
		t.Fatalf("expected symbol result first: %#v", response.Results)
	}
	for _, result := range response.Results {
		if result.FilePath == "config/notes.md" || result.FilePath == "" {
			t.Fatalf("weak fragment-only path match produced a candidate: %#v", response.Results)
		}
	}
}

func TestSearchRepositoryKeepsFullTermPathOnlyCandidates(t *testing.T) {
	repo := t.TempDir()
	// "authentication" matches the path as a full-weight query term, so the
	// path-only fallback candidate must survive even though the body has no
	// query tokens.
	write(t, repo, "docs/authentication.md", "# overview\nplain prose only\n")
	write(t, repo, "svc/main.go", "package svc\nfunc Unrelated() {}\n")

	response, err := SearchRepository(t.Context(), repo, "test", "authentication", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	found := false
	for _, result := range response.Results {
		if result.FilePath == "docs/authentication.md" {
			found = true
			if !containsString(result.Signals, "path") {
				t.Fatalf("path-only candidate lost its path signal: %#v", result)
			}
		}
	}
	if !found {
		t.Fatalf("full-term path-only candidate was dropped: %#v", response.Results)
	}
}

func TestSearchRepositoryKeepsUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	for index := 0; index < 20; index++ {
		write(t, repo, fmt.Sprintf("src/noise_%02d.go", index), fmt.Sprintf("package source\nfunc Noise%d() int { return %d }\n", index, index))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "tracked source")
	write(t, repo, "src/draft.go", "package source\nfunc DraftNeedle() bool { return true }\n")

	response, err := SearchRepository(t.Context(), repo, "test", "DraftNeedle", SearchOptions{
		Worktree:        true,
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "DraftNeedle" {
		t.Fatalf("git preselection lost an untracked edit: %#v", response.Results)
	}
}

func TestSearchGitPreselectionThresholdTargetsLargeWorktrees(t *testing.T) {
	if shouldUseGitGrepPreselection(true, minGitGrepPreselectionFiles-1) {
		t.Fatal("small worktree selected git-grep accelerator")
	}
	if !shouldUseGitGrepPreselection(true, minGitGrepPreselectionFiles) {
		t.Fatal("large worktree did not select git-grep accelerator")
	}
	if shouldUseGitGrepPreselection(false, minGitGrepPreselectionFiles*2) {
		t.Fatal("immutable tree selected worktree-only accelerator")
	}
}

func TestScoreSearchPathsStopsDispatchAfterCancellation(t *testing.T) {
	paths := make([]string, 10_000)
	for index := range paths {
		paths[index] = fmt.Sprintf("src/file_%05d.go", index)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	files := scoreSearchPaths(ctx, paths, 8, func(path string) (searchFileCandidate, bool) {
		if calls.Add(1) == 1 {
			cancel()
		}
		return searchFileCandidate{path: path, score: 1}, true
	})
	if got := calls.Load(); got > 8 {
		t.Fatalf("canceled dispatch scored %d paths, want at most one in-flight path per worker", got)
	}
	if len(files) > 8 {
		t.Fatalf("canceled dispatch returned %d files, want at most one per worker", len(files))
	}
}

func TestSearchTermMatcherIsCaseInsensitiveAndFindsOverlaps(t *testing.T) {
	terms := []string{"list", "listitem", "secondaryaction", "missing"}
	matched := newSearchTermMatcher(terms).match("export const ListItemSecondaryAction = true")
	for _, index := range []int{0, 1, 2} {
		if !matched[index] {
			t.Fatalf("term %q was not matched: %#v", terms[index], matched)
		}
	}
	if matched[3] {
		t.Fatalf("absent term matched: %#v", matched)
	}
}

func TestSearchPreselectionPatternsPreferCodeShapedTerms(t *testing.T) {
	query := buildSearchQuery("refactor RenderProfileButton documentation behavior with trailingAction")
	patterns := searchPreselectionPatterns(query)
	if !containsString(patterns, "renderprofilebutton") || !containsString(patterns, "trailingaction") {
		t.Fatalf("preselection patterns = %#v", patterns)
	}
	for _, generic := range []string{"render", "profile", "button", "trailing", "action"} {
		if containsString(patterns, generic) {
			t.Fatalf("compound identifier fragment %q entered preselection: %#v", generic, patterns)
		}
	}
	if len(patterns) > 12 {
		t.Fatalf("preselection pattern count = %d", len(patterns))
	}
}

func TestSearchPathScoreIgnoresDerivedURLAndIdentifierFragments(t *testing.T) {
	query := buildSearchQuery("https://example.com/packages/ui-kit RenderProfileButton")
	if got := pathSearchScore(query, "packages/ui-kit/src/unrelated.js"); got != 0 {
		t.Fatalf("derived URL fragments created path evidence: %v", got)
	}
	if got := pathSearchScore(query, "packages/renderprofilebutton/implementation.js"); got == 0 {
		t.Fatal("original compound identifier did not create path evidence")
	}
}

func TestSearchTokenVariantsKeepQualifiedCompoundIdentifiers(t *testing.T) {
	variants := searchTokenVariants("d.cc.NewServiceConfig")
	for _, want := range []string{"d.cc.newserviceconfig", "newserviceconfig", "new", "service", "config"} {
		if !containsString(variants, want) {
			t.Fatalf("variant %q missing from %#v", want, variants)
		}
	}
}

func TestSearchQueryPreservesPunctuatedLanguageNames(t *testing.T) {
	for query, want := range map[string]string{"C++": "c++", "C#": "c#", "F#": "f#"} {
		t.Run(query, func(t *testing.T) {
			got := buildSearchQuery(query)
			if !got.termSet[want] {
				t.Fatalf("query %q terms = %#v, want %q", query, got.terms, want)
			}
		})
	}
	for _, query := range []string{"++", "#", ":-"} {
		if got := buildSearchQuery(query); len(got.terms) != 0 {
			t.Fatalf("punctuation-only query %q produced terms %#v", query, got.terms)
		}
	}
}

func TestCodeLikeSearchTokenIgnoresProsePunctuation(t *testing.T) {
	for _, token := range []string{"documentation.", "Currently,", "spaces."} {
		if codeLikeSearchToken(token) {
			t.Fatalf("prose token %q classified as code", token)
		}
	}
	for _, token := range []string{"NewServiceConfig", "resolver_conn_wrapper", "foo/bar.go", "--head", "DOM", "C++", "C#"} {
		if !codeLikeSearchToken(token) {
			t.Fatalf("code token %q was not classified as code", token)
		}
	}
}

func TestCodeLikeSearchWeightDoesNotOverweightShortAcronyms(t *testing.T) {
	if got := codeLikeSearchWeight("DOM"); got >= 2 {
		t.Fatalf("short acronym weight = %v", got)
	}
	if got := codeLikeSearchWeight("NewServiceConfig"); got <= 2 {
		t.Fatalf("compound identifier weight = %v", got)
	}
}

func TestDiverseSelectionDoesNotSpendBudgetOnClones(t *testing.T) {
	candidates := []searchCandidate{
		{score: 10, result: SearchResult{FilePath: "bench/a/item.go", StartLine: 1, EndLine: 2, SymbolName: "same"}},
		{score: 9.9, result: SearchResult{FilePath: "bench/b/item.go", StartLine: 1, EndLine: 2, SymbolName: "same"}},
		{score: 8, result: SearchResult{FilePath: "src/implementation.go", StartLine: 1, EndLine: 2, SymbolName: "implementation"}},
	}
	selected := selectDiverseCandidates(candidates, 2, 3)
	if len(selected) != 2 || selected[1].result.SymbolName != "implementation" {
		t.Fatalf("clone consumed diversity budget: %#v", selected)
	}
}

func TestSearchPathPriorPrefersProductCodeUnlessWorkflowRequested(t *testing.T) {
	issue := buildSearchQuery("render account profile")
	if source, workflow := searchPathPrior(issue, "src/profile/render.go"), searchPathPrior(issue, ".github/workflows/release.yml"); source <= workflow {
		t.Fatalf("source prior %v did not exceed workflow prior %v", source, workflow)
	}
	workflowIssue := buildSearchQuery("fix CI workflow pipeline")
	if got := searchPathPrior(workflowIssue, ".github/workflows/test.yml"); got < 0 {
		t.Fatalf("explicit workflow query was penalized: %v", got)
	}
}

func TestSearchResultContextBudgetPreservesDiverseFocusedResults(t *testing.T) {
	results := make([]SearchResult, 10)
	for index := range results {
		results[index] = SearchResult{
			Rank:             index + 1,
			FilePath:         fmt.Sprintf("src/component_%d.go", index),
			StartLine:        1,
			EndLine:          40,
			FocusLine:        20,
			SnippetStartLine: 1,
			SnippetEndLine:   40,
			Signature:        "func HandleConfigurationRequestWithManyArguments(input string, output string, retries int) error",
			Signals:          []string{"body", "exact-code-token"},
			Snippet:          strings.Repeat("configuration request handler with implementation detail\n", 40),
		}
	}
	const budget = 4096
	compacted, resultBytes, dropped, truncated := fitSearchResultsToBudget(results, buildSearchQuery("configuration handler"), budget)
	encoded, err := json.Marshal(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if resultBytes != len(encoded) || resultBytes > budget {
		t.Fatalf("budget accounting = %d, encoded = %d, budget = %d", resultBytes, len(encoded), budget)
	}
	if len(compacted) < 2 || compacted[0].FilePath != results[0].FilePath {
		t.Fatalf("budgeting lost ranked diversity: %#v", compacted)
	}
	if dropped != len(results)-len(compacted) || truncated == 0 {
		t.Fatalf("unexpected budget stats: dropped=%d truncated=%d results=%d", dropped, truncated, len(compacted))
	}
	for _, result := range compacted {
		if result.FocusLine < result.SnippetStartLine || result.FocusLine > result.SnippetEndLine {
			t.Fatalf("focus line fell outside compacted snippet: %#v", result)
		}
	}
}

func TestCompactSearchResultKeepsLargestFocusedWindowThatFits(t *testing.T) {
	lines := []string{
		"first context line with enough detail to consume bytes",
		"second context line with enough detail to consume bytes",
		"focus configuration handler with enough detail to consume bytes",
		"fourth context line with enough detail to consume bytes",
		"fifth context line with enough detail to consume bytes",
	}
	result := SearchResult{
		Rank:             1,
		FilePath:         "src/configuration.go",
		StartLine:        1,
		EndLine:          len(lines),
		FocusLine:        3,
		SnippetStartLine: 1,
		SnippetEndLine:   len(lines),
		Signature:        "func HandleConfiguration() error",
		Signals:          []string{"body"},
		Snippet:          strings.Join(lines, "\n"),
	}
	threeLine := result
	threeLine.SnippetStartLine = 2
	threeLine.SnippetEndLine = 4
	threeLine.Snippet = strings.Join(lines[1:4], "\n")
	budget := serializedSearchResultBytes(threeLine)
	if serializedSearchResultBytes(result) <= budget {
		t.Fatal("test fixture full snippet unexpectedly fits compacted budget")
	}

	got, size := compactSearchResultToBytes(result, buildSearchQuery("configuration handler"), budget)
	if size > budget {
		t.Fatalf("compacted size = %d, budget = %d", size, budget)
	}
	if got.SnippetStartLine != 2 || got.SnippetEndLine != 4 || got.Snippet != threeLine.Snippet {
		t.Fatalf("compacted snippet = lines %d-%d %q, want balanced lines 2-4", got.SnippetStartLine, got.SnippetEndLine, got.Snippet)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
