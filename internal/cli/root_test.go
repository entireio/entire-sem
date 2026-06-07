package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorPrintsEntireEnvironment(t *testing.T) {
	var out bytes.Buffer
	dataDir := t.TempDir()
	err := Run(t.Context(), Options{
		Env: EntireEnv{
			CLIVersion:    "0.6.3",
			RepoRoot:      t.TempDir(),
			PluginDataDir: dataDir,
		},
		Stdout: &out,
		Stderr: &out,
	}, []string{"doctor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENTIRE_CLI_VERSION=0.6.3", "ENTIRE_REPO_ROOT=", "ENTIRE_PLUGIN_DATA_DIR=", "plugin_data_dir=writable", "repo_root="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorWorksOutsideGitRepo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	var out bytes.Buffer
	err = Run(t.Context(), Options{
		Env:    EntireEnv{PluginDataDir: t.TempDir()},
		Stdout: &out,
		Stderr: &out,
	}, []string{"doctor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repo_root=<unset>", "repo_error="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestProviderJSONCommands(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	var versionOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &versionOut}, []string{"version", "--json"}); err != nil {
		t.Fatal(err)
	}
	var version map[string]string
	if err := json.Unmarshal(versionOut.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version["provider"] != "entire-sem" || version["version"] != "0.1.0" {
		t.Fatalf("version json = %#v", version)
	}

	var doctorOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &doctorOut}, []string{"doctor", "--json"}); err != nil {
		t.Fatal(err)
	}
	var doctor map[string]any
	if err := json.Unmarshal(doctorOut.Bytes(), &doctor); err != nil {
		t.Fatalf("doctor json invalid:\n%s\n%v", doctorOut.String(), err)
	}
	if doctor["repo_root"] != repo {
		t.Fatalf("doctor repo_root = %#v", doctor["repo_root"])
	}
	if doctor["no_egress"] != true {
		t.Fatalf("doctor no_egress = %#v", doctor["no_egress"])
	}

	var capabilitiesOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &capabilitiesOut}, []string{"capabilities", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capabilitiesOut.String(), `"supported_relation_types"`) {
		t.Fatalf("capabilities output:\n%s", capabilitiesOut.String())
	}
}

func TestProviderNDJSONCommands(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)

def check_token(token):
    return validate_token(token)
`)

	tests := []struct {
		command         string
		wantRecordTypes []string
	}{
		{command: "snapshot", wantRecordTypes: []string{"file", "symbol", "relation"}},
		{command: "symbols", wantRecordTypes: []string{"symbol"}},
		{command: "edges", wantRecordTypes: []string{"relation"}},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{tt.command, "--repo", repo, "--format", "ndjson"})
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("%s emitted too few lines:\n%s", tt.command, out.String())
		}
		var header map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
			t.Fatalf("%s invalid header json %q: %v", tt.command, lines[0], err)
		}
		if header["schema_version"] != "1.1" || header["provider"] != "entire-sem" {
			t.Fatalf("%s header = %#v", tt.command, header)
		}
		seenTypes := map[string]bool{}
		allowedTypes := map[string]bool{}
		for _, recordType := range tt.wantRecordTypes {
			allowedTypes[recordType] = true
		}
		for _, line := range lines[1:] {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatalf("%s invalid json line %q: %v", tt.command, line, err)
			}
			recordType, ok := decoded["record_type"].(string)
			if !ok {
				t.Fatalf("%s record missing record_type: %#v", tt.command, decoded)
			}
			if !allowedTypes[recordType] {
				t.Fatalf("%s emitted unexpected record type %q in %#v", tt.command, recordType, decoded)
			}
			seenTypes[recordType] = true
		}
		for _, recordType := range tt.wantRecordTypes {
			if !seenTypes[recordType] {
				t.Fatalf("%s missing record type %q:\n%s", tt.command, recordType, out.String())
			}
		}
	}
}

func TestSnapshotAcceptsNoNetwork(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	var out bytes.Buffer
	err = Run(t.Context(), Options{Version: "0.1.0", Stdout: &out}, []string{"snapshot", "--repo", ".", "--format", "ndjson", "--no-network"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
		t.Fatalf("snapshot output:\n%s", out.String())
	}
}

func TestSnapshotAcceptsWorktree(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"snapshot", "--repo", repo, "--format", "ndjson", "--no-network", "--worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
		t.Fatalf("snapshot output:\n%s", out.String())
	}
}

func TestProviderCommandsAcceptIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".brainignore", "ignored/\n")
	write(t, repo, "ignored/ignored.py", "def ignored():\n    return True\n")
	write(t, repo, "keep.py", "def keep():\n    return True\n")

	for _, command := range []string{"snapshot", "symbols", "edges"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			command,
			"--repo", repo,
			"--format", "ndjson",
			"--worktree",
			"--ignore-file", ".brainignore",
		})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
			t.Fatalf("%s output missing header:\n%s", command, out.String())
		}
		if strings.Contains(out.String(), "ignored.py") || strings.Contains(out.String(), "ignored") {
			t.Fatalf("%s output included ignored path:\n%s", command, out.String())
		}
	}
}

func TestProviderCommandsAcceptIncludeFile(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, ".gitignore", "ignored/\n")
	write(t, repo, ".seminclude", "ignored/\n")
	write(t, repo, "ignored/reopened.py", `def reopened():
    return True
`)

	for _, command := range []string{"snapshot", "symbols", "edges"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			command,
			"--repo", repo,
			"--format", "ndjson",
			"--worktree",
			"--include-file", ".seminclude",
		})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !strings.Contains(out.String(), `"schema_version":"1.1"`) {
			t.Fatalf("%s output missing header:\n%s", command, out.String())
		}
		if !strings.Contains(out.String(), "reopened") {
			t.Fatalf("%s output did not include reopened file:\n%s", command, out.String())
		}
	}
}

func TestAnalyzeJSONCommand(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update")

	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &out,
		Stderr: &out,
	}, []string{"analyze", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"dependents_count"`) {
		t.Fatalf("analyze json missing dependents_count:\n%s", out.String())
	}
}

