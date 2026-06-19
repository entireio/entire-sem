package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type ChangedFile struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
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

func ShowFile(ctx context.Context, repo, rev, path string) (string, bool, error) {
	out, err := run(ctx, repo, "git", "show", rev+":"+path)
	if err != nil {
		if strings.Contains(err.Error(), "exists on disk, but not in") ||
			strings.Contains(err.Error(), "Path") ||
			strings.Contains(err.Error(), "does not exist") ||
			strings.Contains(err.Error(), "not found") {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
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
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repo
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
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
