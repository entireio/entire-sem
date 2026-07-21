package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
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
	case "index":
		return runIndex(ctx, opts, args[1:])
	case "neighbors":
		return runNeighbors(ctx, opts, args[1:])
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
	fmt.Fprintln(out, `entire-graph adds entity-level context to Entire checkpoints.

Usage:
  entire graph commit [rev] [--json] [--repo path]
  entire graph checkpoint <checkpoint-id> [--json] [--repo path]
  entire graph diff --base <rev> --head <rev> [--json] [--repo path] [-- path...]
  entire graph analyze [--base <rev>] [--head <rev>] [--json] [--repo path] [-- path...]
  entire graph doctor [--json]
  entire graph version [--json]
  entire graph capabilities --json
  entire graph snapshot --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire graph symbols --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire graph edges --repo . --format ndjson [--worktree] [--progress] [--ignore-file path] [--include-file path]
  entire graph index --repo . [--profile syntax-only|fast|full] [--cache-dir path] [--format json] [--head] [--ignore-file path] [--include-file path]
  entire graph search --query "issue or concept" --repo . [--format json|ndjson|text|agent] [--top-k 20] [--max-context-bytes 16384] [--head] [--profile syntax-only|fast|full] [--max-indexed-files n|--index-all-files] [--cache-dir path|--no-cache]
  entire graph neighbors --symbol NAME --repo . [--file path] [--relation CALLS] [--direction both|in|out] [--depth 1|2] [--limit 20] [--format json|text|agent] [--max-context-bytes 16384] [--head] [--cache-dir path|--no-cache] [--internal-only] [--exclude-tests]`)
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
			fmt.Fprintf(opts.Stderr, "graph progress phase=%s files=%d/%d symbols=%d relations=%d heap=%d rss=%d elapsed=%s\n",
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

	// Targeted edge query: when --to/--from/--relation is set, emit only matching
	// relations (plus header/summary), never files/symbols. Turns "callers of X"
	// into a tiny reply instead of dumping the whole graph for the caller to grep.
	// Streaming-safe: idMatches keys off the stable ID's trailing name segment, so
	// no in-memory symbol table is needed. Only meaningful in edges/snapshot modes.
	// Capture the summary as it streams past so we can loudly warn on a partial
	// parse. Without this the CLI discards the summary and a run that silently
	// parsed only a fraction of the repo (e.g. a mis-scoped subdir) looks clean.
	var summary *sem.SnapshotSummary
	capture := func(record any) {
		if s, ok := record.(sem.SnapshotSummary); ok {
			s := s
			summary = &s
		}
	}

	filterActive := flags.To != "" || flags.From != "" || len(flags.Relation) > 0
	if filterActive && mode == "symbols" {
		return fmt.Errorf("--to/--from/--relation filter relations; use `edges` (not `symbols`)")
	}
	if filterActive {
		var matched int
		if err := sem.StreamSnapshot(ctx, repo, opts.Version, options, func(record any) error {
			capture(record)
			switch r := record.(type) {
			case sem.RelationRecord:
				if !relationMatches(r, flags) {
					return nil
				}
				matched++
				return encoder.Encode(r)
			case sem.FileRecord, sem.ExternalRecord, sem.SymbolRecord:
				return nil // suppressed for a targeted edge query
			default: // header, summary
				return encoder.Encode(record)
			}
		}); err != nil {
			return err
		}
		warnIfPartial(opts.Stderr, flags.Worktree, summary)
		fmt.Fprintf(opts.Stderr, "graph: %d edge(s) matched (--to=%q --from=%q --relation=%s)\n",
			matched, flags.To, flags.From, strings.Join(flags.Relation, ","))
		return nil
	}

	// Whole-graph dump (no targeted filter): serve from the tree-hash record
	// cache when possible. The cache is keyed on the HEAD tree, the mode, and the
	// output-affecting options, so a repeat call on an unchanged HEAD skips the
	// expensive re-index. It is deliberately bypassed for --worktree (the working
	// tree may differ from HEAD) and, by returning above, for targeted queries.
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	useCache := !flags.DisableCache && !flags.Worktree && cacheDir != ""
	var tree string
	if useCache {
		if t, err := gitutil.RevParse(ctx, repo, "HEAD^{tree}"); err == nil && t != "" {
			tree = t
		} else {
			useCache = false
		}
	}
	if useCache {
		if records, cachedSummary, hit, err := sem.LoadProviderRecords(repo, opts.Version, tree, mode, cacheDir, options); err == nil && hit {
			if _, err := opts.Stdout.Write(records); err != nil {
				return err
			}
			warnIfPartial(opts.Stderr, flags.Worktree, cachedSummary)
			return nil
		}
	}

	// On a miss, tee the streamed NDJSON into a buffer so we can persist it after
	// a successful run without a second pass over the graph.
	var recordBuf bytes.Buffer
	if useCache {
		encoder = json.NewEncoder(io.MultiWriter(opts.Stdout, &recordBuf))
		encoder.SetEscapeHTML(false)
	}
	if err := sem.StreamSnapshot(ctx, repo, opts.Version, options, func(record any) error {
		capture(record)
		if !includeRecord(mode, record) {
			return nil
		}
		return encoder.Encode(record)
	}); err != nil {
		return err
	}
	warnIfPartial(opts.Stderr, flags.Worktree, summary)
	if useCache {
		// Best effort: a failed cache write never fails the command.
		_ = sem.StoreProviderRecords(repo, opts.Version, tree, mode, cacheDir, options, recordBuf.Bytes(), summary)
	}
	return nil
}

