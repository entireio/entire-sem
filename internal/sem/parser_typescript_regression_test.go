package sem

import (
	"strings"
	"testing"
)

// Constructs from entirehq/entire.io that the vendored TypeScript/TSX grammars
// reject; each is rewritten position-preservingly before parsing.

func requireNoTSParseError(t *testing.T, path, src string) []Entity {
	t.Helper()
	entities, _, status := TreeSitterParser{}.ParseWithStatus(path, src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	return entities
}

// `export type * from "./x"` (TS 5.0) is not in the grammar; the `type`
// keyword is blanked so it parses as a plain star re-export.
func TestTypeScriptExportTypeStarParses(t *testing.T) {
	requireNoTSParseError(t, "types.ts", "// Auto-generated database schema types.\nexport type * from \"./schema\"\n")
}

// Import-type references without `typeof` — `import("./types").Foo["k"]` in a
// type position — are rejected by the grammar (entire.io github.ts, shiki
// module declarations).
func TestTypeScriptBareImportTypeParses(t *testing.T) {
	entities := requireNoTSParseError(t, "github.ts",
		"function mapStatus(status: string): import(\"./types\").GitSourceFileStat[\"status\"] {\n"+
			"  return \"added\"\n"+
			"}\n")
	found := false
	for _, e := range entities {
		if e.Name == "mapStatus" {
			found = true
		}
	}
	if !found {
		t.Errorf("mapStatus missing: %+v", entities)
	}
	requireNoTSParseError(t, "shiki-modules.d.ts",
		"declare module \"@shikijs/langs/typescript\" {\n"+
			"  const lang: import(\"shiki\").LanguageRegistration[]\n"+
			"  export default lang\n"+
			"}\n")
}

// `unique` is the grammar's `unique symbol` keyword; after `<` it derails an
// ordinary identifier use (`i < unique.length`, entire.io checkpoints.ts).
func TestTypeScriptUniqueIdentifierAfterLessThanParses(t *testing.T) {
	requireNoTSParseError(t, "checkpoints.ts",
		"const unique = [...new Set([1])]\n"+
			"for (let i = 0; i < unique.length; i += 2) {\n"+
			"  console.log(i)\n"+
			"}\n")
}

// The mask must not touch a real `unique symbol` type operator.
func TestTypeScriptUniqueSymbolTypeUnmasked(t *testing.T) {
	src := "declare const s: unique symbol\ntype M = Map<unique symbol, string>\n"
	if got := maskTypeScriptCommonSyntax(src); got != src {
		t.Errorf("unique symbol should be untouched:\n%s", got)
	}
	requireNoTSParseError(t, "sym.ts", src)
}

// .tsx received no masking at all, so `typeof import(...)` inside generic call
// arguments — the standard vi.mock/importOriginal pattern — swallowed entire
// test files (entire.io frontend *.test.tsx).
func TestTSXTypeofImportGenericCallParses(t *testing.T) {
	src := "import { vi } from \"vitest\"\n" +
		"vi.mock(\"@tanstack/react-router\", async (importOriginal) => {\n" +
		"  const actual = await importOriginal<typeof import(\"@tanstack/react-router\")>()\n" +
		"  return {\n" +
		"    ...actual,\n" +
		"    RouterProvider: () => <div data-testid=\"router-provider\" />,\n" +
		"  }\n" +
		"})\n" +
		"const helper = {\n" +
		"  ...(await importOriginal<\n" +
		"    typeof import(\"@/hooks/useQuery\")\n" +
		"  >()),\n" +
		"  useQuery: (...args: unknown[]) => mock(...args),\n" +
		"}\n" +
		"const actual2 = await vi.importActual<typeof import(\"@/api\")>(\n" +
		"  \"@/api\",\n" +
		")\n"
	requireNoTSParseError(t, "AppRouter.test.tsx", src)
}

// A runtime dynamic import may take an arbitrary expression. The type-import
// mask must not stop at a nested call's first `)` and leave the outer `)` as a
// syntax error (`typeof import(resolveSpecifier())` used to become `any )`).
func TestTSXRuntimeTypeofDynamicImportWithNestedCallUnmasked(t *testing.T) {
	src := "export function moduleKind() {\n" +
		"  return typeof import(resolveSpecifier())\n" +
		"}\n"
	if got := maskTypeScriptCommonSyntax(src); got != src {
		t.Fatalf("runtime dynamic import should be untouched:\n%s", got)
	}
	entities := requireNoTSParseError(t, "runtime.tsx", src)
	found := false
	for _, e := range entities {
		if e.Name == "moduleKind" {
			found = true
		}
	}
	if !found {
		t.Errorf("moduleKind missing: %+v", entities)
	}
}

// Newline-separated type members whose names start with `in` (`in_x:`) close
// the enclosing body early; the keyword-property mask now covers `in_*` names
// and applies to .tsx (entire.io SystemStatus.tsx).
func TestTSXInPrefixedPropertyParses(t *testing.T) {
	entities := requireNoTSParseError(t, "SystemStatus.tsx",
		"interface StatusSummary {\n"+
			"  ongoing_incidents: unknown[]\n"+
			"  in_progress_maintenances: unknown[]\n"+
			"}\n"+
			"export function SystemStatus() {\n"+
			"  return <div />\n"+
			"}\n")
	found := false
	for _, e := range entities {
		if e.Name == "SystemStatus" {
			found = true
		}
		if strings.HasPrefix(e.Name, "ii") {
			t.Errorf("masked identifier leaked into entity name: %+v", e)
		}
	}
	if !found {
		t.Errorf("SystemStatus missing: %+v", entities)
	}
}

// Every rewrite must be byte-length-preserving: entity offsets are computed on
// the masked source but sliced from the original.
func TestTypeScriptMasksPreserveLength(t *testing.T) {
	for _, src := range []string{
		"export type * from \"./schema\"\n",
		"const x: import(\"./t\").Foo[\"k\"] = load()\n",
		"const n = 1 < unique.length\n",
		"const a = await importOriginal<typeof import(\"@x/y\")>()\n",
		"type T = typeof import(\n  \"@x/y\"\n)\n",
	} {
		if got := maskTypeScriptCommonSyntax(src); len(got) != len(src) {
			t.Errorf("length drift %d -> %d for %q", len(src), len(got), src)
		} else if strings.Count(got, "\n") != strings.Count(src, "\n") {
			t.Errorf("line-count drift for %q:\n%s", src, got)
		}
		if got := maskTSXUnsupportedSyntax(src); len(got) != len(src) {
			t.Errorf("tsx length drift %d -> %d for %q", len(src), len(got), src)
		}
	}
}
