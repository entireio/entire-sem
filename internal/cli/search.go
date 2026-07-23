package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

type searchFlags struct {
	Repo              string
	Query             string
	Format            string
	Profile           string
	Worktree          bool
	TopK              int
	ContextLines      int
	MaxRegionLines    int
	MaxSnippetLines   int
	MaxRegionsPerFile int
	IgnoreFiles       []string
	IncludeFiles      []string
	CacheDir          string
	DisableCache      bool
	MaxIndexedFiles   int
	IndexAllFiles     bool
	MaxContextBytes   int
}

func runSearch(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseSearchFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("search received unexpected arguments: %s", strings.Join(rest, " "))
	}
	if strings.TrimSpace(flags.Query) == "" {
		return errors.New("search requires --query")
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	contextBudget := flags.MaxContextBytes
	// Agent output has a much smaller wire representation than the public JSON
	// response. Keep full snippets until that representation is budgeted below.
	if flags.Format == "agent" {
		flags.MaxContextBytes = 0
	}
	response, err := sem.SearchRepository(ctx, repo, opts.Version, flags.Query, sem.SearchOptions{
		Worktree:          flags.Worktree,
		IgnoreFiles:       flags.IgnoreFiles,
		IncludeFiles:      flags.IncludeFiles,
		Profile:           profile,
		TopK:              flags.TopK,
		ContextLines:      flags.ContextLines,
		MaxRegionLines:    flags.MaxRegionLines,
		MaxSnippetLines:   flags.MaxSnippetLines,
		MaxRegionsPerFile: flags.MaxRegionsPerFile,
		CacheDir:          cacheDir,
		DisableCache:      flags.DisableCache,
		MaxIndexedFiles:   flags.MaxIndexedFiles,
		IndexAllFiles:     flags.IndexAllFiles,
		MaxContextBytes:   flags.MaxContextBytes,
	})
	if err != nil {
		return err
	}
	if err := response.Validate(); err != nil {
		return err
	}
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "ndjson":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(map[string]any{
			"record_type": "search_header",
			"query":       response.Query,
			"repo_root":   response.RepoRoot,
			"commit":      response.Commit,
			"tree":        response.Tree,
			"profile":     response.Profile,
		}); err != nil {
			return err
		}
		for _, result := range response.Results {
			if err := encoder.Encode(struct {
				RecordType string `json:"record_type"`
				sem.SearchResult
			}{RecordType: "search_result", SearchResult: result}); err != nil {
				return err
			}
		}
		return encoder.Encode(map[string]any{
			"record_type":      "search_summary",
			"stats":            response.Stats,
			"warnings":         response.Warnings,
			"partial_failures": response.PartialFailures,
			"completeness":     response.Completeness,
		})
	case "text":
		return writeTextSearch(opts.Stdout, response)
	case "agent":
		return writeAgentSearch(opts.Stdout, response, contextBudget)
	default:
		return fmt.Errorf("search --format must be json, ndjson, text, or agent, got %q", flags.Format)
	}
}

// writeTextSearch renders the default `--format text` output. It is tiered: the
// first two ranks are where an agent actually reads the match, so they keep the
// full snippet + signals rendering. Every lower rank is overwhelmingly used only
// to decide "is this worth opening", so a full snippet there is pure token waste
// that crowds out the context an agent still has to load — collapse rank 3+ to a
// single terse "N. path:line symbol" locator line instead.
func writeTextSearch(out interface{ Write([]byte) (int, error) }, response sem.SearchResponse) error {
	for _, result := range response.Results {
		name := result.QualifiedName
		if name == "" {
			name = result.SymbolName
		}
		if result.Rank > 2 {
			line := result.FocusLine
			if line <= 0 {
				line = result.StartLine
			}
			if name != "" {
				fmt.Fprintf(out, "%d. %s:%d %s\n", result.Rank, result.FilePath, line, name)
			} else {
				fmt.Fprintf(out, "%d. %s:%d\n", result.Rank, result.FilePath, line)
			}
			continue
		}
		fmt.Fprintf(out, "%d. %s:%d-%d score=%.4f", result.Rank, result.FilePath, result.StartLine, result.EndLine, result.Score)
		if name != "" {
			fmt.Fprintf(out, " symbol=%s", name)
		}
		fmt.Fprintf(out, " signals=%s\n%s\n\n", strings.Join(result.Signals, ","), result.Snippet)
	}
	return nil
}

