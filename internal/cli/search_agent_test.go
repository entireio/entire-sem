package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestAgentSearchSurfacesCoverageDiagnosticsWithinExactCap(t *testing.T) {
	response := sem.SearchResponse{
		Results: []sem.SearchResult{{
			Rank: 1, FilePath: "src/service.go", StartLine: 1, EndLine: 900,
			FocusLine: 42, SnippetStartLine: 40, SnippetEndLine: 44,
			SymbolName: "serve", Snippet: "func serve() {\n\tprepare()\n\trun()\n\tcleanup()\n}",
		}},
		Warnings:        []sem.ProviderWarning{{Code: "W_DIRTY", FilePath: "src/service.go"}},
		PartialFailures: []sem.PartialFailure{{Code: "E_PARSE_ERROR", FilePath: "broken.go"}},
		Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
			"Go": {Files: 3, Symbols: 8},
		}},
	}

	var roomy bytes.Buffer
	if err := writeAgentSearch(&roomy, response, 512); err != nil {
		t.Fatal(err)
	}
	if roomy.Len() > 512 {
		t.Fatalf("agent output used %d bytes, cap 512", roomy.Len())
	}
	for _, want := range []string{"Coverage: degraded", "warning W_DIRTY: src/service.go", "partial E_PARSE_ERROR: broken.go", "src/service.go:"} {
		if !strings.Contains(roomy.String(), want) {
			t.Fatalf("agent output omitted %q:\n%s", want, roomy.String())
		}
	}

	var tight bytes.Buffer
	if err := writeAgentSearch(&tight, response, 96); err != nil {
		t.Fatal(err)
	}
	if tight.Len() > 96 {
		t.Fatalf("agent output used %d bytes, cap 96: %q", tight.Len(), tight.String())
	}
	if !strings.Contains(tight.String(), "!D W1 F1 L1/3") || !strings.Contains(tight.String(), "src/service.go:42") {
		t.Fatalf("tight output lost coverage marker or focused location: %q", tight.String())
	}
	for _, capBytes := range []int{1, 2, 8, 12} {
		var tiny bytes.Buffer
		if err := writeAgentSearch(&tiny, response, capBytes); err != nil {
			t.Fatal(err)
		}
		if tiny.Len() > capBytes {
			t.Fatalf("tiny agent output used %d bytes, cap %d: %q", tiny.Len(), capBytes, tiny.String())
		}
		if !strings.HasPrefix(tiny.String(), "!") {
			t.Fatalf("tiny output lost reserved degraded marker at cap %d: %q", capBytes, tiny.String())
		}
	}
}

func TestAgentSearchReportsDisplayedSpanAndFocusAfterCompaction(t *testing.T) {
	result := sem.SearchResult{
		Rank: 1, FilePath: "src/worker.go", StartLine: 10, EndLine: 200,
		FocusLine: 103, SnippetStartLine: 100, SnippetEndLine: 106,
		SymbolName: "Work", Snippet: "line100\nline101\nline102\nFOCUS103\nline104\nline105\nline106",
	}
	block := agentSearchBlock(result, 64)
	if len(block) > 64 {
		t.Fatalf("search block used %d bytes, cap 64: %q", len(block), string(block))
	}
	text := string(block)
	if !strings.Contains(text, "src/worker.go:103") || !strings.Contains(text, "FOCUS103") {
		t.Fatalf("tight block lost the focus line: %q", text)
	}
	if strings.Contains(text, ":10-200") || strings.Contains(text, ":100-106") {
		t.Fatalf("header reported stale undisplayed span: %q", text)
	}
}

func TestSearchMaxContextBytesMustBePositive(t *testing.T) {
	_, _, err := parseSearchFlags([]string{"--query", "x", "--max-context-bytes", "0"})
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("zero max context bytes error = %v", err)
	}
}

func TestWriteTextSearchTiersRankOneAndTwoFullRestTerse(t *testing.T) {
	snippet := "func serve() {\n\tprepare()\n\trun()\n\tcleanup()\n}"
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "src/service.go", StartLine: 10, EndLine: 14, FocusLine: 10, Score: 12.5, SymbolName: "serve", Signals: []string{"path", "body"}, Snippet: snippet},
		{Rank: 2, FilePath: "src/other.go", StartLine: 1, EndLine: 3, FocusLine: 1, Score: 9.0, SymbolName: "other", Signals: []string{"body"}, Snippet: "func other() {}"},
		{Rank: 3, FilePath: "src/third.go", StartLine: 20, EndLine: 30, FocusLine: 22, Score: 8.0, QualifiedName: "Third.method", Signals: []string{"body"}, Snippet: "func method() {\n\t// long\n}"},
		{Rank: 4, FilePath: "src/fourth.go", StartLine: 40, EndLine: 44, FocusLine: 0, Score: 7.0, Signals: []string{"body"}, Snippet: "func fourth() {}"},
	}}

	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, snippet) {
		t.Fatalf("rank 1 must keep its full snippet:\n%s", out)
	}
	if !strings.Contains(out, "func other() {}") {
		t.Fatalf("rank 2 must keep its full snippet:\n%s", out)
	}
	if strings.Contains(out, "func method()") || strings.Contains(out, "// long") {
		t.Fatalf("rank 3 must NOT carry its snippet:\n%s", out)
	}
	if !strings.Contains(out, "3. src/third.go:22 Third.method\n") {
		t.Fatalf("rank 3 terse line missing/wrong shape:\n%s", out)
	}
	if strings.Contains(out, "func fourth() {}") {
		t.Fatalf("rank 4 must NOT carry its snippet:\n%s", out)
	}
	if !strings.Contains(out, "4. src/fourth.go:40\n") {
		t.Fatalf("rank 4 terse line should fall back to StartLine when FocusLine unset:\n%s", out)
	}
}
