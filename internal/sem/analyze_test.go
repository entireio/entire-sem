package sem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeGitRange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)

def format_date(value):
    return str(value)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	if len(result.Files[0].Changes) != 2 {
		t.Fatalf("changes = %#v", result.Files[0].Changes)
	}
}

func TestAnalyzeGitRangeReconcilesCrossFileMove(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "util.py", `def transform(value):
    return value * 2


def keep(value):
    return value
`)
	write(t, repo, "helpers.py", `def helper(value):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	// Move transform from util.py to helpers.py with an identical body.
	write(t, repo, "util.py", `def keep(value):
    return value
`)
	write(t, repo, "helpers.py", `def helper(value):
    return value


def transform(value):
    return value * 2
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "move transform")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	var moved *EntityChange
	for fi := range result.Files {
		for ci := range result.Files[fi].Changes {
			change := &result.Files[fi].Changes[ci]
			if change.Type == "moved" {
				moved = change
			}
			if change.Type == "removed" && change.Name == "transform" {
				t.Fatalf("transform reported as removed instead of moved: %#v", result.Files)
			}
			if change.Type == "added" && change.Name == "transform" {
				t.Fatalf("transform reported as added instead of moved: %#v", result.Files)
			}
		}
	}
	if moved == nil {
		t.Fatalf("no moved change in %#v", result.Files)
	}
	if moved.Name != "transform" || moved.Reconciliation != "MOVED" {
		t.Fatalf("moved change = %#v", moved)
	}
	if moved.OldPath != "util.py" || moved.NewPath != "helpers.py" {
		t.Fatalf("moved paths = %q -> %q", moved.OldPath, moved.NewPath)
	}
	if moved.Similarity < moveThreshold {
		t.Fatalf("moved similarity = %v", moved.Similarity)
	}
}

