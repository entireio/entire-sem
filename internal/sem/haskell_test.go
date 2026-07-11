package sem

import (
	"strings"
	"testing"
)

func TestStripHaskellCodeText(t *testing.T) {
	// `--` line comments (but not operators built from dashes), nested
	// `{- -}` block comments, string literals, and character literals must be
	// masked so their contents never register as call sites; primed
	// identifiers and Template Haskell quotes share the quote character and
	// must survive; newlines survive so offsets keep line context.
	in := "run x' = do -- commentedCall x\n" +
		"  {- blockCall y {- Nested.call z -} still commented -}\n" +
		"  putStrLn \"fakeCall inside a string {- not a comment\"\n" +
		"  let sep = ';'\n" +
		"  arrow <- pure (x' --> sep)\n" +
		"#if MIN_VERSION_base(4,9,0)\n" +
		"  cppCall arrow\n" +
		"#endif\n" +
		"  realCall x' sep\n"
	out := stripHaskellCodeText(in)
	for _, gone := range []string{"commentedCall", "blockCall", "Nested.call", "fakeCall", "MIN_VERSION_base"} {
		if strings.Contains(out, gone) {
			t.Fatalf("stripHaskellCodeText left %q in:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"putStrLn", "x' --> sep", "cppCall arrow", "realCall x' sep"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("stripHaskellCodeText dropped %q from:\n%s", kept, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatalf("stripHaskellCodeText changed line count:\n%s", out)
	}
	// The quote characters of masked literals stay: a string or character
	// literal still reads as an argument in application position.
	if !strings.Contains(out, "\"") || strings.Count(out, "'") < 4 {
		t.Fatalf("stripHaskellCodeText dropped literal quotes:\n%s", out)
	}
	// A `'` after an identifier is part of the name, not a character literal:
	// masking it as one would swallow the code between two primed names.
	primes := "f a' b = g a' (h b)"
	if got := stripHaskellCodeText(primes); got != primes {
		t.Fatalf("primed identifiers were masked: %q -> %q", primes, got)
	}
}

func TestHaskellCallSites(t *testing.T) {
	block := `writePersist mbWorkDir distPref lbi = do
  createDirectoryIfMissing False (i distPref)
  writeFileAtomic (i $ localBuildInfoFile distPref) $
    BLC8.unlines [showHeader pkgId, structuredEncode lbi]
  res <- fetchConfig distPref
  case res of
    Just cfg -> useConfig cfg
    Nothing -> pure ()
  pure (map tshow res)
  pure (map Text.pack labels)
  let shadowed = id
  pure (map shadowed res)
  pure $ combine res ` + "`orElse`" + ` fallback
  where
    i = interpretSymbolicPath mbWorkDir
    pkgId = localPackage lbi
process xs = let helper = negate in map helper xs
`
	sites := haskellCallSites(block, nil)
	got := map[haskellCallSite]bool{}
	for _, site := range sites {
		got[site] = true
	}
	for _, want := range []haskellCallSite{
		// Qualified application through a module alias.
		{Path: "BLC8", Name: "unlines"},
		// Bare application at the start of a do statement (the previous
		// line's trailing `)` must not demote it to argument position).
		{Name: "writeFileAtomic"},
		{Name: "createDirectoryIfMissing"},
		// `fn $ arg` and `$`-fed application heads inside brackets.
		{Name: "localBuildInfoFile"},
		{Name: "showHeader"},
		{Name: "structuredEncode"},
		// The call on the right of a do-notation bind (`res <- fetchConfig …`).
		{Name: "fetchConfig"},
		// A call in a case alternative's body.
		{Name: "useConfig"},
		// Backtick infix application.
		{Name: "orElse"},
		// Calls on the right-hand side of where bindings.
		{Name: "interpretSymbolicPath"},
		{Name: "localPackage"},
		// Higher-order library calls can apply a function supplied as an
		// argument, as in `map tshow xs`.
		{Name: "tshow"},
		{Path: "Text", Name: "pack"},
	} {
		if !got[want] {
			t.Fatalf("missing call site %+v in %+v", want, sites)
		}
	}
	// Not calls: the equation head and its parameters bind names
	// (writePersist, mbWorkDir, distPref, lbi); `res`/`cfg` are do- and
	// case-binders; `i` and `pkgId` are where-binders, and the *use* of the
	// shadowing `i` must not surface either; `combine res` sits behind `$`
	// with `res` a plain argument; `fallback` trails the backtick operator
	// with no argument of its own, so it is an operand, not an application.
	for _, bogus := range []haskellCallSite{
		{Name: "writePersist"},
		{Name: "mbWorkDir"},
		{Name: "distPref"},
		{Name: "lbi"},
		{Name: "res"},
		{Name: "cfg"},
		{Name: "i"},
		{Name: "pkgId"},
		{Name: "fallback"},
		{Name: "shadowed"},
		// `helper` is bound by an inline single-line `let ... in`, so the
		// higher-order pass (`map helper xs`) must not surface it as a call.
		{Name: "helper"},
	} {
		if got[bogus] {
			t.Fatalf("bogus call site %+v in %+v", bogus, sites)
		}
	}
}

// The higher-order argument pass reports a function passed as the first
// argument of map/mapM_/filter/… as a call site. The FIRST binder of a braced
// or single-line do/let block is stepped over by the physical-line binder scan
// (it follows `do {`/`do `/`let {`, not a `;` and not a bare `let `), so the
// higher-order pass must consult its own binder set to avoid surfacing that
// do-bound or let-bound local as a call site (it would otherwise resolve to a
// same-named top-level function — a wrong CALLS edge). Each block is a single
// symbol's definition, exactly as the pipeline slices them.
func TestHaskellHigherOrderShadowedLeadingBinder(t *testing.T) {
	cases := []struct {
		block string
		bogus haskellCallSite
	}{
		// Braced single-line do; leading `<-` binder.
		{"run env msgs = do { logger <- getLogger env; mapM_ logger msgs }\n", haskellCallSite{Name: "logger"}},
		// Brace-less single-line do; leading `<-` binder.
		{"run items = do fn <- getFn; mapM_ fn items\n", haskellCallSite{Name: "fn"}},
		// Brace-let; leading `=` binder.
		{"process xs = let { f = getFn; g = other } in map f xs\n", haskellCallSite{Name: "f"}},
	}
	for _, tc := range cases {
		got := map[haskellCallSite]bool{}
		for _, site := range haskellCallSites(tc.block, nil) {
			got[site] = true
		}
		if got[tc.bogus] {
			t.Fatalf("higher-order pass surfaced shadowed local %+v for block %q -> %+v", tc.bogus, tc.block, got)
		}
	}
}

// The higher-order binder set must also cover locals bound by single-line
// pattern destructuring (`let (a, render) = ...`) and lambda parameters
// (`\handler -> ...`). The physical-line binder scan steps over both (they sit
// after the equation's own `=`), so without them the higher-order pass would
// surface the shadowing local as a first-argument call site and resolve it to
// a same-named top-level function — a wrong CALLS edge. Each block is a single
// symbol's definition, as the pipeline slices them.
func TestHaskellHigherOrderShadowedPatternAndLambdaBinder(t *testing.T) {
	cases := []struct {
		block string
		bogus haskellCallSite
	}{
		// Tuple-destructuring let-pattern binder handed to `map`.
		{"draw xs = let (a, render) = prepare xs in map render xs\n", haskellCallSite{Name: "render"}},
		// List-destructuring let-pattern binder.
		{"draw xs = let [render] = prepare xs in map render xs\n", haskellCallSite{Name: "render"}},
		// Constructor-destructuring let-pattern binder.
		{"draw xs = let (Just render) = prepare xs in map render xs\n", haskellCallSite{Name: "render"}},
		// Lambda parameter handed to `mapM_`.
		{"runAll = \\handler -> mapM_ handler queue\n", haskellCallSite{Name: "handler"}},
	}
	for _, tc := range cases {
		got := map[haskellCallSite]bool{}
		for _, site := range haskellCallSites(tc.block, nil) {
			got[site] = true
		}
		if got[tc.bogus] {
			t.Fatalf("higher-order pass surfaced shadowed local %+v for block %q -> %+v", tc.bogus, tc.block, got)
		}
	}
}

// The higher-order binder set must also cover case / \case alternative pattern
// binders in the single-line brace form (`case x of { Just f -> map f xs }`).
// The physical-line binder scan stops at the equation's own `=`, so the
// alternative's `Pat ->` head — at bracket depth >= 1 inside the braces — is
// never reached; without the arrow-LHS coverage the higher-order pass would
// surface the case-bound local as a first-argument call site and resolve it to
// a same-named top-level function (a wrong CALLS edge). Each block is one
// symbol's definition, as the pipeline slices them.
func TestHaskellHigherOrderShadowedArrowLHSBinder(t *testing.T) {
	cases := []struct {
		block string
		bogus haskellCallSite
	}{
		// Single-line brace `case of` alternative binder handed to `mapM_`.
		{"process cb xs = case cb of { Just render -> mapM_ render xs; Nothing -> pure () }\n", haskellCallSite{Name: "render"}},
		// `\case` alternative binder handed to `map`.
		{"process = \\case { Just render -> map render xs; Nothing -> [] }\n", haskellCallSite{Name: "render"}},
		// Multi-way-if guard binder handed to `filter`.
		{"pick p xs = if | Just render <- p -> filter render xs | otherwise -> xs\n", haskellCallSite{Name: "render"}},
	}
	for _, tc := range cases {
		got := map[haskellCallSite]bool{}
		for _, site := range haskellCallSites(tc.block, nil) {
			got[site] = true
		}
		if got[tc.bogus] {
			t.Fatalf("higher-order pass surfaced shadowed arrow-LHS local %+v for block %q -> %+v", tc.bogus, tc.block, got)
		}
	}
}

// End-to-end guard for the confirmed case-alternative false edge: a case-bound
// local that shadows a same-file top-level function, applied as a higher-order
// first argument inside a single-line brace `case of`, must not fabricate a
// CALLS edge to that top-level function through the higher-order argument pass.
func TestHaskellHigherOrderShadowedCaseBinderNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/App.hs", `module App where

render :: Int -> IO ()
render n = print n

process :: Maybe (Int -> IO ()) -> [Int] -> IO ()
process cb xs = case cb of { Just render -> mapM_ render xs; Nothing -> pure () }

report :: [Int] -> [String]
report xs = map draw xs

draw :: Int -> String
draw n = show n
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, okF := symbolsByID[r.FromID]
		to, okT := symbolsByID[r.ToID]
		if !okF || !okT {
			continue
		}
		calls[edge{from.Name, to.Name}] = true
	}
	// The `render` in `mapM_ render xs` is the case-bound local, not the
	// top-level `render` function: no edge.
	if calls[edge{"process", "render"}] {
		t.Fatalf("shadowed case-bound local fabricated CALLS edge process -> render in %v", calls)
	}
	// The higher-order pass still resolves a genuine top-level function passed
	// to `map`, so the guard above is not vacuous.
	if !calls[edge{"report", "draw"}] {
		t.Fatalf("missing legitimate higher-order CALLS edge report -> draw in %v", calls)
	}
}

