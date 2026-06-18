// Command sem-bench clones popular repositories per language and measures the
// semantic provider over them, emitting a machine-readable performance and
// quality report. Cloning (network) is a distinct phase from measurement, which
// runs the provider with NoNetwork so the measured path stays no-egress.
//
// Usage:
//
//	go run ./cmd/sem-bench -update-lock          # resolve and pin repo commits
//	go run ./cmd/sem-bench                        # full run using the lock file
//	go run ./cmd/sem-bench -languages Go,Rust -limit 3
//	go run ./cmd/sem-bench -skip-clone            # offline: measure existing clones
//
// Cloned repositories live under -cache (gitignored) and never enter our own
// commits. Pinning is via bench/repos.lock.json: commit it to make runs
// reproducible across work phases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/suhaanthayyil/entire-sem/internal/bench"
)

type manifest struct {
	Languages map[string][]string `json:"languages"`
}

type repoSpec struct {
	language string
	repoPath string // owner/name
	ref      string // optional manifest-pinned ref
}

func (r repoSpec) cloneURL() string { return "https://github.com/" + r.repoPath + ".git" }
func (r repoSpec) dirName() string  { return strings.ReplaceAll(r.repoPath, "/", "__") }

func main() {
	var (
		manifestPath = flag.String("manifest", "bench/repos.json", "path to the repo manifest")
		cacheDir     = flag.String("cache", "bench/.cache", "directory for cloned repos (gitignored)")
		outDir       = flag.String("out", "bench/results", "directory for the JSON report, or - for stdout")
		lockPath     = flag.String("lock", "bench/repos.lock.json", "path to the commit lock file")
		languages    = flag.String("languages", "", "comma-separated language filter (default: all)")
		limit        = flag.Int("limit", 0, "max repos per language (0 = all)")
		jobs         = flag.Int("jobs", 4, "concurrent clone jobs")
		depth        = flag.Int("depth", 1, "git clone depth")
		skipClone    = flag.Bool("skip-clone", false, "do not clone; measure repos already in cache")
		updateLock   = flag.Bool("update-lock", false, "resolve current commits and rewrite the lock file")
		providerVer  = flag.String("provider-version", "dev", "provider version label recorded in the report")
	)
	flag.Parse()

	if err := run(*manifestPath, *cacheDir, *outDir, *lockPath, *languages, *limit, *jobs, *depth, *skipClone, *updateLock, *providerVer); err != nil {
		fmt.Fprintln(os.Stderr, "sem-bench:", err)
		os.Exit(1)
	}
}

func run(manifestPath, cacheDir, outDir, lockPath, languages string, limit, jobs, depth int, skipClone, updateLock bool, providerVer string) error {
	specs, err := loadSpecs(manifestPath, languages, limit)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("no repositories selected")
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	resolved := map[string]string{} // repoPath -> sha
	var resolvedMu sync.Mutex

	if !skipClone {
		fmt.Fprintf(os.Stderr, "Cloning %d repositories into %s...\n", len(specs), cacheDir)
		cloneAll(ctx, specs, cacheDir, lock, depth, updateLock, jobs, func(repoPath, sha string) {
			resolvedMu.Lock()
			resolved[repoPath] = sha
			resolvedMu.Unlock()
		})
		if updateLock {
			for k, v := range resolved {
				lock[k] = v
			}
			if err := writeLock(lockPath, lock); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Wrote %d pinned commits to %s\n", len(lock), lockPath)
		}
	}

	fmt.Fprintln(os.Stderr, "Measuring (no-egress)...")
	var metrics []bench.RepoMetrics
	for _, spec := range specs {
		dir := filepath.Join(cacheDir, spec.language, spec.dirName())
		if _, statErr := os.Stat(dir); statErr != nil {
			metrics = append(metrics, bench.RepoMetrics{Name: spec.repoPath, Language: spec.language, Error: "not cloned"})
			fmt.Fprintf(os.Stderr, "  skip %-40s (not cloned)\n", spec.repoPath)
			continue
		}
		m, measureErr := bench.MeasureRepo(ctx, spec.repoPath, spec.language, dir, providerVer)
		if measureErr != nil {
			fmt.Fprintf(os.Stderr, "  FAIL %-40s %v\n", spec.repoPath, measureErr)
		} else {
			fmt.Fprintf(os.Stderr, "  ok   %-40s %6d files  %8d LOC  %7.0f LOC/s\n", spec.repoPath, m.Files, m.LOC, m.LOCPerSec)
		}
		metrics = append(metrics, m)
	}

	report := bench.BuildReport(time.Now().UTC().Format(time.RFC3339), providerVer, metrics)
	if err := emitReport(report, outDir); err != nil {
		return err
	}
	printSummary(report)
	return nil
}