func TestAnalyzeGitRangeDependentCounts(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	write(t, repo, "use_auth.py", `def check(token):
    return validate_token(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Name == "validate_token" && change.DependentsCount != 1 {
				t.Fatalf("dependents = %d, want 1 in %#v", change.DependentsCount, change)
			}
		}
	}
}

func TestAnalyzeGitRangeExpandedLanguageSignatureChange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "User.java", `class User {
    boolean validate(String token) { return true; }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "User.java", `class User {
    boolean validate(String token, String issuer) { return true; }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range result.Files {
		if file.Path != "User.java" || file.Language != "Java" {
			continue
		}
		for _, change := range file.Changes {
			if change.Type == "signature_changed" && change.Kind == "method" && change.Name == "User.validate" {
				return
			}
		}
	}
	t.Fatalf("missing Java method signature change in %#v", result.Files)
}

func TestAnalyzeGitRangeMatchesSurvivingOverloadByFingerprint(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "C.java", `class C {
    int F(int value) {
        return 1;
    }

    int F(String value) {
        return 2;
    }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial overloads")
	base := rev(t, repo, "HEAD")

	write(t, repo, "C.java", `class C {
    int F(Object value) {
        return 2;
    }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "remove and edit overloads")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	var removedInt, editedString bool
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Kind != "method" || change.Name != "C.F" {
				continue
			}
			switch change.Type {
			case "removed":
				if strings.Contains(change.OldSignature, "int") {
					removedInt = true
				}
			case "signature_changed":
				if strings.Contains(change.OldSignature, "String") && strings.Contains(change.NewSignature, "Object") {
					editedString = true
				}
			case "added":
				t.Fatalf("surviving overload reported as added: %#v", change)
			}
		}
	}
	if !removedInt || !editedString {
		t.Fatalf("missing method changes removedInt=%v editedString=%v in %#v", removedInt, editedString, result.Files)
	}
}

func TestAnalyzeGitRangeIncludesGitHubWorkflowYAML(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, ".github/workflows/ci.yml", `name: CI
on:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, ".github/workflows/ci.yml", `name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test -race ./...
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update workflow")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	file := result.Files[0]
	if file.Path != ".github/workflows/ci.yml" {
		t.Fatalf("path = %q", file.Path)
	}
	if file.Language != "YAML" {
		t.Fatalf("language = %q", file.Language)
	}

	var sawJob bool
	var sawTrigger bool
	for _, change := range file.Changes {
		if change.Type == "body_changed" && change.Kind == "job" && change.Name == "jobs.test" {
			sawJob = true
		}
		if change.Type == "body_changed" && change.Kind == "section" && change.Name == "on" {
			sawTrigger = true
		}
	}
	if !sawJob || !sawTrigger {
		t.Fatalf("workflow changes missing job=%v trigger=%v in %#v", sawJob, sawTrigger, file.Changes)
	}
}

// Regression for the jdx/mise report: a changed file with no parser support
// (a PowerShell test script there) silently disappeared from the result, so an
// empty diff was indistinguishable from "not analyzed". It must surface as a
// machine-readable skipped marker.
func TestAnalyzeGitRangeMarksUnsupportedChangedFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	write(t, repo, "shim.Tests.ps1", `Describe "shim" {
    It "runs" { $true | Should -BeTrue }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)
`)
	write(t, repo, "shim.Tests.ps1", `Describe "shim" {
    It "runs" { $false | Should -BeFalse }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change both")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "auth.py" {
		t.Fatalf("files = %#v", result.Files)
	}
	var marker *ProviderWarning
	for i, w := range result.Warnings {
		if w.Code == "W_UNSUPPORTED_FILE" {
			marker = &result.Warnings[i]
		}
	}
	if marker == nil {
		t.Fatalf("missing W_UNSUPPORTED_FILE warning: %#v", result.Warnings)
	}
	if marker.FilePath != "shim.Tests.ps1" || marker.Severity != "info" {
		t.Fatalf("unexpected marker %#v", marker)
	}
}

func TestAnalyzeGitRangeKeepsShebangRoutableChangedFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "bin/tool", `#!/usr/bin/env python3

def run(value):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "bin/tool", `#!/usr/bin/env python3

def run(value, strict=False):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change signature")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "bin/tool" || result.Files[0].Language != "Python" {
		t.Fatalf("shebang-routable file was not analyzed: %#v", result)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "W_UNSUPPORTED_FILE" {
			t.Fatalf("shebang-routable file marked unsupported: %#v", warning)
		}
	}
}

func TestAnalyzeGitRangeMarksMixedSupportRenames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fromPath string
		toPath   string
		warnPath string
	}{
		{name: "supported to unsupported", fromPath: "sample.go", toPath: "sample.ps1", warnPath: "sample.ps1"},
		{name: "unsupported to supported", fromPath: "sample.ps1", toPath: "sample.go", warnPath: "sample.ps1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Test")
			git(t, repo, "config", "user.email", "graph@example.com")
			write(t, repo, tc.fromPath, "package sample\n\nfunc Run() {}\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "initial")
			base := rev(t, repo, "HEAD")

			if err := os.Rename(filepath.Join(repo, tc.fromPath), filepath.Join(repo, tc.toPath)); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "-A")
			git(t, repo, "commit", "-m", "rename across parser boundary")
			head := rev(t, repo, "HEAD")

			result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Files) != 0 {
				t.Fatalf("mixed-support rename produced a one-sided delta: %#v", result.Files)
			}
			var marker *ProviderWarning
			for i, warning := range result.Warnings {
				if warning.Code == "W_UNSUPPORTED_FILE" {
					marker = &result.Warnings[i]
					break
				}
			}
			if marker == nil || marker.FilePath != tc.warnPath || !strings.Contains(marker.EffectOnCompleteness, "diff suppressed") {
				t.Fatalf("missing mixed-support marker: %#v", result.Warnings)
			}
		})
	}
}

func TestAnalyzeCheckpointResolvesAssociatedCommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "agent update\n\nEntire-Checkpoint: abc123def456")

	result, err := AnalyzeCheckpoint(context.Background(), repo, "abc123def456")
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint != "abc123def456" {
		t.Fatalf("checkpoint = %q", result.Checkpoint)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
}

func TestCompareEntitiesDisambiguatesSameNameOverloads(t *testing.T) {
	// Issue #35 repro and regression guard for the phase-2 positional
	// fallback: two same-name, same-Kind overloads (reachable in C#/Java)
	// must not collide in the diff keys. Editing the FIRST overload's
	// signature (F(int) -> F(long)) must be reported as signature_changed,
	// and the untouched SECOND overload must not become a spurious
	// remove/add. A naive pure-signature key would regress this to
	// removed+added because the rename reconciler's signature similarity
	// (~0.33) is below renameThreshold.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(long)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one signature change for the first overload", changes)
	}
	c := changes[0]
	if c.Type != "signature_changed" || c.Kind != "method" || c.Name != "C.F" {
		t.Fatalf("change = %#v, want signature_changed for method C.F", c)
	}
	if c.OldSignature != "F(int)" || c.NewSignature != "F(long)" {
		t.Fatalf("change signatures = %q -> %q, want F(int) -> F(long)", c.OldSignature, c.NewSignature)
	}
}