func writeAgentSearch(out interface{ Write([]byte) (int, error) }, response sem.SearchResponse, budget int) error {
	results := response.Results
	stats := response.Stats
	cacheState := "miss"
	if stats.IndexCacheHit {
		cacheState = "hit"
	}
	fullHeader := []byte(fmt.Sprintf(
		"Index: cache-%s (%dms) | Query: %dms | Preselect: %dms | Total: %dms\n",
		cacheState,
		stats.IndexLatencyMS,
		stats.QueryLatencyMS,
		stats.PreselectLatencyMS,
		stats.TotalLatencyMS,
	))
	fullDiagnostics, compactDiagnostics := agentSearchDiagnostics(response)
	if budget <= 0 {
		payload := append([]byte{}, fullHeader...)
		payload = append(payload, fullDiagnostics...)
		if len(results) == 0 {
			payload = append(payload, "No search results.\n"...)
		} else {
			payload = append(payload, fitAgentSearchResults(results, 0)...)
		}
		_, err := out.Write(payload)
		return err
	}

	// Preserve retrieval usefulness under tight budgets. The full telemetry
	// header is preferred, but a compact equivalent leaves room for the
	// top-ranked location at budgets where the expanded diagnostic otherwise
	// crowds out every result.
	compactHeader := []byte(fmt.Sprintf(
		"I:%s/%d Q:%d P:%d T:%d\n",
		cacheState,
		stats.IndexLatencyMS,
		stats.QueryLatencyMS,
		stats.PreselectLatencyMS,
		stats.TotalLatencyMS,
	))
	legacyHeader := []byte(fmt.Sprintf("Index: cache-%s (%dms)\n", cacheState, stats.IndexLatencyMS))
	diagnosticVariants := [][]byte{fullDiagnostics}
	if !bytes.Equal(fullDiagnostics, compactDiagnostics) {
		diagnosticVariants = append(diagnosticVariants, compactDiagnostics)
	}
	for _, header := range [][]byte{fullHeader, compactHeader, legacyHeader} {
		for _, diagnostics := range diagnosticVariants {
			remaining := budget - len(header) - len(diagnostics)
			if remaining <= 0 {
				continue
			}
			if len(results) == 0 {
				noResults := []byte("No search results.\n")
				if len(noResults) <= remaining {
					payload := append(append([]byte{}, header...), diagnostics...)
					_, err := out.Write(append(payload, noResults...))
					return err
				}
				continue
			}
			formatted := fitAgentSearchResults(results, remaining)
			if len(formatted) > 0 {
				payload := append(append([]byte{}, header...), diagnostics...)
				_, err := out.Write(append(payload, formatted...))
				return err
			}
		}
	}

	// Some positive budgets cannot hold even a single ranked location. Preserve
	// a degraded-coverage marker ahead of telemetry when one is required, and
	// never exceed the caller's exact byte cap.
	payload := legacyHeader
	if len(compactDiagnostics) > 0 {
		marker := "!N"
		if len(response.PartialFailures) > 0 {
			marker = "!D"
		}
		combined := []byte(fmt.Sprintf("Index: cache-%s%s\n", cacheState, marker))
		if len(combined) <= budget {
			payload = combined
		} else {
			payload = []byte(fmt.Sprintf("%s I:%s\n", marker, cacheState))
		}
	}
	if len(payload) > budget {
		payload = payload[:budget]
	}
	_, err := out.Write(payload)
	return err
}

