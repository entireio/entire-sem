package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestNeighborsJSONReportsIndexCacheTelemetry(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "calls.go", "package calls\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\n")
	git(t, repo, "add", "calls.go")
	git(t, repo, "commit", "-m", "fixture")

	cacheDir := t.TempDir()
	run := func() (neighborResponse, string) {
		t.Helper()
		var out bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "0.1.0",
			Env:     EntireEnv{RepoRoot: repo},
			Stdout:  &out,
		}, []string{
			"neighbors", "--repo", repo, "--symbol", "Beta", "--head",
			"--cache-dir", cacheDir, "--format", "json",
		})
		if err != nil {
			t.Fatal(err)
		}
		var response neighborResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response, out.String()
	}

	first, firstJSON := run()
	if first.IndexCacheHit {
		t.Fatal("first neighbors query unexpectedly hit the index cache")
	}
	if !strings.Contains(firstJSON, `"index_cache_hit":false`) ||
		!strings.Contains(firstJSON, `"index_latency_ms":`) ||
		!strings.Contains(firstJSON, `"query_latency_ms":`) ||
		!strings.Contains(firstJSON, `"total_latency_ms":`) {
		t.Fatalf("neighbors JSON omitted index telemetry:\n%s", firstJSON)
	}
	if first.TotalLatencyMS < first.IndexLatencyMS || first.TotalLatencyMS < first.QueryLatencyMS {
		t.Fatalf("neighbors telemetry phases are inconsistent: %#v", first)
	}

	second, secondJSON := run()
	if !second.IndexCacheHit {
		t.Fatalf("second neighbors query missed the index cache:\n%s", secondJSON)
	}
}

func TestAgentNeighborsLabelsIndexQueryAndTotalLatency(t *testing.T) {
	var out bytes.Buffer
	if err := writeAgentNeighbors(&out, neighborResponse{
		Query:          "Missing",
		IndexCacheHit:  true,
		IndexLatencyMS: 7,
		QueryLatencyMS: 2,
		TotalLatencyMS: 9,
		Matches:        []neighborFocus{},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Index: cache-hit (7ms) | Query: 2ms | Total: 9ms\n") {
		t.Fatalf("neighbors agent telemetry conflated latency phases:\n%s", out.String())
	}
}

func TestNeighborsLimitBoundsAmbiguousFocusMatchesDeterministically(t *testing.T) {
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus-c", Name: "Target", QualifiedName: "Target", FilePath: "c.go", StartLine: 1},
			{ID: "callee", Name: "Callee", QualifiedName: "Callee", FilePath: "callee.go", StartLine: 1},
			{ID: "focus-a-late", Name: "Target", QualifiedName: "Target", FilePath: "a.go", StartLine: 20},
			{ID: "focus-a-early", Name: "Target", QualifiedName: "Target", FilePath: "a.go", StartLine: 5},
		},
		Relations: []sem.RelationRecord{
			{FromID: "focus-c", ToID: "callee", Type: "CALLS"},
			{FromID: "focus-a-late", ToID: "callee", Type: "CALLS"},
			{FromID: "focus-a-early", ToID: "callee", Type: "CALLS"},
		},
	}
	response := buildNeighborResponse(snapshot, neighborFlags{
		Symbol: "Target", Relation: "CALLS", Direction: "both", Depth: 1, Limit: 2,
	})
	if !response.Truncated || !response.FocusMatchesTruncated || response.FocusMatchesTotal != 3 {
		t.Fatalf("ambiguous focus truncation metadata = %#v", response)
	}
	if len(response.Matches) != 2 ||
		response.Matches[0].Symbol.ID != "focus-a-early" ||
		response.Matches[1].Symbol.ID != "focus-a-late" {
		t.Fatalf("bounded focus order = %#v", response.Matches)
	}
	for _, match := range response.Matches {
		if len(match.Outgoing) != 1 || match.Outgoing[0].Endpoint.ID != "callee" {
			t.Fatalf("adjacency for selected focus %q = %#v", match.Symbol.ID, match.Outgoing)
		}
	}

	var out bytes.Buffer
	if err := writeAgentNeighbors(&out, response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "showing the first 2 of 3 in file/line order; use --file") ||
		strings.Contains(out.String(), "c.go:1") {
		t.Fatalf("agent ambiguity output was not deterministically bounded:\n%s", out.String())
	}
}