func TestCompareEntitiesDetectsSecondOverloadEdit(t *testing.T) {
	// Control: editing only the SECOND overload is reported, and the first is
	// left untouched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(object)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one signature change for the second overload", changes)
	}
	c := changes[0]
	if c.Type != "signature_changed" || c.OldSignature != "F(string)" || c.NewSignature != "F(object)" {
		t.Fatalf("change = %#v, want signature_changed F(string) -> F(object)", c)
	}
}

func TestCompareEntitiesSingleEntityUnchangedBehavior(t *testing.T) {
	// Regression: a lone non-overloaded entity keeps its pre-ordinal behavior.
	before := []Entity{
		{Kind: "function", Name: "validate", Signature: "validate(token)", StartLine: 1},
	}
	after := []Entity{
		{Kind: "function", Name: "validate", Signature: "validate(token, issuer)", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one change", changes)
	}
	if changes[0].Type != "signature_changed" || changes[0].Name != "validate" {
		t.Fatalf("change = %#v, want signature_changed for validate", changes[0])
	}
}

func TestCompareEntitiesAddedOverloadReportedAsAdded(t *testing.T) {
	// Adding a third overload (before has 2, after has 3, appended in file
	// order) must surface exactly one `added` for the new overload; the two
	// pre-existing overloads pair by exact signature and produce no churn.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(removed) != 0 {
		t.Fatalf("unexpected removed: %#v", removed)
	}
	if len(added) != 1 {
		t.Fatalf("added = %#v, want exactly one added overload", added)
	}
	if added[0].Signature != "F(bool)" {
		t.Fatalf("added signature = %q, want F(bool)", added[0].Signature)
	}
}

func TestCompareEntitiesRemovedOverloadReported(t *testing.T) {
	// Removing an overload (before has 3, after has 2, the last in file order
	// dropped) must surface exactly one `removed` for the dropped overload and
	// must not misattribute the removal to a surviving overload.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(added) != 0 {
		t.Fatalf("unexpected added: %#v", added)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %#v, want exactly one removed overload", removed)
	}
	if removed[0].Signature != "F(bool)" {
		t.Fatalf("removed signature = %q, want F(bool)", removed[0].Signature)
	}
}

func TestCompareEntitiesTrueDuplicatesEditOne(t *testing.T) {
	// Two entities with identical Kind:Name:Signature on both sides (true
	// duplicates). Editing the body of one must surface exactly one
	// body_changed and no spurious remove/add for the untouched duplicate.
	// This works because true duplicates are paired by occurrence index in
	// file order, so the Nth duplicate on each side pairs with the Nth on
	// the other (a plain signature key would collide for true duplicates).
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one body change", changes)
	}
	if changes[0].Type != "body_changed" || changes[0].Kind != "method" || changes[0].Name != "C.F" {
		t.Fatalf("change = %#v, want body_changed for method C.F", changes[0])
	}
}

func TestCompareEntitiesDuplicateInsertionPreservesExistingBodies(t *testing.T) {
	// A new exact-signature duplicate inserted before two existing duplicates
	// must not shift occurrence-index pairing and manufacture body changes for
	// both survivors. Stable body hashes identify the existing entities; only
	// the genuinely new h0 entity should remain unmatched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h0", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 {
		t.Fatalf("duplicate insertion caused survivor churn: changes=%#v removed=%#v", changes, removed)
	}
	if len(added) != 1 || added[0].BodyHash != "h0" || added[0].StartLine != 1 {
		t.Fatalf("added = %#v, want only the new h0 duplicate", added)
	}
}

