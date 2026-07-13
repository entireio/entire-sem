package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/entireio/entire-sem/internal/gitutil"
	"github.com/entireio/entire-sem/internal/sem"
)

type Options struct {
	Version string
	Env     EntireEnv
	Stdout  io.Writer
	Stderr  io.Writer
}

func Execute(version string, args []string) error {
	return Run(context.Background(), Options{
		Version: version,
		Env:     EnvFromOS(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}, args)
}

func Run(ctx context.Context, opts Options, args []string) error {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	if len(args) == 0 {
		printHelp(opts.Stdout)
		return nil
	}

	switch args[0] {
	case "diff":
		return runDiff(ctx, opts, args[1:])
	case "commit":
		return runCommit(ctx, opts, args[1:])
	case "checkpoint":
		return runCheckpoint(ctx, opts, args[1:])
	case "analyze":
		return runAnalyze(ctx, opts, args[1:])
	case "doctor":
		return runDoctor(ctx, opts, args[1:])
	case "capabilities":
		return runCapabilities(opts, args[1:])
	case "snapshot":
		return runProviderRecords(ctx, opts, args[1:], "snapshot")
	case "symbols":
		return runProviderRecords(ctx, opts, args[1:], "symbols")
	case "edges":
		return runProviderRecords(ctx, opts, args[1:], "edges")
	case "search":
		return runSearch(ctx, opts, args[1:])
	case "version", "--version", "-v":
		if len(args) > 1 && args[1] == "--json" {
			return json.NewEncoder(opts.Stdout).Encode(map[string]string{
				"provider": sem.ProviderName,
				"version":  opts.Version,
			})
		}
		fmt.Fprintln(opts.Stdout, opts.Version)
		return nil
	case "help", "--help", "-h":
		printHelp(opts.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `entire-sem adds entity-level context to Entire checkpoints.

Usage:
  entire sem commit [rev] [--json] [--repo path]
  entire sem checkpoint <checkpoint-id> [--json] [--repo path]
  entire sem diff --base <rev> --head <rev> [--json] [--repo path] [-- path...]
  entire sem analyze [--base <rev>] [--head <rev>] [--json] [--repo path] [-- path...]
  entire sem doctor [--json]
  entire sem version [--json]
  entire sem capabilities --json
  entire sem snapshot --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire sem symbols --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire sem edges --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire sem search --query "issue or concept" --repo . [--format json|ndjson|text] [--top-k 20] [--max-context-bytes 16384] [--head] [--profile syntax-only|fast|full] [--max-indexed-files 96|--index-all-files] [--cache-dir path|--no-cache]`)
}

func runDoctor(ctx context.Context, opts Options, args []string) error {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 1 || (len(args) == 1 && !asJSON) {
		return errors.New("doctor accepts only --json")
	}
	report := map[string]any{
		"provider":  sem.ProviderName,
		"version":   opts.Version,
		"no_egress": true,
		"environment": map[string]string{
			envCLIVersion:    valueOrUnset(opts.Env.CLIVersion),
			envRepoRoot:      valueOrUnset(opts.Env.RepoRoot),
			envPluginDataDir: valueOrUnset(opts.Env.PluginDataDir),
		},
		"phase_1_local_only": map[string]bool{
			"fetch_remote_code":              false,
			"download_grammars_or_assets":    false,
			"upload_telemetry":               false,
			"call_hosted_model_apis":         false,
			"call_remote_embedding_provider": false,
			"perform_network_discovery":      false,
		},
	}
	if !asJSON {
		fmt.Fprintf(opts.Stdout, "ENTIRE_CLI_VERSION=%s\n", valueOrUnset(opts.Env.CLIVersion))
		fmt.Fprintf(opts.Stdout, "ENTIRE_REPO_ROOT=%s\n", valueOrUnset(opts.Env.RepoRoot))
		fmt.Fprintf(opts.Stdout, "ENTIRE_PLUGIN_DATA_DIR=%s\n", valueOrUnset(opts.Env.PluginDataDir))
		fmt.Fprintln(opts.Stdout, "no_egress=true")
	}

	if opts.Env.PluginDataDir != "" {
		if err := os.MkdirAll(opts.Env.PluginDataDir, 0o700); err != nil {
			return fmt.Errorf("create plugin data dir: %w", err)
		}
		probe, err := os.CreateTemp(opts.Env.PluginDataDir, ".write-test-*")
		if err != nil {
			return fmt.Errorf("write plugin data dir: %w", err)
		}
		probeName := probe.Name()
		if err := probe.Close(); err != nil {
			return fmt.Errorf("close plugin data probe: %w", err)
		}
		if err := os.Remove(probeName); err != nil {
			return fmt.Errorf("remove plugin data probe: %w", err)
		}
		report["plugin_data_dir"] = "writable"
		if !asJSON {
			fmt.Fprintln(opts.Stdout, "plugin_data_dir=writable")
		}
	}

	repo, err := resolveRepo(ctx, opts.Env, "")
	if err != nil {
		report["repo_root"] = ""
		report["repo_error"] = err.Error()
		if asJSON {
			return json.NewEncoder(opts.Stdout).Encode(report)
		}
		fmt.Fprintf(opts.Stdout, "repo_root=%s\n", valueOrUnset(""))
		fmt.Fprintf(opts.Stdout, "repo_error=%s\n", err)
		return nil
	}
	report["repo_root"] = repo
	if asJSON {
		return json.NewEncoder(opts.Stdout).Encode(report)
	}
	fmt.Fprintf(opts.Stdout, "repo_root=%s\n", repo)
	return nil
}

func runCapabilities(opts Options, args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("capabilities requires --json")
	}
	return json.NewEncoder(opts.Stdout).Encode(sem.Capabilities())
}

func runProviderRecords(ctx context.Context, opts Options, args []string, mode string) error {
	flags, rest, err := parseProviderFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("%s received unexpected arguments: %s", mode, strings.Join(rest, " "))
	}
	if flags.Format != "ndjson" {
		return fmt.Errorf("%s requires --format ndjson", mode)
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	if mode != "snapshot" && mode != "symbols" && mode != "edges" {
		return fmt.Errorf("unknown provider record mode %q", mode)
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	options := sem.ProviderSnapshotOptions{
		NoNetwork:    flags.NoNetwork,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFiles,
		IncludeFiles: flags.IncludeFiles,
		Profile:      profile,
	}
	if flags.Progress {
		options.Progress = func(event sem.ProgressEvent) {
			fmt.Fprintf(opts.Stderr, "sem progress phase=%s files=%d/%d symbols=%d relations=%d heap=%d rss=%d elapsed=%s\n",
				event.Phase,
				event.FilesDone,
				event.FilesTotal,
				event.Symbols,
				event.Relations,
				event.HeapAlloc,
				event.MaxRSSBytes,
				event.Elapsed.Round(time.Millisecond),
			)
		}
	}
	// Stream records straight to stdout so peak memory does not scale with the
	// relation count on large repositories.
	encoder := json.NewEncoder(opts.Stdout)
	encoder.SetEscapeHTML(false) // match json.Marshal used elsewhere (no < escaping)
	return sem.StreamSnapshot(ctx, repo, opts.Version, options, func(record any) error {
		if !includeRecord(mode, record) {
			return nil
		}
		return encoder.Encode(record)
	})
}

// includeRecord filters streamed records for the symbols and edges modes, which
// emit a subset of the full snapshot.
func includeRecord(mode string, record any) bool {
	switch record.(type) {
	case sem.FileRecord, sem.ExternalRecord:
		return mode == "snapshot"
	case sem.SymbolRecord:
		return mode == "snapshot" || mode == "symbols"
	case sem.RelationRecord:
		return mode == "snapshot" || mode == "edges"
	default: // header, summary
		return true
	}
}

type commonFlags struct {
	Repo string
	JSON bool
}

type providerFlags struct {
	Repo         string
	Format       string
	Profile      string
	NoNetwork    bool
	Worktree     bool
	Progress     bool
	IgnoreFiles  []string
	IncludeFiles []string
}

// parseProfile validates the --profile value. Empty defaults to full.
func parseProfile(value string) (sem.Profile, error) {
	switch value {
	case "", "full":
		return sem.ProfileFull, nil
	case "fast":
		return sem.ProfileFast, nil
	case "syntax-only":
		return sem.ProfileSyntaxOnly, nil
	default:
		return "", fmt.Errorf("unknown --profile %q (want full, fast, or syntax-only)", value)
	}
}

func parseProviderFlags(args []string) (providerFlags, []string, error) {
	flags := providerFlags{Format: "ndjson"}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--repo requires a value")
			}
			flags.Repo = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--format requires a value")
			}
			flags.Format = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--profile requires a value")
			}
			flags.Profile = args[i]
		case "--no-network":
			flags.NoNetwork = true
		case "--worktree":
			flags.Worktree = true
		case "--progress":
			flags.Progress = true
		case "--ignore-file":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--ignore-file requires a value")
			}
			flags.IgnoreFiles = append(flags.IgnoreFiles, args[i])
		case "--include-file":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--include-file requires a value")
			}
			flags.IncludeFiles = append(flags.IncludeFiles, args[i])
		default:
			rest = append(rest, args[i])
		}
	}
	return flags, rest, nil
}