func TestNeighborsExposeProviderPartialFailuresAndCompleteness(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "good.py", "def target():\n    helper()\n\ndef helper():\n    return True\n")
	write(t, repo, "broken.py", "def broken(:\n    return False\n")
	write(t, repo, "unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")

	var encoded bytes.Buffer
	if err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &encoded,
	}, []string{
		"neighbors", "--repo", repo, "--symbol", "target", "--format", "json", "--no-cache",
	}); err != nil {
		t.Fatal(err)
	}
	var response neighborResponse
	if err := json.Unmarshal(encoded.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	failures := make(map[string]string, len(response.PartialFailures))
	for _, failure := range response.PartialFailures {
		failures[failure.Code] = failure.FilePath
	}
	if failures["E_PARSE_ERROR"] != "broken.py" ||
		failures["E_UNSUPPORTED_LANGUAGE"] != "unsupported.f90" {
		t.Fatalf("neighbors partial failures = %#v", response.PartialFailures)
	}
	if response.Stats.PartialFailures != len(response.PartialFailures) ||
		response.Stats.CompletenessLevel == "" || response.Stats.CompletenessLevel == "ok" {
		t.Fatalf("neighbors completeness stats = %#v", response.Stats)
	}
	if _, ok := response.Completeness.Languages["Python"]; !ok {
		t.Fatalf("neighbors completeness breakdown = %#v", response.Completeness)
	}
	if !strings.Contains(encoded.String(), `"partial_failures":[`) ||
		!strings.Contains(encoded.String(), `"completeness":{`) {
		t.Fatalf("neighbors JSON omitted machine-readable completeness:\n%s", encoded.String())
	}

	var agent bytes.Buffer
	if err := writeAgentNeighbors(&agent, response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agent.String(), "Completeness: ") ||
		!strings.Contains(agent.String(), "E_PARSE_ERROR: broken.py") ||
		!strings.Contains(agent.String(), "E_UNSUPPORTED_LANGUAGE: unsupported.f90") {
		t.Fatalf("neighbors agent output hid partial coverage:\n%s", agent.String())
	}
}

func TestNeighborsScopeFiltersExternalAndTestEndpoints(t *testing.T) {
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Focus", QualifiedName: "Focus", FilePath: "src/focus.ts", StartLine: 10},
			{ID: "caller", Name: "Caller", QualifiedName: "Caller", FilePath: "src/caller.ts", StartLine: 3},
			{ID: "test-caller", Name: "TestCaller", QualifiedName: "TestCaller", FilePath: "tests/focus.test.ts", StartLine: 4},
			{ID: "callee", Name: "Callee", QualifiedName: "Callee", FilePath: "src/callee.ts", StartLine: 5},
			{ID: "test-callee", Name: "TestCallee", QualifiedName: "TestCallee", FilePath: "src/callee_test.go", StartLine: 6},
			{ID: "constructor", Name: "Result", QualifiedName: "Result", Kind: "class", FilePath: "src/result.ts", StartLine: 1},
		},
		Externals: []sem.ExternalRecord{
			{ID: "external", Kind: "external_symbol", Value: "vendor.External", External: true},
		},
		Relations: []sem.RelationRecord{
			{FromID: "caller", ToID: "focus", Type: "CALLS"},
			{FromID: "test-caller", ToID: "focus", Type: "CALLS"},
			{FromID: "external", ToID: "focus", Type: "CALLS"},
			{FromID: "focus", ToID: "callee", Type: "CALLS"},
			{FromID: "focus", ToID: "test-callee", Type: "CALLS"},
			{FromID: "focus", ToID: "external", Type: "CALLS"},
			{FromID: "focus", ToID: "constructor", Type: "CONSTRUCTS"},
		},
	}
	response := buildNeighborResponse(snapshot, neighborFlags{
		Symbol: "Focus", Relation: "CALLS", Direction: "both", Depth: 2, Limit: 20,
		InternalOnly: true, ExcludeTests: true,
	})
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(response.Matches))
	}
	match := response.Matches[0]
	if len(match.Incoming) != 1 || match.Incoming[0].Endpoint.ID != "caller" {
		t.Fatalf("filtered incoming = %#v", match.Incoming)
	}
	if len(match.Outgoing) != 2 ||
		match.Outgoing[0].Endpoint.ID != "callee" ||
		match.Outgoing[1].Endpoint.ID != "constructor" ||
		match.Outgoing[1].Relation != "CONSTRUCTS" {
		t.Fatalf("filtered outgoing = %#v", match.Outgoing)
	}
	if len(match.Paths) != 2 {
		t.Fatalf("filtered paths = %#v, want caller x focus x two production callees", match.Paths)
	}
}

