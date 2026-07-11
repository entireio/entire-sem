package sem

import (
	"strings"
	"testing"
)

// runCallsFrom returns every CALLS relation whose from-symbol short name is the
// given name.
func runCallsFrom(snapshot ProviderSnapshot, from string) []RelationRecord {
	var out []RelationRecord
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == from {
			out = append(out, r)
		}
	}
	return out
}

// A dotted call whose terminal name resolves to an in-repo class method
// (`from myapp import models; models.User.save()`) must NOT fabricate an
// external CALLS edge. The method is kind-gated out of the dotted local edge,
// but the call targets an in-repo member — the generic receiver path already
// emits the correct local edge, and the dotted external fallback must stay
// silent (precision > recall: a wrong external edge is worse than none).
func TestPythonDottedFromImportKindGatedMethodEmitsNoExternal(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "")
	writeFile(t, repo, "myapp/models.py", `class User:
    @classmethod
    def save(cls):
        return 1
`)
	writeFile(t, repo, "consumer.py", `from myapp import models


def go():
    return models.User.save()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.TargetKind == "external" || strings.HasPrefix(r.ToID, "external:symbol:") {
			t.Fatalf("dotted path fabricated external CALLS go -> %s for an in-repo method", r.ToID)
		}
	}
	// The correct in-repo edge from the generic receiver path must still exist.
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "go", "consumer.py", "save", "myapp/models.py") {
		t.Fatalf("missing in-repo CALLS go -> save @ myapp/models.py: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// The alias-import spelling `import myapp.models as models` composes the same
// `myapp.models.User` module qualifier as the from-import form, so it must emit
// the same result: no fabricated external edge, and the in-repo edge preserved.
func TestPythonDottedAliasKindGatedMethodEmitsNoExternal(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "")
	writeFile(t, repo, "myapp/models.py", `class User:
    @classmethod
    def save(cls):
        return 1
`)
	writeFile(t, repo, "consumer.py", `import myapp.models as models


def go():
    return models.User.save()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.TargetKind == "external" || strings.HasPrefix(r.ToID, "external:symbol:") {
			t.Fatalf("dotted path fabricated external CALLS go -> %s for an in-repo method", r.ToID)
		}
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "go", "consumer.py", "save", "myapp/models.py") {
		t.Fatalf("missing in-repo CALLS go -> save @ myapp/models.py: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A dotted call into a genuinely external package (`import requests;
// requests.sessions.session()`) has no in-repo symbol of the terminal name, so
// the external fallback must still fire. The suppression is scoped to in-repo
// targets only and must not silence real external edges.
func TestPythonDottedGenuineExternalStillEmitted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `import requests


def go():
    return requests.sessions.session()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.ToID == externalID("symbol", "requests.sessions.session") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing genuine external CALLS go -> requests.sessions.session: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A `from pkg import Name` binding where Name is a re-exported repo class is
// string-identical to a submodule import in importsByName, but `Name.attr.fn()`
// is class-attribute access, not a `pkg.attr` submodule path. The composer must
// recognise that Name is a repo class defined under pkg and compose nothing, so
// no CALLS edge is fabricated to an unrelated sibling module whose filename
// coincides with the attribute (myapp/settings.py here). Precision > recall: the
// true target (Config.settings being a Settings instance) is unresolved, and no
// edge is better than a wrong one.
func TestPythonDottedReExportedClassAttributeEmitsNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "from myapp._impl import Config\n")
	writeFile(t, repo, "myapp/_impl.py", `class Settings:
    def load(self):
        return 1


class Config:
    settings = Settings()
`)
	writeFile(t, repo, "myapp/settings.py", `def load():
    return 2
`)
	writeFile(t, repo, "consumer.py", `from myapp import Config


def go():
    return Config.settings.load()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "go"); len(edges) != 0 {
		t.Fatalf("re-exported class attribute access must emit no CALLS edge from go, got %#v", edges)
	}
}

// Repro B (the ubiquitous Flask-SQLAlchemy idiom): `from app import db` where
// app/__init__.py binds a module-level singleton `db = SQLAlchemy()`. `db` is
// not a submodule, so `db.session.query()` is instance-attribute access, not an
// `app.session` submodule path. The from-import form composes nothing (no
// app.db submodule), so no CALLS edge is fabricated — in particular no spurious
// external `app.session.query`. Precision > recall.
func TestPythonFromImportSingletonChainedCallEmitsNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/__init__.py", "db = SQLAlchemy()\n")
	writeFile(t, repo, "c.py", `from app import db


def caller():
    return db.session.query()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "caller"); len(edges) != 0 {
		t.Fatalf("from-imported module-level singleton chained call must emit no CALLS edge from caller, got %#v", edges)
	}
}

// Repro A: pkg/__init__.py binds a module-level singleton `service = Service()`.
// `from pkg import service` then `service.helper.fn()` is instance-attribute
// access on that singleton, NOT the pkg.helper submodule. With no pkg.service
// submodule the from-import form composes nothing, so the coincidentally-named
// sibling pkg/helper.py must NOT receive a fabricated local edge.
func TestPythonFromImportSingletonDoesNotResolveToSiblingModule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/__init__.py", "from pkg.impl import Service\nservice = Service()\n")
	writeFile(t, repo, "pkg/impl.py", `class Service:
    pass
`)
	writeFile(t, repo, "pkg/helper.py", `def fn():
    return 1
`)
	writeFile(t, repo, "c.py", `from pkg import service


def caller():
    return service.helper.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "caller"); len(edges) != 0 {
		t.Fatalf("from-imported singleton attribute call must emit no CALLS edge from caller, got %#v", edges)
	}
}

