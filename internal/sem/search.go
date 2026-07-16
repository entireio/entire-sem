package sem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const (
	defaultSearchTopK              = 20
	defaultSearchContextLines      = 8
	defaultSearchMaxRegionLines    = 120
	defaultSearchMaxSnippetLines   = 40
	defaultSearchMaxRegionsPerFile = 3
	defaultSearchMaxIndexedFiles   = 96
	maxSearchQueryTerms            = 48
	minGitGrepPreselectionFiles    = 10_000
)

// SearchOptions controls local issue-to-code retrieval. Search reads the same
// HEAD/worktree view and ignore rules as provider snapshots.
type SearchOptions struct {
	Worktree          bool
	IgnoreFiles       []string
	IncludeFiles      []string
	Profile           Profile
	TopK              int
	ContextLines      int
	MaxRegionLines    int
	MaxSnippetLines   int
	MaxRegionsPerFile int
	MaxParseBytes     int
	CacheDir          string
	DisableCache      bool
	MaxIndexedFiles   int
	IndexAllFiles     bool
	MaxContextBytes   int
}

// SearchResult is a ranked source region suitable for direct agent context.
type SearchResult struct {
	Rank             int      `json:"rank"`
	Score            float64  `json:"score"`
	FilePath         string   `json:"file_path"`
	StartLine        int      `json:"start_line"`
	EndLine          int      `json:"end_line"`
	FocusLine        int      `json:"focus_line"`
	SnippetStartLine int      `json:"snippet_start_line"`
	SnippetEndLine   int      `json:"snippet_end_line"`
	Language         string   `json:"language,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	SymbolID         string   `json:"symbol_id,omitempty"`
	SymbolName       string   `json:"symbol_name,omitempty"`
	QualifiedName    string   `json:"qualified_name,omitempty"`
	Signature        string   `json:"signature,omitempty"`
	Signals          []string `json:"signals"`
	Snippet          string   `json:"snippet"`
}

type SearchStats struct {
	FilesScanned       int   `json:"files_scanned"`
	FilesContentRead   int   `json:"files_content_read_during_preselection"`
	FilesIndexed       int   `json:"files_indexed"`
	SymbolsConsidered  int   `json:"symbols_considered"`
	LexicalCandidates  int   `json:"lexical_candidates"`
	GraphCandidates    int   `json:"graph_candidates"`
	CandidatesSelected int   `json:"candidates_selected"`
	ResultBytes        int   `json:"result_bytes"`
	ContextBudgetBytes int   `json:"context_budget_bytes,omitempty"`
	ResultsDropped     int   `json:"results_dropped_by_budget,omitempty"`
	SnippetsTruncated  int   `json:"snippets_truncated_by_budget,omitempty"`
	IndexCacheHit      bool  `json:"index_cache_hit"`
	IndexLatencyMS     int64 `json:"index_latency_ms"`
	SearchLatencyMS    int64 `json:"search_latency_ms"`
	PreselectLatencyMS int64 `json:"preselect_latency_ms"`
}

type SearchResponse struct {
	Query    string            `json:"query"`
	RepoRoot string            `json:"repo_root"`
	Commit   string            `json:"commit,omitempty"`
	Tree     string            `json:"tree,omitempty"`
	Profile  string            `json:"profile"`
	Results  []SearchResult    `json:"results"`
	Stats    SearchStats       `json:"stats"`
	Warnings []ProviderWarning `json:"warnings"`
}

type searchQuery struct {
	rawLower string
	terms    []string
	termSet  map[string]bool
	weights  map[string]float64
}

type searchCandidate struct {
	result     SearchResult
	termCounts map[string]int
	docLength  int
	baseScore  float64
	score      float64
}

var searchWordPattern = regexp.MustCompile(`[[:alnum:]_./:+#-]+`)

var searchStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "by": true, "can": true, "change": true,
	"code": true, "does": true, "for": true, "from": true, "how": true,
	"i": true, "in": true, "into": true, "is": true, "it": true,
	"make": true, "of": true, "on": true, "or": true, "our": true,
	"should": true, "support": true, "that": true, "the": true,
	"this": true, "to": true, "use": true, "uses": true, "using": true,
	"we": true, "what": true, "when": true, "where": true, "which": true,
	"with": true, "without": true,
}

// SearchRepository performs local hybrid lexical/semantic retrieval. It uses
// no qrels, hosted models, embeddings, or network access.
func SearchRepository(ctx context.Context, repo, providerVersion, query string, options SearchOptions) (SearchResponse, error) {
	q := buildSearchQuery(query)
	if len(q.terms) == 0 {
		return SearchResponse{}, errors.New("search query has no meaningful terms")
	}
	if options.TopK <= 0 {
		options.TopK = defaultSearchTopK
	}
	if options.ContextLines < 0 {
		return SearchResponse{}, errors.New("search context lines cannot be negative")
	}
	if options.ContextLines == 0 {
		options.ContextLines = defaultSearchContextLines
	}
	if options.MaxRegionLines <= 0 {
		options.MaxRegionLines = defaultSearchMaxRegionLines
	}
	if options.MaxSnippetLines <= 0 {
		options.MaxSnippetLines = defaultSearchMaxSnippetLines
	}
	if options.MaxRegionsPerFile <= 0 {
		options.MaxRegionsPerFile = defaultSearchMaxRegionsPerFile
	}
	if options.MaxIndexedFiles <= 0 {
		options.MaxIndexedFiles = defaultSearchMaxIndexedFiles
	}
	if options.Profile == "" {
		options.Profile = ProfileSyntaxOnly
	}
	searchStarted := time.Now()
	preselectStarted := time.Now()
	selection, err := preselectSearchFiles(ctx, repo, q, options)
	if err != nil {
		return SearchResponse{}, err
	}
	preselectLatency := time.Since(preselectStarted)
	selectedFiles := selection.files
	if len(selectedFiles) == 0 {
		return SearchResponse{
			Query:    query,
			RepoRoot: selection.repoRoot,
			Commit:   selection.commit,
			Tree:     selection.tree,
			Profile:  string(options.Profile),
			Results:  []SearchResult{},
			Stats: SearchStats{
				FilesScanned:       selection.filesScanned,
				FilesContentRead:   selection.filesContentRead,
				FilesIndexed:       0,
				ResultBytes:        serializedSearchResultBytes([]SearchResult{}),
				ContextBudgetBytes: options.MaxContextBytes,
				PreselectLatencyMS: preselectLatency.Milliseconds(),
				SearchLatencyMS:    time.Since(searchStarted).Milliseconds(),
			},
			Warnings: selection.warnings,
		}, nil
	}

	snapshotOptions := ProviderSnapshotOptions{
		NoNetwork:     true,
		Worktree:      options.Worktree,
		IgnoreFiles:   options.IgnoreFiles,
		IncludeFiles:  options.IncludeFiles,
		MaxParseBytes: options.MaxParseBytes,
		Profile:       options.Profile,
		OnlyFiles:     selectedFiles,
	}
	indexStarted := time.Now()
	snapshot, cacheHit, err := loadOrBuildSearchSnapshot(ctx, repo, providerVersion, snapshotOptions, options.CacheDir, options.DisableCache)
	if err != nil {
		return SearchResponse{}, err
	}
	indexLatency := time.Since(indexStarted)
	useHead := !options.Worktree && snapshot.Header.Commit != ""
	_, read, _, closeSource, err := openSource(ctx, repo, useHead, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return SearchResponse{}, err
	}
	if closeSource != nil {
		defer closeSource()
	}

	symbolsByFile := make(map[string][]SymbolRecord)
	symbolsByID := make(map[string]SymbolRecord, len(snapshot.Symbols))
	for _, symbol := range snapshot.Symbols {
		symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
		symbolsByID[symbol.ID] = symbol
	}
	for filePath := range symbolsByFile {
		sort.Slice(symbolsByFile[filePath], func(i, j int) bool {
			left, right := symbolsByFile[filePath][i], symbolsByFile[filePath][j]
			if left.StartLine != right.StartLine {
				return left.StartLine < right.StartLine
			}
			return left.EndLine < right.EndLine
		})
	}

	fileLanguages := make(map[string]string, len(snapshot.Files))
	for _, file := range snapshot.Files {
		fileLanguages[file.Path] = file.Language
	}

	fileDF := make(map[string]int, len(q.terms))
	var candidates []searchCandidate
	for _, filePath := range selectedFiles {
		if err := ctx.Err(); err != nil {
			return SearchResponse{}, err
		}
		content, ok := read(filePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			continue
		}
		lowerContent := strings.ToLower(content)
		lowerPath := strings.ToLower(filepath.ToSlash(filePath))
		for _, term := range q.terms {
			if strings.Contains(lowerContent, term) || strings.Contains(lowerPath, term) {
				fileDF[term]++
			}
		}
		lines := strings.Split(content, "\n")
		candidates = append(candidates, candidatesForFile(
			q, filePath, fileLanguages[filePath], lines, symbolsByFile[filePath], options,
		)...)
	}

	stats := SearchStats{
		FilesScanned:       selection.filesScanned,
		FilesContentRead:   selection.filesContentRead,
		FilesIndexed:       len(selectedFiles),
		SymbolsConsidered:  len(snapshot.Symbols),
		LexicalCandidates:  len(candidates),
		IndexCacheHit:      cacheHit,
		IndexLatencyMS:     indexLatency.Milliseconds(),
		PreselectLatencyMS: preselectLatency.Milliseconds(),
	}
	scoreSearchCandidates(candidates, q, fileDF, maxInt(1, len(selectedFiles)))
	sortSearchCandidates(candidates)

	graphCandidates := expandGraphCandidates(candidates, snapshot.Relations, symbolsByID, read, fileLanguages, options)
	stats.GraphCandidates = len(graphCandidates)
	candidates = append(candidates, graphCandidates...)
	sortSearchCandidates(candidates)
	selected := selectDiverseCandidates(candidates, options.TopK, options.MaxRegionsPerFile)
	results := make([]SearchResult, 0, len(selected))
	for i := range selected {
		selected[i].result.Rank = i + 1
		selected[i].result.Score = math.Round(selected[i].score*10000) / 10000
		if selected[i].result.Signals == nil {
			selected[i].result.Signals = []string{}
		}
		results = append(results, selected[i].result)
	}
	results, resultBytes, dropped, truncated := fitSearchResultsToBudget(results, q, options.MaxContextBytes)
	stats.CandidatesSelected = len(results)
	stats.ResultBytes = resultBytes
	stats.ContextBudgetBytes = options.MaxContextBytes
	stats.ResultsDropped = dropped
	stats.SnippetsTruncated = truncated
	stats.SearchLatencyMS = time.Since(searchStarted).Milliseconds()
	if results == nil {
		results = []SearchResult{}
	}
	return SearchResponse{
		Query:    query,
		RepoRoot: snapshot.Header.RepoRoot,
		Commit:   snapshot.Header.Commit,
		Tree:     snapshot.Header.Tree,
		Profile:  string(options.Profile),
		Results:  results,
		Stats:    stats,
		Warnings: snapshot.Header.Warnings,
	}, nil
}

func fitSearchResultsToBudget(results []SearchResult, q searchQuery, budget int) ([]SearchResult, int, int, int) {
	originalCount := len(results)
	if budget <= 0 || len(results) == 0 {
		return results, serializedSearchResultBytes(results), 0, 0
	}

	for count := len(results); count > 0; count-- {
		perResult := maxInt(256, (budget-2-(count-1))/count)
		compacted := make([]SearchResult, count)
		truncated := 0
		for index := range compacted {
			compacted[index], _ = compactSearchResultToBytes(results[index], q, perResult)
			if compacted[index].Snippet != results[index].Snippet || compacted[index].Signature != results[index].Signature {
				truncated++
			}
		}
		resultBytes := serializedSearchResultBytes(compacted)
		if resultBytes <= budget {
			return compacted, resultBytes, originalCount - count, truncated
		}
	}
	return []SearchResult{}, serializedSearchResultBytes([]SearchResult{}), originalCount, 0
}

func compactSearchResultToBytes(result SearchResult, q searchQuery, budget int) (SearchResult, int) {
	if size := serializedSearchResultBytes(result); size <= budget {
		return result, size
	}
	result.Signature = truncateSearchText(result.Signature, 192, q)
	if size := serializedSearchResultBytes(result); size <= budget {
		return result, size
	}

	lines := strings.Split(result.Snippet, "\n")
	focus := result.FocusLine - result.SnippetStartLine
	if focus < 0 || focus >= len(lines) {
		focus = len(lines) / 2
	}
	bestSpan, bestBalance := 0, len(lines)+1
	var best SearchResult
	bestSize := 0
	for left := 0; left <= focus; left++ {
		for right := focus; right < len(lines); right++ {
			candidate := result
			candidate.SnippetStartLine = result.SnippetStartLine + left
			candidate.SnippetEndLine = result.SnippetStartLine + right
			candidate.Snippet = strings.Join(lines[left:right+1], "\n")
			size := serializedSearchResultBytes(candidate)
			if size > budget {
				continue
			}
			span := right - left + 1
			balance := (focus - left) - (right - focus)
			if balance < 0 {
				balance = -balance
			}
			if span > bestSpan || (span == bestSpan && balance < bestBalance) {
				best, bestSize = candidate, size
				bestSpan, bestBalance = span, balance
			}
		}
	}
	if bestSpan > 0 {
		return best, bestSize
	}
	candidate := result
	candidate.SnippetStartLine = result.SnippetStartLine + focus
	candidate.SnippetEndLine = candidate.SnippetStartLine
	candidate.Snippet = truncateSearchText(lines[focus], maxInt(16, budget/3), q)
	return candidate, serializedSearchResultBytes(candidate)
}

func truncateSearchText(value string, maxBytes int, q searchQuery) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	center := len(value) / 2
	lower := strings.ToLower(value)
	for _, term := range q.terms {
		if index := strings.Index(lower, term); index >= 0 {
			center = index + len(term)/2
			break
		}
	}
	window := maxBytes - len("... ") - len(" ...")
	if window <= 0 {
		return ""
	}
	start := maxInt(0, center-window/2)
	end := minInt(len(value), start+window)
	start = maxInt(0, end-window)
	for start < end && !utf8RuneStart(value[start]) {
		start++
	}
	for end > start && end < len(value) && !utf8RuneStart(value[end]) {
		end--
	}
	if start >= end {
		return ""
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "... "
	}
	if end < len(value) {
		suffix = " ..."
	}
	return prefix + value[start:end] + suffix
}

