package sem

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteTextPlainWhenNotTerminal(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("non-terminal output should not contain ANSI escapes:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "~ function validate_token signature changed (1 dependent)") {
		t.Fatalf("plain output missing semantic change:\n%s", out.String())
	}
}

func TestWriteTextColorCanBeForced(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("forced color output missing ANSI escapes:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "validate_token") {
		t.Fatalf("colored output missing semantic change:\n%s", out.String())
	}
}

func TestNoColorOverridesForcedColor(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("NO_COLOR output should not contain ANSI escapes:\n%q", out.String())
	}
}

// A suppressed parse failure must be visible in the default text output even
// when there are no file changes at all — otherwise the user sees only
// "No semantic entity changes detected." and the failure is silent.
func TestWriteTextRendersWarningsWithoutFileChanges(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, Result{
		Base: "HEAD~1",
		Head: "HEAD",
		Warnings: []ProviderWarning{{
			Code:                 "E_PARSE_ERROR",
			Severity:             "warning",
			FilePath:             "svc.ts",
			EffectOnCompleteness: "file diff suppressed; changes omitted because the file could not be parsed",
			Detail:               "syntax error near line 1",
		}},
	})
	text := out.String()
	if !strings.Contains(text, "No semantic entity changes detected.") {
		t.Fatalf("missing empty-diff line:\n%s", text)
	}
	for _, want := range []string{
		"Warnings",
		"! E_PARSE_ERROR svc.ts",
		"file diff suppressed; changes omitted because the file could not be parsed",
		"syntax error near line 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}
}

// Warnings also render after real file changes (the degraded-but-kept diff
// case), and a warning without a file path or detail renders cleanly.
func TestWriteTextRendersWarningsAfterFileChanges(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	result := sampleTextResult()
	result.Warnings = []ProviderWarning{{
		Code:                 "W_MOVE_AMBIGUOUS",
		Severity:             "warning",
		EffectOnCompleteness: "symbol move could not be reconciled unambiguously; reported as remove/add",
	}}
	var out bytes.Buffer
	WriteText(&out, result)
	text := out.String()
	if !strings.Contains(text, "validate_token") {
		t.Fatalf("file changes missing:\n%s", text)
	}
	if !strings.Contains(text, "Warnings") || !strings.Contains(text, "! W_MOVE_AMBIGUOUS\n") {
		t.Fatalf("warning section missing or malformed:\n%s", text)
	}
	if !strings.Contains(text, "symbol move could not be reconciled unambiguously") {
		t.Fatalf("warning effect missing:\n%s", text)
	}
}

// Without warnings the output is unchanged: no Warnings section appears.
func TestWriteTextOmitsWarningSectionWhenNone(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if strings.Contains(out.String(), "Warnings") {
		t.Fatalf("unexpected Warnings section:\n%s", out.String())
	}
}

func sampleTextResult() Result {
	return Result{
		Base: "HEAD~1",
		Head: "HEAD",
		Files: []FileChange{{
			Path:     "auth.py",
			Language: "Python",
			Changes: []EntityChange{{
				Type:            "signature_changed",
				Kind:            "function",
				Name:            "validate_token",
				DependentsCount: 1,
			}},
		}},
	}
}