func agentSearchDiagnostics(response sem.SearchResponse) ([]byte, []byte) {
	if len(response.Warnings) == 0 && len(response.PartialFailures) == 0 {
		return nil, nil
	}
	languages, files := searchCompletenessCounts(response.Completeness)
	level := "notice"
	if len(response.PartialFailures) > 0 {
		level = "degraded"
	}
	var full bytes.Buffer
	fmt.Fprintf(&full, "Coverage: %s (%d language%s/%d file%s; %d warning%s; %d partial failure%s)\n",
		level, languages, pluralSuffix(languages), files, pluralSuffix(files),
		len(response.Warnings), pluralSuffix(len(response.Warnings)),
		len(response.PartialFailures), pluralSuffix(len(response.PartialFailures)),
	)
	const maxAgentDiagnostics = 3
	warningsVisible, failuresVisible := agentDiagnosticVisibility(
		len(response.Warnings), len(response.PartialFailures), maxAgentDiagnostics,
	)
	for _, warning := range response.Warnings[:warningsVisible] {
		fmt.Fprintf(&full, "- warning %s%s\n", warning.Code, agentDiagnosticPath(warning.FilePath))
	}
	for _, failure := range response.PartialFailures[:failuresVisible] {
		fmt.Fprintf(&full, "- partial %s%s\n", failure.Code, agentDiagnosticPath(failure.FilePath))
	}
	visible := warningsVisible + failuresVisible
	if omitted := len(response.Warnings) + len(response.PartialFailures) - visible; omitted > 0 {
		fmt.Fprintf(&full, "- ... %d more diagnostic%s in JSON output\n", omitted, pluralSuffix(omitted))
	}
	marker := "N"
	if level == "degraded" {
		marker = "D"
	}
	compact := []byte(fmt.Sprintf("!%s W%d F%d L%d/%d\n",
		marker, len(response.Warnings), len(response.PartialFailures), languages, files))
	return full.Bytes(), compact
}

func agentDiagnosticVisibility(warnings, failures, limit int) (int, int) {
	if limit <= 0 {
		return 0, 0
	}
	warningsVisible := minIntCLI(warnings, limit)
	failuresVisible := minIntCLI(failures, limit-warningsVisible)
	if failures > 0 && failuresVisible == 0 {
		warningsVisible--
		failuresVisible = 1
	}
	return warningsVisible, failuresVisible
}

func searchCompletenessCounts(report sem.CompletenessReport) (int, int) {
	files := 0
	for _, language := range report.Languages {
		files += language.Files
	}
	return len(report.Languages), files
}

func agentDiagnosticPath(path string) string {
	if path == "" {
		return ""
	}
	return ": " + path
}

func fitAgentSearchResults(results []sem.SearchResult, budget int) []byte {
	if budget <= 0 {
		return renderAgentSearchResults(results, nil)
	}
	for count := len(results); count > 0; count-- {
		available := budget - (count - 1)
		if available <= 0 {
			continue
		}
		resultBudgets := rankedAgentSearchBudgets(count, available)
		formatted := renderAgentSearchResults(results[:count], resultBudgets)
		if len(formatted) <= budget {
			return formatted
		}
	}
	return nil
}

func rankedAgentSearchBudgets(count, budget int) []int {
	if count <= 0 {
		return nil
	}
	budgets := make([]int, count)
	minimum := minIntCLI(128, budget/count)
	remaining := budget - minimum*count
	weightTotal := count * (count + 1) / 2
	allocated := 0
	for index := range budgets {
		extra := 0
		if weightTotal > 0 {
			extra = remaining * (count - index) / weightTotal
		}
		budgets[index] = minimum + extra
		allocated += extra
	}
	// Integer division can leave a few bytes. They are most valuable at the
	// top of the ranking, where agents make their first navigation decision.
	for index := 0; allocated < remaining; index = (index + 1) % count {
		budgets[index]++
		allocated++
	}
	return budgets
}

func renderAgentSearchResults(results []sem.SearchResult, budgets []int) []byte {
	var output bytes.Buffer
	wrote := false
	for index, result := range results {
		budget := 0
		if index < len(budgets) {
			budget = budgets[index]
		}
		block := agentSearchBlock(result, budget)
		if len(block) == 0 {
			return nil
		}
		if wrote {
			output.WriteByte('\n')
		}
		output.Write(block)
		wrote = true
	}
	return output.Bytes()
}

