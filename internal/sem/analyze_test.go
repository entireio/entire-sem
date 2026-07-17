package sem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