func TestNeighborsExcludeTestsPreservesFocusInTestFile(t *testing.T) {
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Focus", QualifiedName: "Focus", FilePath: "tests/focus_test.py", StartLine: 2},
			{ID: "caller", Name: "Caller", QualifiedName: "Caller", FilePath: "src/caller.py", StartLine: 3},
		},
		Relations: []sem.RelationRecord{{FromID: "caller", ToID: "focus", Type: "CALLS"}},
	}
	response := buildNeighborResponse(snapshot, neighborFlags{
		Symbol: "Focus", Relation: "CALLS", Direction: "both", Depth: 1, Limit: 20,
		ExcludeTests: true,
	})
	if len(response.Matches) != 1 || response.Matches[0].Symbol.ID != "focus" ||
		len(response.Matches[0].Incoming) != 1 {
		t.Fatalf("test-file focus or production edge was removed: %#v", response.Matches)
	}
}

func TestConventionalTestPath(t *testing.T) {
	for _, path := range []string{
		"tests/unit/foo.go", "src/__tests__/foo.ts", "pkg/testdata/input.go",
		"pkg/foo_test.go", "src/foo.test.ts", "src/foo.spec.jsx",
		"src/test_foo.py", "src/foo_test.py", "spec/foo_spec.rb", "src/FooTest.java",
	} {
		if !isConventionalTestPath(path) {
			t.Errorf("isConventionalTestPath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"src/contest.go", "src/latest.py", "src/testing/helpers.go", "src/specification.ts",
	} {
		if isConventionalTestPath(path) {
			t.Errorf("isConventionalTestPath(%q) = true, want false", path)
		}
	}
}

func TestAgentNeighborsCompactsCartesianPathsIntoExactFamily(t *testing.T) {
	endpoint := func(id, path string, line int) neighborEndpoint {
		return neighborEndpoint{ID: id, Name: id, QualifiedName: id, FilePath: path, StartLine: line}
	}
	focus := endpoint("Focus", "focus.go", 10)
	callerA := endpoint("CallerA", "a.go", 1)
	callerB := endpoint("CallerB", "b.go", 2)
	calleeA := endpoint("CalleeA", "c.go", 3)
	calleeB := endpoint("CalleeB", "d.go", 4)
	match := neighborFocus{
		Symbol: focus,
		Incoming: []neighborEdge{
			{Direction: "in", Relation: "CALLS", Endpoint: callerA},
			{Direction: "in", Relation: "CALLS", Endpoint: callerB},
		},
		Outgoing: []neighborEdge{
			{Direction: "out", Relation: "CALLS", Endpoint: calleeA},
			{Direction: "out", Relation: "CALLS", Endpoint: calleeB},
		},
	}
	for _, caller := range []neighborEndpoint{callerA, callerB} {
		for _, callee := range []neighborEndpoint{calleeA, calleeB} {
			match.Paths = append(match.Paths, neighborPath{Caller: caller, Focus: focus, Callee: callee})
		}
	}

	var out bytes.Buffer
	if err := writeAgentNeighbors(&out, neighborResponse{
		Query: "Focus", Matches: []neighborFocus{match}, Truncated: true,
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Index: cache-") {
		t.Fatalf("agent output omitted auditable cache state:\n%s", text)
	}
	if !strings.Contains(text, "2 callers × 1 focus × 2 callees = 4 paths") ||
		!strings.Contains(text, "{CallerA (a.go:1); CallerB (b.go:2)} -> Focus -> {CalleeA (c.go:3); CalleeB (d.go:4)}") {
		t.Fatalf("agent output omitted exact compact path family:\n%s", text)
	}
	if strings.Count(text, " -> ") != 2 {
		t.Fatalf("agent output enumerated Cartesian paths instead of one family:\n%s", text)
	}
	if strings.Contains(text, "truncated") {
		t.Fatalf("agent output treated JSON-only path expansion as truncated:\n%s", text)
	}
}