func agentSearchBlock(result sem.SearchResult, budget int) []byte {
	name := result.QualifiedName
	if name == "" {
		name = result.SymbolName
	}
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	snippetStart := result.SnippetStartLine
	if snippetStart <= 0 {
		snippetStart = result.StartLine
	}
	if snippetStart <= 0 {
		snippetStart = 1
	}
	focusLine := result.FocusLine
	if focusLine <= 0 {
		focusLine = snippetStart
	}
	focus := focusLine - snippetStart
	if focus < 0 || focus >= len(lines) {
		focus = len(lines) / 2
		focusLine = snippetStart + focus
	}
	if len(lines) == 0 {
		return fitAgentSearchLocation(result.Rank, result.FilePath, focusLine, name, budget)
	}

	// Prefer the widest balanced span containing the focus line. The location
	// in the header is rebuilt for each candidate, so it always describes the
	// lines actually displayed rather than the original untrimmed region.
	for span := len(lines); span > 0; span-- {
		leftMin := focus - span + 1
		if leftMin < 0 {
			leftMin = 0
		}
		leftMax := focus
		if limit := len(lines) - span; leftMax > limit {
			leftMax = limit
		}
		bestBalance := len(lines) + 1
		var best []byte
		for left := leftMin; left <= leftMax; left++ {
			right := left + span - 1
			text := strings.Join(lines[left:right+1], "\n")
			startLine, endLine := snippetStart+left, snippetStart+right
			for _, header := range agentSearchLocationHeaders(result.Rank, result.FilePath, startLine, endLine, focusLine, name) {
				candidate := []byte(header + text + "\n")
				if budget <= 0 || len(candidate) <= budget {
					balance := focus - left - (right - focus)
					if balance < 0 {
						balance = -balance
					}
					if best == nil || balance < bestBalance {
						best, bestBalance = candidate, balance
					}
					break
				}
			}
		}
		if best != nil {
			return best
		}
	}
	return fitAgentSearchLocation(result.Rank, result.FilePath, focusLine, name, budget)
}

func agentSearchLocationHeaders(rank int, path string, start, end, focus int, name string) []string {
	location := fmt.Sprintf("%d. %s:%d", rank, path, start)
	if end != start {
		location += fmt.Sprintf("-%d", end)
	}
	rich := location
	if name != "" {
		rich += " " + name
	}
	rich += fmt.Sprintf(" [focus:%d]\n", focus)
	compact := location
	if name != "" {
		compact += " " + name
	}
	compact += " *\n"
	minimal := fmt.Sprintf("%s:%d *\n", path, focus)
	return []string{rich, compact, minimal}
}

func fitAgentSearchLocation(rank int, path string, focus int, name string, budget int) []byte {
	for _, header := range agentSearchLocationHeaders(rank, path, focus, focus, focus, name) {
		if budget <= 0 || len(header) <= budget {
			return []byte(header)
		}
	}
	return nil
}

func minIntCLI(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func parseSearchFlags(args []string) (searchFlags, []string, error) {
	flags := searchFlags{Format: "json", Profile: "syntax-only", Worktree: true, MaxContextBytes: 16 * 1024}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Repo, i = value, next
		case "--query":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Query, i = value, next
		case "--format":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Format, i = value, next
		case "--profile":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Profile, i = value, next
		case "--top-k":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.TopK, i = value, next
		case "--context-lines":
			value, next, err := searchNonNegativeIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.ContextLines, i = value, next
		case "--max-region-lines":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxRegionLines, i = value, next
		case "--max-snippet-lines":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxSnippetLines, i = value, next
		case "--max-regions-per-file":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxRegionsPerFile, i = value, next
		case "--ignore-file":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.IgnoreFiles, i = append(flags.IgnoreFiles, value), next
		case "--include-file":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.IncludeFiles, i = append(flags.IncludeFiles, value), next
		case "--cache-dir":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.CacheDir, i = value, next
		case "--no-cache":
			flags.DisableCache = true
		case "--max-indexed-files":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxIndexedFiles, i = value, next
		case "--index-all-files":
			flags.IndexAllFiles = true
		case "--max-context-bytes":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxContextBytes, i = value, next
		case "--worktree", "--no-network":
			if args[i] == "--worktree" {
				flags.Worktree = true
			}
		case "--head":
			flags.Worktree = false
		default:
			rest = append(rest, args[i])
		}
	}
	return flags, rest, nil
}

func searchFlagValue(args []string, index int) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s requires a value", args[index])
	}
	return args[index+1], index + 1, nil
}

func searchPositiveIntFlag(args []string, index int) (int, int, error) {
	value, next, err := searchNonNegativeIntFlag(args, index)
	if err != nil {
		return 0, index, err
	}
	if value == 0 {
		return 0, index, fmt.Errorf("%s must be positive", args[index])
	}
	return value, next, nil
}

func searchNonNegativeIntFlag(args []string, index int) (int, int, error) {
	raw, next, err := searchFlagValue(args, index)
	if err != nil {
		return 0, index, err
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, index, fmt.Errorf("%s requires a non-negative integer", args[index])
	}
	return value, next, nil
}
