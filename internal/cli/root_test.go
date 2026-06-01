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
		command string
		want    string
	}{
		{command: "snapshot", want: `"schema_version":"1.0"`},
		{command: "symbols", want: `"record_type":"symbol"`},
		{command: "edges", want: `"record_type":"relation"`},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{tt.command, "--repo", repo, "--format", "ndjson"})
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if !strings.Contains(out.String(), tt.want) {
			t.Fatalf("%s missing %s:\n%s", tt.command, tt.want, out.String())
		}
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatalf("%s invalid json line %q: %v", tt.command, line, err)
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
	if !strings.Contains(out.String(), `"schema_version":"1.0"`) {
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
	if !strings.Contains(out.String(), `"schema_version":"1.0"`) {
		t.Fatalf("snapshot output:\n%s", out.String())
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

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