// The pattern/lambda binder extension must not suppress a legitimate
// higher-order call: `map render xs` with no shadowing local still resolves
// `render` to a top-level function.
func TestHaskellHigherOrderLegitimateCallNotSuppressed(t *testing.T) {
	block := "report xs = map render xs\n"
	got := map[haskellCallSite]bool{}
	for _, site := range haskellCallSites(block, nil) {
		got[site] = true
	}
	if !got[(haskellCallSite{Name: "render"})] {
		t.Fatalf("legitimate higher-order call `render` was suppressed: %+v", got)
	}
}

// When the workspace defines its own function whose name collides with a
// library combinator (`find`), the higher-order first-argument pass must be
// suppressed for that name: `find db key` calls the user's `find` with `db` as
// ordinary data, so the data argument must not be surfaced as a call site. The
// bare application pass still reports `find` itself.
func TestHaskellCallSitesHOFSuppressedForUserDefinedName(t *testing.T) {
	block := "go key = find db key\n"
	userDefined := func(name string) bool { return name == "find" }
	got := map[haskellCallSite]bool{}
	for _, site := range haskellCallSites(block, userDefined) {
		got[site] = true
	}
	if got[(haskellCallSite{Name: "db"})] {
		t.Fatalf("HOF pass surfaced data argument `db` for user-defined `find`: %+v", got)
	}
	if !got[(haskellCallSite{Name: "find"})] {
		t.Fatalf("bare call `find` was dropped: %+v", got)
	}
	// Positive control: without the predicate the HOF pass DOES surface `db`, so
	// the suppression above is what removes it (the guard is not vacuous).
	base := map[haskellCallSite]bool{}
	for _, site := range haskellCallSites(block, nil) {
		base[site] = true
	}
	if !base[(haskellCallSite{Name: "db"})] {
		t.Fatalf("expected HOF pass to surface `db` when `find` is not user-defined: %+v", base)
	}
}

