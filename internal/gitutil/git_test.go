package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestListFilesHandlesNewlinesInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	path := "dir/line\nbreak.py"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("def ok():\n    return True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add newline path")

	files, err := ListFiles(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != path {
		t.Fatalf("files = %#v, want %#v", files, []string{path})
	}
}

func TestChangedFilesHandlesNewlinesAndTabsInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	path := "dir/line\nbreak\tfile.py"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("def ok():\n    return True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add path")
	base := gitOutput(t, repo, "rev-parse", "HEAD")

	if err := os.WriteFile(full, []byte("def ok():\n    return False\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update path")
	head := gitOutput(t, repo, "rev-parse", "HEAD")

	files, err := ChangedFiles(t.Context(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Status != "M" || files[0].Path != path {
		t.Fatalf("files = %#v, want modified path %#v", files, path)
	}
}

func TestFileCochangesHandlesQuotedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`Windows filenames cannot contain '"' or '\'`)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	// '"' and '\' force git to C-quote the path even under core.quotePath=false;
	// the non-ASCII byte is what plain quotePath would octal-escape. Only -z
	// yields the raw path that matches the snapshot's file keys.
	special := "dir/wéird\"na\\me.py"
	other := "dir/other.py"
	writeBoth := func(content string) {
		t.Helper()
		for _, p := range []string{special, other} {
			full := filepath.Join(repo, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, repo, "add", ".")
	}
	// Two commits touching both files so the pair's co-change count reaches 2.
	writeBoth("v1\n")
	git(t, repo, "commit", "-m", "add files")
	writeBoth("v2\n")
	git(t, repo, "commit", "-m", "update files")

	pairs, err := FileCochanges(t.Context(), repo, 256)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pairs {
		if (p.Left == special && p.Right == other) || (p.Left == other && p.Right == special) {
			found = true
			if p.Count < 2 {
				t.Fatalf("co-change count = %d, want >= 2", p.Count)
			}
		}
	}
	if !found {
		t.Fatalf("FileCochanges dropped the raw quoted-path pair; got %#v", pairs)
	}
}

func TestBatchFileReaderReadsMultipleFilesFromHead(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	for path, content := range map[string]string{
		"a.go":     "package a\nfunc A() {}\n",
		"dir/b.go": "package dir\nfunc B() {}\n",
	} {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add files")

	reader, err := NewBatchFileReader(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, path := range []string{"a.go", "dir/b.go"} {
		batched, ok, err := reader.ReadFile(path)
		if err != nil {
			t.Fatalf("batch read %s: %v", path, err)
		}
		if !ok {
			t.Fatalf("batch read %s: not found", path)
		}
		shown, ok, err := ShowFile(t.Context(), repo, "HEAD", path)
		if err != nil {
			t.Fatalf("show %s: %v", path, err)
		}
		if !ok || batched != shown {
			t.Fatalf("batch read %s = %q (ok %v), want %q", path, batched, ok, shown)
		}
	}
	if _, ok, err := reader.ReadFile("missing.go"); err != nil || ok {
		t.Fatalf("missing read ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