func TestCompareEntitiesRepeatedDuplicateInsertionPreservesContentClass(t *testing.T) {
	// Repeated equal hashes are an equivalence class, not an ambiguity that
	// should disable matching. Pair the two existing h1 entities as a multiset
	// and leave only the distinct h0 insertion unmatched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h0", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 1 || added[0].BodyHash != "h0" {
		t.Fatalf("unexpected duplicate multiset diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestCompareEntitiesDuplicateReorderUsesBodyBeforeFingerprint(t *testing.T) {
	// Fingerprints can legitimately collide for exact-signature duplicates
	// whose bodies normalize alike. An exact body hash is stronger evidence in
	// this phase and must keep a pure reorder inert.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", Fingerprint: "shared", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", Fingerprint: "shared", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 0 {
		t.Fatalf("duplicate reorder must be inert: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestCompareEntitiesExactSignatureOutranksSharedFingerprint(t *testing.T) {
	// Cross-signature fingerprint continuity is useful only after exact
	// signatures are anchored. Shared fingerprints must not turn removal of
	// F(int) into a phantom F(int)->F(string) signature edit.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "shared", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "shared", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(added) != 0 {
		t.Fatalf("exact-signature survivor caused churn: changes=%#v added=%#v", changes, added)
	}
	if len(removed) != 1 || removed[0].Signature != "F(int)" {
		t.Fatalf("removed = %#v, want only F(int)", removed)
	}
}

func TestCompareEntitiesExactSignaturesOutrankSwappedBodies(t *testing.T) {
	// Bodies may be copied or swapped between overloads while both signatures
	// survive. Cross-signature fingerprint evidence must not steal those exact
	// signatures and turn two body edits into two phantom signature edits.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "int-body", Fingerprint: "int-body", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "string-body", Fingerprint: "string-body", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "string-body", Fingerprint: "string-body", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "int-body", Fingerprint: "int-body", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 || len(changes) != 2 {
		t.Fatalf("unexpected swapped-body diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
	for _, change := range changes {
		if change.Type != "body_changed" {
			t.Fatalf("change = %#v, want body_changed", change)
		}
	}
}

func TestCompareEntitiesRemovalAndSignatureEditMatchByFingerprint(t *testing.T) {
	// When one overload is removed while another changes signature, positional
	// leftover pairing chooses the wrong survivor. The unchanged fingerprint
	// anchors F(string) across its F(object) signature edit, leaving F(int) as
	// the actual removal even though the edited entity moved to the first line.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "int-body", Fingerprint: "int-fingerprint", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "old-body", Fingerprint: "survivor", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(object)", BodyHash: "new-body", Fingerprint: "survivor", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(added) != 0 {
		t.Fatalf("unexpected added overloads: %#v", added)
	}
	if len(removed) != 1 || removed[0].Signature != "F(int)" {
		t.Fatalf("removed = %#v, want only F(int)", removed)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want one survivor signature change", changes)
	}
	change := changes[0]
	if change.Type != "signature_changed" || change.OldSignature != "F(string)" || change.NewSignature != "F(object)" {
		t.Fatalf("change = %#v, want signature_changed F(string) -> F(object)", change)
	}
	if change.BeforeStartLine != 5 || change.AfterStartLine != 1 {
		t.Fatalf("change lines = %d -> %d, want 5 -> 1", change.BeforeStartLine, change.AfterStartLine)
	}
}

func TestCompareEntitiesReorderedSignatureEditsMatchUniqueFingerprints(t *testing.T) {
	// Two survivors may both change signature and reorder. Unique fingerprints
	// must produce a one-to-one match independent of the new file order.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "old-a", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "old-b", Fingerprint: "b", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(object)", BodyHash: "new-b", Fingerprint: "b", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(long)", BodyHash: "new-a", Fingerprint: "a", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 || len(changes) != 2 {
		t.Fatalf("unexpected reordered edit diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
	paired := map[string]string{}
	for _, change := range changes {
		if change.Type != "signature_changed" {
			t.Fatalf("change = %#v, want signature_changed", change)
		}
		paired[change.OldSignature] = change.NewSignature
	}
	if paired["F(int)"] != "F(long)" || paired["F(string)"] != "F(object)" {
		t.Fatalf("signature pairs = %#v, want int->long and string->object", paired)
	}
}

func TestCompareEntitiesSameLineOverloadChangesSortDeterministically(t *testing.T) {
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "b", StartLine: 1},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(long)", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(object)", Fingerprint: "b", StartLine: 1},
	}

	for i := 0; i < 100; i++ {
		changes := Compare(before, after)
		if len(changes) != 2 || changes[0].OldSignature != "F(int)" || changes[1].OldSignature != "F(string)" {
			t.Fatalf("iteration %d changes = %#v, want deterministic old-signature order", i, changes)
		}
	}
}

func TestCompareEntitiesRemoveFirstOverloadNoCascade(t *testing.T) {
	// Removing the FIRST of three same-name overloads must report exactly one
	// `removed F(int)`. Pure positional-ordinal keying used to cascade here:
	// signature_changed F(int)->F(string), signature_changed
	// F(string)->F(bool), removed F(bool) -- all phantom. Two-phase matching
	// pairs the surviving overloads by exact signature first, leaving only
	// the truly removed one.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on surviving overloads: %#v", changes)
	}
	if len(added) != 0 {
		t.Fatalf("unexpected added: %#v", added)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %#v, want exactly one removed overload", removed)
	}
	if removed[0].Signature != "F(int)" {
		t.Fatalf("removed signature = %q, want F(int)", removed[0].Signature)
	}
}

