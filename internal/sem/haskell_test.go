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
  pure $ combine res ` + "`orElse`" + ` fallback
  where
    i = interpretSymbolicPath mbWorkDir
    pkgId = localPackage lbi
`
	sites := haskellCallSites(block)
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
	} {
		if got[bogus] {
			t.Fatalf("bogus call site %+v in %+v", bogus, sites)
		}
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
