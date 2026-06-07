package sem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildProviderSnapshotEmitsContractRecords(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", `import json

class AuthService:
    def execute_tool_handler(self):
        return {"tool": "execute", "schema": {}}

def validate_token(token):
    return bool(token)

def check_token(token):
    return validate_token(token)
`)
	writeFile(t, repo, "server.ts", `export function handleRoute() {
  return "/users/{id}"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.SchemaVersion != "1.1" {
		t.Fatalf("schema version = %q", snapshot.Header.SchemaVersion)
	}
	if snapshot.Header.Provider != ProviderName {
		t.Fatalf("provider = %q", snapshot.Header.Provider)
	}
	if snapshot.Header.Stats.CompletenessLevel != "ok" {
		t.Fatalf("completeness = %q", snapshot.Header.Stats.CompletenessLevel)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("files = %#v", snapshot.Files)
	}
	for _, file := range snapshot.Files {
		if file.ID == "" {
			t.Fatalf("file record missing id: %#v", file)
		}
	}

	var validate SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == "validate_token" {
			validate = symbol
		}
	}
	if validate.ID == "" {
		t.Fatalf("missing validate_token in %#v", snapshot.Symbols)
	}
	if validate.StableIDVersion != StableSymbolIDVersion {
		t.Fatalf("stable id version = %q", validate.StableIDVersion)
	}
	if !strings.Contains(validate.ID, "local/") || !strings.Contains(validate.ID, ":Python:auth.py:function:validate_token") {
		t.Fatalf("stable id = %q", validate.ID)
	}

	seenRelations := map[string]bool{}
	for _, relation := range snapshot.Relations {
		seenRelations[relation.Type] = true
		if relation.WarningCodes == nil {
			t.Fatalf("warning_codes should be an array in %#v", relation)
		}
		if relation.Confidence <= 0 || relation.Reason == "" {
			t.Fatalf("relation missing confidence/reason: %#v", relation)
		}
	}
	for _, want := range []string{"DEFINES", "CONTAINS", "IMPORTS", "CALLS", "HANDLES_ROUTE", "HANDLES_TOOL"} {
		if !seenRelations[want] {
			t.Fatalf("missing %s in %#v", want, snapshot.Relations)
		}
	}
	if symbolByKindAndName(snapshot.Symbols, "tool", "AuthService.execute_tool_handler").ID == "" {
		t.Fatalf("missing tool boundary symbol in %#v", snapshot.Symbols)
	}
	if len(snapshot.Externals) == 0 {
		t.Fatalf("missing external endpoint records")
	}
}

