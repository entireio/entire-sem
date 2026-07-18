package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

type indexFlags struct {
	Repo         string
	Profile      string
	CacheDir     string
	Format       string
	IgnoreFiles  []string
	IncludeFiles []string
}

type indexResponse struct {
	FormatVersion   int                    `json:"format_version"`
	Provider        string                 `json:"provider"`
	ProviderVersion string                 `json:"provider_version"`
	RepoRoot        string                 `json:"repo_root"`
	Commit          string                 `json:"commit"`
	Tree            string                 `json:"tree"`
	Profile         string                 `json:"profile"`
	IndexCacheHit   bool                   `json:"index_cache_hit"`
	IndexLatencyMS  int64                  `json:"index_latency_ms"`
	Counts          sem.ProviderStats      `json:"counts"`
	Warnings        []sem.ProviderWarning  `json:"warnings"`
	PartialFailures []sem.PartialFailure   `json:"partial_failures"`
	Completeness    sem.CompletenessReport `json:"completeness"`
}

func runIndex(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseIndexFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("index received unexpected arguments: %s", strings.Join(rest, " "))
	}
	if flags.Format != "json" {
		return fmt.Errorf("index --format must be json, got %q", flags.Format)
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
	if cacheDir == "" {
		return errors.New("index requires --cache-dir or ENTIRE_PLUGIN_DATA_DIR")
	}
	started := time.Now()
	snapshot, cacheHit, err := sem.PreindexProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Profile:      profile,
		IgnoreFiles:  flags.IgnoreFiles,
		IncludeFiles: flags.IncludeFiles,
	}, cacheDir)
	if err != nil {
		return err
	}
	warnings := snapshot.Header.Warnings
	if warnings == nil {
		warnings = []sem.ProviderWarning{}
	}
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := indexResponse{
		FormatVersion:   1,
		Provider:        sem.ProviderName,
		ProviderVersion: opts.Version,
		RepoRoot:        snapshot.Header.RepoRoot,
		Commit:          snapshot.Header.Commit,
		Tree:            snapshot.Header.Tree,
		Profile:         snapshot.Header.Profile,
		IndexCacheHit:   cacheHit,
		IndexLatencyMS:  time.Since(started).Milliseconds(),
		Counts:          snapshot.Header.Stats,
		Warnings:        warnings,
		PartialFailures: partialFailures,
		Completeness:    snapshot.Header.Completeness,
	}
	encoder := json.NewEncoder(opts.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

func parseIndexFlags(args []string) (indexFlags, []string, error) {
	flags := indexFlags{Profile: "full", Format: "json"}
	var rest []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--repo":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Repo, index = value, next
		case "--profile":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Profile, index = value, next
		case "--cache-dir":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.CacheDir, index = value, next
		case "--ignore-file":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.IgnoreFiles, index = append(flags.IgnoreFiles, value), next
		case "--include-file":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.IncludeFiles, index = append(flags.IncludeFiles, value), next
		case "--format":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Format, index = value, next
		case "--head", "--no-network":
			// Indexing is always local-only and always targets committed HEAD.
		case "--worktree":
			return flags, nil, errors.New("index is HEAD-only; --worktree is not supported")
		default:
			rest = append(rest, args[index])
		}
	}
	return flags, rest, nil
}