func loadSpecs(manifestPath, languages string, limit int) ([]repoSpec, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var filter map[string]bool
	if strings.TrimSpace(languages) != "" {
		filter = map[string]bool{}
		for _, language := range strings.Split(languages, ",") {
			filter[strings.TrimSpace(language)] = true
		}
	}

	langNames := make([]string, 0, len(m.Languages))
	for language := range m.Languages {
		langNames = append(langNames, language)
	}
	sort.Strings(langNames)

	var specs []repoSpec
	for _, language := range langNames {
		if filter != nil && !filter[language] {
			continue
		}
		entries := m.Languages[language]
		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}
		for _, entry := range entries {
			repoPath, ref := entry, ""
			if at := strings.LastIndex(entry, "@"); at > 0 {
				repoPath, ref = entry[:at], entry[at+1:]
			}
			specs = append(specs, repoSpec{language: language, repoPath: repoPath, ref: ref})
		}
	}
	return specs, nil
}

func cloneAll(ctx context.Context, specs []repoSpec, cacheDir string, lock map[string]string, depth int, updateLock bool, jobs int, record func(repoPath, sha string)) {
	if jobs < 1 {
		jobs = 1
	}
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(spec repoSpec) {
			defer wg.Done()
			defer func() { <-sem }()
			ref := spec.ref
			if !updateLock {
				if pinned, ok := lock[spec.repoPath]; ok && pinned != "" {
					ref = pinned
				}
			}
			dir := filepath.Join(cacheDir, spec.language, spec.dirName())
			sha, err := ensureRepo(ctx, spec.cloneURL(), ref, dir, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  clone FAIL %-40s %v\n", spec.repoPath, err)
				return
			}
			record(spec.repoPath, sha)
		}(spec)
	}
	wg.Wait()
}

func ensureRepo(ctx context.Context, url, ref, dir string, depth int) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		args := []string{"clone", "--quiet", "--depth", strconv.Itoa(depth)}
		if ref != "" && !looksLikeSHA(ref) {
			args = append(args, "--branch", ref)
		}
		args = append(args, url, dir)
		if out, err := runGit(ctx, "", args...); err != nil {
			return "", fmt.Errorf("%v: %s", err, out)
		}
	}
	if ref != "" {
		// Best-effort fetch of the exact ref so a pinned SHA is available even
		// when it is not on the default branch; ignore fetch errors and let the
		// checkout surface a real failure.
		_, _ = runGit(ctx, dir, "fetch", "--quiet", "--depth", strconv.Itoa(depth), "origin", ref)
		if out, err := runGit(ctx, dir, "checkout", "--quiet", ref); err != nil {
			return "", fmt.Errorf("checkout %s: %v: %s", ref, err, out)
		}
	}
	sha, err := runGit(ctx, dir, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

func looksLikeSHA(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, r := range ref {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func loadLock(path string) (map[string]string, error) {
	lock := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lock, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse lock: %w", err)
	}
	return lock, nil
}

func writeLock(path string, lock map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func emitReport(report bench.Report, outDir string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if outDir == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("result-%d.json", time.Now().Unix())
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote report to %s\n", path)
	return nil
}

func printSummary(report bench.Report) {
	w := tabwriter.NewWriter(os.Stderr, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\nLANGUAGE\tREPOS\tFILES\tLOC\tSYMBOLS\tRELATIONS\tLOC/S\tPARSE_FAIL")
	languages := make([]string, 0, len(report.ByLanguage))
	for language := range report.ByLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		a := report.ByLanguage[language]
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%.0f\t%d\n", language, a.Repos, a.Files, a.LOC, a.Symbols, a.Relations, a.LOCPerSec, a.ParseFailures)
	}
	t := report.Totals
	fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\t%d\t%d\t%.0f\t%d\n", t.Repos, t.Files, t.LOC, t.Symbols, t.Relations, t.LOCPerSec, t.ParseFailures)
	w.Flush()
}