// End-to-end: a user-defined function whose name collides with a library
// higher-order combinator (`find`) must not trigger the first-argument pass,
// so its data argument (`db`) is never fabricated as a CALLS target, while the
// genuine call to the user's `find` still resolves. A sibling `map render xs`
// with no user-defined `map` is the positive control: the HOF pass still fires
// for a real library combinator.
func TestHaskellHigherOrderUserDefinedCombinatorNoDataEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/App.hs", `module App where

db :: Int
db = 0

find :: Int -> Int -> Int
find a b = a + b

go :: Int -> Int
go key = find db key

render :: Int -> String
render n = show n

report :: [Int] -> [String]
report xs = map render xs
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, okF := symbolsByID[r.FromID]
		to, okT := symbolsByID[r.ToID]
		if !okF || !okT {
			continue
		}
		calls[edge{from.Name, to.Name}] = true
	}
	if calls[edge{"go", "db"}] {
		t.Fatalf("HOF pass fabricated CALLS go -> db for user-defined `find` in %v", calls)
	}
	if !calls[edge{"go", "find"}] {
		t.Fatalf("missing genuine CALLS go -> find in %v", calls)
	}
	if !calls[edge{"report", "render"}] {
		t.Fatalf("positive control missing: CALLS report -> render (map render) in %v", calls)
	}
}