func parseCommonFlags(args []string) (commonFlags, []string, error) {
	var flags commonFlags
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			flags.JSON = true
		case "--repo":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--repo requires a value")
			}
			flags.Repo = args[i]
		case "--":
			rest = append(rest, args[i+1:]...)
			return flags, rest, nil
		default:
			rest = append(rest, arg)
		}
	}
	return flags, rest, nil
}

func runCommit(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	rev := "HEAD"
	if len(rest) > 0 {
		rev = rest[0]
	}
	if len(rest) > 1 {
		return errors.New("commit accepts at most one revision")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	base, err := gitutil.FirstParent(ctx, repo, rev)
	if err != nil {
		return err
	}
	return analyzeAndPrint(ctx, opts.Stdout, repo, base, rev, nil, flags.JSON)
}

func runCheckpoint(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("checkpoint requires exactly one checkpoint ID")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	result, err := sem.AnalyzeCheckpoint(ctx, repo, rest[0])
	if err != nil {
		return err
	}
	return printResult(opts.Stdout, result, flags.JSON)
}

func runAnalyze(ctx context.Context, opts Options, args []string) error {
	return runDiff(ctx, opts, args)
}

func runDiff(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}

	base := "HEAD~1"
	head := "HEAD"
	var paths []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--base":
			i++
			if i >= len(rest) {
				return errors.New("--base requires a value")
			}
			base = rest[i]
		case "--head":
			i++
			if i >= len(rest) {
				return errors.New("--head requires a value")
			}
			head = rest[i]
		default:
			paths = append(paths, rest[i])
		}
	}

	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	return analyzeAndPrint(ctx, opts.Stdout, repo, base, head, paths, flags.JSON)
}

func resolveRepo(ctx context.Context, env EntireEnv, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env.RepoRoot != "" {
		return env.RepoRoot, nil
	}
	return gitutil.RepoRoot(ctx, ".")
}

func analyzeAndPrint(ctx context.Context, out io.Writer, repo, base, head string, paths []string, asJSON bool) error {
	result, err := sem.AnalyzeGitRange(ctx, repo, base, head, paths)
	if err != nil {
		return err
	}
	return printResult(out, result, asJSON)
}

func printResult(out io.Writer, result sem.Result, asJSON bool) error {
	if asJSON {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
		return nil
	}
	sem.WriteText(out, result)
	return nil
}