func TestCompareEntitiesMidListInsertOnlyAdded(t *testing.T) {
	// Inserting an overload in the MIDDLE of the list must surface exactly
	// one `added F(string)` and leave the surrounding overloads untouched
	// (no phantom signature_changed on the shifted tail).
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(removed) != 0 {
		t.Fatalf("unexpected removed: %#v", removed)
	}
	if len(added) != 1 {
		t.Fatalf("added = %#v, want exactly one added overload", added)
	}
	if added[0].Signature != "F(string)" {
		t.Fatalf("added signature = %q, want F(string)", added[0].Signature)
	}
}

func TestCompareEntitiesReorderedOverloadsNoChanges(t *testing.T) {
	// Purely reordering same-name overloads (identical signatures and bodies,
	// only file positions swapped) must produce no events at all: exact
	// signature pairing matches each overload to itself regardless of
	// position.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "hi", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "hs", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "hs", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "hi", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 0 {
		t.Fatalf("reorder must be inert: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestAnalyzeGitRangeSurfacesParseFailures(t *testing.T) {
	const validTS = "export function alpha() {\n    return 1\n}\n\nexport function beta() {\n    return 2\n}\n"
	// Parses to zero entities with ParseStatus.ParseError == true.
	const brokenTS = "type Broken = <\n\nexport function alpha(){return 1}\nexport function beta(){return 2}\n"
	// Valid TypeScript with no top-level entities (a genuinely emptied file).
	const emptiedTS = "// all symbols removed\n"

	changesByType := func(result Result, changeType string) []EntityChange {
		var out []EntityChange
		for _, file := range result.Files {
			for _, change := range file.Changes {
				if change.Type == changeType {
					out = append(out, change)
				}
			}
		}
		return out
	}
	parseWarning := func(result Result, path string) *ProviderWarning {
		for i := range result.Warnings {
			w := &result.Warnings[i]
			if w.FilePath == path && (w.Code == "E_PARSE_ERROR" || w.Code == "E_PARSE_TIMEOUT") {
				return w
			}
		}
		return nil
	}

	t.Run("head unparseable surfaces warning without phantom removals", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", validTS, brokenTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		w := parseWarning(result, "svc.ts")
		if w == nil {
			t.Fatalf("expected parse-failure warning for svc.ts, got %#v", result.Warnings)
		}
		if w.Code != "E_PARSE_ERROR" || w.Severity != "warning" || w.EffectOnCompleteness == "" {
			t.Fatalf("unexpected warning shape: %#v", w)
		}
		for _, c := range changesByType(result, "removed") {
			if c.Name == "alpha" || c.Name == "beta" {
				t.Fatalf("phantom removed change for %q: %#v", c.Name, result.Files)
			}
		}
	})

	t.Run("base unparseable surfaces warning without phantom additions", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", brokenTS, validTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if parseWarning(result, "svc.ts") == nil {
			t.Fatalf("expected parse-failure warning for svc.ts, got %#v", result.Warnings)
		}
		for _, c := range changesByType(result, "added") {
			if c.Name == "alpha" || c.Name == "beta" {
				t.Fatalf("phantom added change for %q: %#v", c.Name, result.Files)
			}
		}
	})

	t.Run("genuinely emptied file still reports real removals with no warning", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", validTS, emptiedTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if w := parseWarning(result, "svc.ts"); w != nil {
			t.Fatalf("did not expect a parse-failure warning for a validly emptied file: %#v", w)
		}
		removed := map[string]bool{}
		for _, c := range changesByType(result, "removed") {
			removed[c.Name] = true
		}
		if !removed["alpha"] || !removed["beta"] {
			t.Fatalf("expected real removed changes for alpha and beta, got %#v", result.Files)
		}
	})
}

func buildParseFailureRepo(t *testing.T, file, baseContent, headContent string) (repo, base, head string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, file, baseContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base = rev(t, repo, "HEAD")
	write(t, repo, file, headContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head = rev(t, repo, "HEAD")
	return repo, base, head
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

func rev(t *testing.T, repo, value string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", value)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", value, err, out)
	}
	return string(out[:len(out)-1])
}
