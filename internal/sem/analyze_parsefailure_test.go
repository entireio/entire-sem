package sem

import (
	"context"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// pfValidTS parses to two top-level entities with no parse error.
const pfValidTS = "export function alpha() {\n    return 1\n}\n\nexport function beta() {\n    return 2\n}\n"

// pfBrokenTS parses to ZERO entities with ParseStatus.ParseError == true
// (a hard parse failure).
const pfBrokenTS = "type Broken = <\n\nexport function alpha(){return 1}\nexport function beta(){return 2}\n"

// pfSoftTS is a recoverable syntax error: tree-sitter flags root.HasError but
// still extracts the `ok` function. This is the "soft recovery with entities"
// class the fix must NOT suppress (the earlier Kotlin CI failure pattern) but
// must still flag with a degraded-diff warning.
const pfSoftTS = "export function ok() {\n    return 1\n}\n@@@ !!! broken <<<\n"
const pfSoftTSChanged = "export function ok() {\n    return 2\n}\n@@@ !!! broken <<<\n"

func pfParseWarning(result Result, path string) *ProviderWarning {
	for i := range result.Warnings {
		w := &result.Warnings[i]
		if w.FilePath == path && (w.Code == "E_PARSE_ERROR" || w.Code == "E_PARSE_TIMEOUT") {
			return w
		}
	}
	return nil
}

// Added file (git status "A") that is unparseable: the before side is the empty
// base, which must not make beforeParseFailed misfire. Only the after-side hard
// failure should be surfaced, with no phantom "added" entities.
func TestAnalyzeGitRange_AddedFileUnparseable(t *testing.T) {
	t.Parallel()
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "seed.txt", "seed\n")
	}, func(r string) {
		write(t, r, "svc.ts", pfBrokenTS)
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pfParseWarning(res, "svc.ts") == nil {
		t.Fatalf("expected parse-failure warning for added unparseable file, got %#v", res.Warnings)
	}
	for _, f := range res.Files {
		for _, c := range f.Changes {
			if c.Type == "added" && (c.Name == "alpha" || c.Name == "beta") {
				t.Fatalf("phantom added entity %q on added unparseable file: %#v", c.Name, res.Files)
			}
		}
	}
}

// Deleted file (git status "D") that was unparseable: only the before-side hard
// failure should surface, with no phantom "removed" entities.
func TestAnalyzeGitRange_DeletedFileUnparseable(t *testing.T) {
	t.Parallel()
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "svc.ts", pfBrokenTS)
		write(t, r, "seed.txt", "seed\n")
	}, func(r string) {
		git(t, r, "rm", "svc.ts")
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pfParseWarning(res, "svc.ts") == nil {
		t.Fatalf("expected parse-failure warning for deleted unparseable file, got %#v", res.Warnings)
	}
	for _, f := range res.Files {
		for _, c := range f.Changes {
			if c.Type == "removed" && (c.Name == "alpha" || c.Name == "beta") {
				t.Fatalf("phantom removed entity %q on deleted unparseable file: %#v", c.Name, res.Files)
			}
		}
	}
}

// A file unparseable on BOTH sides yields exactly one warning (not two).
func TestAnalyzeGitRange_BothSidesUnparseable(t *testing.T) {
	t.Parallel()
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "svc.ts", pfBrokenTS)
	}, func(r string) {
		write(t, r, "svc.ts", pfBrokenTS+"\nmore junk <\n")
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, w := range res.Warnings {
		if w.FilePath == "svc.ts" && w.Code == "E_PARSE_ERROR" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one parse-failure warning for both-sides failure, got %d: %#v", count, res.Warnings)
	}
}

// A soft syntax error that still extracts entities must NOT be suppressed: its
// real change (body_changed for `ok`) must surface. But because the recovered
// entity set may be incomplete, the diff is degraded and a parse warning must
// accompany it.
func TestAnalyzeGitRange_SoftErrorWithEntitiesKeptWithWarning(t *testing.T) {
	t.Parallel()
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "svc.ts", pfSoftTS)
	}, func(r string) {
		write(t, r, "svc.ts", pfSoftTSChanged)
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := pfParseWarning(res, "svc.ts")
	if w == nil {
		t.Fatalf("soft-error-with-entities must carry a degraded-diff warning, got %#v", res.Warnings)
	}
	if w.EffectOnCompleteness != "file parsed with syntax errors on one side; diff kept but may be incomplete or contain phantom changes" {
		t.Fatalf("degraded-diff effect wrong: %q", w.EffectOnCompleteness)
	}
	var sawChange bool
	for _, f := range res.Files {
		for _, c := range f.Changes {
			if c.Name == "ok" && (c.Type == "body_changed" || c.Type == "signature_changed") {
				sawChange = true
			}
		}
	}
	if !sawChange {
		t.Fatalf("expected a real change for `ok` on the soft-error file, got %#v", res.Files)
	}
}

