package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ChangedFile struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

type FileCochange struct {
	Left  string
	Right string
	Count int
}

// GrepMatch is one matched substring from a tracked-worktree fixed-string grep.
type GrepMatch struct {
	Path string
	Text string
}

func RepoRoot(ctx context.Context, cwd string) (string, error) {
	out, err := run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func RevParse(ctx context.Context, repo, rev string) (string, error) {
	out, err := run(ctx, repo, "git", "rev-parse", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func FirstParent(ctx context.Context, repo, rev string) (string, error) {
	out, err := run(ctx, repo, "git", "rev-parse", rev+"^")
	if err != nil {
		return "", fmt.Errorf("resolve first parent for %s: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

func FindCommitWithCheckpoint(ctx context.Context, repo, checkpointID string) (string, error) {
	out, err := run(ctx, repo, "git", "log", "--all", "--format=%H", "-n", "1", "--grep=Entire-Checkpoint: "+checkpointID)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", fmt.Errorf("checkpoint %s has no associated commit in this repository", checkpointID)
	}
	return commit, nil
}

func ListFiles(ctx context.Context, repo, rev string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// ListIndexFiles lists the tracked files of the working tree's git index
// (`git ls-files -z`), relative to repo. It runs one git subprocess for the
// whole listing; callers use it to decide tracked-ness without per-path git
// calls. A non-git directory returns an error.
func ListIndexFiles(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// GrepIndexMatches returns a bounded sample of matched terms per tracked
// worktree file. Fixed strings and NUL-delimited paths keep query terms and
// unusual paths from changing grep semantics.
func GrepIndexMatches(ctx context.Context, repo string, patterns []string, maxPerFile int) ([]GrepMatch, error) {
	if len(patterns) == 0 {
		return []GrepMatch{}, nil
	}
	if maxPerFile <= 0 {
		maxPerFile = 32
	}
	args := []string{"grep", "-z", "-I", "-i", "-F", "-o", "-m", strconv.Itoa(maxPerFile)}
	for _, pattern := range patterns {
		if pattern != "" {
			args = append(args, "-e", pattern)
		}
	}
	if len(args) == 8 {
		return []GrepMatch{}, nil
	}
	args = append(args, "--")
	// Preserve the caller's locale here. Unlike the other git commands in this
	// package, `git grep -i` uses LC_CTYPE for non-ASCII case folding; forcing
	// the C locale would make Unicode matches disappear.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 && stderr.Len() == 0 {
			return []GrepMatch{}, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}

	data := stdout.Bytes()
	matches := make([]GrepMatch, 0)
	for len(data) > 0 {
		pathEnd := bytes.IndexByte(data, 0)
		if pathEnd < 0 {
			return nil, fmt.Errorf("git grep returned malformed path metadata")
		}
		path := string(data[:pathEnd])
		data = data[pathEnd+1:]
		textEnd := bytes.IndexByte(data, '\n')
		if textEnd < 0 {
			textEnd = len(data)
		}
		matches = append(matches, GrepMatch{Path: path, Text: string(data[:textEnd])})
		if textEnd == len(data) {
			data = nil
		} else {
			data = data[textEnd+1:]
		}
	}
	return matches, nil
}

func ChangedFiles(ctx context.Context, repo, base, head string, paths []string) ([]ChangedFile, error) {
	args := []string{"diff", "-z", "--name-status", "--find-renames", base, head, "--"}
	args = append(args, paths...)
	out, err := run(ctx, repo, "git", args...)
	if err != nil {
		return nil, err
	}

	var files []ChangedFile
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		switch {
		case strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C"):
			if i+1 < len(fields) {
				files = append(files, ChangedFile{Status: status[:1], OldPath: fields[i], Path: fields[i+1]})
				i += 2
			}
		default:
			if i < len(fields) {
				files = append(files, ChangedFile{Status: status[:1], Path: fields[i]})
				i++
			}
		}
	}
	return files, nil
}

func FileCochanges(ctx context.Context, repo string, maxCommits int) ([]FileCochange, error) {
	if maxCommits <= 0 {
		maxCommits = 256
	}
	// -z makes git emit raw, NUL-terminated pathnames with no quoting at all,
	// matching the file keys produced by ListFiles (`ls-tree -z`). A plain
	// --name-only (even with core.quotePath=false) still C-quotes paths
	// containing '"', '\', tabs, or newlines, which would never match those
	// keys. The per-commit marker is emitted via --pretty=format; under -z each
	// commit's output is either the marker alone (no files, e.g. a merge) or
	// "<marker>\n<first file>" followed by NUL-separated paths.
	const marker = "--entire-graph-commit--"
	// maxFilesPerCommit bounds the O(n^2) co-change pair expansion for a single
	// commit. A commit touching more files than this is a mass change (initial
	// import, tree-wide rename/format, generated-file regeneration, large merge),
	// whose pairs are co-change noise rather than signal — and enumerating them
	// blows up memory: one 10k-file commit alone produces ~50M pair keys (multi-GB).
	// Real feature/fix commits touch a handful of related files and stay well under.
	const maxFilesPerCommit = 50
	out, err := run(ctx, repo, "git", "log", "-z", "--name-only", "--pretty=format:"+marker, "-n", strconv.Itoa(maxCommits), "--")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	var commitFiles []string
	flush := func() {
		if len(commitFiles) < 2 {
			commitFiles = nil
			return
		}
		sort.Strings(commitFiles)
		uniq := commitFiles[:0]
		for _, path := range commitFiles {
			if len(uniq) == 0 || uniq[len(uniq)-1] != path {
				uniq = append(uniq, path)
			}
		}
		if len(uniq) > maxFilesPerCommit {
			commitFiles = nil
			return // mass-change commit: skip its O(n^2) noise pairs (the memory explosion source)
		}
		for i := 0; i < len(uniq); i++ {
			for j := i + 1; j < len(uniq); j++ {
				counts[uniq[i]+"\x00"+uniq[j]]++
			}
		}
		commitFiles = nil
	}
	for _, tok := range strings.Split(out, "\x00") {
		if tok == marker {
			flush()
			continue
		}
		if first, ok := strings.CutPrefix(tok, marker+"\n"); ok {
			flush()
			if first != "" {
				commitFiles = append(commitFiles, first)
			}
			continue
		}
		if tok != "" {
			commitFiles = append(commitFiles, tok)
		}
	}
	flush()

	pairs := make([]FileCochange, 0, len(counts))
	for key, count := range counts {
		if count < 2 {
			continue
		}
		left, right, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		pairs = append(pairs, FileCochange{Left: left, Right: right, Count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		if pairs[i].Left != pairs[j].Left {
			return pairs[i].Left < pairs[j].Left
		}
		return pairs[i].Right < pairs[j].Right
	})
	if len(pairs) > 1000 {
		pairs = pairs[:1000]
	}
	return pairs, nil
}

func ShowFile(ctx context.Context, repo, rev, path string) (string, bool, error) {
	// Classify against git's stderr only, never the wrapped error that echoes
	// the argv (which includes rev+":"+path). Matching the full error text made
	// any real failure on a path containing a marker substring (e.g. "Path" in
	// src/PathHelper.go) look like a missing file, swallowing the error.
	// Peel the revision to a tree before resolving the path. Without the type
	// constraint, a missing full object ID or a blob object can produce the
	// same path-looking diagnostic as a genuinely absent file.
	objectSpec := rev + "^{tree}:" + path
	out, stderr, err := runWithStderr(ctx, repo, "git", "show", objectSpec)
	if err != nil {
		if isMissingPathDiagnostic(stderr) {
			return "", false, nil
		}
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return "", false, fmt.Errorf("git show %s: %s", objectSpec, msg)
	}
	return out, true, nil
}

func isMissingPathDiagnostic(stderr string) bool {
	// ShowFile runs git under the C locale, so only classify Git's specific
	// missing-path diagnostics. Broad substring checks can match a bad revision
	// or an unrelated error merely because an argv value contains the phrase.
	return strings.HasPrefix(stderr, "fatal: path '") &&
		(strings.Contains(stderr, "' does not exist in '") ||
			strings.Contains(stderr, "' exists on disk, but not in '"))
}

// BatchFileReader reads blobs from one revision through a persistent
// `git cat-file --batch` process. It avoids spawning one git process per file
// while preserving HEAD-tree snapshot semantics.
type BatchFileReader struct {
	rev    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
	mu     sync.Mutex
	closed bool
}

func NewBatchFileReader(ctx context.Context, repo, rev string) (*BatchFileReader, error) {
	cmd := newCmd(ctx, repo, "git", "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("git cat-file --batch: %w", err)
	}
	return &BatchFileReader{
		rev:    rev,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
		stderr: &stderr,
	}, nil
}

func (r *BatchFileReader) ReadFile(path string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", false, fmt.Errorf("git cat-file batch reader is closed")
	}
	if _, err := fmt.Fprintf(r.stdin, "%s:%s\n", r.rev, path); err != nil {
		return "", false, err
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return "", false, fmt.Errorf("read git cat-file header: %w", err)
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") {
		return "", false, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", false, fmt.Errorf("unexpected git cat-file header %q", header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", false, fmt.Errorf("parse git cat-file size %q: %w", fields[2], err)
	}
	if fields[1] != "blob" {
		if _, err := io.CopyN(io.Discard, r.stdout, size+1); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, content); err != nil {
		return "", false, err
	}
	trailing, err := r.stdout.ReadByte()
	if err != nil {
		return "", false, err
	}
	if trailing != '\n' {
		return "", false, fmt.Errorf("git cat-file blob missing trailing newline separator")
	}
	return string(content), true, nil
}

func (r *BatchFileReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	stdin := r.stdin
	r.mu.Unlock()
	if err := stdin.Close(); err != nil {
		return err
	}
	if err := r.cmd.Wait(); err != nil {
		msg := strings.TrimSpace(r.stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git cat-file --batch: %s", msg)
	}
	return nil
}

func RemoteURLs(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return nil, err
	}
	origin := ""
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := fields[0]
		url := fields[1]
		if key == "remote.origin.url" {
			origin = url
			continue
		}
		urls = append(urls, url)
	}
	if origin != "" {
		urls = append([]string{origin}, urls...)
	}
	return urls, nil
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	stdout, stderr, err := runWithStderr(ctx, dir, name, args...)
	if err != nil {
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout, nil
}

// newCmd builds the exec.Cmd used by subprocesses whose diagnostics must be
// stable. It pins the subprocess locale to C (LC_ALL=C overrides LANG and any
// LC_*; LANG=C is set as a belt-and-braces default) so git's stderr messages
// are always the English ones our error classification matches — e.g.
// ShowFile's absent-file detection would otherwise break under a non-English
// git locale. GrepIndexMatches intentionally bypasses this helper and keeps
// the caller's locale because git grep uses LC_CTYPE for case folding.
func newCmd(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Cmd.Environ observes Dir and updates PWD accordingly. Starting from
	// os.Environ would leave child processes with the parent's stale PWD.
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "LANG=C")
	return cmd
}

// runWithStderr runs a command and returns its stdout and trimmed stderr
// separately, so callers can classify failures against git's own message
// without the wrapped error text (which echoes the argv, including paths).
func runWithStderr(ctx context.Context, dir, name string, args ...string) (string, string, error) {
	cmd := newCmd(ctx, dir, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), strings.TrimSpace(stderr.String()), err
}