func TestDiffJSONCommandCoversEverySupportedLanguage(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	fixtures := []struct {
		path     string
		language string
		before   string
		after    string
	}{
		{path: "auth.sh", language: "Bash", before: "validate_token() { echo ok; }\n", after: "validate_token() { echo ok; }\nrun_task() { echo run; }\n"},
		{path: "main.c", language: "C", before: "int validate(int token) { return token; }\n", after: "int validate(int token) { return token; }\nint audit(int token) { return token; }\n"},
		{path: "User.cs", language: "C#", before: "class User { public bool Validate(string token) { return true; } }\n", after: "class User { public bool Validate(string token) { return true; } public bool Audit(string token) { return true; } }\n"},
		{path: "main.cpp", language: "C++", before: "class User { public: void run() {} };\n", after: "class User { public: void run() {} void audit() {} };\n"},
		{path: "schema.cue", language: "CUE", before: "#User: { name: string }\n", after: "#User: { name: string }\nvalidate: true\n"},
		{path: "auth.ex", language: "Elixir", before: "defmodule User do\n  def validate(token), do: true\nend\n", after: "defmodule User do\n  def validate(token), do: true\n  def audit(token), do: token\nend\n"},
		{path: "main.go", language: "Go", before: "package main\nfunc Validate(token string) bool { return token != \"\" }\n", after: "package main\nfunc Validate(token string) bool { return token != \"\" }\nfunc Audit(token string) bool { return true }\n"},
		{path: "Auth.groovy", language: "Groovy", before: "class User { boolean validate(String token) { true } }\n", after: "class User { boolean validate(String token) { true } boolean audit(String token) { true } }\n"},
		{path: "main.tf", language: "HCL", before: "resource \"aws_instance\" \"web\" { ami = \"x\" }\n", after: "resource \"aws_instance\" \"web\" { ami = \"x\" }\nvariable \"name\" {}\n"},
		{path: "User.java", language: "Java", before: "class User { boolean validate(String token) { return true; } }\n", after: "class User { boolean validate(String token) { return true; } boolean audit(String token) { return true; } }\n"},
		{path: "app.js", language: "JavaScript", before: "function validate(token) { return Boolean(token); }\n", after: "function validate(token) { return Boolean(token); }\nfunction audit(token) { return token; }\n"},
		{path: "User.kt", language: "Kotlin", before: "class User { fun validate(token: String): Boolean { return true } }\n", after: "class User { fun validate(token: String): Boolean { return true } fun audit(token: String): Boolean { return true } }\n"},
		{path: "auth.lua", language: "Lua", before: "function validate(token) return true end\n", after: "function validate(token) return true end\nfunction audit(token) return token end\n"},
		{path: "auth.ml", language: "OCaml", before: "let validate token = true\n", after: "let validate token = true\nlet audit token = token\n"},
		{path: "auth.php", language: "PHP", before: "<?php\nfunction validate($token) { return true; }\n", after: "<?php\nfunction validate($token) { return true; }\nfunction audit($token) { return $token; }\n"},
		{path: "auth.proto", language: "Protocol Buffers", before: "syntax = \"proto3\";\nmessage User { string name = 1; }\n", after: "syntax = \"proto3\";\nmessage User { string name = 1; }\nmessage Audit { string id = 1; }\n"},
		{path: "auth.py", language: "Python", before: "def validate_token(token):\n    return bool(token)\n", after: "def validate_token(token):\n    return bool(token)\n\ndef audit_token(token):\n    return token\n"},
		{path: "auth.rb", language: "Ruby", before: "def validate(token)\n  true\nend\n", after: "def validate(token)\n  true\nend\ndef audit(token)\n  token\nend\n"},
		{path: "lib.rs", language: "Rust", before: "pub fn validate(value: &str) -> bool { true }\n", after: "pub fn validate(value: &str) -> bool { true }\npub fn audit(value: &str) -> bool { true }\n"},
		{path: "schema.sql", language: "SQL", before: "CREATE TABLE users (id INT);\n", after: "CREATE TABLE users (id INT);\nCREATE TABLE audit_events (id INT);\n"},
		{path: "Auth.scala", language: "Scala", before: "class User { def validate(token: String): Boolean = true }\n", after: "class User { def validate(token: String): Boolean = true; def audit(token: String): Boolean = true }\n"},
		{path: "Auth.swift", language: "Swift", before: "struct User { func validate(token: String) -> Bool { true } }\n", after: "struct User { func validate(token: String) -> Bool { true } func audit(token: String) -> Bool { true } }\n"},
		{path: "app.ts", language: "TypeScript", before: "class User { validate(value: string) { return value } }\n", after: "class User { validate(value: string) { return value } audit(value: string) { return value } }\n"},
	}

	for _, fixture := range fixtures {
		write(t, repo, fixture.path, fixture.before)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial languages")
	base := rev(t, repo, "HEAD")

	for _, fixture := range fixtures {
		write(t, repo, fixture.path, fixture.after)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update languages")
	head := rev(t, repo, "HEAD")

	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &out,
		Stderr: &out,
	}, []string{"diff", "--repo", repo, "--base", base, "--head", head, "--json"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Files []struct {
			Path     string `json:"path"`
			Language string `json:"language"`
			Changes  []any  `json:"changes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("diff json invalid:\n%s\n%v", out.String(), err)
	}
	seen := map[string]string{}
	for _, file := range payload.Files {
		if len(file.Changes) == 0 {
			t.Fatalf("%s had no semantic changes: %#v", file.Path, file)
		}
		seen[file.Language] = file.Path
	}
	for _, fixture := range fixtures {
		if seen[fixture.language] == "" {
			t.Fatalf("missing %s diff for %s in %#v", fixture.language, fixture.path, payload.Files)
		}
	}
	if len(seen) != len(fixtures) {
		t.Fatalf("languages = %d, want %d: %#v", len(seen), len(fixtures), seen)
	}
}

func write(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rev(t *testing.T, repo, value string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", value)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", value, err, out)
	}
	return strings.TrimSpace(string(out))
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
