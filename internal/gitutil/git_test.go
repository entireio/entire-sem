package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestListFilesHandlesNewlinesInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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

func TestGrepIndexMatchesUsesFixedStringsAndUnstagedContent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for path, content := range map[string]string{
		"src/target.go": "package source\nfunc Initial() {}\n",
		"src/other.go":  "package source\nfunc Other() {}\n",
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
	git(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(repo, "src/target.go"), []byte("package source\nfunc NeedlePattern() {}\nfunc AnotherNeedlePattern() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, err := GrepIndexMatches(t.Context(), repo, []string{"NeedlePattern"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("grep match count = %d, want 2: %#v", len(matches), matches)
	}
	for _, match := range matches {
		if match.Path != "src/target.go" || match.Text != "NeedlePattern" {
			t.Fatalf("grep match = %#v, want path src/target.go and exact fixed-string text", match)
		}
	}
	empty, err := GrepIndexMatches(t.Context(), repo, []string{"absent-fixed-string"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("no-match grep results = %#v", empty)
	}
}

func TestGrepTreeMatchesUsesCommittedTreeAndStripsTreeishPrefix(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	path := "src/target:with-colon.go"
	if runtime.GOOS == "windows" {
		// A colon names an NTFS alternate data stream on Windows rather than a
		// tracked file. Keep the cross-platform committed-tree assertion while
		// exercising the colon-delimited display-prefix case on platforms where
		// colons are valid path bytes.
		path = "src/target with space.go"
	}
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package source\nfunc CommittedNeedle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(full, []byte("package source\nfunc DirtyNeedle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := GrepTreeMatches(t.Context(), repo, "HEAD", []string{"CommittedNeedle"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(committed) != 1 || committed[0].Path != path || committed[0].Text != "CommittedNeedle" {
		t.Fatalf("committed grep = %#v", committed)
	}
	dirty, err := GrepTreeMatches(t.Context(), repo, "HEAD", []string{"DirtyNeedle"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 0 {
		t.Fatalf("tree grep observed dirty worktree content: %#v", dirty)
	}
}

func TestGrepTreePathsMatchesTextAPIAndHandlesUnusualPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	paths := []string{"src/ordinary.go", "src/line\nbreak:target.go"}
	for _, path := range paths {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package source\n// ExactTreeNeedle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	commit := gitOutput(t, repo, "rev-parse", "HEAD")

	got, err := GrepTreePaths(t.Context(), repo, commit, []string{"ExactTreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(paths)
	if !reflect.DeepEqual(got, paths) {
		t.Fatalf("path-only grep = %#v, want %#v", got, paths)
	}
	textMatches, err := GrepTreeMatches(t.Context(), repo, commit, []string{"ExactTreeNeedle"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	textPaths := make([]string, len(textMatches))
	for index, match := range textMatches {
		textPaths[index] = match.Path
	}
	sort.Strings(textPaths)
	if !reflect.DeepEqual(got, textPaths) {
		t.Fatalf("path-only/text grep mismatch: paths=%#v text=%#v", got, textPaths)
	}
	noHit, err := GrepTreePaths(t.Context(), repo, commit, []string{"AbsentTreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(noHit) != 0 {
		t.Fatalf("path-only no-hit grep = %#v", noHit)
	}
}

// TestGrepTreePathsIncludingBinaryFindsFilesGrepTreePathsExcludes pins the
// only behavioral difference between the two functions: a blob Git itself
// classifies as binary because of an early embedded NUL byte. GrepTreePaths
// (which passes -I) must silently exclude it from its result even though it
// contains a matching pattern; GrepTreePathsIncludingBinary must still find
// it, and both functions must agree on an ordinary text file.
func TestGrepTreePathsIncludingBinaryFindsFilesGrepTreePathsExcludes(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	textPath := "src/text.go"
	binaryPath := "src/binary.go"
	for path, content := range map[string]string{
		textPath:   "package source\n// TreeNeedle\n",
		binaryPath: "package source\n// TreeNeedle\x00 embedded nul\n",
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
	git(t, repo, "commit", "-m", "initial")
	commit := gitOutput(t, repo, "rev-parse", "HEAD")

	textOnly, err := GrepTreePaths(t.Context(), repo, commit, []string{"TreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(textOnly) != 1 || textOnly[0] != textPath {
		t.Fatalf("GrepTreePaths = %#v, want only %#v (the NUL-containing file must be excluded by -I)", textOnly, []string{textPath})
	}

	includingBinary, err := GrepTreePathsIncludingBinary(t.Context(), repo, commit, []string{"TreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(includingBinary)
	want := []string{binaryPath, textPath}
	if !reflect.DeepEqual(includingBinary, want) {
		t.Fatalf("GrepTreePathsIncludingBinary = %#v, want %#v", includingBinary, want)
	}
}

func TestGrepIndexMatchesPreservesUnicodeCaseFoldingLocale(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	const path = "src/unicode.go"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package source\n// wéird\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add unicode content")

	// Locale names vary across platforms. Find one under which this Git build
	// supports the Unicode case fold before asserting that GrepIndexMatches
	// preserves it; the regression forced LC_ALL=C after this probe succeeded.
	unicodeLocale := ""
	for _, candidate := range []string{"C.UTF-8", "en_US.UTF-8", "en_US.utf8"} {
		cmd := exec.Command("git", "grep", "-q", "-i", "-F", "-e", "WÉIRD", "--")
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), "LC_ALL="+candidate, "LANG="+candidate)
		if err := cmd.Run(); err == nil {
			unicodeLocale = candidate
			break
		}
	}
	if unicodeLocale == "" {
		t.Skip("installed Git has no available UTF-8 locale with Unicode case folding")
	}
	t.Setenv("LC_ALL", unicodeLocale)
	t.Setenv("LANG", unicodeLocale)

	matches, err := GrepIndexMatches(t.Context(), repo, []string{"WÉIRD"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != path || matches[0].Text != "wéird" {
		t.Fatalf("unicode grep matches = %#v, want one case-folded match in %s", matches, path)
	}
}

func TestChangedFilesHandlesNewlinesAndTabsInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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

	revision := gitOutput(t, repo, "rev-parse", "HEAD")
	pairs, err := FileCochanges(t.Context(), repo, revision, 256)
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

func TestFileCochangesUsesExactRevision(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	writePair := func(paths [2]string, content string) {
		t.Helper()
		for _, path := range paths {
			full := filepath.Join(repo, path)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, repo, "add", ".")
	}
	oldPair := [2]string{"old_a.go", "old_b.go"}
	writePair(oldPair, "v1\n")
	git(t, repo, "commit", "-m", "old pair one")
	writePair(oldPair, "v2\n")
	git(t, repo, "commit", "-m", "old pair two")
	pinned := gitOutput(t, repo, "rev-parse", "HEAD")

	newPair := [2]string{"new_a.go", "new_b.go"}
	writePair(newPair, "v1\n")
	git(t, repo, "commit", "-m", "new pair one")
	writePair(newPair, "v2\n")
	git(t, repo, "commit", "-m", "new pair two")

	pairs, err := FileCochanges(t.Context(), repo, pinned, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFileCochangePair(pairs, oldPair) {
		t.Fatalf("pinned history lost old pair: %#v", pairs)
	}
	if hasFileCochangePair(pairs, newPair) {
		t.Fatalf("pinned history leaked commits after %s: %#v", pinned, pairs)
	}
}

func hasFileCochangePair(pairs []FileCochange, paths [2]string) bool {
	for _, pair := range pairs {
		if (pair.Left == paths[0] && pair.Right == paths[1]) ||
			(pair.Left == paths[1] && pair.Right == paths[0]) {
			return true
		}
	}
	return false
}

func TestBatchFileReaderReadsMultipleFilesFromHead(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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

func TestShowFileClassifiesErrorsByStderrNotPath(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	// Path deliberately contains the substring "Path" — the old classifier
	// treated any error mentioning "Path" as a missing file, and ShowFile's
	// wrapped error always echoed the argv (which includes the path).
	const path = "src/PathHelper.go"
	const content = "package src\n\nfunc Help() {}\n"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add path helper")

	// Regression: a bad rev on a path containing "Path" must surface a real
	// error, not be swallowed as file-absent.
	if _, ok, err := ShowFile(t.Context(), repo, "BADREV", path); err == nil {
		t.Fatalf("ShowFile with bad rev = (ok %v, err nil), want non-nil error", ok)
	}

	// A syntactically valid but unwritten object ID and an existing blob ID can
	// both make an untyped `git show REV:PATH` emit a path-looking diagnostic.
	// Neither is a valid tree revision, so both must remain hard errors.
	unwrittenCmd := exec.Command("git", "hash-object", "--stdin")
	unwrittenCmd.Dir = repo
	unwrittenCmd.Stdin = strings.NewReader("entire-graph pr36 unwritten object\n")
	unwrittenOut, err := unwrittenCmd.Output()
	if err != nil {
		t.Fatalf("git hash-object --stdin: %v", err)
	}
	unwrittenOID := strings.TrimSpace(string(unwrittenOut))
	blobOID := gitOutput(t, repo, "rev-parse", "HEAD:"+path)

	for _, tc := range []struct {
		name string
		rev  string
		path string
	}{
		{name: "unwritten full object ID", rev: unwrittenOID, path: path},
		{name: "blob object", rev: blobOID, path: path},
		{name: "marker phrase in invalid revision", rev: "not found", path: path},
		{name: "marker phrase in outside path", rev: "HEAD", path: "../not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, ok, err := ShowFile(t.Context(), repo, tc.rev, tc.path); err == nil || ok || out != "" {
				t.Fatalf("ShowFile(%q, %q) = (%q, ok %v, err %v), want (\"\", false, non-nil)", tc.rev, tc.path, out, ok, err)
			}
		})
	}

	// A genuinely missing path at a valid rev is still reported as absent.
	if out, ok, err := ShowFile(t.Context(), repo, "HEAD", "src/DoesNotExist.go"); err != nil || ok || out != "" {
		t.Fatalf("ShowFile missing path = (%q, ok %v, err %v), want (\"\", false, nil)", out, ok, err)
	}

	// An existing file at HEAD returns its content.
	if out, ok, err := ShowFile(t.Context(), repo, "HEAD", path); err != nil || !ok || out != content {
		t.Fatalf("ShowFile existing = (%q, ok %v, err %v), want (%q, true, nil)", out, ok, err, content)
	}
}

func TestNewCmdPinsSubprocessLocaleToC(t *testing.T) {
	// ShowFile classifies file-absent by matching git's English stderr text, so
	// diagnostic subprocesses run with a pinned C locale. LC_ALL=C must come
	// after the inherited environment so it overrides LC_ALL/LANG/LC_MESSAGES.
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("LANG", "fr_FR.UTF-8")
	dir := t.TempDir()
	cmd := newCmd(context.Background(), dir, "git", "version")
	lcAll, lang, pwd := "", "", ""
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "LC_ALL="); ok {
			lcAll = v
		}
		if v, ok := strings.CutPrefix(kv, "LANG="); ok {
			lang = v
		}
		if v, ok := strings.CutPrefix(kv, "PWD="); ok {
			pwd = v
		}
	}
	if lcAll != "C" || lang != "C" {
		t.Fatalf("effective subprocess locale LC_ALL=%q LANG=%q, want both \"C\"", lcAll, lang)
	}
	if runtime.GOOS != "windows" && filepath.Clean(pwd) != filepath.Clean(dir) {
		t.Fatalf("subprocess PWD=%q, want command directory %q", pwd, dir)
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