func utf8RuneStart(value byte) bool {
	return value&0xc0 != 0x80
}

func serializedSearchResultBytes(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

type searchFileCandidate struct {
	path  string
	score float64
}

type searchFileSelection struct {
	files            []string
	repoRoot         string
	commit           string
	tree             string
	warnings         []ProviderWarning
	filesScanned     int
	filesContentRead int
}

func preselectSearchFiles(ctx context.Context, repo string, q searchQuery, options SearchOptions) (searchFileSelection, error) {
	source, err := prepareSource(ctx, repo, ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     options.Worktree,
		IgnoreFiles:  options.IgnoreFiles,
		IncludeFiles: options.IncludeFiles,
	})
	if err != nil {
		return searchFileSelection{}, err
	}
	if source.close != nil {
		defer source.close()
	}
	selection := searchFileSelection{
		repoRoot:     source.absRepo,
		commit:       source.commit,
		tree:         source.tree,
		warnings:     append([]ProviderWarning{}, source.warnings...),
		filesScanned: len(source.paths),
	}
	if options.IndexAllFiles || len(source.paths) <= options.MaxIndexedFiles {
		selection.files = append([]string(nil), source.paths...)
		return selection, nil
	}
	queryWeight := 0.0
	for _, weight := range q.weights {
		queryWeight += weight
	}
	matcher := newSearchTermMatcher(q.terms)
	scanPaths := source.paths
	if shouldUseGitGrepPreselection(options.Worktree, len(source.paths)) {
		matches, grepErr := gitutil.GrepIndexMatches(ctx, source.absRepo, searchPreselectionPatterns(q), 32)
		tracked, trackedErr := gitutil.ListIndexFiles(ctx, source.absRepo)
		if grepErr == nil && trackedErr == nil {
			allowed := make(map[string]bool, len(source.paths))
			for _, filePath := range source.paths {
				allowed[filePath] = true
			}
			trackedSet := make(map[string]bool, len(tracked))
			for _, filePath := range tracked {
				trackedSet[filePath] = true
			}
			termMatches := make(map[string][]bool)
			for _, match := range matches {
				if !allowed[match.Path] {
					continue
				}
				seen := termMatches[match.Path]
				if seen == nil {
					seen = make([]bool, len(q.terms))
				}
				for index, matched := range matcher.match(match.Text) {
					seen[index] = seen[index] || matched
				}
				termMatches[match.Path] = seen
			}
			provisional := make([]searchFileCandidate, 0, len(termMatches)+16)
			for filePath, seen := range termMatches {
				matchedWeight := 0.0
				for index, matched := range seen {
					if matched {
						matchedWeight += q.weights[q.terms[index]]
					}
				}
				pathScore := pathSearchScore(q, filePath)
				score := 2*pathScore + matchedWeight + searchPathPrior(q, filePath)
				if queryWeight > 0 {
					score += 4 * matchedWeight / queryWeight
				}
				provisional = append(provisional, searchFileCandidate{path: filePath, score: score})
			}
			untracked := make([]string, 0, 16)
			for _, filePath := range source.paths {
				if !trackedSet[filePath] {
					untracked = append(untracked, filePath)
					continue
				}
				if _, exists := termMatches[filePath]; !exists {
					if pathScore := pathSearchScore(q, filePath); pathScore > 0 {
						provisional = append(provisional, searchFileCandidate{
							path:  filePath,
							score: 2*pathScore + searchPathPrior(q, filePath),
						})
					}
				}
			}
			sort.Slice(provisional, func(i, j int) bool {
				if provisional[i].score != provisional[j].score {
					return provisional[i].score > provisional[j].score
				}
				return provisional[i].path < provisional[j].path
			})
			poolLimit := minInt(len(provisional), options.MaxIndexedFiles*4)
			scanPaths = make([]string, 0, poolLimit+len(untracked))
			for _, candidate := range provisional[:poolLimit] {
				scanPaths = append(scanPaths, candidate.path)
			}
			scanPaths = append(scanPaths, untracked...)
			if len(scanPaths) == 0 {
				scanPaths = source.paths
			}
		}
	}
	scoreFile := func(filePath string) (searchFileCandidate, bool) {
		if err := ctx.Err(); err != nil {
			return searchFileCandidate{}, false
		}
		content, ok := source.read(filePath)
		if !ok || strings.IndexByte(content, 0) >= 0 {
			return searchFileCandidate{}, false
		}
		pathScore := pathSearchScore(q, filePath)
		matchedWeight := 0.0
		for index, matched := range matcher.match(content) {
			if matched {
				term := q.terms[index]
				weight := q.weights[term]
				matchedWeight += weight
			}
		}
		if pathScore == 0 && matchedWeight == 0 {
			return searchFileCandidate{}, false
		}
		score := 2*pathScore + matchedWeight + searchPathPrior(q, filePath)
		if queryWeight > 0 {
			score += 4 * matchedWeight / queryWeight
		}
		return searchFileCandidate{path: filePath, score: score}, true
	}
	workers := 1
	if options.Worktree {
		workers = minInt(8, maxInt(1, runtime.GOMAXPROCS(0)))
	}
	files := scoreSearchPaths(ctx, scanPaths, workers, scoreFile)
	if err := ctx.Err(); err != nil {
		return searchFileSelection{}, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].score != files[j].score {
			return files[i].score > files[j].score
		}
		return files[i].path < files[j].path
	})
	if len(files) > options.MaxIndexedFiles {
		files = files[:options.MaxIndexedFiles]
	}
	selected := make([]string, len(files))
	for i, file := range files {
		selected[i] = file.path
	}
	selection.files = selected
	selection.filesContentRead = len(scanPaths)
	return selection, nil
}

