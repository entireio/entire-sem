package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/entireio/entire-sem/internal/sem"
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
			"record_type": "search_summary",
			"stats":       response.Stats,
			"warnings":    response.Warnings,
		})
	case "text":
		for _, result := range response.Results {
			name := result.QualifiedName
			if name == "" {
				name = result.SymbolName
			}
			fmt.Fprintf(opts.Stdout, "%d. %s:%d-%d score=%.4f", result.Rank, result.FilePath, result.StartLine, result.EndLine, result.Score)
			if name != "" {
				fmt.Fprintf(opts.Stdout, " symbol=%s", name)
			}
			fmt.Fprintf(opts.Stdout, " signals=%s\n%s\n\n", strings.Join(result.Signals, ","), result.Snippet)
		}
		return nil
	default:
		return fmt.Errorf("search --format must be json, ndjson, or text, got %q", flags.Format)
	}
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
			value, next, err := searchNonNegativeIntFlag(args, i)
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