// Scoping the higher-order binder names separately means a local bound by a
// nested do-bind or inline let no longer suppresses a legitimate head-position
// bare call to a same-named symbol elsewhere in the block (the bare-application
// pass keeps consuming the original block-wide binder set only).
func TestHaskellBareCallNotSuppressedByNestedBinder(t *testing.T) {
	block := "run n = wrap (parse n)\n  where\n    aux = do { a <- get; parse <- other; pure parse }\n"
	got := map[haskellCallSite]bool{}
	for _, site := range haskellCallSites(block, nil) {
		got[site] = true
	}
	if !got[(haskellCallSite{Name: "parse"})] {
		t.Fatalf("head-position bare call `parse` suppressed by nested do-bind of the same name: %+v", got)
	}
}

// End-to-end guard for the confirmed false edge: a do-bound local that shadows
// a same-file top-level function, bound as the leading binder of a braced
// single-line do, must not fabricate a CALLS edge to that top-level function
// through the higher-order argument pass.
func TestHaskellHigherOrderShadowedLocalNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/App.hs", `module App where

logger :: String -> IO ()
logger msg = putStrLn msg

render :: Int -> String
render n = show n

run :: env -> [String] -> IO ()
run env msgs = do { logger <- getLogger env; mapM_ logger msgs }

report :: [Int] -> [String]
report xs = map render xs
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, okF := symbolsByID[r.FromID]
		to, okT := symbolsByID[r.ToID]
		if !okF || !okT {
			continue
		}
		calls[edge{from.Name, to.Name}] = true
	}
	// The `logger` in `mapM_ logger msgs` is the do-bound local, not the
	// top-level `logger` function: no edge.
	if calls[edge{"run", "logger"}] {
		t.Fatalf("shadowed do-bound local fabricated CALLS edge run -> logger in %v", calls)
	}
	// The higher-order pass still resolves a genuine top-level function passed
	// to `map`, so the guard above is not vacuous.
	if !calls[edge{"report", "render"}] {
		t.Fatalf("missing legitimate higher-order CALLS edge report -> render in %v", calls)
	}
}

func TestHaskellImports(t *testing.T) {
	content := `module Distribution.Simple.Configure (configure) where

import qualified Data.ByteString.Lazy.Char8 as BLC8
import Distribution.Simple.Utils
import Distribution.Utils.Structured (structuredDecodeOrFailIO, structuredEncode)
import Distribution.Simple.Setup.Common as Setup
import qualified Distribution.Simple.PackageIndex
import Distribution.PackageDescription.Check hiding (doesFileExist)
import Data.List
  ( intersect
  , stripPrefix
  )
`
	imports := haskellImports(content)
	for alias, module := range map[string]string{
		"BLC8":                                  "Data.ByteString.Lazy.Char8",
		"Setup":                                 "Distribution.Simple.Setup.Common",
		"Distribution.Simple.PackageIndex":      "Distribution.Simple.PackageIndex",
		"Distribution.Utils.Structured":         "Distribution.Utils.Structured",
		"Distribution.PackageDescription.Check": "Distribution.PackageDescription.Check",
	} {
		if imports.aliases[alias] != module {
			t.Fatalf("alias %q = %q, want %q (all: %#v)", alias, imports.aliases[alias], module, imports.aliases)
		}
	}
	for name, module := range map[string]string{
		"structuredEncode": "Distribution.Utils.Structured",
		"intersect":        "Data.List",
		"stripPrefix":      "Data.List",
	} {
		if len(imports.explicitNames[name]) != 1 || imports.explicitNames[name][0] != module {
			t.Fatalf("explicit name %q = %#v, want [%q]", name, imports.explicitNames[name], module)
		}
	}
	open := map[string]bool{}
	for _, module := range imports.openModules {
		open[module] = true
	}
	// List-less imports (with or without an `as` alias) and `hiding` imports
	// deliver unqualified names; qualified and explicit-list imports do not.
	for _, want := range []string{"Distribution.Simple.Utils", "Distribution.Simple.Setup.Common", "Distribution.PackageDescription.Check"} {
		if !open[want] {
			t.Fatalf("open module %q missing from %#v", want, imports.openModules)
		}
	}
	for _, bogus := range []string{"Data.ByteString.Lazy.Char8", "Distribution.Utils.Structured", "Data.List", "Distribution.Simple.PackageIndex"} {
		if open[bogus] {
			t.Fatalf("module %q should not be open in %#v", bogus, imports.openModules)
		}
	}
	// A name from a hiding list is not an imported name.
	if len(imports.explicitNames["doesFileExist"]) != 0 {
		t.Fatalf("hiding list leaked into explicit names: %#v", imports.explicitNames["doesFileExist"])
	}
}

