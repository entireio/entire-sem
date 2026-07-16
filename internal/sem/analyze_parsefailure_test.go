package sem

import (
	"context"
	"testing"
)

// pfValidTS parses to two top-level entities with no parse error.
const pfValidTS = "export function alpha() {\n    return 1\n}\n\nexport function beta() {\n    return 2\n}\n"

// pfBrokenTS parses to ZERO entities with ParseStatus.ParseError == true
// (a hard parse failure).
const pfBrokenTS = "type Broken = <\n\nexport function alpha(){return 1}\nexport function beta(){return 2}\n"

// pfSoftTS is a recoverable syntax error: tree-sitter flags root.HasError but
// still extracts the `ok` function. This is the "soft recovery with entities"
// class the fix must NOT suppress (the earlier Kotlin CI failure pattern).
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
// real change (body_changed for `ok`) must surface and no parse-failure warning
// should be emitted for it.
func TestAnalyzeGitRange_SoftErrorWithEntitiesNotSuppressed(t *testing.T) {
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
	if w := pfParseWarning(res, "svc.ts"); w != nil {
		t.Fatalf("soft-error-with-entities must not be suppressed, but got warning: %#v", w)
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

// parseFailureWarning maps both error codes (and defaults an empty code) exactly
// like the provider path, without needing a slow real timeout to exercise the
// E_PARSE_TIMEOUT branch.
func TestParseFailureWarning_CodeMapping(t *testing.T) {
	t.Parallel()
	e := parseFailureWarning("a.ts", ParseStatus{ParseError: true, Code: "E_PARSE_ERROR", Detail: "d"})
	if e.Code != "E_PARSE_ERROR" || e.Severity != "warning" || e.FilePath != "a.ts" || e.Detail != "d" {
		t.Fatalf("error mapping wrong: %#v", e)
	}
	if e.EffectOnCompleteness != "file parsed with syntax errors; semantic facts may be incomplete" {
		t.Fatalf("error effect wrong: %q", e.EffectOnCompleteness)
	}
	tmo := parseFailureWarning("b.ts", ParseStatus{ParseError: true, Code: "E_PARSE_TIMEOUT", Detail: "slow"})
	if tmo.Code != "E_PARSE_TIMEOUT" {
		t.Fatalf("timeout code wrong: %#v", tmo)
	}
	if tmo.EffectOnCompleteness != "file record emitted but symbol parsing skipped because parser time budget was exceeded" {
		t.Fatalf("timeout effect wrong: %q", tmo.EffectOnCompleteness)
	}
	def := parseFailureWarning("c.ts", ParseStatus{ParseError: true})
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
