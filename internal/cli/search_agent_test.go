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