func scoreSearchPaths(
	ctx context.Context,
	paths []string,
	workers int,
	score func(string) (searchFileCandidate, bool),
) []searchFileCandidate {
	initialCapacity := minInt(len(paths), 1024)
	if workers <= 1 {
		files := make([]searchFileCandidate, 0, initialCapacity)
		for _, filePath := range paths {
			if ctx.Err() != nil {
				break
			}
			if candidate, ok := score(filePath); ok {
				files = append(files, candidate)
			}
		}
		return files
	}

	jobs := make(chan string)
	results := make(chan searchFileCandidate)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for filePath := range jobs {
				if ctx.Err() != nil {
					return
				}
				if candidate, ok := score(filePath); ok {
					select {
					case results <- candidate:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, filePath := range paths {
			select {
			case jobs <- filePath:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()
	files := make([]searchFileCandidate, 0, initialCapacity)
	for candidate := range results {
		files = append(files, candidate)
	}
	return files
}

func shouldUseGitGrepPreselection(worktree bool, fileCount int) bool {
	return worktree && fileCount >= minGitGrepPreselectionFiles
}

func searchPreselectionPatterns(q searchQuery) []string {
	const (
		maxPatterns            = 12
		maxPrimaryPatterns     = 8
		maxInitialCodePatterns = 4
		maxOriginalPatterns    = 4
		maxMorphPatterns       = 2
		maxFragmentTerms       = 2
	)
	patterns := make([]string, 0, maxPatterns)
	seen := make(map[string]bool, maxPatterns)
	appendTier := func(limit int, include func(float64) bool) {
		if limit <= 0 {
			return
		}
		added := 0
		for _, term := range q.terms {
			if seen[term] || !include(q.weights[term]) {
				continue
			}
			patterns = append(patterns, term)
			seen[term] = true
			added++
			if added == limit || len(patterns) == maxPatterns {
				return
			}
		}
	}
	appendTier(maxInitialCodePatterns, func(weight float64) bool {
		return weight >= 1.25
	})
	appendTier(maxOriginalPatterns, func(weight float64) bool {
		return weight == 1
	})
	appendTier(maxPrimaryPatterns-len(patterns), func(weight float64) bool {
		return weight >= 1.25 || weight == 1
	})
	appendTier(maxMorphPatterns, func(weight float64) bool {
		return weight < 1
	})
	appendTier(maxFragmentTerms, func(weight float64) bool {
		return weight > 1 && weight < 1.25
	})
	appendTier(maxPatterns-len(patterns), func(weight float64) bool {
		return weight >= 1.25 || weight == 1
	})
	return patterns
}

func candidatesForFile(q searchQuery, filePath, language string, lines []string, symbols []SymbolRecord, options SearchOptions) []searchCandidate {
	var out []searchCandidate
	covered := make([]bool, len(lines)+1)
	for _, symbol := range symbols {
		start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
		if start == 0 {
			continue
		}
		searchStart := precedingDocumentationStart(lines, start, options.ContextLines)
		for line := searchStart; line <= end; line++ {
			covered[line] = true
		}
		if end-searchStart+1 <= options.MaxRegionLines {
			if candidate, ok := makeSearchCandidate(q, filePath, language, lines, searchStart, end, symbol, options.MaxSnippetLines); ok {
				out = append(out, candidate)
			}
			continue
		}
		for _, region := range matchingLineRegions(q, lines, searchStart, end, options.ContextLines, options.MaxRegionLines) {
			if candidate, ok := makeSearchCandidate(q, filePath, language, lines, region[0], region[1], symbol, options.MaxSnippetLines); ok {
				out = append(out, candidate)
			}
		}
	}

	var uncoveredHits []int
	for index, line := range lines {
		lineNumber := index + 1
		if !covered[lineNumber] && textMatchesSearchQuery(q, line) {
			uncoveredHits = append(uncoveredHits, lineNumber)
		}
	}
	for _, region := range uncoveredRegionsAroundHits(uncoveredHits, covered, len(lines), options.ContextLines, options.MaxRegionLines) {
		if candidate, ok := makeSearchCandidate(q, filePath, language, lines, region[0], region[1], SymbolRecord{}, options.MaxSnippetLines); ok {
			out = append(out, candidate)
		}
	}

	if len(out) == 0 && textMatchesSearchQuery(q, filepath.ToSlash(filePath)) {
		end := minInt(len(lines), maxInt(1, options.ContextLines*2+1))
		// A path can match only through weak derived fragments (compound
		// identifier parts or morphological variants) that pathSearchScore
		// does not treat as evidence. Such files produce no valid candidate
		// and must be skipped, not emitted as zero-value results.
		if candidate, ok := makeSearchCandidate(q, filePath, language, lines, 1, end, SymbolRecord{}, options.MaxSnippetLines); ok {
			candidate.baseScore += pathSearchScore(q, filePath)
			candidate.result.Signals = appendUnique(candidate.result.Signals, "path")
			out = append(out, candidate)
		}
	}
	return out
}

func uncoveredRegionsAroundHits(hits []int, covered []bool, lineCount, context, maxLines int) [][2]int {
	baseRegions := regionsAroundHits(hits, 1, lineCount, context, maxLines)
	hitSet := make(map[int]bool, len(hits))
	for _, hit := range hits {
		hitSet[hit] = true
	}
	var out [][2]int
	for _, region := range baseRegions {
		runStart := 0
		runHasHit := false
		flush := func(end int) {
			if runStart > 0 && runHasHit {
				out = append(out, [2]int{runStart, end})
			}
			runStart = 0
			runHasHit = false
		}
		for line := region[0]; line <= region[1]; line++ {
			if line < len(covered) && covered[line] {
				flush(line - 1)
				continue
			}
			if runStart == 0 {
				runStart = line
			}
			runHasHit = runHasHit || hitSet[line]
		}
		flush(region[1])
	}
	return out
}

func precedingDocumentationStart(lines []string, symbolStart, limit int) int {
	start := symbolStart
	if limit <= 0 {
		return start
	}
	sawComment := false
	for line := symbolStart - 1; line >= 1 && symbolStart-line <= limit; line-- {
		trimmed := strings.TrimSpace(lines[line-1])
		isComment := strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "--")
		if isComment {
			sawComment = true
			start = line
			continue
		}
		if trimmed == "" && sawComment {
			start = line
			continue
		}
		break
	}
	return start
}

func makeSearchCandidate(q searchQuery, filePath, language string, lines []string, start, end int, symbol SymbolRecord, maxSnippetLines int) (searchCandidate, bool) {
	start, end = clampRegion(start, end, len(lines))
	if start == 0 {
		return searchCandidate{}, false
	}
	regionText := strings.Join(lines[start-1:end], "\n")
	counts, length := searchTermCounts(regionText, q.termSet)
	focus := searchFocusLine(q, lines, start, end)
	if symbol.ID != "" {
		focus = maxInt(focus, symbol.StartLine)
	}
	snippetStart, snippetEnd := focusedSnippetRegion(start, end, focus, maxSnippetLines)
	snippet := strings.Join(lines[snippetStart-1:snippetEnd], "\n")
	pathScore := pathSearchScore(q, filePath)
	base := pathScore
	signals := []string{}
	if pathScore > 0 {
		signals = append(signals, "path")
	}
	if len(counts) > 0 {
		signals = append(signals, "body")
	}
	if symbol.ID != "" {
		nameScore, nameSignals := symbolSearchScore(q, symbol)
		base += nameScore
		signals = append(signals, nameSignals...)
	}
	if len(counts) == 0 && pathScore == 0 && len(signals) == 0 {
		return searchCandidate{}, false
	}
	base += searchPathPrior(q, filePath)
	return searchCandidate{
		result: SearchResult{
			FilePath:         filePath,
			StartLine:        start,
			EndLine:          end,
			FocusLine:        focus,
			SnippetStartLine: snippetStart,
			SnippetEndLine:   snippetEnd,
			Language:         language,
			Kind:             symbol.Kind,
			SymbolID:         symbol.ID,
			SymbolName:       symbol.Name,
			QualifiedName:    symbol.QualifiedName,
			Signature:        symbol.Signature,
			Signals:          appendUnique(nil, signals...),
			Snippet:          snippet,
		},
		termCounts: counts,
		docLength:  length,
		baseScore:  base,
	}, true
}

func scoreSearchCandidates(candidates []searchCandidate, q searchQuery, fileDF map[string]int, fileCount int) {
	if len(candidates) == 0 {
		return
	}
	averageLength := 0.0
	for _, candidate := range candidates {
		averageLength += float64(maxInt(1, candidate.docLength))
	}
	averageLength /= float64(len(candidates))
	queryWeight := 0.0
	idf := make(map[string]float64, len(q.terms))
	for _, term := range q.terms {
		df := fileDF[term]
		weight := math.Log(1+(float64(fileCount-df)+0.5)/(float64(df)+0.5)) * q.weights[term]
		idf[term] = weight
		queryWeight += weight
	}
	for i := range candidates {
		candidate := &candidates[i]
		bm25 := 0.0
		coveredWeight := 0.0
		codeTokenBonus := 0.0
		for _, term := range q.terms {
			frequency := candidate.termCounts[term]
			if frequency == 0 {
				continue
			}
			weight := idf[term]
			coveredWeight += weight
			if q.weights[term] >= 2 {
				codeTokenBonus += 6 * q.weights[term]
			}
			denominator := float64(frequency) + 1.2*(0.25+0.75*float64(maxInt(1, candidate.docLength))/averageLength)
			bm25 += weight * (float64(frequency) * 2.2 / denominator)
		}
		coverage := 0.0
		if queryWeight > 0 {
			coverage = coveredWeight / queryWeight
		}
		candidate.score = candidate.baseScore + bm25 + 7*coverage + minFloat64(24, codeTokenBonus)
		if codeTokenBonus > 0 {
			candidate.result.Signals = appendUnique(candidate.result.Signals, "exact-code-token")
		}
		if coverage == 1 {
			candidate.score += 2
			candidate.result.Signals = appendUnique(candidate.result.Signals, "all-query-terms")
		}
	}
}

func minFloat64(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func expandGraphCandidates(seeds []searchCandidate, relations []RelationRecord, symbolsByID map[string]SymbolRecord, read contentReader, languages map[string]string, options SearchOptions) []searchCandidate {
	if len(seeds) == 0 || len(relations) == 0 {
		return nil
	}
	seedScores := map[string]float64{}
	for _, candidate := range seeds {
		if len(seedScores) >= 10 {
			break
		}
		if candidate.result.SymbolID != "" && candidate.score > seedScores[candidate.result.SymbolID] {
			seedScores[candidate.result.SymbolID] = candidate.score
		}
	}
	if len(seedScores) == 0 {
		return nil
	}
	best := map[string]searchCandidate{}
	contentCache := map[string][]string{}
	for _, relation := range relations {
		if relation.Confidence < 0.7 || !searchExpansionRelation(relation.Type) {
			continue
		}
		pairs := [][3]string{{relation.FromID, relation.ToID, "outgoing"}, {relation.ToID, relation.FromID, "incoming"}}
		for _, pair := range pairs {
			seedScore, ok := seedScores[pair[0]]
			if !ok {
				continue
			}
			symbol, ok := symbolsByID[pair[1]]
			if !ok || symbol.ID == pair[0] {
				continue
			}
			lines, ok := contentCache[symbol.FilePath]
			if !ok {
				content, readable := read(symbol.FilePath)
				if !readable {
					continue
				}
				lines = strings.Split(content, "\n")
				contentCache[symbol.FilePath] = lines
			}
			start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
			if start == 0 {
				continue
			}
			if end-start+1 > options.MaxRegionLines {
				end = minInt(len(lines), start+options.MaxRegionLines-1)
			}
			snippetStart, snippetEnd := focusedSnippetRegion(start, end, start, options.MaxSnippetLines)
			candidate := searchCandidate{
				result: SearchResult{
					FilePath:         symbol.FilePath,
					StartLine:        start,
					EndLine:          end,
					FocusLine:        start,
					SnippetStartLine: snippetStart,
					SnippetEndLine:   snippetEnd,
					Language:         languages[symbol.FilePath],
					Kind:             symbol.Kind,
					SymbolID:         symbol.ID,
					SymbolName:       symbol.Name,
					QualifiedName:    symbol.QualifiedName,
					Signature:        symbol.Signature,
					Snippet:          strings.Join(lines[snippetStart-1:snippetEnd], "\n"),
				},
			}
			candidate.score = 0.28*seedScore + relation.Confidence
			candidate.baseScore = candidate.score
			candidate.result.Signals = appendUnique(candidate.result.Signals, "graph:"+strings.ToLower(relation.Type), "graph:"+pair[2])
			if previous, exists := best[symbol.ID]; !exists || candidate.score > previous.score {
				best[symbol.ID] = candidate
			}
		}
	}
	out := make([]searchCandidate, 0, len(best))
	for _, candidate := range best {
		out = append(out, candidate)
	}
	sortSearchCandidates(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func searchExpansionRelation(relation string) bool {
	switch relation {
	case "CALLS", "CONSTRUCTS", "ASYNC_CALLS", "IMPORTS", "EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES", "USES_TYPE", "TESTS", "CONFIGURES":
		return true
	default:
		return false
	}
}

func selectDiverseCandidates(candidates []searchCandidate, topK, maxPerFile int) []searchCandidate {
	remaining := append([]searchCandidate(nil), candidates...)
	selected := make([]searchCandidate, 0, minInt(topK, len(remaining)))
	perFile := map[string]int{}
	perSymbolName := map[string]int{}
	perBaseName := map[string]int{}
	for len(selected) < topK {
		bestIndex := -1
		bestAdjusted := -math.MaxFloat64
		for i := range remaining {
			candidate := remaining[i]
			if perFile[candidate.result.FilePath] >= maxPerFile || overlapsSelected(candidate, selected) {
				continue
			}
			symbolKey := strings.ToLower(candidate.result.QualifiedName)
			if symbolKey == "" {
				symbolKey = strings.ToLower(candidate.result.SymbolName)
			}
			symbolPenalty := 0.0
			if symbolKey != "" {
				symbolPenalty = 4 * float64(perSymbolName[symbolKey])
			}
			baseKey := strings.ToLower(filepath.Base(candidate.result.FilePath))
			adjusted := candidate.score -
				0.8*float64(perFile[candidate.result.FilePath]) -
				symbolPenalty -
				2*float64(perBaseName[baseKey])
			if adjusted > bestAdjusted || (adjusted == bestAdjusted && searchCandidateLess(candidate, remaining[bestIndex])) {
				bestIndex = i
				bestAdjusted = adjusted
			}
		}
		if bestIndex < 0 {
			break
		}
		selected = append(selected, remaining[bestIndex])
		chosen := remaining[bestIndex].result
		perFile[chosen.FilePath]++
		symbolKey := strings.ToLower(chosen.QualifiedName)
		if symbolKey == "" {
			symbolKey = strings.ToLower(chosen.SymbolName)
		}
		if symbolKey != "" {
			perSymbolName[symbolKey]++
		}
		perBaseName[strings.ToLower(filepath.Base(chosen.FilePath))]++
		remaining = append(remaining[:bestIndex], remaining[bestIndex+1:]...)
	}
	return selected
}

func overlapsSelected(candidate searchCandidate, selected []searchCandidate) bool {
	for _, existing := range selected {
		if candidate.result.FilePath != existing.result.FilePath {
			continue
		}
		intersection := minInt(candidate.result.EndLine, existing.result.EndLine) - maxInt(candidate.result.StartLine, existing.result.StartLine) + 1
		if intersection <= 0 {
			continue
		}
		shorter := minInt(candidate.result.EndLine-candidate.result.StartLine+1, existing.result.EndLine-existing.result.StartLine+1)
		if float64(intersection)/float64(shorter) >= 0.6 {
			return true
		}
	}
	return false
}

func sortSearchCandidates(candidates []searchCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return searchCandidateLess(candidates[i], candidates[j])
	})
}

func searchCandidateLess(left, right searchCandidate) bool {
	if left.result.FilePath != right.result.FilePath {
		return left.result.FilePath < right.result.FilePath
	}
	if left.result.StartLine != right.result.StartLine {
		return left.result.StartLine < right.result.StartLine
	}
	return left.result.EndLine < right.result.EndLine
}

func matchingLineRegions(q searchQuery, lines []string, start, end, context, maxLines int) [][2]int {
	var hits []int
	for line := start; line <= end; line++ {
		if textMatchesSearchQuery(q, lines[line-1]) {
			hits = append(hits, line)
		}
	}
	return regionsAroundHits(hits, start, end, context, maxLines)
}

func regionsAroundHits(hits []int, lower, upper, context, maxLines int) [][2]int {
	if len(hits) == 0 {
		return nil
	}
	var regions [][2]int
	groupStart := hits[0]
	groupEnd := hits[0]
	flush := func() {
		start := maxInt(lower, groupStart-context)
		end := minInt(upper, groupEnd+context)
		if end-start+1 > maxLines {
			end = start + maxLines - 1
		}
		regions = append(regions, [2]int{start, end})
	}
	for _, hit := range hits[1:] {
		if hit-groupEnd <= context*2+1 && hit-groupStart < maxLines {
			groupEnd = hit
			continue
		}
		flush()
		groupStart, groupEnd = hit, hit
	}
	flush()
	return regions
}

func buildSearchQuery(query string) searchQuery {
	weights := map[string]float64{}
	add := func(term string, weight float64) {
		if len(term) < 2 || searchStopWords[term] {
			return
		}
		hasAlphanumeric := false
		for _, character := range term {
			if unicode.IsLetter(character) || unicode.IsDigit(character) {
				hasAlphanumeric = true
				break
			}
		}
		if !hasAlphanumeric {
			return
		}
		if weight > weights[term] {
			weights[term] = weight
		}
	}
	for _, raw := range searchWordPattern.FindAllString(query, -1) {
		codeLike := codeLikeSearchToken(raw)
		for index, term := range searchTokenVariants(raw) {
			weight := 1.0
			if codeLike && index == 0 {
				weight = codeLikeSearchWeight(raw)
			} else if codeLike {
				weight = 1.1
			}
			add(term, weight)
		}
	}
	originalTerms := make([]string, 0, len(weights))
	for term := range weights {
		originalTerms = append(originalTerms, term)
	}
	for _, term := range originalTerms {
		for _, related := range morphologicalSearchTerms(term) {
			add(related, 0.55)
		}
	}
	termSet := make(map[string]bool, len(weights))
	terms := make([]string, 0, len(weights))
	for term := range weights {
		termSet[term] = true
		terms = append(terms, term)
	}
	sort.Slice(terms, func(i, j int) bool {
		if weights[terms[i]] != weights[terms[j]] {
			return weights[terms[i]] > weights[terms[j]]
		}
		if len(terms[i]) != len(terms[j]) {
			return len(terms[i]) > len(terms[j])
		}
		return terms[i] < terms[j]
	})
	if len(terms) > maxSearchQueryTerms {
		terms = terms[:maxSearchQueryTerms]
		termSet = make(map[string]bool, len(terms))
		trimmedWeights := make(map[string]float64, len(terms))
		for _, term := range terms {
			termSet[term] = true
			trimmedWeights[term] = weights[term]
		}
		weights = trimmedWeights
	}
	return searchQuery{
		rawLower: strings.ToLower(strings.TrimSpace(query)),
		terms:    terms,
		termSet:  termSet,
		weights:  weights,
	}
}

func codeLikeSearchWeight(raw string) float64 {
	trimmed := strings.Trim(raw, "./:+-")
	letters := 0
	allUpper := true
	for _, character := range trimmed {
		if !unicode.IsLetter(character) {
			continue
		}
		letters++
		if unicode.IsLower(character) {
			allUpper = false
		}
	}
	if allUpper && letters > 0 && letters <= 4 {
		return 1.4
	}
	return 2.5
}

func codeLikeSearchToken(raw string) bool {
	trimmed := strings.Trim(raw, "./:-")
	if strings.ContainsAny(trimmed, "_./:$+#") || strings.HasPrefix(raw, "--") {
		return true
	}
	uppercase := 0
	lowercase := 0
	for index, character := range trimmed {
		if unicode.IsUpper(character) {
			uppercase++
			if index > 0 {
				return true
			}
		} else if unicode.IsLower(character) {
			lowercase++
		}
	}
	return uppercase >= 2 && lowercase > 0
}

func morphologicalSearchTerms(term string) []string {
	var out []string
	switch {
	case strings.HasSuffix(term, "ization") && len(term) > len("ization")+2:
		out = append(out, strings.TrimSuffix(term, "ization")+"ize")
	case strings.HasSuffix(term, "ification") && len(term) > len("ification")+2:
		out = append(out, strings.TrimSuffix(term, "ification")+"ify")
	case strings.HasSuffix(term, "ing") && len(term) > 5:
		stem := strings.TrimSuffix(term, "ing")
		out = append(out, stem, stem+"e")
	case strings.HasSuffix(term, "ed") && len(term) > 4:
		stem := strings.TrimSuffix(term, "ed")
		out = append(out, stem, stem+"e")
	}
	if strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss") && len(term) > 4 {
		out = append(out, strings.TrimSuffix(term, "s"))
	}
	return appendUnique(nil, out...)
}

func searchTokenVariants(raw string) []string {
	raw = strings.Trim(raw, "./:-")
	if raw == "" {
		return nil
	}
	variants := []string{strings.ToLower(raw)}
	for _, segment := range strings.FieldsFunc(raw, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	}) {
		variants = append(variants, strings.ToLower(segment))
	}
	var current []rune
	runes := []rune(raw)
	flush := func() {
		if len(current) > 0 {
			variants = append(variants, strings.ToLower(string(current)))
			current = nil
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(current) > 0 && unicode.IsUpper(r) {
			previous := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower) {
				flush()
			}
		}
		current = append(current, r)
	}
	flush()
	return appendUnique(nil, variants...)
}

func searchTermCounts(text string, queryTerms map[string]bool) (map[string]int, int) {
	counts := map[string]int{}
	length := 0
	for _, raw := range searchWordPattern.FindAllString(text, -1) {
		for _, token := range searchTokenVariants(raw) {
			if len(token) < 2 {
				continue
			}
			length++
			if queryTerms[token] {
				counts[token]++
			}
		}
	}
	return counts, length
}

func textMatchesSearchQuery(q searchQuery, text string) bool {
	lower := strings.ToLower(text)
	for _, term := range q.terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func searchFocusLine(q searchQuery, lines []string, start, end int) int {
	bestLine := start
	bestMatches := 0.0
	for line := start; line <= end; line++ {
		lower := strings.ToLower(lines[line-1])
		matches := 0.0
		for _, term := range q.terms {
			if strings.Contains(lower, term) {
				matches += q.weights[term]
			}
		}
		if matches > bestMatches {
			bestLine = line
			bestMatches = matches
		}
	}
	return bestLine
}

func focusedSnippetRegion(start, end, focus, maxLines int) (int, int) {
	if maxLines <= 0 || end-start+1 <= maxLines {
		return start, end
	}
	half := maxLines / 2
	snippetStart := maxInt(start, focus-half)
	snippetEnd := minInt(end, snippetStart+maxLines-1)
	if snippetEnd-snippetStart+1 < maxLines {
		snippetStart = maxInt(start, snippetEnd-maxLines+1)
	}
	return snippetStart, snippetEnd
}

func pathSearchScore(q searchQuery, filePath string) float64 {
	lower := strings.ToLower(filepath.ToSlash(filePath))
	base := strings.ToLower(filepath.Base(filePath))
	score := 0.0
	for _, term := range q.terms {
		weight := q.weights[term]
		if weight != 1 && weight < 1.25 {
			continue
		}
		if strings.Contains(base, term) {
			score += 2.5 * weight
		} else if strings.Contains(lower, term) {
			score += 1.25 * weight
		}
	}
	return score
}

func searchPathPrior(q searchQuery, filePath string) float64 {
	lower := "/" + strings.ToLower(filepath.ToSlash(filePath))
	score := 0.0
	if strings.Contains(lower, "/src/") || strings.Contains(lower, "/include/") || strings.Contains(lower, "/lib/") || strings.Contains(lower, "/packages/") {
		score += 0.5
	}
	if strings.Contains(lower, "/.github/workflows/") && !searchQuerySupplied(q, "workflow", "pipeline", "ci", "action") {
		score -= 6
	}
	if strings.Contains(lower, "/dependencies/") || strings.Contains(lower, "/third_party/") || strings.Contains(lower, "/third-party/") {
		score -= 5
	}
	return score
}

func searchQuerySupplied(q searchQuery, terms ...string) bool {
	for _, term := range terms {
		if q.weights[term] >= 1 {
			return true
		}
	}
	return false
}

func symbolSearchScore(q searchQuery, symbol SymbolRecord) (float64, []string) {
	name := strings.ToLower(symbol.Name)
	qualified := strings.ToLower(symbol.QualifiedName)
	signature := strings.ToLower(symbol.Signature)
	compactQuery := compactSearchIdentifier(q.rawLower)
	compactName := compactSearchIdentifier(name)
	score := 0.0
	switch symbol.Kind {
	case "function", "method":
		score += 2
	case "class", "interface", "struct", "trait", "type", "enum", "record", "object", "protocol":
		score += 0.75
	case "block", "workflow", "document":
		score -= 1.5
	case "section":
		score -= 0.5
	}
	var signals []string
	if compactQuery != "" && compactQuery == compactName {
		score += 14
		signals = append(signals, "exact-symbol")
	}
	for _, term := range q.terms {
		weight := q.weights[term]
		switch {
		case name == term:
			score += 6 * weight
			signals = append(signals, "symbol-name")
		case strings.Contains(name, term) || strings.Contains(qualified, term):
			score += 3 * weight
			signals = append(signals, "symbol-name")
		case strings.Contains(signature, term):
			score += 1.5 * weight
			signals = append(signals, "signature")
		}
	}
	return score, appendUnique(nil, signals...)
}

func compactSearchIdentifier(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

func clampRegion(start, end, lineCount int) (int, int) {
	if lineCount <= 0 {
		return 0, 0
	}
	start = maxInt(1, start)
	end = minInt(lineCount, maxInt(start, end))
	if start > lineCount {
		return 0, 0
	}
	return start, end
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func (response SearchResponse) Validate() error {
	if actual := serializedSearchResultBytes(response.Results); response.Stats.ResultBytes != actual {
		return fmt.Errorf("search result byte accounting mismatch: %d != %d", response.Stats.ResultBytes, actual)
	}
	if response.Stats.ContextBudgetBytes > 0 && response.Stats.ResultBytes > response.Stats.ContextBudgetBytes {
		return fmt.Errorf("search result context exceeds byte budget: %d > %d", response.Stats.ResultBytes, response.Stats.ContextBudgetBytes)
	}
	for index, result := range response.Results {
		if result.Rank != index+1 {
			return fmt.Errorf("search result rank %d at index %d", result.Rank, index)
		}
		if result.FilePath == "" || result.StartLine < 1 || result.EndLine < result.StartLine || result.FocusLine < result.StartLine || result.FocusLine > result.EndLine || result.SnippetStartLine < result.StartLine || result.SnippetEndLine > result.EndLine || result.SnippetEndLine < result.SnippetStartLine {
			return fmt.Errorf("invalid search result at rank %d", result.Rank)
		}
	}
	return nil
}