// pfPartialHeadTS keeps alpha (with a changed body relative to pfValidTS) but
// corrupts beta's declaration so badly that tree-sitter recovers ONLY alpha:
// ParseError == true with a PARTIAL entity set. This is the reviewer's repro
// for the phantom-removal-without-warning gap: beta vanishes from the
// recovered set, so the diff reports it removed even though it may still exist.
const pfPartialHeadTS = "export function alpha() {\n    return 42\n}\n\nexport function bet@@@ <<<\n"

// The high-severity repro: base has alpha+beta (clean), head keeps alpha but
// corrupts beta's declaration. Tree-sitter recovers a partial entity set
// (ParseError=true, entities=[alpha]). The diff must be KEPT (alpha's real
// body change surfaces) but flagged with a parse warning, because the missing
// beta shows up as a potentially-phantom removal.
func TestAnalyzeGitRange_PartialRecoveryKeepsDiffWithWarning(t *testing.T) {
	t.Parallel()
	// Guard the fixture: the corruption must actually yield a partial recovery
	// (ParseError with a non-empty entity set) on this parser, or the test
	// would silently exercise the wrong branch.
	entities, _, status := TreeSitterParser{}.ParseWithStatus("svc.ts", pfPartialHeadTS)
	if !status.ParseError || len(entities) == 0 {
		t.Fatalf("fixture must parse as partial recovery, got ParseError=%v entities=%#v", status.ParseError, entities)
	}

	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "svc.ts", pfValidTS)
	}, func(r string) {
		write(t, r, "svc.ts", pfPartialHeadTS)
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := pfParseWarning(res, "svc.ts")
	if w == nil {
		t.Fatalf("expected degraded-diff warning for partial recovery, got %#v", res.Warnings)
	}
	if w.EffectOnCompleteness != "file parsed with syntax errors on one side; diff kept but may be incomplete or contain phantom changes" {
		t.Fatalf("degraded-diff effect wrong: %q", w.EffectOnCompleteness)
	}
	var sawAlphaChange bool
	for _, f := range res.Files {
		for _, c := range f.Changes {
			if c.Name == "alpha" && (c.Type == "body_changed" || c.Type == "signature_changed") {
				sawAlphaChange = true
			}
		}
	}
	if !sawAlphaChange {
		t.Fatalf("partial-recovery diff must be kept (alpha's real change), got %#v", res.Files)
	}
}

// A rename where only the BEFORE side fails to parse must warn about the OLD
// path — that is where the unparseable blob lives; the new path parses fine.
func TestAnalyzeGitRange_RenameBeforeSideParseFailureWarnsOldPath(t *testing.T) {
	t.Parallel()
	// Head content: the base content minus the broken first line, so git's
	// rename detection pairs the delete+add and the after side parses cleanly.
	const renamedTS = "\nexport function alpha(){return 1}\nexport function beta(){return 2}\n"
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "broken.ts", pfBrokenTS)
		write(t, r, "seed.txt", "seed\n")
	}, func(r string) {
		git(t, r, "rm", "broken.ts")
		write(t, r, "moved.ts", renamedTS)
	})

	// Guard the fixture: git must actually report a rename, or the test would
	// exercise the plain delete/add paths instead of the rename path.
	changed, err := gitutil.ChangedFiles(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sawRename bool
	for _, f := range changed {
		if f.Status == "R" && f.OldPath == "broken.ts" && f.Path == "moved.ts" {
			sawRename = true
		}
	}
	if !sawRename {
		t.Fatalf("fixture must produce a git rename broken.ts -> moved.ts, got %#v", changed)
	}

	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := pfParseWarning(res, "moved.ts"); w != nil {
		t.Fatalf("warning must not point at the new path (it parses fine): %#v", w)
	}
	if pfParseWarning(res, "broken.ts") == nil {
		t.Fatalf("expected parse-failure warning on old path broken.ts, got %#v", res.Warnings)
	}
}