func TestBuildProviderSnapshotAddsBoundarySourceLocations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "apps/web/src/app/oauth/device/code/route.ts", `export async function POST(request: Request) {
  return Response.json({ ok: true })
}
`)
	writeFile(t, repo, "apps/web/src/app/api/internal/feed-crawler/tick/route.ts", `async function handleFeedCrawlerTick(request: Request) {
  return Response.json({ ok: true })
}

export async function GET(request: Request) {
  return handleFeedCrawlerTick(request)
}
`)
	writeFile(t, repo, "src/app/api/internal/post-transcription/tick/route.ts", `export async function GET(request: Request) {
  return Response.json({ ok: true })
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	route := symbolByKindAndName(snapshot.Symbols, "route", "/oauth/device/code")
	if route.ID == "" {
		t.Fatalf("missing route boundary in %#v", snapshot.Symbols)
	}
	if route.FilePath != "apps/web/src/app/oauth/device/code/route.ts" || route.StartLine != 1 {
		t.Fatalf("route source = %#v", route)
	}

	workflow := symbolByKindAndName(snapshot.Symbols, "workflow", "feed-crawler")
	if workflow.ID == "" {
		t.Fatalf("missing workflow boundary in %#v", snapshot.Symbols)
	}
	if workflow.FilePath != "apps/web/src/app/api/internal/feed-crawler/tick/route.ts" {
		t.Fatalf("workflow source = %#v", workflow)
	}

	rootRoute := symbolByKindAndName(snapshot.Symbols, "route", "/api/internal/post-transcription/tick")
	if rootRoute.ID == "" {
		t.Fatalf("missing route boundary for repo-root src/app path in %#v", snapshot.Symbols)
	}
	rootWorkflow := symbolByKindAndName(snapshot.Symbols, "workflow", "post-transcription")
	if rootWorkflow.ID == "" {
		t.Fatalf("missing workflow boundary for repo-root src/app path in %#v", snapshot.Symbols)
	}
}

func TestCapabilitiesAdvertiseExpandedLanguageSet(t *testing.T) {
	caps := Capabilities()
	if caps.SchemaVersion != SchemaVersion || caps.Provider != ProviderName {
		t.Fatalf("capabilities identity = %#v", caps)
	}
	seen := map[string]bool{}
	for _, language := range caps.SupportedLanguages {
		seen[language] = true
	}
	for _, want := range []string{
		"Bash",
		"C",
		"C#",
		"C++",
		"CUE",
		"Elixir",
		"Go",
		"Groovy",
		"HCL",
		"Java",
		"JavaScript",
		"Kotlin",
		"Lua",
		"OCaml",
		"PHP",
		"Protocol Buffers",
		"Python",
		"Ruby",
		"Rust",
		"SQL",
		"Scala",
		"Swift",
		"TypeScript",
	} {
		if !seen[want] {
			t.Fatalf("capabilities missing language %q in %#v", want, caps.SupportedLanguages)
		}
	}
	for _, want := range []string{".go", ".py", ".ts", ".rs", ".swift", ".proto"} {
		if !contains(caps.SupportedFileExtensions, want) {
			t.Fatalf("capabilities missing extension %q in %#v", want, caps.SupportedFileExtensions)
		}
	}
	for _, want := range relationTypes {
		if !contains(caps.SupportedRelationTypes, want) {
			t.Fatalf("capabilities missing relation type %q in %#v", want, caps.SupportedRelationTypes)
		}
	}
	if caps.ParserVersions["go-tree-sitter"] == "" {
		t.Fatalf("capabilities missing parser metadata: %#v", caps.ParserVersions)
	}
	for feature, requiresNetwork := range caps.FeaturesRequiringNetworkAccess {
		if requiresNetwork {
			t.Fatalf("feature %s should not require network access", feature)
		}
	}
	for _, feature := range []string{"stable_symbol_ids", "semantic_diff", "ndjson_snapshot"} {
		if !caps.OptionalLocalOnlyFeatures[feature] {
			t.Fatalf("optional feature %s not advertised: %#v", feature, caps.OptionalLocalOnlyFeatures)
		}
	}
}

func TestWriteSnapshotNDJSON(t *testing.T) {
	snapshot := ProviderSnapshot{
		Header: SnapshotHeader{
			SchemaVersion:   SchemaVersion,
			Provider:        ProviderName,
			ProviderVersion: "test",
		},
		Files: []FileRecord{{RecordType: "file", ID: "repo:file:main.go", Path: "main.go", Blob: "abc"}},
		Symbols: []SymbolRecord{{
			RecordType:      "symbol",
			ID:              "id",
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "function",
			Name:            "main",
			QualifiedName:   "main",
			FilePath:        "main.go",
			Language:        "Go",
		}},
		Relations: []RelationRecord{{RecordType: "relation", FromID: "file", ToID: "id", Type: "DEFINES", WarningCodes: []string{}}},
	}

	var out bytes.Buffer
	if err := WriteSnapshotNDJSON(&out, snapshot); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d:\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("invalid json line %q: %v", line, err)
		}
	}
}

func TestBuildProviderSnapshotReportsParseErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "good.py", "def valid():\n    return True\n")
	writeFile(t, repo, "bad.py", "def broken(:\n    return False\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	seenSymbol := false
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == "valid" {
			seenSymbol = true
		}
	}
	if !seenSymbol {
		t.Fatalf("valid file symbols were not emitted: %#v", snapshot.Symbols)
	}
	var found bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.Code == "E_PARSE_ERROR" && failure.FilePath == "bad.py" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing parse error partial failure: %#v", snapshot.Header.PartialFailures)
	}
	if snapshot.Header.Stats.CompletenessLevel == "ok" {
		t.Fatalf("parse failures should affect completeness: %#v", snapshot.Header.Stats)
	}
}

func TestBuildRelationsUsesSymbolBlockIdentifierLookup(t *testing.T) {
	const symbolCount = 5000

	files := make([]FileRecord, 0, symbolCount+1)
	recordsByFile := make(map[string][]SymbolRecord, symbolCount+1)
	contentByFile := make(map[string]string, symbolCount+1)

	for i := 0; i < symbolCount; i++ {
		path := "pkg/symbol_" + strconv.Itoa(i) + ".go"
		name := "UnrelatedSymbol" + strconv.Itoa(i)
		if i == symbolCount-1 {
			name = "TargetSymbol"
		}
		symbol := SymbolRecord{
			RecordType:    "symbol",
			ID:            "sym-" + strconv.Itoa(i),
			Kind:          "function",
			Name:          name,
			QualifiedName: name,
			FilePath:      path,
			StartLine:     1,
			EndLine:       3,
			Language:      "Go",
		}
		files = append(files, FileRecord{RecordType: "file", ID: fileID("repo", path), Path: path, Language: "Go"})
		recordsByFile[path] = []SymbolRecord{symbol}
		contentByFile[path] = "package pkg\nfunc " + name + "() {}\n"
	}

	caller := SymbolRecord{
		RecordType:    "symbol",
		ID:            "caller",
		Kind:          "function",
		Name:          "Caller",
		QualifiedName: "Caller",
		FilePath:      "pkg/caller.go",
		StartLine:     2,
		EndLine:       4,
		Language:      "Go",
	}
	files = append(files, FileRecord{RecordType: "file", ID: fileID("repo", caller.FilePath), Path: caller.FilePath, Language: "Go"})
	recordsByFile[caller.FilePath] = []SymbolRecord{caller}
	contentByFile[caller.FilePath] = "package pkg\nfunc Caller() {\n\tTargetSymbol()\n}\n"

	relations := buildRelations("repo", files, recordsByFile, contentByFile)

	var sawTargetCall bool
	for _, relation := range relations {
		if relation.Type != "CALLS" {
			continue
		}
		if relation.FromID != caller.ID {
			t.Fatalf("unexpected CALLS relation from non-caller symbol: %#v", relation)
		}
		switch relation.ToID {
		case "sym-" + strconv.Itoa(symbolCount-1):
			sawTargetCall = true
		case "sym-0":
			t.Fatalf("unrelated symbol was emitted as a CALLS relation: %#v", relation)
		default:
			t.Fatalf("unexpected CALLS relation: %#v", relation)
		}
	}
	if !sawTargetCall {
		t.Fatalf("missing CALLS relation from caller to TargetSymbol in %#v", relations)
	}
}

func TestBuildRelationsDropsAmbiguousCrossFileCallNameCollisions(t *testing.T) {
	files := []FileRecord{
		{RecordType: "file", ID: fileID("repo", "caller.go"), Path: "caller.go", Language: "Go"},
		{RecordType: "file", ID: fileID("repo", "embeddings.ts"), Path: "embeddings.ts", Language: "TypeScript"},
		{RecordType: "file", ID: fileID("repo", "runtime.ts"), Path: "runtime.ts", Language: "TypeScript"},
	}
	recordsByFile := map[string][]SymbolRecord{
		"caller.go": {{
			RecordType:    "symbol",
			ID:            "caller",
			Kind:          "function",
			Name:          "Login",
			QualifiedName: "Login",
			FilePath:      "caller.go",
			StartLine:     1,
			EndLine:       4,
			Language:      "Go",
		}},
		"embeddings.ts": {{
			RecordType:    "symbol",
			ID:            "embeddings-sleep",
			Kind:          "function",
			Name:          "sleep",
			QualifiedName: "sleep",
			FilePath:      "embeddings.ts",
			StartLine:     1,
			EndLine:       3,
			Language:      "TypeScript",
		}},
		"runtime.ts": {{
			RecordType:    "symbol",
			ID:            "runtime-sleep",
			Kind:          "function",
			Name:          "sleep",
			QualifiedName: "sleep",
			FilePath:      "runtime.ts",
			StartLine:     1,
			EndLine:       3,
			Language:      "TypeScript",
		}},
	}
	contentByFile := map[string]string{
		"caller.go":     "func Login() {\n\tsleep := options.Sleep\n\tsleep(interval)\n}\n",
		"embeddings.ts": "function sleep(ms: number) {}\n",
		"runtime.ts":    "function sleep(ms: number) {}\n",
	}

	for _, relation := range buildRelations("repo", files, recordsByFile, contentByFile) {
		if relation.Type == "CALLS" && relation.FromID == "caller" {
			t.Fatalf("ambiguous sleep call should not resolve globally: %#v", relation)
		}
	}
}

func TestEntitySymbolsDisambiguatesDuplicateNames(t *testing.T) {
	symbols := entitySymbols("gh/example/repo", "src/session.ts", "TypeScript", []Entity{
		{Kind: "method", Name: "Session.toTime", StartLine: 10, EndLine: 12},
		{Kind: "method", Name: "Session.toTime", StartLine: 20, EndLine: 22},
		{Kind: "method", Name: "Session.toPosition", StartLine: 30, EndLine: 32},
	})

	ids := map[string]bool{}
	for _, symbol := range symbols {
		if ids[symbol.ID] {
			t.Fatalf("duplicate symbol id %q in %#v", symbol.ID, symbols)
		}
		ids[symbol.ID] = true
	}
	if symbols[0].ID == "gh/example/repo:TypeScript:src/session.ts:method:Session.toTime" {
		t.Fatalf("first duplicate was not disambiguated: %#v", symbols)
	}
	if symbols[1].ID == "gh/example/repo:TypeScript:src/session.ts:method:Session.toTime" {
		t.Fatalf("second duplicate was not disambiguated: %#v", symbols)
	}
	if symbols[2].ID != "gh/example/repo:TypeScript:src/session.ts:method:Session.toPosition" {
		t.Fatalf("unique symbol id changed: %q", symbols[2].ID)
	}
}

func TestEntitySymbolsKeepCompoundIDStableAcrossBodyEdits(t *testing.T) {
	before := entitySymbols("gh/example/repo", "src/auth.py", "Python", []Entity{
		{Kind: "function", Name: "validate_token", StartLine: 1, EndLine: 2, Signature: "def validate_token(token):", BodyHash: "old"},
	})
	after := entitySymbols("gh/example/repo", "src/auth.py", "Python", []Entity{
		{Kind: "function", Name: "validate_token", StartLine: 1, EndLine: 4, Signature: "def validate_token(token):", BodyHash: "new"},
	})
	if before[0].ID != after[0].ID {
		t.Fatalf("compound id changed across body edit: before=%q after=%q", before[0].ID, after[0].ID)
	}
}

func TestBuildProviderSnapshotReadsAdvertisedHeadTree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "tracked.py", "def dirty():\n    return False\n")
	writeFile(t, repo, "untracked.py", "def should_not_emit():\n    return True\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.Commit == "" || snapshot.Header.Tree == "" {
		t.Fatalf("missing git metadata: %#v", snapshot.Header)
	}

	seenSymbols := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		seenSymbols[symbol.QualifiedName] = true
		if symbol.FilePath == "untracked.py" {
			t.Fatalf("snapshot included untracked file symbol: %#v", symbol)
		}
	}
	if !seenSymbols["committed"] {
		t.Fatalf("snapshot did not include committed symbol: %#v", snapshot.Symbols)
	}
	if seenSymbols["dirty"] || seenSymbols["should_not_emit"] {
		t.Fatalf("snapshot included working-tree-only symbols: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeIncludesDirtyFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "tracked.py", "def dirty():\n    return False\n")
	writeFile(t, repo, "untracked.py", "def worktree_only():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		NoNetwork: true,
		Worktree:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	seenSymbols := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		seenSymbols[symbol.QualifiedName] = true
	}
	if !seenSymbols["dirty"] || !seenSymbols["worktree_only"] {
		t.Fatalf("snapshot did not include worktree symbols: %#v", snapshot.Symbols)
	}
	if seenSymbols["committed"] {
		t.Fatalf("snapshot included HEAD-only symbol: %#v", snapshot.Symbols)
	}
	if len(snapshot.Header.Warnings) != 1 || snapshot.Header.Warnings[0].Code != "W_WORKTREE_SNAPSHOT" {
		t.Fatalf("warnings = %#v", snapshot.Header.Warnings)
	}
	if snapshot.Header.Commit == "" || snapshot.Header.Tree == "" {
		t.Fatalf("worktree snapshot should include HEAD commit/tree metadata: %#v", snapshot.Header)
	}
}

func TestBuildProviderSnapshotWorktreeHonorsRootGitignore(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\n")
	writeFile(t, repo, "cache/ignored.py", "def ignored():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "cache/")
	if snapshotHasSymbol(snapshot, "ignored") {
		t.Fatalf("snapshot included ignored symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeHonorsAdditionalIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "generated/\n")
	writeFile(t, repo, "generated/ignored.py", "def ignored():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{".brainignore"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "generated/")
}

func TestBuildProviderSnapshotWorktreeCombinesMultipleIgnoreFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "cache/\n")
	writeFile(t, repo, ".semignore", "# comments and blanks are ignored\n\n**/generated.py\nbenchmarks/agent-brain/results/\n")
	writeFile(t, repo, "cache/cache.py", "def cache():\n    return True\n")
	writeFile(t, repo, "src/generated.py", "def generated():\n    return True\n")
	writeFile(t, repo, "benchmarks/agent-brain/results/result.py", "def result():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{".brainignore", ".semignore"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	for _, prefix := range []string{"cache/", "benchmarks/agent-brain/results/"} {
		assertSnapshotOmitsPathPrefix(t, snapshot, prefix)
	}
	if snapshotHasPath(snapshot, "src/generated.py") {
		t.Fatalf("snapshot included ignored recursive glob path: %#v", snapshot.Files)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeFileReopensIgnoredDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\n")
	writeFile(t, repo, ".seminclude", "cache/\n")
	writeFile(t, repo, "cache/included.py", "def included():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "included") {
		t.Fatalf("snapshot did not include file from reopened directory: %#v", snapshot.Symbols)
	}
	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeFileWinsAfterIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "generated/\n")
	writeFile(t, repo, ".seminclude", "generated/\n")
	writeFile(t, repo, "generated/included.py", "def included():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IgnoreFiles:  []string{".brainignore"},
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "included") {
		t.Fatalf("snapshot did not include file from include-file override: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeDirectoryKeepsSpecificFileIgnore(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\ncache/skip.py\n")
	writeFile(t, repo, ".seminclude", "cache/\n")
	writeFile(t, repo, "cache/include.py", "def include_me():\n    return True\n")
	writeFile(t, repo, "cache/skip.py", "def skip_me():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "include_me") {
		t.Fatalf("snapshot did not include file from reopened directory: %#v", snapshot.Symbols)
	}
	if snapshotHasPath(snapshot, "cache/skip.py") || snapshotHasSymbol(snapshot, "skip_me") {
		t.Fatalf("snapshot included specifically ignored file: files=%#v symbols=%#v", snapshot.Files, snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotMissingIncludeFileFailsClosed(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{"does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected missing include file error")
	}
	if !strings.Contains(err.Error(), "include file") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("missing include file error was not clear: %v", err)
	}
}

func TestBuildProviderSnapshotIgnoredUnsupportedFilesDoNotProduceFailures(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "ignored/\n")
	writeFile(t, repo, "ignored/Unsupported.dart", "class Ignored {}\n")
	writeFile(t, repo, "Visible.dart", "class Visible {}\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawVisibleFailure bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == "ignored/Unsupported.dart" {
			t.Fatalf("ignored unsupported file produced a partial failure: %#v", snapshot.Header.PartialFailures)
		}
		if failure.FilePath == "Visible.dart" && failure.Code == "E_UNSUPPORTED_LANGUAGE" {
			sawVisibleFailure = true
		}
	}
	if !sawVisibleFailure {
		t.Fatalf("visible unsupported file did not produce a partial failure: %#v", snapshot.Header.PartialFailures)
	}
}

func TestBuildProviderSnapshotMissingIgnoreFileFailsClosed(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{"does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected missing ignore file error")
	}
	if !strings.Contains(err.Error(), "ignore file") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("missing ignore file error was not clear: %v", err)
	}
}

func TestBuildProviderSnapshotHeadDoesNotReadLiveIgnoreFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, ".gitignore", "tracked.py\nignored/\n")
	writeFile(t, repo, "ignored/worktree.py", "def worktree_only():\n    return True\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "committed") {
		t.Fatalf("HEAD snapshot did not include committed tracked symbol: %#v", snapshot.Symbols)
	}
	if snapshotHasSymbol(snapshot, "worktree_only") {
		t.Fatalf("HEAD snapshot included ignored untracked symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWarnsWithoutGitHead(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Header.Warnings) != 1 {
		t.Fatalf("warnings = %#v", snapshot.Header.Warnings)
	}
	if snapshot.Header.Warnings[0].Code != "E_NO_GIT_HEAD" {
		t.Fatalf("warning code = %q", snapshot.Header.Warnings[0].Code)
	}
	if snapshot.Header.Commit != "" || snapshot.Header.Tree != "" {
		t.Fatalf("unexpected git metadata: %#v", snapshot.Header)
	}
}

func TestBuildProviderSnapshotUsesGitHubSSHRepoKey(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "remote", "add", "origin", "git@github.com:jayparikh/agentviz.git")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "gh/jayparikh/agentviz" {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotUsesGitHubHTTPSRepoKey(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "remote", "add", "origin", "https://github.com/jayparikh/agentviz.git/")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "gh/jayparikh/agentviz" {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotFallsBackWithoutSupportedRemote(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "local/"+filepath.Base(repo) {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotReportsUnsupportedSourceFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Supported.py", "def validate_token(token):\n    return bool(token)\n")
	writeFile(t, repo, "Unsupported.dart", "class Unsupported {}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.Code == "E_UNSUPPORTED_LANGUAGE" && failure.FilePath == "Unsupported.dart" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing unsupported language partial failure: %#v", snapshot.Header.PartialFailures)
	}
}

func TestGoImportScannerOnlyReadsImportDeclarations(t *testing.T) {
	imports := importsFor("main.go", `package main

import (
	"fmt"
	alias "net/http"
)

var notImport = "not/a/package"

func main() {
	_ = "also/not/imported"
	fmt.Println(http.MethodGet)
}
`)
	got := strings.Join(imports, ",")
	if got != "fmt,net/http" {
		t.Fatalf("imports = %q", got)
	}
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotHasSymbol(snapshot ProviderSnapshot, qualifiedName string) bool {
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == qualifiedName {
			return true
		}
	}
	return false
}

func snapshotHasPath(snapshot ProviderSnapshot, path string) bool {
	for _, file := range snapshot.Files {
		if file.Path == path {
			return true
		}
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == path {
			return true
		}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == path {
			return true
		}
	}
	for _, warning := range snapshot.Header.Warnings {
		if warning.FilePath == path {
			return true
		}
	}
	return false
}

func assertSnapshotOmitsPathPrefix(t *testing.T, snapshot ProviderSnapshot, prefix string) {
	t.Helper()
	for _, file := range snapshot.Files {
		if strings.HasPrefix(file.Path, prefix) {
			t.Fatalf("snapshot included ignored file record: %#v", file)
		}
	}
	for _, symbol := range snapshot.Symbols {
		if strings.HasPrefix(symbol.FilePath, prefix) {
			t.Fatalf("snapshot included ignored symbol record: %#v", symbol)
		}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if strings.HasPrefix(failure.FilePath, prefix) {
			t.Fatalf("snapshot included ignored partial failure: %#v", failure)
		}
	}
	for _, warning := range snapshot.Header.Warnings {
		if strings.HasPrefix(warning.FilePath, prefix) {
			t.Fatalf("snapshot included ignored warning: %#v", warning)
		}
	}
	for _, relation := range snapshot.Relations {
		if strings.Contains(relation.FromID, prefix) || strings.Contains(relation.ToID, prefix) {
			t.Fatalf("snapshot included ignored relation: %#v", relation)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func externalByID(records []ExternalRecord, id string) ExternalRecord {
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	return ExternalRecord{}
}

func symbolByKindAndName(records []SymbolRecord, kind, qualifiedName string) SymbolRecord {
	for _, record := range records {
		if record.Kind == kind && record.QualifiedName == qualifiedName {
			return record
		}
	}
	return SymbolRecord{}
}