// warnIfPartial prints a loud stderr banner when the snapshot did not fully cover
// the repository, so a silent partial parse (the #1 sharp edge: running in a
// mis-scoped subdir without --worktree, which indexes only a stray config file
// and reports "ok") becomes impossible to miss. Silent on a clean "ok" run.
func warnIfPartial(w io.Writer, worktree bool, s *sem.SnapshotSummary) {
	if s == nil {
		return
	}
	level := s.Stats.CompletenessLevel
	if level == "" || level == "ok" {
		return
	}
	fmt.Fprintf(w, "\n⚠️  graph is %s: parsed %d/%d files, %d symbols, %d relations (languages: %s).\n",
		strings.ToUpper(level), s.Stats.ParsedFiles, s.Stats.Files, s.Stats.Symbols,
		s.Stats.Relations, strings.Join(s.Languages, ", "))
	switch {
	case s.Stats.Files <= 2 && !worktree:
		fmt.Fprintf(w, "   Only %d file(s) were discovered — you may be indexing a subdirectory or an\n"+
			"   unexpected commit. Run from the repo root, or pass --worktree to index the\n"+
			"   working tree instead of HEAD.\n", s.Stats.Files)
	case s.Stats.ParsedFiles*2 < s.Stats.Files:
		fmt.Fprintf(w, "   Over half the discovered files were not parsed (unsupported language or\n"+
			"   parse errors). Graph queries will miss code in those files.\n")
	default:
		fmt.Fprintf(w, "   The graph is incomplete; treat query results as partial.\n")
	}
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
	// Targeted edge filters (edges mode). When any is set the command emits only
	// the matching relation records (plus header/summary) instead of the whole
	// graph, so "callers of X" is a tiny reply rather than a 50MB dump that the
	// caller then greps client-side. --to/--from match a symbol by full stable ID
	// or by trailing name segment (IDs are `...:kind:name`); --relation is one or
	// more edge types (comma-separated, case-insensitive), e.g. CALLS,REFERENCES.
	To       string
	From     string
	Relation []string
	// CacheDir/DisableCache control the tree-hash record cache. Empty CacheDir
	// falls back to ENTIRE_PLUGIN_DATA_DIR; --no-cache disables it entirely.
	CacheDir     string
	DisableCache bool
}

// idMatches reports whether a stable symbol ID matches a user-supplied selector:
// either the exact ID, or a trailing name segment (IDs end `...:kind:name`, so
// `getConfPath` or `function:getConfPath` both select it). Streaming-safe — no
// symbol table needed.
func idMatches(id, sel string) bool {
	return id == sel || strings.HasSuffix(id, ":"+sel)
}

// relationMatches applies the --to/--from/--relation predicate to one edge.
func relationMatches(r sem.RelationRecord, f providerFlags) bool {
	if f.To != "" && !idMatches(r.ToID, f.To) {
		return false
	}
	if f.From != "" && !idMatches(r.FromID, f.From) {
		return false
	}
	if len(f.Relation) > 0 {
		t := strings.ToUpper(r.Type)
		hit := false
		for _, want := range f.Relation {
			if t == want {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
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
		case "--cache-dir":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--cache-dir requires a value")
			}
			flags.CacheDir = args[i]
		case "--no-cache":
			flags.DisableCache = true
		case "--to":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--to requires a value")
			}
			flags.To = args[i]
		case "--from":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--from requires a value")
			}
			flags.From = args[i]
		case "--relation":
			i++
			if i >= len(args) {
				return flags, nil, errors.New("--relation requires a value")
			}
			for _, part := range strings.Split(args[i], ",") {
				if part = strings.ToUpper(strings.TrimSpace(part)); part != "" {
					flags.Relation = append(flags.Relation, part)
				}
			}
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