// parseFailureWarning reuses the provider path's error codes (and defaults an
// empty code) but with diff-specific effect wording that distinguishes a
// suppressed delta (total failure) from a kept-but-degraded diff (partial
// recovery), without needing a slow real timeout to exercise the
// E_PARSE_TIMEOUT branch.
func TestParseFailureWarning_CodeMapping(t *testing.T) {
	t.Parallel()
	e := parseFailureWarning("a.ts", ParseStatus{ParseError: true, Code: "E_PARSE_ERROR", Detail: "d"}, true)
	if e.Code != "E_PARSE_ERROR" || e.Severity != "warning" || e.FilePath != "a.ts" || e.Detail != "d" {
		t.Fatalf("error mapping wrong: %#v", e)
	}
	if e.EffectOnCompleteness != "file diff suppressed; changes omitted because the file could not be parsed" {
		t.Fatalf("suppressed effect wrong: %q", e.EffectOnCompleteness)
	}
	tmo := parseFailureWarning("b.ts", ParseStatus{ParseError: true, Code: "E_PARSE_TIMEOUT", Detail: "slow"}, true)
	if tmo.Code != "E_PARSE_TIMEOUT" {
		t.Fatalf("timeout code wrong: %#v", tmo)
	}
	if tmo.EffectOnCompleteness != "file diff suppressed; changes omitted because parser time budget was exceeded" {
		t.Fatalf("suppressed timeout effect wrong: %q", tmo.EffectOnCompleteness)
	}
	deg := parseFailureWarning("d.ts", ParseStatus{ParseError: true, Code: "E_PARSE_ERROR"}, false)
	if deg.EffectOnCompleteness != "file parsed with syntax errors on one side; diff kept but may be incomplete or contain phantom changes" {
		t.Fatalf("degraded effect wrong: %q", deg.EffectOnCompleteness)
	}
	def := parseFailureWarning("c.ts", ParseStatus{ParseError: true}, true)
	if def.Code != "E_PARSE_ERROR" {
		t.Fatalf("empty code should default to E_PARSE_ERROR, got %q", def.Code)
	}
}

// A parse-failure warning emitted before the reconcileMoves append must survive
// alongside a move-ambiguity warning produced by the reconcile pass. This guards
// the `result.Warnings = append(..., reconcileMoves(...)...)` change against a
// regression back to a clobbering assignment.
func TestAnalyzeGitRange_ParseFailurePreservedWithReconcileWarning(t *testing.T) {
	t.Parallel()
	const fooBody = "export function foo() {\n    const a = 1\n    const b = 2\n    return a + b\n}\n"
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "a.ts", fooBody)
		write(t, r, "svc.ts", pfValidTS)
	}, func(r string) {
		// remove foo from a.ts, add identical foo to b.ts and c.ts (ambiguous
		// cross-file move), and break svc.ts (hard parse failure).
		write(t, r, "a.ts", "// emptied\n")
		write(t, r, "b.ts", fooBody)
		write(t, r, "c.ts", fooBody)
		write(t, r, "svc.ts", pfBrokenTS)
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	var parseFail, moveAmb bool
	for _, w := range res.Warnings {
		if w.Code == "E_PARSE_ERROR" && w.FilePath == "svc.ts" {
			parseFail = true
		}
		if w.Code == "W_MOVE_AMBIGUOUS" {
			moveAmb = true
		}
	}
	if !parseFail {
		t.Fatalf("parse-failure warning was lost (clobbered by reconcile append): %#v", res.Warnings)
	}
	if !moveAmb {
		t.Fatalf("expected a move-ambiguity warning to also be present: %#v", res.Warnings)
	}
}

type linearRepo struct{ repo, base, head string }

// buildLinearRepo creates a two-commit repo: seed applies mutations for the base
// commit, change applies mutations for the head commit. Each mutation callback
// receives the repo root and is responsible for writing/removing files; staging
// and committing are handled here.
func buildLinearRepo(t *testing.T, seed, change func(repo string)) linearRepo {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	seed(repo)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	base := rev(t, repo, "HEAD")
	change(repo)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "head")
	head := rev(t, repo, "HEAD")
	return linearRepo{repo: repo, base: base, head: head}
}