// Haskell CALLS extraction (evidence: on haskell/cabal the focus function
// writePersistBuildConfig had near-zero call matches). Qualified applications
// `Alias.fn args` resolve through the file's `import qualified ... as Alias`
// declarations to the callable named `fn` in the module's conventional source
// file (module A.B.C lives in A/B/C.hs); bare applications resolve to
// same-file top-level bindings, to explicit-import-list names, to modules
// imported unqualified, and — for re-exported names the import section cannot
// place — to a workspace-unique function name.
func TestHaskellCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/App/Config.hs", `module App.Config where

import qualified App.Util as Util
import App.Store (persistBlob)
import App.Render

header :: String
header = "top"

encodeConfig :: Int -> String
encodeConfig n = show n

writeConfig :: Int -> IO ()
writeConfig n = do
  -- Util.render commentedOut n
  let banner = "log: Util.render inside a string"
  ensureDir banner
  persistBlob (encodeConfig n) $
    Util.render [header, renderBody n]
  where
    ensureDir = putStrLn
`)
	writeFile(t, repo, "src/App/Util.hs", `module App.Util where

render :: [String] -> String
render = unlines
`)
	writeFile(t, repo, "src/App/Store.hs", `module App.Store where

persistBlob :: String -> String -> IO ()
persistBlob key payload = putStrLn (key ++ payload)
`)
	writeFile(t, repo, "src/App/Render.hs", `module App.Render where

renderBody :: Int -> String
renderBody = show
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		to, ok := symbolsByID[r.ToID]
		if !ok || to.Language != "Haskell" {
			continue
		}
		from, ok := symbolsByID[r.FromID]
		if !ok {
			// The only non-symbol CALLS source is the file-level top-level
			// scan; Haskell's top level is declarations (imports, exports,
			// signatures) that must not register as call sites.
			t.Fatalf("file-level CALLS edge into Haskell symbol %s should not exist", to.Name)
		}
		calls[edge{from.FilePath + ":" + from.Name, to.FilePath + ":" + to.Name}] = true
	}
	for _, want := range []edge{
		// Qualified application through the import alias (Util.render).
		{"src/App/Config.hs:writeConfig", "src/App/Util.hs:render"},
		// Bare application of a same-file top-level binding.
		{"src/App/Config.hs:writeConfig", "src/App/Config.hs:encodeConfig"},
		// Bare application of an explicit-import-list name.
		{"src/App/Config.hs:writeConfig", "src/App/Store.hs:persistBlob"},
		// Bare application delivered by an open (list-less) import.
		{"src/App/Config.hs:writeConfig", "src/App/Render.hs:renderBody"},
	} {
		if !calls[want] {
			t.Fatalf("missing Haskell CALLS edge %v in %v", want, calls)
		}
	}
	for e := range calls {
		// The commented-out application and the call named inside a string
		// literal must not fabricate edges, `header` is an argument inside
		// the list (not an application head), and the where-binder
		// `ensureDir` must not pull in anything.
		if e.to == "src/App/Config.hs:header" {
			t.Fatalf("argument-position name fabricated Haskell CALLS edge %v", e)
		}
		if e.from != "src/App/Config.hs:writeConfig" && strings.HasPrefix(e.from, "src/App/Config.hs") && e.to == "src/App/Util.hs:render" {
			t.Fatalf("masked text fabricated Haskell CALLS edge %v", e)
		}
	}
}