// `import x.y as x` binds the local `x` to module x.y — an alias rename that
// happens to shadow its own leading segment. `x.z.fn()` therefore means
// x.y.z.fn and must resolve to x/y/z.py, never the sibling x/z.py that a
// leading-segment reading would name, even though both define fn. Exactly one
// edge, to x/y/z.py.
func TestPythonSelfShadowAliasRenameResolvesToRenamedModule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "x/__init__.py", "")
	writeFile(t, repo, "x/z.py", `def fn():
    return 1
`)
	writeFile(t, repo, "x/y/__init__.py", "")
	writeFile(t, repo, "x/y/z.py", `def fn():
    return 2
`)
	writeFile(t, repo, "consumer.py", `import x.y as x


def go():
    return x.z.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var targets []string
	for _, r := range runCallsFrom(snapshot, "go") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "fn" {
			targets = append(targets, to.FilePath)
		}
	}
	if len(targets) != 1 || targets[0] != "x/y/z.py" {
		t.Fatalf("`import x.y as x` + x.z.fn() targets = %#v, want exactly [x/y/z.py]", targets)
	}
}

// A reassignment of an already-typed local nested inside a fluent chain's
// method-call arguments (`$u->set($w = get())`) must still invalidate that
// local: $w is reassigned away from its Foo ctor type, so the later single-hop
// `$w->render` must NOT emit a type_inferred edge to Foo::render. The chain's
// consumed span protects the typer's own `$v =` lead but excludes the
// argument-paren interior where the foreign write lives. Positive control: the
// same chain with a write-free argument keeps every type, so `$w->render` still
// resolves.
func TestPerlNestedChainArgWriteDropsInferredType(t *testing.T) {
	fooBar := map[string]string{
		"lib/Foo.pm": `package Foo;

sub new {
  return bless {}, shift;
}

sub render {
  return 1;
}
`,
		"lib/Bar.pm": `package Bar;

sub new {
  return bless {}, shift;
}

sub set {
  return shift;
}

sub go {
  return shift;
}
`,
	}

	// Negative: the nested `$w = get()` invalidates $w, so no type_inferred edge.
	nested := t.TempDir()
	writeFile(t, nested, "lib/App.pm", `package App;

sub run {
  my $w = Foo->new;
  my $u = Bar->new;
  my $v = $u->set($w = get())->go;
  $w->render;
}
`)
	for p, c := range fooBar {
		writeFile(t, nested, p, c)
	}
	nestedSnap, err := BuildProviderSnapshotWithOptions(t.Context(), nested, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(nestedSnap, "run") {
		if r.Resolution == "type_inferred" && lastSegment(r.ToID) == "render" {
			t.Fatalf("stale $w type fabricated a type_inferred CALLS run -> render: %#v", r)
		}
	}

	// Positive control: a write-free argument keeps $w typed Foo, so the
	// single-hop $w->render still resolves via the inferred package type.
	clean := t.TempDir()
	writeFile(t, clean, "lib/App.pm", `package App;

sub run {
  my $w = Foo->new;
  my $u = Bar->new;
  my $v = $u->set(cfg())->go;
  $w->render;
}
`)
	for p, c := range fooBar {
		writeFile(t, clean, p, c)
	}
	cleanSnap, err := BuildProviderSnapshotWithOptions(t.Context(), clean, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range runCallsFrom(cleanSnap, "run") {
		if r.Resolution == "type_inferred" && lastSegment(r.ToID) == "render" {
			found = true
		}
	}
	if !found {
		t.Fatalf("write-free chain argument must keep $w typed: expected type_inferred CALLS run -> render, got %#v", relationsOfType(cleanSnap.Relations, "CALLS"))
	}
}

// A Perl-inferred receiver type (`my $x = Foo->new`) must never reach the
// language-agnostic generic type-inference loop. When Foo.pm has no `render`
// sub but a TypeScript `class Foo { render() }` exists, the Perl receiver must
// NOT resolve cross-language to the TS method: the Perl-only type map is
// consumed solely by the language-gated Perl resolver, so no edge is emitted.
func TestPerlInferredTypeDoesNotLeakToCrossLanguageClass(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x->render;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new {
  return bless {}, shift;
}
`)
	writeFile(t, repo, "web/foo.ts", `export class Foo {
  render(): number {
    return 1;
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range runCallsFrom(snapshot, "run") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "render" {
			t.Fatalf("Perl inferred type leaked into a cross-language edge: run -> render @ %s (%s)", to.FilePath, to.Language)
		}
	}
}

// When both a Perl `Foo::go` and a TypeScript `Foo.go` exist, a Perl receiver
// typed `Foo` must resolve to exactly ONE edge — the Perl one. The generic loop
// must never see the Perl-inferred type and double-emit a second edge to the TS
// class.
func TestPerlInferredTypePrefersPerlOverCollidingClass(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x->go;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new {
  return bless {}, shift;
}

sub go {
  return 1;
}
`)
	writeFile(t, repo, "web/foo.ts", `export class Foo {
  go(): number {
    return 1;
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var goEdges []RelationRecord
	for _, r := range runCallsFrom(snapshot, "run") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "go" {
			goEdges = append(goEdges, r)
		}
	}
	if len(goEdges) != 1 {
		t.Fatalf("expected exactly one CALLS run -> go edge, got %d: %#v", len(goEdges), goEdges)
	}
	if to := symbolsByID[goEdges[0].ToID]; to.Language != "Perl" || to.FilePath != "lib/Foo.pm" {
		t.Fatalf("run -> go resolved to the wrong target: %#v", to)
	}
}
