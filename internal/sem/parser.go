package sem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	clojure "github.com/entireio/entire-sem/internal/sem/grammars/clojure"
	csharp "github.com/entireio/entire-sem/internal/sem/grammars/csharp"
	dart "github.com/entireio/entire-sem/internal/sem/grammars/dart"
	erlang "github.com/entireio/entire-sem/internal/sem/grammars/erlang"
	fsharp "github.com/entireio/entire-sem/internal/sem/grammars/fsharp"
	haskell "github.com/entireio/entire-sem/internal/sem/grammars/haskell"
	julia "github.com/entireio/entire-sem/internal/sem/grammars/julia"
	objc "github.com/entireio/entire-sem/internal/sem/grammars/objc"
	perl "github.com/entireio/entire-sem/internal/sem/grammars/perl"
	rlang "github.com/entireio/entire-sem/internal/sem/grammars/r"
	zig "github.com/entireio/entire-sem/internal/sem/grammars/zig"
	"github.com/entireio/entire-sem/internal/sem/pgsql"
	"github.com/entireio/entire-sem/internal/sem/zsh"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	"github.com/smacker/go-tree-sitter/ocaml"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/swift"
	treesittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	treesitterts "github.com/smacker/go-tree-sitter/typescript/typescript"
	treesitteryaml "github.com/smacker/go-tree-sitter/yaml"
)

type languageSpec struct {
	language      string
	grammar       *sitter.Language
	inventoryOnly bool
}

var treeSitterLanguages = map[string]languageSpec{
	".bash":     {language: "Bash", grammar: bash.GetLanguage()},
	".c":        {language: "C", grammar: c.GetLanguage()},
	".cc":       {language: "C++", grammar: cpp.GetLanguage()},
	".cpp":      {language: "C++", grammar: cpp.GetLanguage()},
	".clj":      {language: "Clojure", grammar: clojure.GetLanguage()},
	".cljc":     {language: "Clojure", grammar: clojure.GetLanguage()},
	".cljs":     {language: "ClojureScript", grammar: clojure.GetLanguage()},
	".cs":       {language: "C#", grammar: csharp.GetLanguage()},
	".cue":      {language: "CUE", grammar: cue.GetLanguage()},
	".cxx":      {language: "C++", grammar: cpp.GetLanguage()},
	".dart":     {language: "Dart", grammar: dart.GetLanguage()},
	".erl":      {language: "Erlang", grammar: erlang.GetLanguage()},
	".ex":       {language: "Elixir", grammar: elixir.GetLanguage()},
	".exs":      {language: "Elixir", grammar: elixir.GetLanguage()},
	".hrl":      {language: "Erlang", grammar: erlang.GetLanguage()},
	".fs":       {language: "F#", grammar: fsharp.GetLanguage()},
	".fsi":      {language: "F#", grammar: fsharp.GetLanguage()},
	".fsscript": {language: "F#", grammar: fsharp.GetLanguage()},
	".fsx":      {language: "F#", grammar: fsharp.GetLanguage()},
	".go":       {language: "Go", grammar: golang.GetLanguage()},
	// Groovy has no grammar entry: it routes to the dedicated structural
	// parser in groovy.go (the best available tree-sitter-groovy grammar
	// fails on fundamental Groovy syntax).
	".gradle":     {language: "Groovy"},
	".groovy":     {language: "Groovy"},
	".gvy":        {language: "Groovy"},
	".h":          {language: "C", grammar: c.GetLanguage()},
	".hcl":        {language: "HCL", grammar: hcl.GetLanguage()},
	".html":       {language: "HTML"},
	".hh":         {language: "C++", grammar: cpp.GetLanguage()},
	".hs":         {language: "Haskell", grammar: haskell.GetLanguage()},
	".hsc":        {language: "Haskell", grammar: haskell.GetLanguage()},
	".hpp":        {language: "C++", grammar: cpp.GetLanguage()},
	".hxx":        {language: "C++", grammar: cpp.GetLanguage()},
	".java":       {language: "Java", grammar: java.GetLanguage()},
	".jl":         {language: "Julia", grammar: julia.GetLanguage()},
	".js":         {language: "JavaScript", grammar: javascript.GetLanguage()},
	".json":       {language: "JSON"},
	".json5":      {language: "JSON5"},
	".jsx":        {language: "JavaScript", grammar: treesittertsx.GetLanguage()},
	".kt":         {language: "Kotlin", grammar: kotlin.GetLanguage()},
	".kts":        {language: "Kotlin", grammar: kotlin.GetLanguage()},
	".css":        {language: "CSS"},
	".lua":        {language: "Lua", grammar: lua.GetLanguage()},
	".markdown":   {language: "Markdown"},
	".md":         {language: "Markdown"},
	".mk":         {language: "Make"},
	".m":          {language: "Objective-C", grammar: objc.GetLanguage()},
	".ml":         {language: "OCaml", grammar: ocaml.GetLanguage()},
	".mli":        {language: "OCaml", grammar: ocaml.GetLanguage()},
	".php":        {language: "PHP", grammar: php.GetLanguage()},
	".pl":         {language: "Perl", grammar: perl.GetLanguage()},
	".pm":         {language: "Perl", grammar: perl.GetLanguage()},
	".proto":      {language: "Protocol Buffers", grammar: protobuf.GetLanguage()},
	".py":         {language: "Python", grammar: python.GetLanguage()},
	".r":          {language: "R", grammar: rlang.GetLanguage()},
	".rb":         {language: "Ruby", grammar: ruby.GetLanguage()},
	".rs":         {language: "Rust", grammar: rust.GetLanguage()},
	".sbt":        {language: "Scala", grammar: scala.GetLanguage()},
	".scala":      {language: "Scala", grammar: scala.GetLanguage()},
	".sc":         {language: "Scala", grammar: scala.GetLanguage()},
	".sh":         {language: "Bash", grammar: bash.GetLanguage()},
	".sql":        {language: "SQL"},
	".swift":      {language: "Swift", grammar: swift.GetLanguage()},
	".svelte":     {language: "Svelte"},
	".tf":         {language: "HCL", grammar: hcl.GetLanguage()},
	".tfvars":     {language: "HCL", grammar: hcl.GetLanguage()},
	".toml":       {language: "TOML"},
	".ts":         {language: "TypeScript", grammar: treesitterts.GetLanguage()},
	".tsx":        {language: "TypeScript", grammar: treesittertsx.GetLanguage()},
	".vue":        {language: "Vue"},
	".xml":        {language: "XML"},
	".yaml":       {language: "YAML", grammar: treesitteryaml.GetLanguage()},
	".yml":        {language: "YAML", grammar: treesitteryaml.GetLanguage()},
	".zig":        {language: "Zig", grammar: zig.GetLanguage()},
	".zsh":        {language: "Zsh", grammar: zsh.GetLanguage()},
	".dockerfile": {language: "Dockerfile"},
}

type TreeSitterParser struct{}

const treeSitterParseTimeout = 5 * time.Second

type ParseStatus struct {
	ParseError bool
	Code       string
	Detail     string
}

func (TreeSitterParser) Parse(path, content string) ([]Entity, string) {
	entities, language, _ := TreeSitterParser{}.ParseWithStatus(path, content)
	return entities, language
}

func (TreeSitterParser) ParseWithStatus(path, content string) ([]Entity, string, ParseStatus) {
	spec, ok := languageForContent(path, content)
	if !ok {
		return nil, "", ParseStatus{}
	}
	if spec.language == "Kustomize" && looksLikeFluxKustomizationManifest(content) {
		spec = treeSitterLanguages[".yaml"]
	}
	if strings.EqualFold(filepath.Ext(path), ".h") && looksLikeObjectiveC(content) {
		// Objective-C classes are canonically anchored at their .h header
		// (`@interface Foo : NSObject` lives there; the .m holds the
		// @implementation), so sniffed headers parse with the vendored
		// tree-sitter-objc grammar like .m files instead of falling back to
		// inventory. Headers that don't sniff keep the C/C++ routing below.
		spec = treeSitterLanguages[".m"]
	} else if strings.EqualFold(filepath.Ext(path), ".h") && looksLikeCPlusPlusHeader(content) {
		spec = treeSitterLanguages[".hpp"]
	}
	flowJS := false
	if spec.language == "JavaScript" && looksLikeFlowJavaScript(content) {
		// Flow-typed JavaScript parses with the vendored TSX grammar (a
		// near-superset of Flow's annotation syntax) plus a small
		// position-preserving mask; tree-sitter-javascript chokes on every
		// type annotation. The language label stays "JavaScript", mirroring
		// the .jsx routing. See flowjs.go for the probe evidence.
		spec.grammar = treesittertsx.GetLanguage()
		flowJS = true
	}
	if spec.language == "SQL" {
		spec.grammar = pgsql.GetLanguage()
	}
	if spec.language == "Groovy" {
		// Groovy uses a dedicated structural parser (see groovy.go): the best
		// available tree-sitter-groovy grammar fails on fundamental syntax —
		// quoted method names, casts, slashy strings — leaving 1,400+ parse
		// errors on real repos, so it is not consulted at all.
		entities, status := groovyEntities(content)
		return entities, spec.language, status
	}
	if spec.grammar == nil {
		return fallbackEntities(path, content, spec.language), spec.language, ParseStatus{}
	}
	src := []byte(content)
	parseSrc := src
	if spec.language == "SQL" {
		parseSrc = []byte(maskPostgresUnsupportedSyntax(content))
	}
	if spec.language == "C" {
		parseSrc = []byte(maskCUnsupportedSyntax(content))
	}
	if spec.language == "Objective-C" {
		parseSrc = []byte(maskObjectiveCUnsupportedSyntax(content))
	}
	if spec.language == "Bash" {
		parseSrc = []byte(maskBashUnsupportedSyntax(content))
	}
	if spec.language == "Zsh" {
		parseSrc = []byte(maskZshUnsupportedSyntax(content))
	}
	if spec.language == "Java" {
		parseSrc = []byte(maskJavaUnsupportedSyntax(content))
	}
	if spec.language == "C#" {
		parseSrc = []byte(maskCSharpUnsupportedSyntax(content))
	}
	if spec.language == "C++" {
		parseSrc = []byte(maskCPlusPlusUnsupportedSyntax(content))
	}
	if spec.language == "Kotlin" {
		parseSrc = []byte(maskKotlinUnsupportedSyntax(path, content))
	}
	if spec.language == "Swift" {
		parseSrc = []byte(maskSwiftUnsupportedSyntax(content))
	}
	if spec.language == "Dart" {
		parseSrc = []byte(maskDartUnsupportedSyntax(content))
	}
	if spec.language == "OCaml" && strings.EqualFold(filepath.Ext(path), ".mli") {
		parseSrc = []byte(maskOCamlInterfaceSyntax(content))
	}
	if spec.language == "YAML" {
		parseSrc = []byte(maskYAMLUnsupportedSyntax(content))
	}
	if spec.language == "TypeScript" && !strings.EqualFold(filepath.Ext(path), ".tsx") {
		parseSrc = []byte(maskTypeScriptUnsupportedSyntax(content))
	}
	if spec.language == "Rust" {
		parseSrc = []byte(maskRustUnsupportedSyntax(content))
	}
	if flowJS {
		parseSrc = []byte(maskFlowJavaScriptUnsupportedSyntax(content))
	}
	ctx, cancel := context.WithTimeout(context.Background(), treeSitterParseTimeout)
	defer cancel()
	// Own the parser and tree explicitly and free their native (cgo) memory on
	// return. sitter.ParseCtx leaves the C parser and tree to be reclaimed by Go
	// finalizers, but the Go heap stays small while parsing, so GC — and thus those
	// finalizers — runs only every couple of minutes. Re-parse-heavy passes on large
	// repos then pile up thousands of tree-sitter trees off-heap (12+ GB RSS on
	// microsoft/TypeScript) even though the live Go heap is well under 1 GB.
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.grammar)
	tree, err := parser.ParseCtx(ctx, nil, parseSrc)
	if tree != nil {
		defer tree.Close()
	}
	var root *sitter.Node
	if tree != nil {
		root = tree.RootNode()
	}
	if err != nil || root == nil || root.IsNull() {
		detail := "tree-sitter parse failed"
		code := "E_PARSE_ERROR"
		if err != nil {
			detail = err.Error()
			if ctx.Err() != nil {
				code = "E_PARSE_TIMEOUT"
				detail = fmt.Sprintf("tree-sitter parse exceeded %s", treeSitterParseTimeout)
			}
		}
		return nil, spec.language, ParseStatus{ParseError: true, Code: code, Detail: detail}
	}
	if spec.language == "YAML" {
		status := ParseStatus{}
		if root.HasError() {
			status = ParseStatus{ParseError: true, Code: "E_PARSE_ERROR", Detail: parseErrorDetail(root, src)}
		}
		return yamlEntities(path, content), spec.language, status
	}

	var entities []Entity
	walkEntities(root, src, spec.language, "", &entities)
	if spec.language == "C++" {
		entities = appendMissingEntities(entities, cPlusPlusTypeAliasEntities(content)...)
	}
	if spec.language == "Kotlin" {
		entities = append(entities, kotlinPrimaryConstructorFieldEntities(content)...)
	}
	if spec.language == "JavaScript" || spec.language == "TypeScript" {
		entities = appendMissingEntities(entities, javascriptExportedVariableEntities(content)...)
		entities = appendMissingEntities(entities, javascriptAssignmentMethodEntities(content)...)
		entities = append(entities, graphqlResolverEntities(path, content)...)
	}
	if spec.language == "SQL" {
		// Run the regex fallback extractors on comment-stripped source so that
		// commented-out (or otherwise non-DDL) text is not picked up as a phantom
		// entity. The tree-sitter walk above already used masked source.
		regexSrc := []byte(stripSQLComments(string(src)))
		entities = append(entities, postgresFunctionEntities(regexSrc)...)
		entities = append(entities, postgresPolicyEntities(regexSrc)...)
	}
	if spec.language == "Lua" {
		// tree-sitter-lua can recover from large annotated files with incomplete
		// top-level function coverage. Supplement it with the canonical Lua
		// declaration forms so exported table functions stay discoverable.
		entities = appendMissingEntities(entities, luaFunctionEntities(content)...)
	}
	if spec.language == "Objective-C" {
		// Large Objective-C implementation files with blocks/macros can leave
		// recoverable method definitions out of the tree-sitter walk. Supplement
		// only brace-backed implementation methods; header prototypes end in ';'.
		entities = appendMissingEntities(entities, objectiveCMethodEntities(content)...)
	}
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].StartLine == entities[j].StartLine {
			return entities[i].Name < entities[j].Name
		}
		return entities[i].StartLine < entities[j].StartLine
	})
	status := ParseStatus{}
	if root.HasError() {
		status = ParseStatus{ParseError: true, Code: "E_PARSE_ERROR", Detail: parseErrorDetail(root, src)}
	}
	return entities, spec.language, status
}

func parseErrorDetail(root *sitter.Node, src []byte) string {
	details := collectParseErrorDetails(root, src, 5)
	if len(details) == 0 {
		return "tree-sitter syntax error nodes present"
	}
	return "tree-sitter syntax error nodes present: " + strings.Join(details, "; ")
}

func collectParseErrorDetails(root *sitter.Node, src []byte, limit int) []string {
	if root == nil || root.IsNull() || limit <= 0 {
		return nil
	}
	var details []string
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil || node.IsNull() || len(details) >= limit {
			return
		}
		if node.IsError() || node.IsMissing() {
			point := node.StartPoint()
			kind := "error"
			if node.IsMissing() {
				kind = "missing"
			}
			snippet := strings.TrimSpace(node.Content(src))
			if snippet == "" {
				snippet = strings.TrimSpace(sourceLineAt(src, int(point.Row)+1))
			}
			if len(snippet) > 80 {
				snippet = snippet[:80] + "..."
			}
			details = append(details, fmt.Sprintf("%s %s at line %d column %d near %q", kind, node.Type(), point.Row+1, point.Column+1, snippet))
		}
		for i := 0; i < int(node.ChildCount()) && len(details) < limit; i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	return details
}

func sourceLineAt(src []byte, line int) string {
	if line <= 0 {
		return ""
	}
	current := 1
	start := 0
	for i, b := range src {
		if current == line && b == '\n' {
			return string(src[start:i])
		}
		if b == '\n' {
			current++
			start = i + 1
		}
	}
	if current == line && start <= len(src) {
		return string(src[start:])
	}
	return ""
}

var (
	tsKeywordTypePropertyPattern  = regexp.MustCompile(`^(\s*)in(\??\s*:)`)
	tsTypeImportPattern           = regexp.MustCompile(`typeof\s+import\(([^)]*)\)`)
	tsStaticAccessorMethodPattern = regexp.MustCompile(`\bstatic\s+accessor(\s*\()`)
)

func maskTypeScriptUnsupportedSyntax(content string) string {
	masked := tsTypeImportPattern.ReplaceAllStringFunc(content, sameLengthIdentifierMask)
	lines := strings.SplitAfter(masked, "\n")
	maskingGenericCallSignature := false
	maskingGenericCallSignatureReturn := false
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		trimmed := strings.TrimSpace(text)
		if maskingGenericCallSignature {
			if trimmed == "" {
				maskingGenericCallSignature = false
				maskingGenericCallSignatureReturn = false
				continue
			}
			lines[i] = maskLineText(text) + newline
			if typeScriptGenericCallSignatureEnds(trimmed, maskingGenericCallSignatureReturn) {
				maskingGenericCallSignature = false
				maskingGenericCallSignatureReturn = false
			} else if typeScriptGenericCallSignatureReturnStarts(trimmed) {
				maskingGenericCallSignatureReturn = true
			}
			continue
		}
		text = maskTypeScriptKeywordTypeProperty(text)
		text = maskTypeScriptStaticAccessorMethod(text)
		trimmed = strings.TrimSpace(text)
		if typeScriptGenericCallSignatureStarts(trimmed) {
			lines[i] = maskLineText(text) + newline
			if typeScriptGenericCallSignatureReturnStarts(trimmed) {
				maskingGenericCallSignatureReturn = true
			}
			if !typeScriptGenericCallSignatureEnds(trimmed, maskingGenericCallSignatureReturn) {
				maskingGenericCallSignature = true
			}
			continue
		}
		lines[i] = text + newline
	}
	return strings.Join(lines, "")
}

func sameLengthIdentifierMask(value string) string {
	if len(value) <= 3 {
		return strings.Repeat("_", len(value))
	}
	return "any" + strings.Repeat(" ", len(value)-3)
}

func splitLineEnding(line string) (text, newline string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func maskTypeScriptKeywordTypeProperty(line string) string {
	return tsKeywordTypePropertyPattern.ReplaceAllString(line, "${1}ii${2}")
}

func maskTypeScriptStaticAccessorMethod(line string) string {
	return tsStaticAccessorMethodPattern.ReplaceAllString(line, "static accessoR${1}")
}

// rustItemWrapperMacroHint cheaply detects source that may contain a
// brace-delimited cfg_*! macro invocation so unaffected files skip the extra
// unwrap parse below.
var rustItemWrapperMacroHint = regexp.MustCompile(`\bcfg_[A-Za-z0-9_]*\s*!\s*\{`)

// rustItemWrapperMacroName gates which macros maskRustUnsupportedSyntax may
// unwrap: tokio's declarative config family (cfg_net!, cfg_io_util!, ...) plus
// cfg_if!, whose brace bodies wrap real items. Arbitrary macros (quote!,
// macro_rules!, matches!, vec!) must stay opaque token trees.
var rustItemWrapperMacroName = regexp.MustCompile(`^cfg_[A-Za-z0-9_]*$`)

// maskRustUnsupportedSyntax unwraps known item-wrapping macro invocations so
// the items they wrap parse as real items. tree-sitter-rust parses a macro
// invocation's body as an opaque token_tree, so `cfg_net! { pub struct
// TcpListener { ... } }` (pervasive in tokio) otherwise yields no entities at
// all. The macro name, `!`, and wrapping braces are blanked with same-length
// whitespace so the contents parse at identical byte offsets. Wrappers nest
// (`cfg_net! { cfg_io! { ... } }`), so unwrapping iterates to a fixed point.
func maskRustUnsupportedSyntax(content string) string {
	if !rustItemWrapperMacroHint.MatchString(content) {
		return content
	}
	src := []byte(content)
	const maxUnwrapDepth = 8
	for i := 0; i < maxUnwrapDepth; i++ {
		if !unwrapRustItemWrapperMacros(src) {
			break
		}
	}
	return string(src)
}

// unwrapRustItemWrapperMacros blanks one layer of item-position cfg_*! macro
// wrappers in src in place and reports whether anything changed.
func unwrapRustItemWrapperMacros(src []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), treeSitterParseTimeout)
	defer cancel()
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(rust.GetLanguage())
	tree, err := parser.ParseCtx(ctx, nil, src)
	if tree != nil {
		defer tree.Close()
	}
	if err != nil || tree == nil {
		return false
	}
	root := tree.RootNode()
	if root == nil || root.IsNull() {
		return false
	}
	changed := false
	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child == nil || child.IsNull() {
				continue
			}
			if child.Type() == "macro_invocation" && unwrapRustItemWrapperMacro(src, child, node) {
				changed = true
				continue
			}
			walk(child)
		}
	}
	walk(root)
	return changed
}

// unwrapRustItemWrapperMacro blanks a single cfg_*! wrapper when the
// invocation sits at item position (file, module, impl, or trait scope — not
// inside a function body) with a brace-delimited body. cfg_if!-style bodies
// (`if #[cfg(...)] { items } else { items }`) additionally get the if/else
// scaffolding blanked, keeping only the branch interiors.
func unwrapRustItemWrapperMacro(src []byte, node, parent *sitter.Node) bool {
	switch parent.Type() {
	case "source_file", "declaration_list":
	default:
		return false
	}
	name := node.ChildByFieldName("macro")
	if name == nil || name.IsNull() {
		return false
	}
	macroName := string(src[name.StartByte():name.EndByte()])
	if dot := strings.LastIndex(macroName, "::"); dot >= 0 {
		macroName = macroName[dot+2:]
	}
	if !rustItemWrapperMacroName.MatchString(macroName) {
		return false
	}
	var body *sitter.Node
	for i := int(node.NamedChildCount()) - 1; i >= 0; i-- {
		child := node.NamedChild(i)
		if child != nil && !child.IsNull() && child.Type() == "token_tree" {
			body = child
			break
		}
	}
	if body == nil || src[body.StartByte()] != '{' {
		return false
	}
	// Byte ranges inside the invocation whose contents survive the blanking.
	var keep [][2]int
	if rustTokenTreeStartsWithIf(src, body) {
		// cfg_if! style: keep only the brace-delimited branch bodies.
		for i := 0; i < int(body.NamedChildCount()); i++ {
			branch := body.NamedChild(i)
			if branch == nil || branch.IsNull() || branch.Type() != "token_tree" {
				continue
			}
			if src[branch.StartByte()] != '{' {
				continue
			}
			keep = append(keep, [2]int{int(branch.StartByte()) + 1, int(branch.EndByte()) - 1})
		}
	} else {
		keep = append(keep, [2]int{int(body.StartByte()) + 1, int(body.EndByte()) - 1})
	}
	pos := int(node.StartByte())
	for _, segment := range keep {
		maskBytesPreservingNewlines(src, pos, segment[0])
		pos = segment[1]
	}
	maskBytesPreservingNewlines(src, pos, int(node.EndByte()))
	return true
}

// rustTokenTreeStartsWithIf reports whether the first token inside the brace
// body is the `if` keyword, i.e. a cfg_if!-style conditional wrapper.
func rustTokenTreeStartsWithIf(src []byte, body *sitter.Node) bool {
	i := int(body.StartByte()) + 1
	end := int(body.EndByte()) - 1
	for i < end {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		}
		break
	}
	if i+2 > end || src[i] != 'i' || src[i+1] != 'f' {
		return false
	}
	if i+2 == end {
		return true
	}
	next := src[i+2]
	return !(next == '_' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9'))
}

var (
	javaModuleImportPattern      = regexp.MustCompile(`^(\s*import\s+)module\s+`)
	javaVarargsAnnotationPattern = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*\s*\.\.\.`)
)

func maskJavaUnsupportedSyntax(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		text = javaModuleImportPattern.ReplaceAllString(text, "${1}       ")
		text = javaVarargsAnnotationPattern.ReplaceAllStringFunc(text, func(match string) string {
			return strings.Repeat(" ", len(match)-3) + "..."
		})
		lines[i] = text + newline
	}
	return strings.Join(lines, "")
}

var (
	kotlinSuspendLambdaPattern        = regexp.MustCompile(`\bsuspend\s+\{`)
	kotlinMultiDollarString           = regexp.MustCompile(`\$+\s*"`)
	kotlinCallTypeArgumentsWithParen  = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)<[^<>\n(){}]+>\(`)
	kotlinCallTypeArgumentsWithLambda = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)<[^<>\n(){}]+>(\s*\{)`)
	kotlinEmptyArrayDefault           = regexp.MustCompile(`=\s*\[\]`)
	kotlinOverrideCallPattern         = regexp.MustCompile(`\boverride\(\)`)
	kotlinFunInterfacePattern         = regexp.MustCompile(`\bfun(\s+interface\s+[A-Za-z_])`)
)

func maskKotlinUnsupportedSyntax(path, content string) string {
	// tree-sitter-kotlin does not support `fun interface` (SAM interface)
	// declarations: the whole declaration becomes an ERROR node and the
	// interface symbol is lost (evidence: okhttp3.Interceptor was absent from
	// the square/okhttp snapshot). Blank the `fun` modifier — same length, so
	// node positions still line up with the original source — and the
	// declaration parses as a plain interface; signatures and refineKind read
	// the original text, which keeps the `fun` spelling visible there.
	content = kotlinFunInterfacePattern.ReplaceAllStringFunc(content, func(match string) string {
		return "   " + match[3:]
	})
	content = kotlinSuspendLambdaPattern.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat(" ", len(match)-1) + "{"
	})
	content = kotlinMultiDollarString.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat(" ", len(match)-1) + "\""
	})
	content = maskKotlinCallTypeArguments(content)
	content = kotlinEmptyArrayDefault.ReplaceAllStringFunc(content, func(match string) string {
		return sameLengthReplacement("= 0", len(match))
	})
	content = kotlinOverrideCallPattern.ReplaceAllString(content, "masked__()")
	content = maskKotlinUnsupportedLines(content)
	content = maskKotlinTrailingCommas(content)
	if strings.EqualFold(filepath.Ext(path), ".kts") {
		content = maskKotlinGradleOptionValueAssignments(content)
		content = maskKotlinGradleWhenGetOrElse(content)
		content = maskKotlinGradleNamedBlock(content, "allOpen", "run {}")
	}
	return content
}

func maskKotlinUnsupportedLines(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "findViewById(") && strings.Contains(trimmed, ").") && strings.Contains(trimmed, "="):
			lines[i] = paddedReplacement(leadingWhitespace(text), "findViewById(R.id.masked)", len(text)) + newline
		case strings.Contains(text, " withOptions {"):
			suffix := strings.Index(text, " withOptions {")
			replacement := strings.TrimRight(text[:suffix], " \t")
			lines[i] = paddedReplacement(leadingWhitespace(text), strings.TrimSpace(replacement), len(text)) + newline
			indent := leadingWhitespace(text)
			for j := i + 1; j < len(lines); j++ {
				lineText, lineNewline := splitLineEnding(lines[j])
				lines[j] = maskLineText(lineText) + lineNewline
				if leadingWhitespace(lineText) == indent && strings.TrimSpace(lineText) == "}" {
					i = j
					break
				}
			}
		}
	}
	return strings.Join(lines, "")
}

func maskKotlinCallTypeArguments(content string) string {
	content = kotlinCallTypeArgumentsWithParen.ReplaceAllStringFunc(content, func(match string) string {
		open := strings.Index(match, "<")
		if open < 0 {
			return match
		}
		replacement := match[:open] + "("
		return replacement + strings.Repeat(" ", len(match)-len(replacement))
	})
	return kotlinCallTypeArgumentsWithLambda.ReplaceAllStringFunc(content, func(match string) string {
		parts := kotlinCallTypeArgumentsWithLambda.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		replacement := parts[1] + parts[2]
		return replacement + strings.Repeat(" ", len(match)-len(replacement))
	})
}

func maskKotlinTrailingCommas(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if !strings.HasSuffix(trimmed, ",") {
			continue
		}
		next := kotlinNextNonEmptyTrimmedLine(lines, i+1)
		if !strings.HasPrefix(next, ")") && !strings.HasPrefix(next, "]") {
			continue
		}
		comma := strings.LastIndex(text, ",")
		if comma >= 0 {
			text = text[:comma] + " " + text[comma+1:]
			lines[i] = text + newline
		}
	}
	return strings.Join(lines, "")
}

func kotlinNextNonEmptyTrimmedLine(lines []string, start int) string {
	for i := start; i < len(lines); i++ {
		text, _ := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maskYAMLUnsupportedSyntax(content string) string {
	// Antora playbooks commonly use @PLACEHOLDER@ values before templating.
	// Bare YAML scalars cannot start with "@", but replacing it in parse-only
	// input preserves line and column positions while leaving entity extraction
	// on the original source.
	content = strings.Map(func(r rune) rune {
		if r == '@' {
			return 'x'
		}
		return r
	}, content)
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		lines[i] = maskYAMLQuotedMappingKey(text) + newline
	}
	return strings.Join(lines, "")
}

func maskYAMLQuotedMappingKey(line string) string {
	colon := yamlKeyColonIndex(line)
	if colon < 0 {
		return line
	}
	prefix := line[:colon]
	trimmed := strings.TrimSpace(prefix)
	if strings.HasPrefix(trimmed, "- ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
	}
	if len(trimmed) < 2 {
		return line
	}
	quote := trimmed[0]
	if (quote != '\'' && quote != '"') || trimmed[len(trimmed)-1] != quote {
		return line
	}
	start := strings.IndexByte(line[:colon], quote)
	end := strings.LastIndexByte(line[:colon], quote)
	if start < 0 || end <= start {
		return line
	}
	return line[:start] + string(quote) + "key" + string(quote) + line[end+1:]
}

func maskCSharpUnsupportedSyntax(content string) string {
	// Neutralize byte-order marks with a byte-length-preserving replacement.
	// The UTF-8 BOM is 3 bytes; replacing it with a single space would shrink
	// the masked source by 2 bytes and drift every downstream symbol offset by
	// -2 relative to the unmasked content the names are sliced from (corrupting
	// every name in a BOM-prefixed file, e.g. "SqlMapper" -> "s SqlMapp").
	content = strings.ReplaceAll(content, "\ufeff", "   ")
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		trimmed := strings.TrimPrefix(strings.TrimSpace(text), "\ufeff")
		switch {
		case strings.HasPrefix(trimmed, "#"):
			lines[i] = maskLineText(text) + newline
		default:
			// The vendored C# 13 grammar parses collection expressions,
			// primary constructors, and params collections natively. Two
			// constructs still degrade to ERROR nodes: prefix dereference
			// of a pointer cast (`*(T*)&x`, unsafe hot paths) and
			// dictionary index initializers with non-literal keys
			// (`{ [key] = value }`).
			text = maskPointerDerefCasts(text)
			text = maskBareAsyncArguments(text)
			lines[i] = replacePatternSameLength(text, cSharpDictionaryIndexInitializerPattern, "{}") + newline
		}
	}
	return strings.Join(lines, "")
}

// maskPointerDerefCasts rewrites `*(T*)expr` into ` (T )expr` (same byte
// length) because tree-sitter-c-sharp cannot disambiguate a prefix pointer
// dereference applied directly to a pointer-cast expression.
func maskPointerDerefCasts(text string) string {
	return cSharpPointerDerefCastPattern.ReplaceAllStringFunc(text, func(m string) string {
		return strings.ReplaceAll(m, "*", " ")
	})
}

var ocamlValSignaturePattern = regexp.MustCompile(`^(\s*)val\s+(\([^)\n]*\)|[A-Za-z_][\w']*)\b`)
var ocamlSigOpenWord = regexp.MustCompile(`\b(?:sig|object)\b`)
var ocamlSigCloseWord = regexp.MustCompile(`\bend\b`)

// maskOCamlInterfaceSyntax rewrites OCaml .mli interface signatures into forms
// the implementation grammar accepts. The package ships only the .ml grammar,
// so top-level `val NAME : <type>` signatures (which have no implementation)
// raise parse errors. Rewrite each to `let NAME = ()` (preserving NAME for
// symbol extraction) and blank the type, including `:`/`->`/`|` continuation
// lines. Only top-level vals are rewritten: inside `sig`/`object ... end`
// blocks `let` is invalid, so those are left untouched (not made worse).
// ocamlValStartKeywords begin a new top-level signature item, so a line
// starting with one ends the preceding val's type continuation.
var ocamlValStartKeywords = map[string]struct{}{
	"val": {}, "type": {}, "module": {}, "include": {}, "exception": {},
	"external": {}, "open": {}, "let": {}, "class": {}, "end": {}, "and": {},
	"sig": {}, "object": {}, "method": {}, "inherit": {}, "constraint": {},
}

// ocamlContinuesValSignature reports whether a line continues the multi-line
// type of the preceding `val` (i.e. it is non-blank, not a comment, and does
// not begin a new top-level signature item).
// maskBareAsyncArguments rewrites a bare `async` identifier in argument
// position (`F(request, async, this)`) into `true ` (same byte length).
// tree-sitter-c-sharp reads argument-position `async` as the start of an
// async lambda and the resulting ERROR can swallow the enclosing class
// declaration (dotnet/runtime HttpConnectionPool.cs lost its class symbol
// and every member's qualification). Lambda forms (`async x => ...`,
// `async () => ...`) never match: the token must be followed directly by
// `,` or `)`. Applied twice because adjacent matches share delimiters.
func maskBareAsyncArguments(text string) string {
	for i := 0; i < 2; i++ {
		text = cSharpBareAsyncArgumentPattern.ReplaceAllString(text, "${1}true $2")
	}
	return text
}

func ocamlContinuesValSignature(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "(*") {
		return false
	}
	word := t
	if idx := strings.IndexFunc(t, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ':' || r == '(' || r == '='
	}); idx >= 0 {
		word = t[:idx]
	}
	_, isKeyword := ocamlValStartKeywords[word]
	return !isKeyword
}

func maskOCamlInterfaceSyntax(content string) string {
	lines := strings.SplitAfter(content, "\n")
	depth := 0
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		if depth == 0 {
			if m := ocamlValSignaturePattern.FindStringSubmatch(text); m != nil {
				lines[i] = paddedReplacement(m[1], "let "+m[2]+" = ()", len(text)) + newline
				// The type signature may wrap over several lines whose
				// continuations start with anything (`(`, identifiers, `'a`,
				// `->`). Mask until a blank line or a new top-level construct.
				for i+1 < len(lines) {
					nextText, nextNewline := splitLineEnding(lines[i+1])
					if !ocamlContinuesValSignature(nextText) {
						break
					}
					lines[i+1] = maskLineText(nextText) + nextNewline
					i++
				}
				continue
			}
		}
		trimmed := strings.TrimSpace(text)
		depth += len(ocamlSigOpenWord.FindAllString(trimmed, -1)) - len(ocamlSigCloseWord.FindAllString(trimmed, -1))
		if depth < 0 {
			depth = 0
		}
	}
	return strings.Join(lines, "")
}

// dartClassModifierPattern matches a Dart 3 class modifier immediately ahead
// of the `class` keyword (`final class`, `sealed class`, `base mixin class`,
// `abstract interface class`, ...). The vendored tree-sitter-dart grammar
// predates class modifiers, so an unmasked `final class ByteStream extends
// StreamView<List<int>>` parses as an ERROR node and the class symbol is lost
// (its factory constructor gets recovered as a bare function instead).
var dartClassModifierPattern = regexp.MustCompile(`\b(final|base|interface|sealed|mixin)(\s+)(class\b)`)

// maskDartUnsupportedSyntax blanks Dart 3 class modifiers the grammar cannot
// parse, preserving byte length so node offsets keep pointing into the
// original source. `abstract` is left alone (the grammar knows `abstract
// class`); stacked modifiers (`base mixin class`) resolve over the fixpoint
// iterations.
func maskDartUnsupportedSyntax(content string) string {
	for {
		masked := dartClassModifierPattern.ReplaceAllStringFunc(content, func(match string) string {
			m := dartClassModifierPattern.FindStringSubmatch(match)
			return strings.Repeat(" ", len(m[1])) + m[2] + m[3]
		})
		if masked == content {
			return masked
		}
		content = masked
	}
}

func maskSwiftUnsupportedSyntax(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		text, newline := splitLineEnding(line)
		if end, ok := maskSwiftComputedStringPropertyBlock(lines, i, text, newline); ok {
			i = end
			continue
		}
		text = maskSwiftPropertyWrapperPrefix(text)
		text = maskSwiftEmptyAttributeCalls(text)
		text = replacePatternSameLength(text, swiftTypedThrowsPattern, "throws")
		if replacement, ok := maskSwiftAsyncForLine(text); ok {
			text = replacement
		}
		if replacement, ok := maskSwiftOptionalBindingShorthand(text); ok {
			text = replacement
		}
		if strings.Contains(text, `"""`) {
			text, i = maskSwiftMultilineStringLiteral(lines, i, text, newline)
			if i < len(lines) {
				continue
			}
			break
		}
		if swiftLeadingOperatorContinuationLinePattern.MatchString(text) {
			if strings.Contains(text, "{") {
				balance := strings.Count(text, "(") - strings.Count(text, ")")
				balance += strings.Count(text, "{") - strings.Count(text, "}")
				lines[i] = maskLineText(text) + newline
				for balance > 0 && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					balance += strings.Count(text, "{") - strings.Count(text, "}")
					lines[i] = maskLineText(text) + newline
				}
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(text), ")") && !strings.Contains(text, "(") {
				text = paddedReplacement(leadingWhitespace(text), ")", len(text))
			} else {
				text = maskLineText(text)
			}
		}
		lines[i] = text + newline
	}
	return strings.Join(lines, "")
}

func maskSwiftComputedStringPropertyBlock(lines []string, i int, text, newline string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	matches := swiftComputedStringPropertyStartPattern.FindStringSubmatch(trimmed)
	if len(matches) != 4 {
		return i, false
	}
	indent := leadingWhitespace(text)
	end := -1
	hasMultilineString := strings.Contains(text, `"""`)
	for j := i + 1; j < len(lines); j++ {
		lineText, _ := splitLineEnding(lines[j])
		if strings.Contains(lineText, `"""`) {
			hasMultilineString = true
		}
		if leadingWhitespace(lineText) == indent && strings.TrimSpace(lineText) == "}" {
			end = j
			break
		}
	}
	if end < 0 || !hasMultilineString || i+1 >= end {
		if end < 0 || !swiftComputedStringPropertyHasRecoverableBody(lines[i+1:end]) {
			return i, false
		}
	}
	value := `"s"`
	if matches[3] == "[String]" {
		value = `[]`
	}
	replacement := strings.TrimSpace(matches[1] + "var " + matches[2] + ": " + matches[3] + " = " + value)
	if len(replacement) > len(trimmed) && matches[1] != "" {
		replacement = "var " + matches[2] + ": " + matches[3] + " = " + value
	}
	if len(replacement) > len(trimmed) {
		return i, false
	}
	lines[i] = paddedReplacement(leadingWhitespace(text), replacement, len(text)) + newline
	for j := i + 1; j <= end; j++ {
		lineText, lineNewline := splitLineEnding(lines[j])
		lines[j] = maskLineText(lineText) + lineNewline
	}
	return end, true
}

func maskSwiftMultilineStringLiteral(lines []string, i int, text, newline string) (string, int) {
	start := strings.Index(text, `"""`)
	if start < 0 {
		return text, i
	}
	replacement := text[:start] + sameLengthReplacement(`"s"`, len(text)-start)
	lines[i] = replacement + newline
	for j := i + 1; j < len(lines); j++ {
		lineText, lineNewline := splitLineEnding(lines[j])
		if strings.Contains(lineText, `"""`) {
			closeIndex := strings.Index(lineText, `"""`)
			suffix := strings.TrimSpace(lineText[closeIndex+len(`"""`):])
			if suffix != "" {
				lines[j] = paddedReplacement(leadingWhitespace(lineText), suffix, len(lineText)) + lineNewline
				return replacement, j
			}
			next := swiftNextNonEmptyTrimmedLine(lines, j+1)
			lines[j] = maskLineText(lineText) + lineNewline
			if strings.HasPrefix(next, ".") {
				return replacement, maskSwiftChainedDotLines(lines, j+1)
			}
			return replacement, j
		}
		lines[j] = maskLineText(lineText) + lineNewline
	}
	return replacement, len(lines)
}

func maskSwiftChainedDotLines(lines []string, start int) int {
	last := start - 1
	for i := start; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		if !strings.HasPrefix(strings.TrimSpace(text), ".") {
			return last
		}
		lines[i] = maskLineText(text) + newline
		last = i
	}
	return last
}

func swiftNextNonEmptyTrimmedLine(lines []string, start int) string {
	for i := start; i < len(lines); i++ {
		text, _ := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maskSwiftEmptyAttributeCalls(text string) string {
	return swiftEmptyAttributeCallPattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.TrimSuffix(match, "()") + strings.Repeat(" ", len("()"))
	})
}

func swiftComputedStringPropertyHasRecoverableBody(lines []string) bool {
	hasExpression := false
	for _, line := range lines {
		text, _ := splitLineEnding(line)
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, `"`) || strings.HasPrefix(trimmed, "[") {
			hasExpression = true
			continue
		}
		return false
	}
	return hasExpression
}

func maskSwiftPropertyWrapperPrefix(text string) string {
	matches := swiftPropertyWrapperPrefixPattern.FindStringIndex(text)
	if len(matches) != 2 {
		return text
	}
	wrapperEnd := strings.LastIndex(text[:matches[1]], " var ")
	if wrapperEnd < 0 {
		wrapperEnd = strings.LastIndex(text[:matches[1]], " let ")
	}
	if wrapperEnd < 0 {
		return text
	}
	declStart := wrapperEnd + 1
	return text[:matches[0]] + strings.Repeat(" ", declStart-matches[0]) + text[declStart:]
}

func maskSwiftAsyncForLine(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "for try await ") || !strings.Contains(trimmed, " in try ") {
		return text, false
	}
	afterAwait := strings.TrimPrefix(trimmed, "for try await ")
	parts := strings.SplitN(afterAwait, " in try ", 2)
	if len(parts) != 2 {
		return text, false
	}
	variable := strings.Fields(parts[0])
	if len(variable) == 0 {
		return text, false
	}
	collection := strings.TrimSpace(strings.TrimSuffix(parts[1], "{"))
	replacement := "for " + variable[0] + " in " + collection + " {"
	return paddedReplacement(leadingWhitespace(text), replacement, len(text)), true
}

func maskSwiftOptionalBindingShorthand(text string) (string, bool) {
	matches := swiftOptionalBindingShorthandPattern.FindStringSubmatchIndex(text)
	if len(matches) != 4 {
		return text, false
	}
	replacement := "if true {"
	return text[:matches[0]] + sameLengthReplacement(replacement, matches[1]-matches[0]) + text[matches[1]:], true
}

var (
	cSharpPointerDerefCastPattern               = regexp.MustCompile(`\*+\(\s*[A-Za-z_@][\w<>,.\s]*?\*+\s*\)`)
	cSharpBareAsyncArgumentPattern              = regexp.MustCompile(`([(,]\s*)async(\s*[,)])`)
	cSharpDictionaryIndexInitializerPattern     = regexp.MustCompile(`\{\s*\[[^\]\n]+\]\s*=\s*[^{}\n]+\}`)
	swiftTypedThrowsPattern                     = regexp.MustCompile(`throws\([A-Za-z_][A-Za-z0-9_.<>]*\)`)
	swiftOptionalBindingShorthandPattern        = regexp.MustCompile(`\bif\s+let\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	swiftEmptyAttributeCallPattern              = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_]*\(\)`)
	swiftPropertyWrapperPrefixPattern           = regexp.MustCompile(`^(\s*)@[A-Za-z_][A-Za-z0-9_]*(?:\([^)]*\))?\s+(?:var|let)\s+`)
	swiftLeadingOperatorContinuationLinePattern = regexp.MustCompile(`^\s*(?:<|>|<=|>=|==|!=|&&|\|\|)\s+`)
	swiftComputedStringPropertyStartPattern     = regexp.MustCompile(`^((?:(?:private|fileprivate|internal|public)\s+)?)var\s+([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(String|\[String\])\s*\{$`)
	cControlIteratorMacroPattern                = regexp.MustCompile(`^(?:(?:TAILQ|STAILQ|LIST|SLIST|RB|SPLAY)_(?:FOREACH|FOREACH_SAFE|FOREACH_REVERSE|FOREACH_REVERSE_SAFE)|(?:foreach|foreach_ptr|foreach_node|foreach_oid|foreach_int|foreach_xid|foreach_delete_current|forboth|for_both_cell|forthree|for_fourth_cell|for_each_from|dlist_foreach(?:_modify)?|dclist_foreach(?:_modify)?|slist_foreach(?:_modify)?|hash_seq_search|SGITITERATE))\s*\(`)
	cGenerateMacroPattern                       = regexp.MustCompile(`^(?:TAILQ|STAILQ|LIST|SLIST|RB|SPLAY)_(?:HEAD|ENTRY|PROTOTYPE|PROTOTYPE_STATIC|GENERATE|GENERATE_STATIC)\s*\(`)
	cEnumMacroPattern                           = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_KEYS\s*\(`)
	cFileScopeStatementMacroPattern             = regexp.MustCompile(`^(?:PG_MODULE_MAGIC(?:_EXT)?|PG_FUNCTION_INFO_V1|PGDLLEXPORT|PG_KEYWORD|PG_FUNCTION_ARGS|PG_USED_FOR_ASSERTS_ONLY|DECLARE_[A-Z][A-Z0-9_]*|MAKE_SYSCACHE)\s*\(`)
	// PostgreSQL system-catalog header macros: the `CATALOG(name,oid,...) BKI_...`
	// struct opener (the `{` follows on the next line) and BKI_* field/struct
	// annotations. BEGIN/END_CATALOG_STRUCT markers are handled as bare lines.
	cCatalogStructPattern   = regexp.MustCompile(`^CATALOG\s*\(`)
	cBKIMacroPattern        = regexp.MustCompile(`\bBKI_[A-Z0-9_]*(?:\s*\([^)\n]*\))?`)
	cStringMacroPattern     = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_FEATURE\s*\([^)\n]*\)`)
	cAnnotationMacroPattern = regexp.MustCompile(`\b(?:printflike|__dead|__packed|__unused|__maybe_unused|__attribute__|pg_attribute_\w+)\s*\([^)\n]*(?:\)[^)\n]*)?\)`)
	// Bare (parenless) C qualifier/attribute macros that prefix or annotate
	// declarations and break the C grammar. `\w*DLL(IMPORT|EXPORT)` generalizes
	// across C runtimes (PostgreSQL PGDLLIMPORT, Julia JL_DLLEXPORT, ...); the
	// remaining names are the high-frequency PostgreSQL declaration qualifiers
	// surfaced by the postgres/postgres failure clustering.
	// `\w+_EXTERN` generalizes export-annotation macros (curl CURL_EXTERN,
	// GLib GLIB_EXTERN, ...); `\w+_NORETURN`/`\w+_STDCALL` follow the same
	// attribute/calling-convention shape. UNITTEST (curl, empty-or-static),
	// WARN_UNUSED_RESULT, APIENTRY/WINBASEAPI/CALLBACK (Windows headers) and
	// z_const (zlib) are cross-project annotation names that never appear in
	// expression position.
	cBareAnnotationPattern = regexp.MustCompile(`\b(?:__dead|__packed|__unused|__maybe_unused|\w*DLL(?:IMPORT|EXPORT)|\w+_EXTERN|\w+_NORETURN|\w+_STDCALL|\w+_INLINE|PG_USED_FOR_ASSERTS_ONLY|NON_EXEC_STATIC|pg_attribute_\w+|WINAPI|APIENTRY|WINBASEAPI|CALLBACK|_CRTIMP|UNITTEST|WARN_UNUSED_RESULT|z_const)\b`)
	// Printf-format attribute macros after a declarator: `... , ...) CURL_PRINTF(2, 3);`
	// (expands to __attribute__((format(printf, ...)))). The digit-only argument
	// list keeps the shape distinct from real function calls.
	cPrintfAttributeMacroPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_PRINTF\s*\(\s*\d+\s*,\s*\d+\s*\)`)
	// Bare block begin/end statement macros (curl UNITTEST_BEGIN_SIMPLE /
	// UNITTEST_END(stop()) pairs, BEGIN_C_DECLS/END_C_DECLS, ...). Each BEGIN
	// expands to an opener whose `}` lives in the matching END macro, so
	// blanking both keeps braces balanced.
	cBlockBeginEndMacroPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(?:BEGIN|END)(?:_[A-Z0-9_]+)?\s*(?:\(.*\))?$`)
	// Statement macros wrapping a declaration (curl `VERBOSE(const char *p);`,
	// `VERBOSE(size_t calls = 0);`): an all-caps macro call whose first tokens
	// look like a declaration (two identifiers, optionally pointer/const/struct
	// qualified) cannot be parsed as a call argument.
	cDeclarationStatementMacroPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*\(\s*(?:const\s+|struct\s+|unsigned\s+|signed\s+)*[A-Za-z_]\w*\s+\**\s*[A-Za-z_]\w*\s*[=;,\[\)]`)
	// Macro invocations with empty arguments (`CS_ENTRY(0x1301, TLS,AES,128,GCM,SHA256,,,),`
	// or a trailing `...,SHA256,),`) are unparseable as calls; such lines are
	// pure macro data, so blank them.
	cEmptyArgMacroLinePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*\([^()\n]*,\s*[,)][^()\n]*\)?\s*[,;]?$`)
	// Clang availability builtin takes version specs, not expressions:
	// `if(__builtin_available(macOS 10.9, iOS 7, *))`.
	cBuiltinAvailablePattern = regexp.MustCompile(`\b__builtin_available\s*\([^()\n]*\)`)
	// `va_arg(ap, TYPE)` calls: the second argument is a type name, which the C
	// grammar cannot parse as a call argument (va_arg is compiler magic).
	cVaArgCallPattern = regexp.MustCompile(`\bva_arg\s*\(`)
	// A line of only all-caps macro words (optionally with argument lists)
	// annotating the following declaration (curl `ALLOC_FUNC` above
	// `void *curl_dbg_malloc(...)`, `CURL_EXTERN ALLOC_FUNC ALLOC_SIZE(1)`);
	// only blanked when the next code line looks like a declaration so bare
	// enumerators and expression continuations are untouched.
	cLoneAnnotationMacroLinePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}(?:\s*\([^()\n]*\))?(?:\s+[A-Z][A-Z0-9_]{2,}(?:\s*\([^()\n]*\))?)*$`)
	cDeclarationStartLinePattern    = regexp.MustCompile(`^[A-Za-z_][^={}]*\(`)
	// A bare `MACRO(` line opening a statement-wrapping macro
	// (curl `CURL_IGNORE_DEPRECATION(` ... `)`); handled in the line loop by
	// blanking only the opener and its bare `)`/`);` closer line, keeping the
	// wrapped statements. Guarded on the interior containing a `;`, which call
	// arguments cannot.
	cBareMacroOpenLinePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*\($`)
	// va_arg-wrapper macro calls whose (last) argument is a type name
	// (`avalue = form_ptr_arg(char *);`, `APR_ARRAY_IDX(args, i, char *)`).
	// Anchored on a preceding `=`/`(`/`,` so real prototypes with unnamed
	// parameters (`void foo(char *);`) are never rewritten; a genuine call can
	// never take a type argument.
	cTypeArgMacroCallPattern = regexp.MustCompile(`[=(,]\s*[A-Za-z_]\w*\s*\((?:[^()\n;]*,)?(\s*(?:const\s+)?(?:unsigned\s+|signed\s+)?(?:void|char|short|int|long|float|double|struct\s+\w+|\w+_t)\s*\*+\s*)\)`)
	// Parameter-list prefix macros in the libev style: `(EV_P_ struct ev_timer *w`
	// — a trailing-underscore all-caps macro directly before a type keyword can
	// only be a macro (two type tokens cannot open a parameter otherwise).
	cParamPrefixMacroPattern = regexp.MustCompile(`\(\s*([A-Z][A-Z0-9_]*_)\s+(?:struct|const|unsigned|signed|void|char|short|int|long|float|double)\b`)
	// All-caps annotation macro before the return type of a declarator
	// (`ALLOC_FUNC FILE *curl_dbg_fopen(...)`): three word tokens ahead of a
	// parameter list are one too many for a declaration, so the leading
	// all-caps word is blanked. Even when the first token is the real return
	// type and the middle a calling-convention macro (`HRESULT STDMETHODCALLTYPE
	// f(...)`), blanking the first still leaves a parseable `type name(`.
	cAnnotationBeforeTypePattern = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{2,})[ \t]+[A-Za-z_]\w*[ \t]+\**[A-Za-z_]\w*\s*\(`)
	// Attribute macro between a type and its declarator
	// (`static const char CURL_USED min_stack[] = "..."`): TYPE MACRO name.
	// Guarded in maskCInterDeclarationAnnotations so struct/union/enum tags
	// (`struct FOO bar;`) are never blanked.
	cTypeAnnotationNamePattern = regexp.MustCompile(`\b([A-Za-z_]\w*)[ \t]+([A-Z][A-Z0-9_]{2,})[ \t]+([A-Za-z_]*[a-z]\w*)\s*(\[[^\]\n]*\])?\s*[=;,]`)
	// Attribute macro between the declarator and its initializer
	// (`gss_OID_desc Curl_spnego_mech_oid CURL_ALIGN8 = {`): TYPE name MACRO =.
	// The declarator must contain a lowercase letter so an all-caps declarator
	// (`MyType FLAGS = {0}`) is never mistaken for the macro.
	cNameAnnotationInitPattern = regexp.MustCompile(`\b([A-Za-z_]\w*)[ \t]+([A-Za-z_]*[a-z]\w*)[ \t]+([A-Z][A-Z0-9_]{2,})\s*=`)
	// Qualifier macro between a pointer star and the declarator
	// (`struct Curl_addrinfo *vqualifier canext;`, a volatile-style qualifier):
	// `TYPE *word name;` is only valid C when `word` is a qualifier, so any
	// non-keyword word in that slot is a macro to blank.
	cPointerQualifierMacroPattern = regexp.MustCompile(`\*[ \t]*([a-z_]\w*)[ \t]+([A-Za-z_]\w*)\s*[;=,\[)]`)
	// The vendored tree-sitter-c grammar only accepts `\x` escapes with two or
	// more hex digits; blank the backslash of a single-digit `"\xb"` escape so
	// the (still valid) string literal parses.
	cShortHexEscapePattern = regexp.MustCompile(`\\x[0-9a-fA-F][^0-9a-fA-F]`)
	// Pointer-slot words that are real C qualifiers, not macros.
	cPointerQualifierKeywords = map[string]bool{
		"const": true, "volatile": true, "restrict": true,
		"__restrict": true, "__restrict__": true, "register": true,
	}
	// C++ library namespace-opening/closing macros (asmjit ASMJIT_BEGIN_NAMESPACE
	// / ASMJIT_BEGIN_SUB_NAMESPACE(x), and the *_NAMESPACE_BEGIN order) expand to
	// `namespace x {` / `}`. The fmt/nlohmann variants are handled by exact cases
	// above; this generalizes to other libraries. Each begin/end pair maps to one
	// brace, which keeps braces balanced regardless of real nesting depth.
	cxxBeginNamespaceMacroPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(?:BEGIN(?:_SUB)?_NAMESPACE|NAMESPACE_BEGIN)\b`)
	cxxEndNamespaceMacroPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(?:END(?:_SUB)?_NAMESPACE|NAMESPACE_END)\b`)
	// Julia annotation macros (JL_NOTSAFEPOINT, JL_GLOBALLY_ROOTED, ...) annotate
	// declarations and break the C/C++ grammar; mask them (with any args).
	jlAnnotationMacroPattern = regexp.MustCompile(`\bJL_[A-Z][A-Z0-9_]*\b(?:\s*\([^)\n]*\))?`)
	cTypeMacroPattern        = regexp.MustCompile(`\b(?:TAILQ|STAILQ|LIST|SLIST|RB|SPLAY)_(?:HEAD|ENTRY)\s*\([^)\n]*\)`)
	cHeadInitializerPattern  = regexp.MustCompile(`\b(?:(?:TAILQ|STAILQ|LIST|SLIST)_(?:HEAD_)?INITIALIZER|RB_INITIALIZER|SPLAY_INITIALIZER)\s*\([^)\n]*\)`)
)

func maskCUnsupportedSyntax(content string) string {
	lines := strings.SplitAfter(content, "\n")
	var preprocessorSkipStack []bool
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "#") {
			preprocessorSkipStack = updateCPreprocessorSkipStack(preprocessorSkipStack, trimmed)
			for {
				lines[i] = maskLineText(text) + newline
				if !strings.HasSuffix(strings.TrimRight(text, " \t"), "\\") || i+1 >= len(lines) {
					break
				}
				i++
				text, newline = splitLineEnding(lines[i])
			}
			// A block comment opened on the (now blanked) directive line may
			// span lines (`#endif /* FOO ||\n  BAR */`); blank through its
			// closing `*/` so the tail tokens do not leak into the parse.
			if cLineOpensBlockComment(text) {
				for i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					lines[i] = maskLineText(text) + newline
					if strings.Contains(text, "*/") {
						break
					}
				}
			}
			continue
		}
		if cPreprocessorSkipping(preprocessorSkipStack) {
			lines[i] = maskLineText(text) + newline
			continue
		}
		if trimmed == "BEGIN_CATALOG_STRUCT" || trimmed == "END_CATALOG_STRUCT" {
			lines[i] = maskLineText(text) + newline
			continue
		}
		if cCatalogStructPattern.MatchString(trimmed) {
			// `CATALOG(name,oid,...) BKI_... ` opens a system-catalog struct whose
			// `{` is on the next line; replace the whole macro line with a plain
			// struct opener so the (valid C) field body parses.
			lines[i] = paddedReplacement(leadingWhitespace(text), "struct c_catalog", len(text)) + newline
			continue
		}
		if cFileScopeStatementMacroPattern.MatchString(trimmed) {
			balance := strings.Count(text, "(") - strings.Count(text, ")")
			lines[i] = maskLineText(text) + newline
			for balance > 0 && i+1 < len(lines) {
				i++
				nextText, nextNewline := splitLineEnding(lines[i])
				balance += strings.Count(nextText, "(") - strings.Count(nextText, ")")
				lines[i] = maskLineText(nextText) + nextNewline
			}
			continue
		}
		if cControlIteratorMacroPattern.MatchString(trimmed) {
			combined := trimmed
			end := i
			for !strings.Contains(combined, ")") && end+1 < len(lines) {
				end++
				nextText, _ := splitLineEnding(lines[end])
				combined += " " + strings.TrimSpace(nextText)
			}
			replacement := "for (;;)"
			if strings.Contains(combined, "{") {
				replacement = "for (;;) {"
			}
			lines[i] = paddedReplacement(leadingWhitespace(text), replacement, len(text)) + newline
			for j := i + 1; j <= end; j++ {
				nextText, nextNewline := splitLineEnding(lines[j])
				lines[j] = maskLineText(nextText) + nextNewline
			}
			i = end
			continue
		}
		if strings.HasPrefix(trimmed, "__attribute__") {
			if strings.Contains(trimmed, " extern ") {
				lines[i] = maskCAnnotationMacros(text) + newline
			} else {
				replacement := ""
				if strings.HasSuffix(trimmed, ";") {
					replacement = ";"
				}
				lines[i] = paddedReplacement(leadingWhitespace(text), replacement, len(text)) + newline
			}
			continue
		}
		if cEnumMacroPattern.MatchString(trimmed) {
			lines[i] = maskLineText(text) + newline
			continue
		}
		if cBlockBeginEndMacroPattern.MatchString(trimmed) || cEmptyArgMacroLinePattern.MatchString(trimmed) {
			lines[i] = maskLineText(text) + newline
			continue
		}
		if cLoneAnnotationMacroLinePattern.MatchString(trimmed) && cNextCodeLineStartsDeclaration(lines, i+1) {
			lines[i] = maskLineText(text) + newline
			continue
		}
		if cBareMacroOpenLinePattern.MatchString(trimmed) {
			// Statement-wrapping macro (`CURL_IGNORE_DEPRECATION(` ... `)`).
			// Blank only the opener and the bare closer line, keeping the
			// wrapped statements. The interior must contain a `;` — real call
			// arguments cannot — and the closer must sit alone on its line.
			balance := 1
			end := i
			sawStatement := false
			for balance > 0 && end+1 < len(lines) {
				end++
				nextText, _ := splitLineEnding(lines[end])
				balance += strings.Count(nextText, "(") - strings.Count(nextText, ")")
				if balance > 0 && strings.Contains(nextText, ";") {
					sawStatement = true
				}
			}
			lastText, lastNewline := splitLineEnding(lines[end])
			lastTrimmed := strings.TrimSpace(lastText)
			if balance == 0 && sawStatement && (lastTrimmed == ")" || lastTrimmed == ");") {
				lines[i] = maskLineText(text) + newline
				lines[end] = maskLineText(lastText) + lastNewline
				continue
			}
		}
		if cDeclarationStatementMacroPattern.MatchString(trimmed) {
			// Statement macro wrapping a declaration; blank the whole (possibly
			// multi-line) invocation, tracking paren balance like the file-scope
			// statement macros above. Look ahead first: the construct must close
			// with `;` to be a statement — a macro-typed parameter inside a real
			// signature (`CURL_THREAD_RETURN_T(X *func)(void *), void *arg)`)
			// must not be blanked.
			balance := strings.Count(text, "(") - strings.Count(text, ")")
			end := i
			for balance > 0 && end+1 < len(lines) {
				end++
				nextText, _ := splitLineEnding(lines[end])
				balance += strings.Count(nextText, "(") - strings.Count(nextText, ")")
			}
			lastText, _ := splitLineEnding(lines[end])
			if balance == 0 && strings.HasSuffix(strings.TrimSpace(lastText), ";") {
				for j := i; j <= end; j++ {
					jText, jNewline := splitLineEnding(lines[j])
					lines[j] = maskLineText(jText) + jNewline
				}
				i = end
				continue
			}
		}
		if cGenerateMacroPattern.MatchString(trimmed) {
			for {
				lines[i] = maskLineText(text) + newline
				if strings.Contains(text, ";") || i+1 >= len(lines) {
					break
				}
				i++
				text, newline = splitLineEnding(lines[i])
			}
			continue
		}
		text = maskCStringMacros(text)
		text = maskCAnnotationMacros(text)
		text = maskCTypeMacros(text)
		text = maskCVaArgTypeArguments(text)
		text = maskCTypeArgumentMacroCalls(text)
		text = maskCInterDeclarationAnnotations(text)
		text = maskCShortHexEscapes(text)
		text = replacePatternSameLength(text, cBuiltinAvailablePattern, "1")
		text = cBKIMacroPattern.ReplaceAllStringFunc(text, func(m string) string { return strings.Repeat(" ", len(m)) })
		text = replaceAllSameLength(text, ", >)", ", 0)")
		text = replaceAllSameLength(text, ", <)", ", 0)")
		lines[i] = text + newline
	}
	return strings.Join(lines, "")
}

func updateCPreprocessorSkipStack(stack []bool, trimmed string) []bool {
	directive := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	fields := strings.Fields(directive)
	if len(fields) == 0 {
		return stack
	}
	switch fields[0] {
	case "if":
		stack = append(stack, len(fields) > 1 && fields[1] == "0")
	case "ifdef", "ifndef":
		stack = append(stack, false)
	case "elif", "else":
		if len(stack) > 0 {
			stack[len(stack)-1] = true
		}
	case "endif":
		if len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
	}
	return stack
}

func cPreprocessorSkipping(stack []bool) bool {
	for _, skipping := range stack {
		if skipping {
			return true
		}
	}
	return false
}

var (
	bashHereDocPipePattern      = regexp.MustCompile(`<<-?['"]?[A-Za-z_][A-Za-z0-9_]*['"]?\|`)
	bashHereDocPipeNamePattern  = regexp.MustCompile(`<<-?['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?\|`)
	bashCommandParameterPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:\+[^}\n;]+;[^}\n]*\}`)
	zshGlobParameterPattern     = regexp.MustCompile(`\$\{(?:\([^}\n]*\))?[@A-Za-z_][^}\n]*:#\([^}\n]*\)\}`)
	zshNestedParameterPattern   = regexp.MustCompile(`\$\{#[^}\n]*\$\{[^}\n]+\}[^}\n]*\}`)
	// bashSubstringExpansionPattern matches substring expansions
	// (`${arg:$index:1}`) while excluding the `:-` `:=` `:+` `:?` operator
	// forms; bashBareVariableRefPattern then finds bare `$var` offsets/lengths
	// inside them, which the vendored tree-sitter-bash grammar cannot parse
	// (it accepts `${arg:0:1}` and `${arg:${i}:1}` but emits a missing-`}`
	// error for `${arg:$i:1}` that derails everything after it — pyenv's
	// python-build lost can_use_homebrew and neighbors to one such expansion).
	bashSubstringExpansionPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:[ \t]*[^-=+?}\n][^}\n]*\}`)
	bashBareVariableRefPattern    = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*`)
	// bashSplicedTestArgsPattern matches `[ operand "$@" ]` test commands,
	// where "$@" splices the operator and right operand in at runtime (pyenv's
	// is_mac does `[ "$(osx_version)" "$@" ]`). The grammar has no production
	// for a two-operand test without an operator, and the resulting ERROR node
	// swallows adjacent function definitions.
	bashSplicedTestArgsPattern = regexp.MustCompile(`\[ [^]\[\n]+ ("\$@") \]`)
)

// maskBashSplicedTestArgs rewrites the trailing `"$@"` of a two-operand test
// command to the same-length `= xx` operator-and-operand pair so the grammar
// sees a well-formed binary test. Line and column positions are unchanged.
func maskBashSplicedTestArgs(content string) string {
	return bashSplicedTestArgsPattern.ReplaceAllStringFunc(content, func(match string) string {
		fields := strings.Fields(strings.TrimSuffix(match, ` "$@" ]`))
		// When the token before "$@" is a test operator (`[ -n "$@" ]`,
		// `[ "$a" -ot "$@" ]`), the test is already well-formed.
		if len(fields) == 0 || strings.HasPrefix(fields[len(fields)-1], "-") {
			return match
		}
		return strings.Replace(match, `"$@" ]`, `= xx ]`, 1)
	})
}

// maskBashSubstringVariableOffsets rewrites bare `$var` offsets/lengths in
// substring expansions to same-length digit runs (`${arg:$index:1}` →
// `${arg:000000:1}`), which the grammar parses as numbers. Line and column
// positions are unchanged.
func maskBashSubstringVariableOffsets(content string) string {
	return bashSubstringExpansionPattern.ReplaceAllStringFunc(content, func(match string) string {
		colon := strings.Index(match, ":")
		head, body := match[:colon+1], match[colon+1:]
		if !bashBareVariableRefPattern.MatchString(body) {
			return match
		}
		return head + bashBareVariableRefPattern.ReplaceAllStringFunc(body, func(ref string) string {
			return strings.Repeat("0", len(ref))
		})
	})
}

func maskBashUnsupportedSyntax(content string) string {
	masked := bashCommandParameterPattern.ReplaceAllStringFunc(content, func(match string) string {
		return sameLengthReplacement(`""`, len(match))
	})
	masked = maskBashSubstringVariableOffsets(masked)
	masked = maskBashSplicedTestArgs(masked)
	masked = zshGlobParameterPattern.ReplaceAllStringFunc(masked, func(match string) string {
		return sameLengthReplacement(`"x"`, len(match))
	})
	masked = zshNestedParameterPattern.ReplaceAllStringFunc(masked, func(match string) string {
		return sameLengthReplacement(`"1"`, len(match))
	})
	lines := strings.SplitAfter(masked, "\n")
	skipHereDocUntil := ""
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		if skipHereDocUntil != "" {
			if strings.TrimSpace(text) == skipHereDocUntil {
				skipHereDocUntil = ""
			}
			lines[i] = maskLineText(text) + newline
			continue
		}
		if bashHereDocPipePattern.MatchString(text) {
			if match := bashHereDocPipeNamePattern.FindStringSubmatch(text); len(match) == 2 {
				skipHereDocUntil = match[1]
			}
			replacement := "true"
			if strings.HasPrefix(strings.TrimSpace(text), "(") {
				replacement = "(true) || true"
			}
			text = paddedReplacement(leadingWhitespace(text), replacement, len(text))
		}
		if strings.Contains(text, `${(@f)"$(`) {
			text = paddedReplacement(leadingWhitespace(text), `completions=("x")`, len(text))
		}
		lines[i] = text + newline
	}
	return strings.Join(lines, "")
}

func maskZshUnsupportedSyntax(content string) string {
	content = maskZshParameterExpansions(content)
	content = strings.ReplaceAll(content, ">|", "> ")
	content = maskZshAnonymousFunctions(content)
	return maskBashUnsupportedSyntax(content)
}

func maskZshParameterExpansions(content string) string {
	var out strings.Builder
	out.Grow(len(content))
	for i := 0; i < len(content); {
		if i+1 >= len(content) || content[i] != '$' || content[i+1] != '{' {
			out.WriteByte(content[i])
			i++
			continue
		}
		end := findShellExpansionEnd(content, i+2)
		if end < 0 {
			out.WriteByte(content[i])
			i++
			continue
		}
		expansion := content[i : end+1]
		inner := content[i+2 : end]
		if zshSpecificParameterExpansion(inner) {
			out.WriteString(sameLengthReplacement("0", len(expansion)))
		} else {
			out.WriteString(expansion)
		}
		i = end + 1
	}
	return out.String()
}

func findShellExpansionEnd(content string, start int) int {
	depth := 1
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func zshSpecificParameterExpansion(inner string) bool {
	return strings.HasPrefix(inner, "(") ||
		strings.HasPrefix(inner, "+") ||
		strings.Contains(inner, "${") ||
		strings.Contains(inner, ":#") ||
		strings.Contains(inner, ":|") ||
		strings.Contains(inner, ":gs") ||
		strings.Contains(inner, ":h") ||
		strings.Contains(inner, "[(I") ||
		strings.Contains(inner, "[(r") ||
		strings.Contains(inner, "[(R")
}

func maskZshAnonymousFunctions(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		trimmed := strings.TrimSpace(text)
		switch trimmed {
		case "() {":
			lines[i] = paddedReplacement(leadingWhitespace(text), "f(){", len(text)) + newline
		case "function {":
			lines[i] = paddedReplacement(leadingWhitespace(text), "anon() {", len(text)) + newline
		}
	}
	return strings.Join(lines, "")
}

func maskCAnnotationMacros(text string) string {
	text = cAnnotationMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.Repeat(" ", len(match))
	})
	return cPrintfAttributeMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.Repeat(" ", len(match))
	})
}

// cNextCodeLineStartsDeclaration reports whether the next non-blank line looks
// like the start of a declaration (identifier leading to a parameter list).
func cNextCodeLineStartsDeclaration(lines []string, from int) bool {
	for i := from; i < len(lines); i++ {
		text, _ := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		return cDeclarationStartLinePattern.MatchString(trimmed)
	}
	return false
}

// maskCTypeArgumentMacroCalls blanks the type argument of va_arg-wrapper macro
// calls (`avalue = form_ptr_arg(char *);`, `APR_ARRAY_IDX(args, i, char *)`)
// to a same-length integer literal.
func maskCTypeArgumentMacroCalls(text string) string {
	matches := cTypeArgMacroCallPattern.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text
	}
	b := []byte(text)
	for _, m := range matches {
		copy(b[m[2]:m[3]], sameLengthReplacement("0", m[3]-m[2]))
	}
	return string(b)
}

// cInterDeclarationKeywords are leading tokens that make a TYPE-MACRO-name or
// TYPE-name-MACRO match part of regular C (struct tags, storage classes,
// multi-keyword types) rather than an annotation macro to blank.
var cInterDeclarationKeywords = map[string]bool{
	"struct": true, "union": true, "enum": true, "const": true, "static": true,
	"unsigned": true, "signed": true, "long": true, "short": true,
	"volatile": true, "register": true, "case": true, "return": true,
	"goto": true, "typedef": true, "else": true, "extern": true,
}

// maskCInterDeclarationAnnotations blanks all-caps attribute macros wedged
// inside a declaration, either between the type and the declarator
// (`static const char CURL_USED min_stack[] = ...`) or between the declarator
// and its initializer (`gss_OID_desc Curl_spnego_mech_oid CURL_ALIGN8 = {`).
func maskCInterDeclarationAnnotations(text string) string {
	text = blankGuardedSubmatch(text, cTypeAnnotationNamePattern, 2, cInterDeclarationKeywords)
	text = blankGuardedSubmatch(text, cNameAnnotationInitPattern, 3, cInterDeclarationKeywords)
	text = blankGuardedSubmatch(text, cPointerQualifierMacroPattern, 1, cPointerQualifierKeywords)
	text = blankGuardedSubmatch(text, cParamPrefixMacroPattern, 1, nil)
	return blankGuardedSubmatch(text, cAnnotationBeforeTypePattern, 1, nil)
}

// blankGuardedSubmatch blanks capture group `group` of every pattern match
// whose guard token (group 1) is not one of the given C keywords.
func blankGuardedSubmatch(text string, pattern *regexp.Regexp, group int, keywords map[string]bool) string {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text
	}
	b := []byte(text)
	for _, m := range matches {
		guard := text[m[2]:m[3]]
		if keywords[guard] {
			continue
		}
		for i := m[2*group]; i < m[2*group+1]; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// maskCShortHexEscapes blanks the backslash of single-hex-digit `\x` string
// escapes (`"\xb"`), which the vendored tree-sitter-c grammar rejects; the
// literal stays a valid string of the same length. Applied twice because a
// match consumes the character that follows the escape, which may itself be
// the backslash of an adjacent short escape (`"\xb\xc"`).
func maskCShortHexEscapes(text string) string {
	blank := func(m string) string { return " " + m[1:] }
	text = cShortHexEscapePattern.ReplaceAllStringFunc(text, blank)
	return cShortHexEscapePattern.ReplaceAllStringFunc(text, blank)
}

// cLineOpensBlockComment reports whether text opens a `/*` block comment that
// is not closed on the same line.
func cLineOpensBlockComment(text string) bool {
	open := strings.LastIndex(text, "/*")
	if open < 0 {
		return false
	}
	return !strings.Contains(text[open+2:], "*/")
}

// maskCVaArgTypeArguments blanks the type argument of `va_arg(ap, TYPE)`
// invocations to a same-length integer literal so the call parses; va_arg is
// compiler magic and its second argument is a type name, not an expression.
func maskCVaArgTypeArguments(text string) string {
	locs := cVaArgCallPattern.FindAllStringIndex(text, -1)
	if locs == nil {
		return text
	}
	b := []byte(text)
	for _, loc := range locs {
		open := loc[1] - 1
		end := balancedCallEnd(text, open)
		if end < 0 {
			continue
		}
		depth, comma := 0, -1
		for i := open; i < end && comma < 0; i++ {
			switch text[i] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 1 {
					comma = i
				}
			}
		}
		if comma < 0 {
			continue
		}
		start := comma + 1
		for start < end-1 && (b[start] == ' ' || b[start] == '\t') {
			start++
		}
		if start >= end-1 {
			continue
		}
		b[start] = '0'
		for i := start + 1; i < end-1; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

func maskCStringMacros(text string) string {
	return cStringMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
		return sameLengthReplacement(`""`, len(match))
	})
}

func maskCTypeMacros(text string) string {
	text = cTypeMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
		return sameLengthReplacement("struct c_macro", len(match))
	})
	text = cHeadInitializerPattern.ReplaceAllStringFunc(text, func(match string) string {
		return sameLengthReplacement("0", len(match))
	})
	return cBareAnnotationPattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.Repeat(" ", len(match))
	})
}

func maskKotlinGradleOptionValueAssignments(content string) string {
	return maskKotlinGradleBlocks(content, ".value =", "maskedGradleOptionValue()")
}

func maskKotlinGradleWhenGetOrElse(content string) string {
	return maskKotlinGradleBlocks(content, ".getOrElse(when (", ".getOrElse(\"masked\")")
}

func maskKotlinGradleNamedBlock(content, name, replacement string) string {
	lines := strings.SplitAfter(content, "\n")
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		if strings.TrimSpace(text) != name+" {" {
			continue
		}
		indent := leadingWhitespace(text)
		blankUntil := i
		balance := 0
		for j := i; j < len(lines); j++ {
			lineText, _ := splitLineEnding(lines[j])
			balance += strings.Count(lineText, "{") - strings.Count(lineText, "}")
			blankUntil = j
			if j > i && balance <= 0 {
				break
			}
		}
		lines[i] = paddedReplacement(indent, replacement, len(text)) + newline
		for j := i + 1; j <= blankUntil; j++ {
			lineText, lineNewline := splitLineEnding(lines[j])
			lines[j] = maskLineText(lineText) + lineNewline
		}
		i = blankUntil
	}
	return strings.Join(lines, "")
}

func maskKotlinGradleBlocks(content, marker, replacement string) string {
	lines := strings.SplitAfter(content, "\n")
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		markerIndex := strings.Index(text, marker)
		if markerIndex < 0 {
			continue
		}
		indent := leadingWhitespace(text)
		blankUntil := i
		balance := 0
		for j := i; j < len(lines); j++ {
			lineText, _ := splitLineEnding(lines[j])
			balance += strings.Count(lineText, "(") - strings.Count(lineText, ")")
			balance += strings.Count(lineText, "{") - strings.Count(lineText, "}")
			blankUntil = j
			if j > i && balance <= 0 {
				break
			}
		}
		lines[i] = paddedReplacement(indent, replacement, len(text)) + newline
		for j := i + 1; j <= blankUntil; j++ {
			lineText, lineNewline := splitLineEnding(lines[j])
			lines[j] = maskLineText(lineText) + lineNewline
		}
		i = blankUntil
	}
	return strings.Join(lines, "")
}

func maskCPlusPlusUnsupportedSyntax(content string) string {
	content = maskCPlusPlusTemplateDecltypeExpressions(content)
	content = jlAnnotationMacroPattern.ReplaceAllStringFunc(content, func(m string) string { return strings.Repeat(" ", len(m)) })
	lines := strings.SplitAfter(content, "\n")
	var preprocessorSkipStack []bool
	for i := 0; i < len(lines); i++ {
		text, newline := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "#") {
			preprocessorSkipStack = updateCPlusPlusPreprocessorSkipStack(preprocessorSkipStack, trimmed)
			for {
				lines[i] = maskLineText(text) + newline
				if !strings.HasSuffix(strings.TrimRight(text, " \t"), "\\") || i+1 >= len(lines) {
					break
				}
				i++
				text, newline = splitLineEnding(lines[i])
			}
			continue
		}
		if cPreprocessorSkipping(preprocessorSkipStack) {
			lines[i] = maskLineText(text) + newline
			continue
		}
		switch trimmed {
		case "FMT_BEGIN_NAMESPACE":
			lines[i] = paddedReplacement(leadingWhitespace(text), "namespace fmt {", len(text)) + newline
		case "FMT_END_NAMESPACE":
			lines[i] = paddedReplacement(leadingWhitespace(text), "}", len(text)) + newline
		case "NLOHMANN_JSON_NAMESPACE_BEGIN":
			lines[i] = paddedReplacement(leadingWhitespace(text), "namespace nlohmann {", len(text)) + newline
		case "NLOHMANN_JSON_NAMESPACE_END":
			lines[i] = paddedReplacement(leadingWhitespace(text), "}", len(text)) + newline
		case "NLOHMANN_BASIC_JSON_TPL_DECLARATION":
			lines[i] = paddedReplacement(leadingWhitespace(text), "template<typename BasicJsonType>", len(text)) + newline
		case "FMT_BEGIN_EXPORT", "FMT_END_EXPORT":
			lines[i] = maskLineText(text) + newline
		case "FMT_TRY {":
			lines[i] = paddedReplacement(leadingWhitespace(text), "try {", len(text)) + newline
		case "FMT_CATCH(...) {}":
			lines[i] = paddedReplacement(leadingWhitespace(text), "catch(...) {}", len(text)) + newline
		case "JSON_TRY":
			lines[i] = paddedReplacement(leadingWhitespace(text), "try", len(text)) + newline
		default:
			if cxxBeginNamespaceMacroPattern.MatchString(trimmed) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "namespace ns {", len(text)) + newline
				continue
			}
			if cxxEndNamespaceMacroPattern.MatchString(trimmed) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "}", len(text)) + newline
				continue
			}
			if strings.HasPrefix(trimmed, "JSON_CATCH(...) {}") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "catch(...) {}", len(text)) + newline
				continue
			}
			if strings.HasPrefix(trimmed, "JSON_CATCH") || strings.HasPrefix(trimmed, "JSON_INTERNAL_CATCH") {
				catchName := "JSON_CATCH"
				if strings.HasPrefix(trimmed, "JSON_INTERNAL_CATCH") {
					catchName = "JSON_INTERNAL_CATCH"
				}
				if masked, ok := maskCPlusPlusCatchMacro(text, catchName); ok {
					lines[i] = masked + newline
					continue
				}
			}
			if strings.HasPrefix(trimmed, "export module ") {
				lines[i] = maskLineText(text) + newline
				continue
			}
			if strings.HasPrefix(trimmed, "import ") && strings.Contains(trimmed, ".") {
				lines[i] = maskLineText(text) + newline
				continue
			}
			text = replaceAllSameLength(text, "FMT_TRY", "try")
			text = replaceAllSameLength(text, "FMT_CATCH", "catch")
			text = replaceAllSameLength(text, "using typename ", "using ")
			if strings.HasPrefix(trimmed, "JSON_PRIVATE_UNLESS_TESTED") {
				lines[i] = maskLineText(text) + newline
			} else if strings.HasPrefix(trimmed, "void_t<decltype(") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "void>", len(text)) + newline
			} else if strings.Contains(text, "-> decltype(") && balancedCallEnd(text, strings.Index(text, "-> decltype(")+len("-> decltype")) < 0 {
				startLine := i
				startText := text
				startNewline := newline
				marker := strings.Index(text, "-> decltype(")
				balance := strings.Count(text[marker:], "(") - strings.Count(text[marker:], ")")
				for balance > 0 && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					lines[i] = maskLineText(text) + newline
				}
				replacement := "-> void"
				if strings.HasSuffix(strings.TrimSpace(text), "{") {
					replacement = "-> void {"
				} else if !cPlusPlusNextNonEmptyLineStarts(lines, i+1, "{") {
					replacement = "-> void;"
				}
				lines[startLine] = startText[:marker] + sameLengthReplacement(replacement, len(startText)-marker) + startNewline
			} else if cPlusPlusMultilineTestMacroStart(trimmed) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "void test_macro()", len(text)) + newline
				balance := strings.Count(text, "(") - strings.Count(text, ")")
				for balance > 0 && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					lines[i] = maskLineText(text) + newline
				}
			} else if cPlusPlusMultilineControlMacroStart(trimmed) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "if (true)", len(text)) + newline
				balance := strings.Count(text, "(") - strings.Count(text, ")")
				for balance > 0 && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					lines[i] = maskLineText(text) + newline
				}
			} else if strings.HasPrefix(trimmed, "GMOCK_KIND_OF_(") || strings.HasPrefix(trimmed, "GMOCK_FLAG(") {
				text = maskCPlusPlusFunctionLikeMacro(text, "GMOCK_KIND_OF_", "kBool")
				text = maskCPlusPlusFunctionLikeMacro(text, "GMOCK_FLAG", "gmock_flag")
				lines[i] = text + newline
			} else if cPlusPlusMultilineBlankMacroStart(trimmed) {
				balance := 0
				firstLine := true
				statementMacro := cPlusPlusStatementReplacementMacroLinePattern.MatchString(trimmed)
				for {
					if !firstLine && !strings.HasPrefix(trimmed, "CHECK_THROWS") && (strings.TrimSpace(text) == "}" || strings.TrimSpace(text) == "};") {
						break
					}
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					if firstLine && statementMacro {
						lines[i] = paddedReplacement(leadingWhitespace(text), "0;", len(text)) + newline
					} else {
						lines[i] = maskLineText(text) + newline
					}
					firstLine = false
					if (balance <= 0 && strings.Contains(text, ")")) || i+1 >= len(lines) {
						break
					}
					i++
					text, newline = splitLineEnding(lines[i])
				}
				if statementMacro {
					i = maskCPlusPlusStreamingMacroContinuations(lines, i)
				}
			} else if cPlusPlusMultilineAnnotationMacroStart(trimmed) {
				balance := 0
				for {
					balance += strings.Count(text, "(") - strings.Count(text, ")")
					lines[i] = maskLineText(text) + newline
					if (balance <= 0 && strings.Contains(text, ")")) || i+1 >= len(lines) {
						break
					}
					i++
					text, newline = splitLineEnding(lines[i])
				}
			} else if masked, ok := maskCPlusPlusTestMacroDefinition(text); ok {
				lines[i] = masked + newline
			} else if masked, ok := maskCPlusPlusDoctestControlMacro(text); ok {
				lines[i] = masked + newline
			} else if strings.Contains(trimmed, "= delete;") {
				lines[i] = maskLineText(text) + newline
			} else if strings.HasPrefix(trimmed, "extern template ") {
				for {
					lines[i] = maskLineText(text) + newline
					if strings.Contains(text, ";") || i+1 >= len(lines) {
						break
					}
					i++
					text, newline = splitLineEnding(lines[i])
				}
			} else if cPlusPlusLeadingAnnotationMacroLine(trimmed) {
				text = maskCPlusPlusAnnotationMacros(text)
				text = maskCPlusPlusFunctionLikeMacro(text, "DOCTEST_REF_WRAP", "T")
				text = normalizeCPlusPlusPrimitiveSpecifiers(text)
				lines[i] = text + newline
			} else if strings.HasPrefix(trimmed, "struct StringMaker : public detail::StringMakerBase<") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "struct StringMaker {};", len(text)) + newline
				for !strings.Contains(text, "{};") && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					lines[i] = maskLineText(text) + newline
				}
			} else if strings.HasPrefix(trimmed, "DOCTEST_INTERNAL_ERROR(") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "0;", len(text)) + newline
			} else if cPlusPlusBlankMacroLine(trimmed) {
				if cPlusPlusStatementReplacementMacroLinePattern.MatchString(trimmed) {
					lines[i] = paddedReplacement(leadingWhitespace(text), "0;", len(text)) + newline
					i = maskCPlusPlusStreamingMacroContinuations(lines, i)
				} else {
					lines[i] = maskLineText(text) + newline
				}
			} else if strings.Contains(trimmed, `result["compiler"] = "hp"`) {
				lines[i] = paddedReplacement(leadingWhitespace(text), `result["compiler"]="hp";`, len(text)) + newline
			} else if strings.Contains(text, "std::partial_ordering operator<=>") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "bool compare_spaceship() const noexcept", len(text)) + newline
			} else if strings.Contains(text, "FMT_ENABLE_IF(") && balancedCallEnd(text, strings.Index(text, "FMT_ENABLE_IF(")+len("FMT_ENABLE_IF")) < 0 {
				marker := strings.Index(text, "FMT_ENABLE_IF(")
				lines[i] = text[:marker] + sameLengthReplacement("int = 0>", len(text)-marker) + newline
				for !strings.Contains(text, ")>") && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					lines[i] = maskLineText(text) + newline
				}
			} else if strings.Contains(text, "using ") && strings.Contains(text, "= typename std::enable_if<") {
				replacement := "using cxx_enable_if = void;"
				if alias := cPlusPlusUsingAliasName(text); alias != "" {
					replacement = "using " + alias + " = void;"
				}
				lines[i] = paddedReplacement(leadingWhitespace(text), replacement, len(text)) + newline
				for i+1 < len(lines) {
					nextText, nextNewline := splitLineEnding(lines[i+1])
					i++
					lines[i] = maskLineText(nextText) + nextNewline
					if strings.Contains(nextText, "::type") || strings.Contains(nextText, ">;") || strings.Contains(nextText, ";") {
						break
					}
				}
			} else if strings.Contains(text, "std::enable_if<") && !strings.Contains(text, "::type") {
				templateParam := strings.Contains(text, "template <") || strings.Contains(text, "template<") || cPlusPlusPreviousNonBlankLineStartsTemplate(lines, i)
				if templateParam {
					replacement := "typename E=void>"
					if marker := strings.Index(text, "typename std::enable_if<"); marker >= 0 && strings.Contains(text[:marker], "template") {
						replacement = text[:marker] + sameLengthReplacement("typename E=void>", len(text)-marker)
					} else if strings.Contains(text, "template") && strings.Contains(text, "= std::enable_if<") {
						replacement = "template <typename E = void>"
					} else if cPlusPlusEnableIfContinuationEndsWithComma(lines, i) {
						replacement = "typename E=void,"
					}
					if len(replacement) == len(text) {
						lines[i] = replacement + newline
					} else {
						lines[i] = paddedReplacement(leadingWhitespace(text), replacement, len(text)) + newline
					}
				} else {
					lines[i] = paddedReplacement(leadingWhitespace(text), "int* enabler", len(text)) + newline
				}
				for i+1 < len(lines) {
					nextText, nextNewline := splitLineEnding(lines[i+1])
					if !strings.Contains(nextText, "::type") {
						i++
						lines[i] = maskLineText(nextText) + nextNewline
						continue
					}
					i++
					lines[i] = maskLineText(nextText) + nextNewline
					break
				}
			} else if marker := cPlusPlusEnableIfTypenameStart(text); marker >= 0 && cPlusPlusEnableIfTypePointerTemplateDefaultLinePattern.MatchString(text) {
				replacement := text[:marker] + sameLengthReplacement("typename E=void>", len(text)-marker)
				lines[i] = replacement + newline
			} else if cPlusPlusEnableIfTypePointerTemplateDefaultLinePattern.MatchString(text) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "typename = void>", len(text)) + newline
			} else if cPlusPlusEnableIfTypePointerParamLinePattern.MatchString(text) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "int* enabler", len(text)) + newline
			} else if cPlusPlusGTestPointerTemplateDefaultPattern.MatchString(text) {
				lines[i] = maskLineText(text) + newline
				if i+1 < len(lines) {
					nextText, nextNewline := splitLineEnding(lines[i+1])
					if strings.Contains(nextText, "= delete;") {
						i++
						lines[i] = maskLineText(nextText) + nextNewline
					}
				}
			} else if cPlusPlusEnableIfPointerDefaultPattern.MatchString(text) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "int* p = nullptr", len(text)) + newline
				if i+1 < len(lines) {
					nextText, nextNewline := splitLineEnding(lines[i+1])
					if strings.Contains(nextText, "static_cast") && strings.Contains(nextText, "{") {
						i++
						lines[i] = paddedReplacement(leadingWhitespace(nextText), ") {", len(nextText)) + nextNewline
					} else if idx := strings.Index(nextText, "nullptr"); idx >= 0 {
						i++
						lines[i] = nextText[:idx] + strings.Repeat(" ", len("nullptr")) + nextText[idx+len("nullptr"):] + nextNewline
					}
				}
			} else if cPlusPlusDirectInitializerTernaryPattern.MatchString(text) && i+1 < len(lines) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "auto cxx_value = value;", len(text)) + newline
				i++
				nextText, nextNewline := splitLineEnding(lines[i])
				lines[i] = maskLineText(nextText) + nextNewline
			} else if strings.Contains(text, "typename std::is_pointer") && strings.Contains(text, "::type()") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "std::true_type(),", len(text)) + newline
			} else if strings.HasPrefix(trimmed, "template class ") {
				for {
					lines[i] = maskLineText(text) + newline
					if strings.Contains(text, ";") || i+1 >= len(lines) {
						break
					}
					i++
					text, newline = splitLineEnding(lines[i])
				}
			} else if strings.HasPrefix(trimmed, "template struct ") {
				lines[i] = maskLineText(text) + newline
			} else if strings.HasPrefix(trimmed, "FMT_PRAGMA_") {
				lines[i] = maskLineText(text) + newline
			} else if trimmed == `extern "C" {` {
				for {
					lines[i] = maskLineText(text) + newline
					if (strings.HasPrefix(strings.TrimSpace(text), `}  // extern "C"`) || strings.HasPrefix(strings.TrimSpace(text), `} // extern "C"`)) || i+1 >= len(lines) {
						break
					}
					i++
					text, newline = splitLineEnding(lines[i])
				}
			} else if strings.HasPrefix(trimmed, `}  // extern "C"`) || strings.HasPrefix(trimmed, `} // extern "C"`) {
				lines[i] = maskLineText(text) + newline
			} else if strings.HasPrefix(trimmed, "(void)") && (strings.Contains(trimmed, "{}") || strings.Contains(trimmed, "{};")) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "(void)0;", len(text)) + newline
			} else if strings.HasPrefix(trimmed, ": std::conditional<") {
				lines[i] = paddedReplacement(leadingWhitespace(text), ": std::true_type {};", len(text)) + newline
				for !strings.Contains(text, "{}") && !strings.Contains(text, "{ }") && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					lines[i] = maskLineText(text) + newline
				}
			} else if cPlusPlusStdIntegralConstantBoolExpressionLinePattern.MatchString(trimmed) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "struct cxx_bool_constant : std::false_type {};", len(text)) + newline
			} else if cPlusPlusBoolConstantCallLinePattern.MatchString(trimmed) {
				lines[i] = replacePatternSameLength(text, cPlusPlusBoolConstantCallPattern, "bool_constant<true>") + newline
			} else if !strings.HasPrefix(trimmed, "struct ") && !strings.HasPrefix(trimmed, "class ") && strings.Contains(trimmed, ")") && strings.HasSuffix(trimmed, "{};") {
				lines[i] = text[:strings.LastIndex(text, ";")] + strings.Repeat(" ", len(text)-strings.LastIndex(text, ";")) + newline
			} else if strings.HasPrefix(trimmed, "}; // namespace") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "} // namespace", len(text)) + newline
			} else if trimmed == "};" && cPlusPlusLikelyFunctionBodyCloseSemi(lines, i) {
				lines[i] = paddedReplacement(leadingWhitespace(text), "}", len(text)) + newline
			} else if strings.HasPrefix(trimmed, "return {") {
				lines[i] = paddedReplacement(leadingWhitespace(text), "return {};", len(text)) + newline
				for !strings.Contains(text, ";") && i+1 < len(lines) {
					i++
					text, newline = splitLineEnding(lines[i])
					lines[i] = maskLineText(text) + newline
				}
			} else if !strings.HasPrefix(trimmed, "#") {
				text = maskCPlusPlusFunctionLikeMacro(text, "FMT_ENABLE_IF", "typename T = void")
				text = maskCPlusPlusFunctionLikeMacro(text, "FMT_SO_VISIBILITY", "")
				text = maskCPlusPlusFunctionLikeMacro(text, "FMT_VISIBILITY", "")
				text = maskCPlusPlusFunctionLikeMacro(text, "DOCTEST_REF_WRAP", "T")
				text = maskCPlusPlusFunctionLikeMacro(text, "GTEST_BIND_", "TestSel")
				text = maskCPlusPlusFunctionLikeMacro(text, "GTEST_REMOVE_REFERENCE_AND_CONST_", "T")
				text = maskCPlusPlusFunctionLikeMacro(text, "GMOCK_KIND_OF_", "kBool")
				text = maskCPlusPlusFunctionLikeMacro(text, "GMOCK_FLAG", "gmock_flag")
				text = maskCPlusPlusFunctionLikeMacro(text, "DOCTEST_STRINGIFY", `"expr"`)
				text = maskCPlusPlusFunctionLikeMacro(text, "__declspec", "")
				text = maskCPlusPlusLikelyMacro(text, "JSON_HEDLEY_LIKELY")
				text = maskCPlusPlusLikelyMacro(text, "JSON_HEDLEY_UNLIKELY")
				text = maskCPlusPlusFunctionLikeMacro(text, "STRINGIZE", `"s"`)
				text = maskCPlusPlusFunctionLikeMacro(text, "DOCTEST_BRANCH_ON_DISABLED", "{}")
				text = maskCPlusPlusFunctionLikeMacroPattern(text, cPlusPlusHedleyFunctionLikeMacroPattern, "")
				text = maskCPlusPlusFunctionLikeMacroPattern(text, cPlusPlusAnnotationFunctionLikeMacroPattern, "")
				text = maskCPlusPlusTrailingDecltypeReturn(text)
				text = maskCPlusPlusDependentTemplateKeyword(text)
				text = maskCPlusPlusMemberOperatorCall(text)
				text = maskCPlusPlusOperatorCall(text)
				text = replaceAllSameLength(text, "std::strong_ordering", "bool")
				text = normalizeCPlusPlusPrimitiveSpecifiers(text)
				text = replaceAllSameLength(text, "operator<=>", "operator<")
				text = replaceAllSameLength(text, " <=> ", " < ")
				text = replacePatternSameLength(text, cPlusPlusDependentTypenameTemporaryPattern, "= T{}")
				text = replacePatternSameLength(text, cPlusPlusDependentTypenameParenPattern, "= T(")
				text = replacePatternSameLength(text, cPlusPlusDependentTypenameConstructorPattern, "T(")
				text = replacePatternSameLength(text, cPlusPlusArrayReferenceTemplateDefaultPattern, "typename Array = T")
				text = replacePatternSameLength(text, cPlusPlusPointerTemplateDefaultPattern, "typename Ptr = T")
				text = replacePatternSameLength(text, cPlusPlusCommentedPointerParamPattern, "T* unused")
				text = replacePatternSameLength(text, cPlusPlusMemberPointerDeclPattern, "size_t (*Fn)(")
				text = replacePatternSameLength(text, cPlusPlusMemberPointerFieldPattern, "int *field_")
				text = replacePatternSameLength(text, cPlusPlusMemberFunctionPointerTemplateArgPattern, "PropertyType (*)()")
				text = replacePatternSameLength(text, cPlusPlusEmptyBraceDefaultPattern, "= 0")
				text = replacePatternSameLength(text, cPlusPlusTemplatePointerDefaultPattern, "> = 0 >")
				text = replacePatternSameLength(text, cPlusPlusEnableIfPointerDefaultPattern, "typename = void")
				text = replaceAllSameLength(text, "operator*()", "op()")
				text = replaceAllSameLength(text, "NLOHMANN_BASIC_JSON_TPL", "BasicJsonType")
				text = replaceAllSameLength(text, "JSON_INLINE_VARIABLE", "inline")
				text = replaceAllSameLength(text, "JSON_NO_UNIQUE_ADDRESS", "")
				text = replaceAllSameLength(text, "decltype(filters)(9)", "{}")
				text = replaceAllSameLength(text, "GTEST_NAME_", `"gtest"`)
				text = replacePatternSameLength(text, cPlusPlusGTestPointerTemplateDefaultPattern, "typename R, int N = 0")
				text = replacePatternSameLength(text, cPlusPlusMemberPointerDecltypePattern, "int")
				text = maskCPlusPlusUnsignedFunctionalCast(text)
				text = maskCPlusPlusEmptyDefaultInitializers(text)
				text = replaceAllSameLength(text, "template <typename,", "template <class T, ")
				if strings.Contains(text, "->*") {
					text = paddedReplacement(leadingWhitespace(text), "auto call_result = call();", len(text))
				}
				if strings.HasPrefix(strings.TrimSpace(text), "if (auto ") {
					replacement := "if (true)"
					if strings.Contains(text, "{") {
						replacement = "if (true) {"
					}
					text = paddedReplacement(leadingWhitespace(text), replacement, len(text))
				}
				if strings.Contains(text, ".*(&") {
					text = paddedReplacement(leadingWhitespace(text), "return {};", len(text))
				}
				if strings.Contains(text, "::operator bool()") {
					text = paddedReplacement(leadingWhitespace(text), "bool op() {", len(text))
				}
				text = replacePatternSameLength(text, cPlusPlusAnonymousEnumPattern, "enum cxx_enum")
				text = replacePatternSameLength(text, cPlusPlusExplicitOperatorCallPattern, "call(")
				text = replacePatternSameLength(text, cPlusPlusConversionOperatorDeclPattern, "convert()")
				lines[i] = maskCPlusPlusAnnotationMacros(text) + newline
			}
		}
	}
	return strings.Join(lines, "")
}

func cPlusPlusPreviousNonBlankLineStartsTemplate(lines []string, i int) bool {
	for j := i - 1; j >= 0 && i-j <= 8; j-- {
		text, _ := splitLineEnding(lines[j])
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "template") {
			return true
		}
		if !strings.HasPrefix(trimmed, "typename") {
			return false
		}
	}
	return false
}

func cPlusPlusUsingAliasName(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "using ") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "using "))
	end := strings.Index(rest, "=")
	if end < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:end])
	if name == "" || strings.ContainsAny(name, " \t<>") {
		return ""
	}
	return name
}

func cPlusPlusLeadingAnnotationMacroLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "DOCTEST_NOINLINE ") ||
		strings.HasPrefix(trimmed, "DOCTEST_NORETURN ") ||
		strings.HasPrefix(trimmed, "DOCTEST_INTERFACE ") ||
		strings.HasPrefix(trimmed, "DOCTEST_INTERFACE_DECL ") ||
		strings.HasPrefix(trimmed, "DOCTEST_CONSTEXPR_FUNC ") ||
		strings.HasPrefix(trimmed, "DOCTEST_THREAD_LOCAL ") ||
		strings.HasPrefix(trimmed, "ATTRIBUTE_TARGET_") ||
		strings.HasPrefix(trimmed, "ATTRIBUTE_NO_SANITIZE_") ||
		strings.HasPrefix(trimmed, "NO_SANITIZE_")
}

func normalizeCPlusPlusPrimitiveSpecifiers(text string) string {
	text = replaceAllSameLength(text, "double long", "long double")
	text = replaceAllSameLength(text, "char signed", "signed char")
	text = replaceAllSameLength(text, "char unsigned", "unsigned char")
	text = replaceAllSameLength(text, "short unsigned", "unsigned short")
	text = replaceAllSameLength(text, "long long unsigned", "unsigned long long")
	text = replaceAllSameLength(text, "long unsigned", "unsigned long")
	return text
}

func cPlusPlusLikelyFunctionBodyCloseSemi(lines []string, i int) bool {
	balance := 1
	for j := i - 1; j >= 0 && i-j <= 200; j-- {
		text, _ := splitLineEnding(lines[j])
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		balance += strings.Count(text, "}") - strings.Count(text, "{")
		if balance == 0 {
			openBrace := strings.Index(trimmed, "{")
			if openBrace < 0 || strings.Contains(trimmed[:openBrace], "=") {
				return false
			}
			return strings.Contains(trimmed[:openBrace], ")")
		}
		if balance < 0 {
			return false
		}
	}
	return false
}

func cPlusPlusEnableIfTypenameStart(text string) int {
	start := -1
	for _, needle := range []string{
		"typename std::enable_if",
		"typename types::enable_if",
		"typename detail::types::enable_if",
		"typename doctest::detail::types::enable_if",
	} {
		if idx := strings.Index(text, needle); idx >= 0 && (start < 0 || idx < start) {
			start = idx
		}
	}
	return start
}

func cPlusPlusEnableIfContinuationEndsWithComma(lines []string, i int) bool {
	for j := i + 1; j < len(lines) && j-i <= 8; j++ {
		text, _ := splitLineEnding(lines[j])
		if strings.Contains(text, "::type") {
			return strings.Contains(text, ",")
		}
	}
	return false
}

func updateCPlusPlusPreprocessorSkipStack(stack []bool, trimmed string) []bool {
	directive := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	fields := strings.Fields(directive)
	if len(fields) == 0 {
		return stack
	}
	switch fields[0] {
	case "if", "ifdef", "ifndef":
		stack = append(stack, cPlusPlusPreprocessorDirectiveSkipsBlock(trimmed))
	case "elif", "else":
		if len(stack) > 0 {
			stack[len(stack)-1] = !stack[len(stack)-1]
		}
	case "endif":
		if len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
	}
	return stack
}

func cPlusPlusPreprocessorDirectiveSkipsBlock(trimmed string) bool {
	directive := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	fields := strings.Fields(directive)
	return (len(fields) >= 2 && fields[0] == "if" && fields[1] == "0") ||
		strings.Contains(trimmed, "__cpp_lib_ranges")
}

var (
	cPlusPlusAnnotationMacroPattern                        = regexp.MustCompile(`\b(?:FMT_(?:API|FUNC|EXPORT|INLINE(?:_VARIABLE)?|CONSTEXPR(?:20|_STRING)?|CONSTEVAL|ALWAYS_INLINE|NODISCARD|NORETURN|DEPRECATED|MAYBE_UNUSED|NO_UNIQUE_ADDRESS|LIFETIMEBOUND|BUILTIN)|JSON_(?:HEDLEY_[A-Z0-9_]+|EXPLICIT|INLINE_VARIABLE)|DOCTEST_(?:INTERFACE(?:_DECL|_DEF)?|CONSTEXPR(?:_FUNC)?|NOEXCEPT|INLINE|NOINLINE|NORETURN|THREAD_LOCAL|ATTRIBUTE_[A-Z0-9_]+)|GTEST_API_|GMOCK_API_|GTEST_NO_INLINE_|GTEST_MUST_USE_RESULT_|GTEST_INTERNAL_EMPTY_BASE_CLASS|GTEST_ATTRIBUTE_NO_SANITIZE_[A-Z_]+|ATTRIBUTE_TARGET_[A-Z0-9_]+|ATTRIBUTE_NO_SANITIZE_[A-Z0-9_]+|NO_SANITIZE_[A-Z0-9_]+|__stdcall|CALLBACK|WINAPI)\b`)
	cPlusPlusAnnotationFunctionLikeMacroPattern            = regexp.MustCompile(`\b(?:GTEST_API_|GMOCK_API_|GTEST_DISABLE_MSC_WARNINGS_PUSH_|GTEST_DISABLE_MSC_WARNINGS_POP_|GTEST_ATTRIBUTE_[A-Za-z0-9_]+|GTEST_INTERNAL_DEPRECATED|GTEST_LOCK_EXCLUDED_|GTEST_EXCLUSIVE_LOCK_REQUIRED_|DOCTEST_MSVC_SUPPRESS_WARNING|DOCTEST_CLANG_SUPPRESS_WARNING|DOCTEST_GCC_SUPPRESS_WARNING|__attribute__)\s*\(`)
	cPlusPlusTestMacroPattern                              = regexp.MustCompile(`^(\s*)(?:TEST|TEST_F|TEST_P|TYPED_TEST|TYPED_TEST_P|MATCHER|TEST_CASE(?:_[A-Z0-9_]+)*|TEMPLATE_TEST_CASE(?:_[A-Z0-9_]+)*|SCENARIO|GIVEN|WHEN|THEN)\s*\(`)
	cPlusPlusDoctestControlMacroPattern                    = regexp.MustCompile(`^(\s*)(?:SECTION|SUBCASE|AND_WHEN|AND_THEN)\s*\(.*\)\s*`)
	cPlusPlusStatementMacroLinePattern                     = regexp.MustCompile(`^(?:CAPTURE|INFO|WARN|FAIL|SUCCEED|ADD_FAILURE(?:_AT)?|EXPECT(?:_[A-Z0-9_]+)?|ASSERT(?:_[A-Z0-9_]+)?|CHECK(?:_[A-Z0-9_]+)?|REQUIRE(?:_[A-Z0-9_]+)?|STATIC_REQUIRE(?:_[A-Z0-9_]+)?|JSON_(?:ASSERT|THROW|DIAGNOSTIC_IGNORE)|GTEST_(?:DEFINE|DISABLE|ALLOW|SUPPRESS)[A-Za-z0-9_]*|GMOCK_[A-Za-z0-9_]+)\s*\(`)
	cPlusPlusStatementReplacementMacroLinePattern          = regexp.MustCompile(`^(?:CAPTURE|INFO|WARN|FAIL|SUCCEED|ADD_FAILURE(?:_AT)?|EXPECT(?:_[A-Z0-9_]+)?|ASSERT(?:_[A-Z0-9_]+)?|CHECK(?:_[A-Z0-9_]+)?|REQUIRE(?:_[A-Z0-9_]+)?|STATIC_REQUIRE(?:_[A-Z0-9_]+)?|JSON_(?:ASSERT|THROW|DIAGNOSTIC_IGNORE))\s*\(`)
	cPlusPlusHedleyFunctionLikeMacroPattern                = regexp.MustCompile(`\bJSON_HEDLEY_[A-Z0-9_]+\s*\(`)
	cPlusPlusDependentTemplatePattern                      = regexp.MustCompile(`(\.|->)template\s+([A-Za-z_][A-Za-z0-9_]*)`)
	cPlusPlusMemberOperatorPattern                         = regexp.MustCompile(`(\.|->)operator\s*(?:\[\]|\(\)|[+\-*/<>=!&|^%,]+)`)
	cPlusPlusOperatorCallPattern                           = regexp.MustCompile(`(?:::)?operator\s*(?:<<|>>|\[\]|\(\)|[+\-*/<>=!&|^%,]+)\s*\(`)
	cPlusPlusDependentTypenameTemporaryPattern             = regexp.MustCompile(`=\s*typename\s+[A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)+\s*\{\}`)
	cPlusPlusDependentTypenameParenPattern                 = regexp.MustCompile(`=\s*typename\s+[A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)+\s*\(`)
	cPlusPlusDependentTypenameConstructorPattern           = regexp.MustCompile(`\btypename\s+[A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)+\s*\(`)
	cPlusPlusArrayReferenceTemplateDefaultPattern          = regexp.MustCompile(`typename\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*[A-Za-z_][A-Za-z0-9_]*\s*\(\s*&\s*\)\s*\[[^\]\n]+\]`)
	cPlusPlusPointerTemplateDefaultPattern                 = regexp.MustCompile(`typename\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*(?:const\s+)?[A-Za-z_][A-Za-z0-9_:]*\s*\*`)
	cPlusPlusCommentedPointerParamPattern                  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_:]*\s*\*\s*/\*[^*\n]*\*/`)
	cPlusPlusMemberPointerDeclPattern                      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_:]*\s*\(\s*[A-Za-z_][A-Za-z0-9_:]*::\*[A-Za-z_][A-Za-z0-9_]*\s*\)\s*\(`)
	cPlusPlusMemberPointerFieldPattern                     = regexp.MustCompile(`(?:const\s+)?[A-Za-z_][A-Za-z0-9_:<>]*\s+[A-Za-z_][A-Za-z0-9_:<>]*::\*[A-Za-z_][A-Za-z0-9_]*`)
	cPlusPlusMemberFunctionPointerTemplateArgPattern       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_:<>]*\s+\([A-Za-z_][A-Za-z0-9_:<>]*::\*\)\(\)\s*const`)
	cPlusPlusEmptyBraceDefaultPattern                      = regexp.MustCompile(`=\s*\{\}`)
	cPlusPlusTemplatePointerDefaultPattern                 = regexp.MustCompile(`>\s*\*\s*=\s*nullptr\s*>`)
	cPlusPlusEnableIfPointerDefaultPattern                 = regexp.MustCompile(`typename[ \t]+(?:[A-Za-z_][A-Za-z0-9_]*::)*[A-Za-z_]*enable_if[^;\n]*::type[ \t]*\*[ \t]*=`)
	cPlusPlusEnableIfTypePointerTemplateDefaultLinePattern = regexp.MustCompile(`::type[ \t]*\*[ \t]*=[ \t]*nullptr>`)
	cPlusPlusEnableIfTypePointerParamLinePattern           = regexp.MustCompile(`::type[ \t]*\*[ \t]*$`)
	cPlusPlusUnsignedCastPattern                           = regexp.MustCompile(`\bunsigned\(([^)\n]+)\)`)
	cPlusPlusAnonymousEnumPattern                          = regexp.MustCompile(`\benum\s*:\s*[A-Za-z_][A-Za-z0-9_:]*\b`)
	cPlusPlusExplicitOperatorCallPattern                   = regexp.MustCompile(`operator\(\)<[^>\n]+>\(`)
	cPlusPlusConversionOperatorDeclPattern                 = regexp.MustCompile(`operator\s+[A-Za-z_][A-Za-z0-9_:<>]*\s*\(\s*\)`)
	cPlusPlusStdIntegralConstantBoolExpressionLinePattern  = regexp.MustCompile(`^template<[^>]+>\s*struct\s+[A-Za-z_][A-Za-z0-9_]*\s*:\s*std::integral_constant\s*<\s*bool\s*,[^>]+>\s*\{\s*\};?$`)
	cPlusPlusBoolConstantCallPattern                       = regexp.MustCompile(`bool_constant<[A-Za-z_][A-Za-z0-9_:<>]*\(\)>`)
	cPlusPlusBoolConstantCallLinePattern                   = regexp.MustCompile(`^template<[^>]+>\s*struct\s+[A-Za-z_][A-Za-z0-9_]*\s*:\s*bool_constant<[A-Za-z_][A-Za-z0-9_:<>]*\(\)>\s*\{\s*\};?$`)
	cPlusPlusGTestPointerTemplateDefaultPattern            = regexp.MustCompile(`typename\s+R\s*,\s*R\s*\*\s*=\s*nullptr`)
	cPlusPlusMemberPointerDecltypePattern                  = regexp.MustCompile(`decltype\s*\(\s*\([^;\n]*->\*[^;\n]*\)\s*\(\s*\)\s*\)`)
	cPlusPlusDirectInitializerTernaryPattern               = regexp.MustCompile(`^\s*(?:const\s+)?[A-Za-z_][A-Za-z0-9_:<>]*\s*&\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^;\n]*\?\s*[^;\n]*:\s*$`)
	cPlusPlusNoArgGTestMacroLinePattern                    = regexp.MustCompile(`^GTEST_[A-Z0-9_]+_\(\)\s*;?\s*(?://.*)?$`)
)

func maskCPlusPlusAnnotationMacros(text string) string {
	return cPlusPlusAnnotationMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.Repeat(" ", len(match))
	})
}

func cPlusPlusBlankMacroLine(trimmed string) bool {
	switch {
	case strings.HasPrefix(trimmed, "MOCK_METHOD("):
		return true
	case strings.HasPrefix(trimmed, "GMOCK_DECLARE_KIND_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_COMPILE_ASSERT_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_DISALLOW_COPY_AND_ASSIGN_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_REPEATER_METHOD_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_REVERSE_REPEATER_METHOD_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_IMPL_FORMAT_C_STRING_AS_POINTER_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_IMPL_FORMAT_C_STRING_AS_STRING_("):
		return true
	case strings.HasPrefix(trimmed, "GTEST_IMPL_CMP_HELPER_("):
		return true
	case strings.HasPrefix(trimmed, "VISIT_TYPE("):
		return true
	case strings.HasPrefix(trimmed, "SPECIALIZE_MAKE_SIGNED("):
		return true
	case strings.HasPrefix(trimmed, "FMT_TYPE_CONSTANT("):
		return true
	case strings.HasPrefix(trimmed, "FMT_FORMAT_AS("):
		return true
	case strings.HasPrefix(trimmed, "NLOHMANN_DEFINE_"):
		return true
	case strings.HasPrefix(trimmed, "JSON_IMPLEMENT_OPERATOR("):
		return true
	case strings.HasPrefix(trimmed, "DOCTEST_"):
		return true
	case strings.HasPrefix(trimmed, "BENCHMARK_CAPTURE("):
		return true
	case strings.HasPrefix(trimmed, "SETUP_TESTCASES("):
		return true
	case cPlusPlusStatementMacroLinePattern.MatchString(trimmed):
		return true
	case cPlusPlusNoArgGTestMacroLinePattern.MatchString(trimmed):
		return true
	default:
		return false
	}
}

func cPlusPlusMultilineBlankMacroStart(trimmed string) bool {
	return strings.HasPrefix(trimmed, "NLOHMANN_JSON_SERIALIZE_ENUM") ||
		strings.HasPrefix(trimmed, "TEST_CASE_TEMPLATE_INVOKE") ||
		strings.HasPrefix(trimmed, "JSON_IMPLEMENT_OPERATOR") ||
		strings.HasPrefix(trimmed, "GTEST_DEFINE_") ||
		strings.HasPrefix(trimmed, "GMOCK_DEFINE_") ||
		strings.HasPrefix(trimmed, "GTEST_COMPILE_ASSERT_") ||
		cPlusPlusStatementMacroLinePattern.MatchString(trimmed)
}

func cPlusPlusMultilineAnnotationMacroStart(trimmed string) bool {
	return strings.HasPrefix(trimmed, "GTEST_INTERNAL_DEPRECATED(") ||
		(strings.HasPrefix(trimmed, "GTEST_ATTRIBUTE_") && strings.Contains(trimmed, "("))
}

func cPlusPlusNextNonEmptyLineStarts(lines []string, start int, prefix string) bool {
	for i := start; i < len(lines); i++ {
		text, _ := splitLineEnding(lines[i])
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		return strings.HasPrefix(trimmed, prefix)
	}
	return false
}

func cPlusPlusMultilineTestMacroStart(trimmed string) bool {
	return (strings.HasPrefix(trimmed, "TEST_CASE_TEMPLATE") && !strings.HasPrefix(trimmed, "TEST_CASE_TEMPLATE_INVOKE")) ||
		strings.HasPrefix(trimmed, "TEMPLATE_TEST_CASE")
}

func cPlusPlusMultilineControlMacroStart(trimmed string) bool {
	return strings.HasPrefix(trimmed, "SECTION(") ||
		strings.HasPrefix(trimmed, "SUBCASE(")
}

func maskCPlusPlusStreamingMacroContinuations(lines []string, i int) int {
	for i+1 < len(lines) {
		text, newline := splitLineEnding(lines[i+1])
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "<<") && !strings.HasPrefix(trimmed, ".") {
			return i
		}
		i++
		lines[i] = maskLineText(text) + newline
		if strings.Contains(text, ";") {
			return i
		}
	}
	return i
}

func maskCPlusPlusTestMacroDefinition(text string) (string, bool) {
	match := cPlusPlusTestMacroPattern.FindString(text)
	if match == "" {
		return "", false
	}
	open := strings.LastIndex(match, "(")
	if open < 0 {
		return "", false
	}
	end := balancedCallEnd(text, open)
	if end < 0 {
		return "", false
	}
	brace := strings.Index(text[end:], "{")
	if brace < 0 {
		return paddedReplacement(leadingWhitespace(text), "void test_macro();", len(text)), true
	}
	brace += end
	replacement := paddedReplacement(leadingWhitespace(text), "void test_macro() ", brace)
	return replacement + text[brace:], true
}

func maskCPlusPlusDoctestControlMacro(text string) (string, bool) {
	match := cPlusPlusDoctestControlMacroPattern.FindString(text)
	if match == "" {
		return "", false
	}
	brace := strings.Index(text[len(match):], "{")
	if brace < 0 {
		return paddedReplacement(leadingWhitespace(text), "if (true)", len(text)), true
	}
	brace += len(match)
	replacement := paddedReplacement(leadingWhitespace(text), "if (true) ", brace)
	return replacement + text[brace:], true
}

func maskCPlusPlusFunctionLikeMacro(text, name, replacement string) string {
	for {
		start := strings.Index(text, name+"(")
		if start < 0 {
			return text
		}
		end := balancedCallEnd(text, start+len(name))
		if end < 0 {
			return text
		}
		text = text[:start] + sameLengthReplacement(replacement, end-start) + text[end:]
	}
}

func maskCPlusPlusLikelyMacro(text, name string) string {
	for {
		start := strings.Index(text, name+"(")
		if start < 0 {
			return text
		}
		open := start + len(name)
		end := balancedCallEnd(text, open)
		if end < 0 {
			return text
		}
		inner := text[open+1 : end-1]
		text = text[:start] + sameLengthReplacement(inner, end-start) + text[end:]
	}
}

func maskCPlusPlusTrailingDecltypeReturn(text string) string {
	for {
		start := strings.Index(text, "-> decltype(")
		if start < 0 {
			return text
		}
		open := start + len("-> decltype")
		end := balancedCallEnd(text, open)
		if end < 0 {
			return text
		}
		text = text[:start] + sameLengthReplacement("-> void", end-start) + text[end:]
	}
}

func maskCPlusPlusTemplateDecltypeExpressions(text string) string {
	searchFrom := 0
	for {
		relativeStart := strings.Index(text[searchFrom:], "decltype(")
		if relativeStart < 0 {
			return text
		}
		start := searchFrom + relativeStart
		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		prefix := text[lineStart:start]
		if !cPlusPlusTemplateDecltypeContext(prefix) {
			searchFrom = start + len("decltype(")
			continue
		}
		open := start + len("decltype")
		end := balancedCallEnd(text, open)
		if end < 0 {
			return text
		}
		text = text[:start] + sameLengthReplacementPreserveNewlines("void", text[start:end]) + text[end:]
		searchFrom = start + len("void")
	}
}

func cPlusPlusTemplateDecltypeContext(prefix string) bool {
	return strings.Contains(prefix, "<") &&
		(strings.Contains(prefix, "void_t<") ||
			strings.Contains(prefix, ",") ||
			strings.Contains(prefix, "std::is_") ||
			strings.Contains(prefix, "struct ") ||
			strings.Contains(prefix, "using "))
}

func maskCPlusPlusCatchMacro(text, name string) (string, bool) {
	start := strings.Index(text, name)
	if start < 0 {
		return "", false
	}
	open := strings.Index(text[start+len(name):], "(")
	if open < 0 {
		return "", false
	}
	open += start + len(name)
	end := balancedCallEnd(text, open)
	if end < 0 {
		return "", false
	}
	return text[:start] + sameLengthReplacement("catch(...)", end-start) + text[end:], true
}

func maskCPlusPlusFunctionLikeMacroPattern(text string, pattern *regexp.Regexp, replacement string) string {
	for {
		location := pattern.FindStringIndex(text)
		if location == nil {
			return text
		}
		open := strings.LastIndex(text[location[0]:location[1]], "(")
		if open < 0 {
			return text
		}
		start := location[0]
		end := balancedCallEnd(text, location[0]+open)
		if end < 0 {
			return text
		}
		text = text[:start] + sameLengthReplacement(replacement, end-start) + text[end:]
	}
}

func maskCPlusPlusDependentTemplateKeyword(text string) string {
	return cPlusPlusDependentTemplatePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := cPlusPlusDependentTemplatePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return sameLengthReplacement(parts[1]+parts[2], len(match))
	})
}

func maskCPlusPlusMemberOperatorCall(text string) string {
	return cPlusPlusMemberOperatorPattern.ReplaceAllStringFunc(text, func(match string) string {
		if strings.HasPrefix(match, "->") {
			return sameLengthReplacement("->op", len(match))
		}
		return sameLengthReplacement(".op", len(match))
	})
}

func maskCPlusPlusOperatorCall(text string) string {
	return cPlusPlusOperatorCallPattern.ReplaceAllStringFunc(text, func(match string) string {
		if strings.HasPrefix(match, "::") {
			return sameLengthReplacement("::op(", len(match))
		}
		return sameLengthReplacement("op(", len(match))
	})
}

func maskCPlusPlusUnsignedFunctionalCast(text string) string {
	return cPlusPlusUnsignedCastPattern.ReplaceAllString(text, "uint32_t($1)")
}

func maskCPlusPlusEmptyDefaultInitializers(text string) string {
	text = replaceAllSameLength(text, "locale_ref loc = {}", "locale_ref loc = loc")
	text = replaceAllSameLength(text, "locale_ref = {}", "locale_ref = loc")
	text = replaceAllSameLength(text, "format_specs = {}", "format_specs = s")
	text = replaceAllSameLength(text, "const format_specs& specs = {}", "const format_specs& specs = s")
	text = replaceAllSameLength(text, "const format_specs& = {}", "const format_specs& = s")
	return text
}

func replaceAllSameLength(text, old, replacement string) string {
	return strings.ReplaceAll(text, old, sameLengthReplacement(replacement, len(old)))
}

func replacePatternSameLength(text string, pattern *regexp.Regexp, replacement string) string {
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		return sameLengthReplacement(replacement, len(match))
	})
}

func sameLengthReplacement(replacement string, length int) string {
	if len(replacement) >= length {
		return replacement[:length]
	}
	return replacement + strings.Repeat(" ", length-len(replacement))
}

func sameLengthReplacementPreserveNewlines(replacement, original string) string {
	var b strings.Builder
	b.Grow(len(original))
	replacementOffset := 0
	for i := 0; i < len(original); i++ {
		if original[i] == '\n' {
			b.WriteByte('\n')
			continue
		}
		if replacementOffset < len(replacement) {
			b.WriteByte(replacement[replacementOffset])
			replacementOffset++
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func balancedCallEnd(text string, open int) int {
	if open >= len(text) || text[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func leadingWhitespace(text string) string {
	return text[:len(text)-len(strings.TrimLeft(text, " \t"))]
}

func paddedReplacement(indent, replacement string, width int) string {
	out := indent + replacement
	if len(out) >= width {
		return out[:width]
	}
	return out + strings.Repeat(" ", width-len(out))
}

func typeScriptGenericCallSignatureStarts(trimmed string) bool {
	return trimmed == "<" || (strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, "("))
}

func typeScriptGenericCallSignatureReturnStarts(trimmed string) bool {
	return strings.HasPrefix(trimmed, "):") || strings.HasPrefix(trimmed, ") =>")
}

func typeScriptGenericCallSignatureEnds(trimmed string, inReturn bool) bool {
	if trimmed == "" {
		return true
	}
	if inReturn && trimmed == ">" {
		return true
	}
	if strings.HasSuffix(trimmed, "<") || strings.HasSuffix(trimmed, ",") {
		return false
	}
	if strings.HasPrefix(trimmed, "):") {
		return !strings.HasSuffix(trimmed, "(") && !strings.HasSuffix(trimmed, "<")
	}
	return strings.HasPrefix(trimmed, ") =>") && !strings.HasSuffix(trimmed, "<")
}

func maskLineText(text string) string {
	return strings.Repeat(" ", len(text))
}

func Supported(path string) bool {
	_, ok := languageForPath(path)
	return ok
}

func looksLikeFluxKustomizationManifest(content string) bool {
	return regexp.MustCompile(`(?m)^apiVersion:\s*kustomize\.toolkit\.fluxcd\.io/`).MatchString(content) &&
		regexp.MustCompile(`(?m)^kind:\s*Kustomization\s*$`).MatchString(content)
}

// objcSelectorName returns the first selector segment of an Objective-C
// method_definition. The grammar emits each selector segment as a bare
// identifier child (parameters live in method_parameter nodes), so the first
// direct identifier child is the method name — `startMonitoring` for a unary
// selector, `initWithBaseURL` for `initWithBaseURL:sessionConfiguration:`.
func objcSelectorName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if validNode(child) && child.Type() == "identifier" {
			return strings.TrimSpace(child.Content(src))
		}
	}
	return ""
}

// objcBareAuditMacroPattern matches file-scope nullability/sendability audit
// macros that expand to nothing statement-like (`NS_ASSUME_NONNULL_BEGIN`,
// `NS_HEADER_AUDIT_BEGIN(nullability, sendability)`); tree-sitter-objc has no
// production for a bare identifier at file scope, so an unmasked occurrence
// derails the parse of everything that follows (AFHTTPSessionManager.h lost
// its whole @interface to this).
var objcBareAuditMacroPattern = regexp.MustCompile(`^NS_(?:ASSUME_NONNULL_(?:BEGIN|END)|HEADER_AUDIT_(?:BEGIN|END)\s*\([^)]*\))$`)

// maskObjectiveCUnsupportedSyntax blanks file-scope audit macros (see
// objcBareAuditMacroPattern) with same-length whitespace so byte offsets and
// line numbers of the surviving code are unchanged.
func maskObjectiveCUnsupportedSyntax(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		if objcBareAuditMacroPattern.MatchString(strings.TrimSpace(text)) {
			lines[i] = maskLineText(text) + newline
		}
	}
	return strings.Join(lines, "")
}

func looksLikeObjectiveC(content string) bool {
	return regexp.MustCompile(`(?m)^\s*@(?:interface|implementation|protocol|class|end)\b`).MatchString(content) ||
		regexp.MustCompile(`(?m)^\s*#import\s+[<"]`).MatchString(content)
}

func looksLikeCPlusPlusHeader(content string) bool {
	return regexp.MustCompile(`(?m)^\s*(namespace|template\s*<|class\s+\w|struct\s+\w+\s*:|using\s+\w+\s*=|(?:inline\s+)?auto\s+\w+\s*\()`).MatchString(content) ||
		strings.Contains(content, "std::") ||
		strings.Contains(content, "extern \"C\"") ||
		strings.Contains(content, "::")
}

func languageForPath(path string) (languageSpec, bool) {
	base := strings.ToLower(filepath.Base(path))
	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
		return languageSpec{language: "Dockerfile"}, true
	}
	if base == "makefile" || strings.HasPrefix(base, "makefile.") || base == "gnumakefile" {
		return languageSpec{language: "Make"}, true
	}
	if base == "kustomization.yaml" || base == "kustomization.yml" || base == "kustomization" {
		return languageSpec{language: "Kustomize"}, true
	}
	if spec, ok := inventoryLanguageForSuffix(base); ok {
		return spec, true
	}
	ext := strings.ToLower(filepath.Ext(path))
	if spec, ok := treeSitterLanguages[ext]; ok {
		return spec, true
	}
	if spec, ok := inventoryLanguageExtensions[ext]; ok {
		return spec, true
	}
	if spec, ok := inventoryLanguageFilenames[base]; ok {
		return spec, true
	}
	return languageSpec{}, false
}

func inventoryLanguageForSuffix(base string) (languageSpec, bool) {
	var suffixes []string
	for suffix := range inventoryLanguageExtensions {
		if strings.Count(suffix, ".") > 1 {
			suffixes = append(suffixes, suffix)
		}
	}
	sort.Slice(suffixes, func(i, j int) bool {
		if len(suffixes[i]) == len(suffixes[j]) {
			return suffixes[i] < suffixes[j]
		}
		return len(suffixes[i]) > len(suffixes[j])
	})
	for _, suffix := range suffixes {
		if strings.HasSuffix(base, suffix) {
			return inventoryLanguageExtensions[suffix], true
		}
	}
	return languageSpec{}, false
}

func fallbackEntities(path, content, language string) []Entity {
	switch language {
	case "Dockerfile":
		return dockerfileEntities(content)
	case "Kustomize":
		return kustomizeEntities(content)
	case "JSON", "JSON5":
		return jsonLikeEntities(content)
	case "TOML":
		return tomlEntities(content)
	case "XML":
		return xmlEntities(content)
	case "Make":
		return makeEntities(content)
	case "Markdown":
		return markdownEntities(content)
	case "HTML":
		return htmlEntities(path, content)
	case "CSS":
		return cssEntities(content)
	case "GraphQL":
		entities := inventoryEntities(path, content, language)
		entities = append(entities, graphqlSchemaEntities(content)...)
		return entities
	case "Vue", "Svelte":
		return componentEntities(path, content, language)
	default:
		return inventoryEntities(path, content, language)
	}
}

func inventoryEntities(path, content, language string) []Entity {
	lines := strings.Split(content, "\n")
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base == "" {
		base = filepath.Base(path)
	}
	if base == "" {
		base = strings.ToLower(language)
	}
	kind := "document"
	signature := strings.ToLower(language) + " document " + base
	return []Entity{simpleFallbackEntity(kind, base, signature, 1, maxInt(1, len(lines)), content)}
}

func dockerfileEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	var entities []Entity
	stageIndex := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		stageIndex++
		name := fmt.Sprintf("stage%d", stageIndex)
		for i := 2; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i], "AS") {
				name = fields[i+1]
				break
			}
		}
		startLine := index + 1
		endLine := len(lines)
		for j := index + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if len(strings.Fields(next)) > 0 && strings.EqualFold(strings.Fields(next)[0], "FROM") {
				endLine = j
				break
			}
		}
		block := strings.Join(lines[startLine-1:endLine], "\n")
		entities = append(entities, Entity{
			Kind:        "stage",
			Name:        name,
			Signature:   normalize(trimmed),
			StartLine:   startLine,
			EndLine:     endLine,
			BodyHash:    hash(normalize(block)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: trimmed}, block))),
		})
	}
	return entities
}

func kustomizeEntities(content string) []Entity {
	return yamlKeyEntities(content, "kustomize")
}

func yamlKeyEntities(content, prefix string) []Entity {
	lines := strings.Split(content, "\n")
	keyRe := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):`)
	var entities []Entity
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		match := keyRe.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		name := match[1]
		entities = append(entities, simpleFallbackEntity("section", prefix+"/"+name, "section "+name, i+1, i+1, trimmed))
	}
	return entities
}

func jsonLikeEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	keyRe := regexp.MustCompile(`^\s*["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*:`)
	var entities []Entity
	seen := map[string]bool{}
	for i, line := range lines {
		match := keyRe.FindStringSubmatch(line)
		if match == nil || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		entities = append(entities, simpleFallbackEntity("section", match[1], "json key "+match[1], i+1, i+1, strings.TrimSpace(line)))
	}
	return entities
}

func tomlEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	sectionRe := regexp.MustCompile(`^\s*\[+\s*([A-Za-z0-9_.-]+)\s*\]+`)
	keyRe := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*=`)
	var entities []Entity
	seen := map[string]bool{}
	for i, line := range lines {
		name := ""
		kind := "section"
		if match := sectionRe.FindStringSubmatch(line); match != nil {
			name = match[1]
		} else if match := keyRe.FindStringSubmatch(line); match != nil {
			name = match[1]
			kind = "setting"
		}
		if name == "" || seen[kind+"\x00"+name] {
			continue
		}
		seen[kind+"\x00"+name] = true
		entities = append(entities, simpleFallbackEntity(kind, name, kind+" "+name, i+1, i+1, strings.TrimSpace(line)))
	}
	return entities
}

func xmlEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	tagRe := regexp.MustCompile(`<\s*([A-Za-z_][A-Za-z0-9_.:-]*)\b`)
	var entities []Entity
	seen := map[string]bool{}
	for i, line := range lines {
		match := tagRe.FindStringSubmatch(line)
		if match == nil || strings.HasPrefix(match[1], "?") || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		entities = append(entities, simpleFallbackEntity("element", match[1], "xml element "+match[1], i+1, i+1, strings.TrimSpace(line)))
	}
	return entities
}

func makeEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	targetRe := regexp.MustCompile(`^([A-Za-z0-9_.%/-]+)\s*:`)
	var entities []Entity
	for i, line := range lines {
		if strings.HasPrefix(line, "\t") || strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		match := targetRe.FindStringSubmatch(line)
		if match == nil || strings.Contains(match[1], "=") {
			continue
		}
		entities = append(entities, simpleFallbackEntity("target", match[1], "make target "+match[1], i+1, i+1, strings.TrimSpace(line)))
	}
	return entities
}

func markdownEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	headingRe := regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	fenceRe := regexp.MustCompile("^```\\s*([A-Za-z0-9_+-]*)")
	var entities []Entity
	fenceIndex := 0
	for i, line := range lines {
		if match := headingRe.FindStringSubmatch(line); match != nil {
			name := strings.TrimSpace(strings.Trim(match[2], "#"))
			entities = append(entities, simpleFallbackEntity("section", slugName(name), "markdown heading "+name, i+1, i+1, strings.TrimSpace(line)))
			continue
		}
		if match := fenceRe.FindStringSubmatch(line); match != nil {
			fenceIndex++
			lang := match[1]
			if lang == "" {
				lang = "text"
			}
			name := fmt.Sprintf("code_fence_%d_%s", fenceIndex, lang)
			entities = append(entities, simpleFallbackEntity("code_fence", name, "markdown code fence "+lang, i+1, i+1, strings.TrimSpace(line)))
		}
	}
	return entities
}

func htmlEntities(path, content string) []Entity {
	lines := strings.Split(content, "\n")
	idRe := regexp.MustCompile(`\bid\s*=\s*["']([^"']+)["']`)
	var entities []Entity
	seen := map[string]bool{}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entities = append(entities, simpleFallbackEntity("document", base, "html document "+base, 1, maxInt(1, len(lines)), base))
	for i, line := range lines {
		for _, match := range idRe.FindAllStringSubmatch(line, -1) {
			name := slugName(match[1])
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			entities = append(entities, simpleFallbackEntity("element", name, "html id "+match[1], i+1, i+1, strings.TrimSpace(line)))
		}
	}
	return entities
}

func cssEntities(content string) []Entity {
	lines := strings.Split(content, "\n")
	selectorRe := regexp.MustCompile(`^\s*([.#]?[A-Za-z_][A-Za-z0-9_-]*)\s*\{`)
	var entities []Entity
	for i, line := range lines {
		match := selectorRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(match[1], "."), "#")
		entities = append(entities, simpleFallbackEntity("selector", name, "css selector "+match[1], i+1, i+1, strings.TrimSpace(line)))
	}
	return entities
}

func componentEntities(path, content, language string) []Entity {
	lines := strings.Split(content, "\n")
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entities := []Entity{simpleFallbackEntity("component", base, language+" component "+base, 1, maxInt(1, len(lines)), base)}
	if language == "Vue" {
		entities = append(entities, htmlEntities(path, content)...)
	}
	return entities
}

var (
	cFamilyTypeLineRe     = regexp.MustCompile(`^\s*(?:typedef\s+)?(struct|union|enum|class)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	cFamilyTypedefNameRe  = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(?:\[[^\]]*\])?\s*$`)
	cFamilyFunctionNameRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\([^;{}]*\)\s*(?:[A-Za-z_][A-Za-z0-9_]*\s*)*$`)
)

var cFamilyNonFunctionNames = map[string]bool{
	"alignof": true,
	"catch":   true,
	"do":      true,
	"for":     true,
	"if":      true,
	"return":  true,
	"sizeof":  true,
	"switch":  true,
	"typeof":  true,
	"while":   true,
}

func cFamilyTypedefAliasName(node *sitter.Node, src []byte) string {
	text := strings.TrimSpace(stripCodeLiteralsAndComments(node.Content(src)))
	if !strings.HasPrefix(text, "typedef ") {
		return ""
	}
	text = strings.TrimSuffix(strings.TrimSpace(text), ";")
	match := cFamilyTypedefNameRe.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	name := match[1]
	if cFamilyNonFunctionNames[name] {
		return ""
	}
	return name
}

func fastCFamilyEntities(path, content, language string) []Entity {
	_ = path
	_ = language
	stripped := stripCodeLiteralsAndComments(content)
	lines := strings.Split(stripped, "\n")
	originalLines := strings.Split(content, "\n")
	var entities []Entity
	entities = append(entities, fastCFamilyTypeEntities(lines, originalLines)...)
	entities = append(entities, fastCFamilyFunctionEntities(lines, originalLines)...)
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].StartLine == entities[j].StartLine {
			if entities[i].Kind == entities[j].Kind {
				return entities[i].Name < entities[j].Name
			}
			return entities[i].Kind < entities[j].Kind
		}
		return entities[i].StartLine < entities[j].StartLine
	})
	return entities
}

func fastCFamilyTypeEntities(lines, originalLines []string) []Entity {
	var entities []Entity
	seen := map[string]bool{}
	depth := 0
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if depth != 0 || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			depth += braceDelta(lines[i])
			if depth < 0 {
				depth = 0
			}
			continue
		}
		for _, match := range cFamilyTypeLineRe.FindAllStringSubmatch(trimmed, -1) {
			kind := match[1]
			name := match[2]
			key := kind + "\x00" + name + "\x00" + fmt.Sprint(i+1)
			if seen[key] {
				continue
			}
			seen[key] = true
			end := fastCFamilyStatementEnd(lines, i)
			entities = append(entities, fastCFamilyEntity(kind, name, i+1, end, originalLines))
		}
		if !strings.HasPrefix(trimmed, "typedef ") {
			continue
		}
		end := fastCFamilyStatementEnd(lines, i)
		statement := strings.TrimSpace(strings.Join(lines[i:end], " "))
		statement = strings.TrimSuffix(statement, ";")
		if match := cFamilyTypedefNameRe.FindStringSubmatch(statement); match != nil {
			name := match[1]
			key := "type\x00" + name + "\x00" + fmt.Sprint(i+1)
			if seen[key] {
				continue
			}
			seen[key] = true
			entities = append(entities, fastCFamilyEntity("type", name, i+1, end, originalLines))
		}
		depth += braceDelta(lines[i])
		if depth < 0 {
			depth = 0
		}
	}
	return entities
}

func fastCFamilyFunctionEntities(lines, originalLines []string) []Entity {
	var entities []Entity
	depth := 0
	pendingStart := -1
	pending := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if depth != 0 {
			depth += braceDelta(line)
			if depth < 0 {
				depth = 0
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			pending = ""
			pendingStart = -1
			continue
		}
		if pendingStart < 0 {
			pendingStart = i
		}
		pending = strings.TrimSpace(pending + " " + trimmed)
		if brace := strings.IndexByte(line, '{'); brace >= 0 {
			signature := strings.TrimSpace(pending)
			if idx := strings.IndexByte(signature, '{'); idx >= 0 {
				signature = strings.TrimSpace(signature[:idx])
			} else if braceText := strings.TrimSpace(line[brace:]); braceText != "" {
				signature = strings.TrimSpace(strings.TrimSuffix(signature, braceText))
			}
			if name := fastCFamilyFunctionName(signature); name != "" {
				end := fastCFamilyBraceEnd(lines, i)
				entities = append(entities, fastCFamilyEntity("function", name, pendingStart+1, end, originalLines))
				i = end - 1
				pending = ""
				pendingStart = -1
				depth = 0
				continue
			}
			depth += braceDelta(line)
			pending = ""
			pendingStart = -1
			continue
		}
		if strings.Contains(line, ";") {
			pending = ""
			pendingStart = -1
		}
	}
	return entities
}

func fastCFamilyFunctionName(signature string) string {
	signature = strings.TrimSpace(signature)
	if signature == "" || strings.Contains(signature, "=") {
		return ""
	}
	match := cFamilyFunctionNameRe.FindStringSubmatch(signature)
	if match == nil {
		return ""
	}
	name := match[1]
	if cFamilyNonFunctionNames[name] {
		return ""
	}
	return name
}

func fastCFamilyStatementEnd(lines []string, start int) int {
	depth := 0
	for i := start; i < len(lines); i++ {
		depth += braceDelta(lines[i])
		if strings.Contains(lines[i], ";") && depth <= 0 {
			return i + 1
		}
		if depth <= 0 && strings.Contains(lines[i], "}") {
			return i + 1
		}
	}
	return start + 1
}

func fastCFamilyBraceEnd(lines []string, start int) int {
	depth := 0
	for i := start; i < len(lines); i++ {
		depth += braceDelta(lines[i])
		if depth <= 0 && i > start {
			return i + 1
		}
	}
	return len(lines)
}

func braceDelta(line string) int {
	delta := 0
	for _, ch := range line {
		switch ch {
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	return delta
}

func fastCFamilyEntity(kind, name string, startLine, endLine int, lines []string) Entity {
	signature := ""
	if startLine > 0 && startLine <= len(lines) {
		signature = strings.TrimSpace(lines[startLine-1])
	}
	if signature == "" {
		signature = kind + " " + name
	}
	return Entity{
		Kind:        kind,
		Name:        name,
		Signature:   normalize(signature),
		StartLine:   startLine,
		EndLine:     maxInt(startLine, endLine),
		BodyHash:    "",
		Fingerprint: "",
	}
}

func simpleFallbackEntity(kind, name, signature string, startLine, endLine int, block string) Entity {
	name = slugName(name)
	if name == "" {
		name = kind
	}
	return Entity{
		Kind:        kind,
		Name:        name,
		Signature:   normalize(signature),
		StartLine:   startLine,
		EndLine:     endLine,
		BodyHash:    hash(normalize(block)),
		Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
	}
}

func slugName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, `"'`)
	name = regexp.MustCompile(`[^A-Za-z0-9_.:/-]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	return name
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func walkEntities(node *sitter.Node, src []byte, language, scope string, entities *[]Entity) {
	walkEntitiesScoped(node, src, language, scope, false, entities)
}

// walkEntitiesScoped tracks whether the current node is inside a function body
// (inFunc), so a callable defined there is marked Entity.Local — a nested/closure
// def that call resolution must not name-match across scopes.
func walkEntitiesScoped(node *sitter.Node, src []byte, language, scope string, inFunc bool, entities *[]Entity) {
	if !validNode(node) {
		return
	}
	// Field/property declarations emit one entity per declared name and are not
	// descended into (their name nodes would otherwise look like field accesses).
	if fields, ok := fieldEntities(node, src, language, scope, inFunc); ok {
		*entities = append(*entities, fields...)
		return
	}
	entity, ok := entityFromNode(node, src, language, scope)
	childScope := scope
	childInFunc := inFunc
	if ok {
		if inFunc && (entity.Kind == "function" || entity.Kind == "method") {
			entity.Local = true // nested inside another function
		}
		*entities = append(*entities, entity)
		if scopesChildren(entity.Kind) {
			childScope = entity.Name
		}
		if entity.Kind == "function" || entity.Kind == "method" {
			childInFunc = true // descendants of this callable are function-local
		}
	}
	// A Rust `impl Foo {}` / `impl Trait for Foo {}` block is not a symbol itself,
	// but it scopes its functions to the implementing type: without this they'd be
	// bare top-level functions with no container, so self./typed-receiver call
	// resolution can't find them. Scope descendants to the concrete type.
	if node.Type() == "impl_item" {
		if t := rustImplTypeName(node, src); t != "" {
			childScope = t
		}
	}
	// A Swift `extension Foo { ... }` block is likewise not a symbol itself
	// (entityFromNode skips it), but it scopes its members to the extended type
	// so they emit as Foo.method and resolve against the primary declaration.
	if language == "Swift" && node.Type() == "class_declaration" && swiftExtensionDeclaration(node) {
		if t := swiftExtensionTypeName(node, src); t != "" {
			childScope = t
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		walkEntitiesScoped(node.NamedChild(i), src, language, childScope, childInFunc, entities)
	}
}

// swiftExtensionDeclaration reports whether a tree-sitter-swift
// class_declaration node is an `extension` block (the grammar reuses one node
// type for class/struct/enum/actor/extension; the introducing keyword is the
// first child).
func swiftExtensionDeclaration(node *sitter.Node) bool {
	if node.Type() != "class_declaration" {
		return false
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		switch node.Child(i).Type() {
		case "modifiers", "attribute":
			continue // `public extension Foo`, `@retroactive extension ...`
		case "extension":
			return true
		default:
			return false
		}
	}
	return false
}

// swiftExtensionTypeName returns the extended type of a Swift extension block
// (the user_type after the `extension` keyword; `extension Foo.Bar` keeps its
// dotted path). Generic arguments are cut so `extension Array<Element>` scopes
// to Array.
func swiftExtensionTypeName(node *sitter.Node, src []byte) string {
	t := firstNamedChildOfType(node, "user_type")
	if !validNode(t) {
		return ""
	}
	name := strings.TrimSpace(t.Content(src))
	if idx := strings.IndexByte(name, '<'); idx >= 0 {
		name = strings.TrimSpace(name[:idx])
	}
	return name
}

// zigContainerKind classifies a Zig variable_declaration whose value is a
// container literal (`const Name = struct/union/enum {...}`) into the symbol
// vocabulary: struct -> "struct", enum -> "enum", union -> "type" (unions have
// no dedicated kind). It returns "" for plain value bindings, which are not
// declarations of a named type.
func zigContainerKind(node *sitter.Node) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		switch node.NamedChild(i).Type() {
		case "struct_declaration":
			return "struct"
		case "enum_declaration":
			return "enum"
		case "union_declaration":
			return "type"
		}
	}
	return ""
}

// rustImplTypeName returns the implementing type of a Rust impl block (the `type`
// field; for `impl Trait for Foo` that is Foo), so methods inside scope under it.
func rustImplTypeName(node *sitter.Node, src []byte) string {
	t := node.ChildByFieldName("type")
	if !validNode(t) {
		return ""
	}
	if t.Type() == "type_identifier" {
		return strings.TrimSpace(t.Content(src))
	}
	if id := firstDescendantOfType(t, "type_identifier"); validNode(id) {
		return strings.TrimSpace(id.Content(src))
	}
	return ""
}

// haskellBindingName returns the LHS name of a Haskell `function`, `bind`, or
// `signature` node (the `name` field: `f` in `f x = …` / `f :: …`). It returns
// "" for shapes without a single LHS name — pattern binds, infix operator
// definitions, multi-name signatures (`f, g :: …`), and the `function` nodes
// that are type arrows (`Int -> Int`) nested inside signatures.
func haskellBindingName(node *sitter.Node, src []byte) string {
	nameNode := node.ChildByFieldName("name")
	if !validNode(nameNode) {
		return ""
	}
	return strings.TrimSpace(nameNode.Content(src))
}

// haskellPrevDeclSibling / haskellNextDeclSibling step over trivia (comments,
// haddock, pragmas, CPP lines like `#ifdef`) so that equation deduplication and
// signature/binding pairing still see adjacent declarations when trivia sits
// between them.
func haskellPrevDeclSibling(node *sitter.Node) *sitter.Node {
	prev := node.PrevNamedSibling()
	for validNode(prev) && haskellTriviaNode(prev.Type()) {
		prev = prev.PrevNamedSibling()
	}
	return prev
}

func haskellNextDeclSibling(node *sitter.Node) *sitter.Node {
	next := node.NextNamedSibling()
	for validNode(next) && haskellTriviaNode(next.Type()) {
		next = next.NextNamedSibling()
	}
	return next
}

func haskellTriviaNode(nodeType string) bool {
	switch nodeType {
	case "comment", "haddock", "pragma", "cpp":
		return true
	default:
		return false
	}
}

// fsharpModuleName returns the (possibly dotted) name of an F# module
// declaration. A file-heading `named_module` carries the full long_identifier
// in its name field (`module Paket.UpdateProcess`); a nested `module_defn`
// names itself with a plain identifier child after optional attributes.
func fsharpModuleName(node *sitter.Node, src []byte) string {
	if name := node.ChildByFieldName("name"); validNode(name) {
		return strings.TrimSpace(name.Content(src))
	}
	if id := firstNamedChildOfType(node, "identifier"); validNode(id) {
		return strings.TrimSpace(id.Content(src))
	}
	return ""
}

// fsharpTypeName returns the declared name of an F# type_definition, which
// lives on the type_name node of the concrete *_type_defn child (record,
// union, anon/class, ...).
func fsharpTypeName(node *sitter.Node, src []byte) string {
	if tn := firstDescendantOfType(node, "type_name"); validNode(tn) {
		return firstNameDescendant(tn, src)
	}
	return ""
}

// fsharpLetBinding classifies an F# `let` binding (function_or_value_defn).
// A binding with a parameter list (function_declaration_left) is a function,
// as is a bare binding whose body is a `fun`/`function` expression. Any other
// value binding is a variable, emitted only outside function and member
// bodies so the pervasive function-local `let x = ...` bindings do not
// surface as module-level variable symbols (function-local functions still
// surface and are marked Local by the walker).
func fsharpLetBinding(node *sitter.Node, src []byte) (string, string) {
	if left := firstNamedChildOfType(node, "function_declaration_left"); validNode(left) {
		if id := firstNamedChildOfType(left, "identifier"); validNode(id) {
			return "function", strings.TrimSpace(id.Content(src))
		}
		return "", ""
	}
	left := firstNamedChildOfType(node, "value_declaration_left")
	if !validNode(left) {
		return "", ""
	}
	// The declared name lives in the identifier_pattern; descending blindly
	// would latch onto a leading attribute (`let [<Literal>] Name = ...`
	// parses as attribute_pattern with the attributes first).
	var name string
	if pattern := firstDescendantOfType(left, "identifier_pattern"); validNode(pattern) {
		name = firstNameDescendant(pattern, src)
	}
	if body := node.ChildByFieldName("body"); validNode(body) {
		switch body.Type() {
		case "fun_expression", "function_expression":
			return "function", name
		}
	}
	if fsharpInsideCallable(node) {
		return "", ""
	}
	return "variable", name
}

// fsharpInsideCallable reports whether node is nested inside another F# let
// binding or member body, i.e. it is a local binding rather than a module- or
// type-level declaration.
func fsharpInsideCallable(node *sitter.Node) bool {
	for parent := node.Parent(); validNode(parent); parent = parent.Parent() {
		switch parent.Type() {
		case "function_or_value_defn", "member_defn":
			return true
		}
	}
	return false
}

// fsharpMemberName returns the declared name of an F# member_defn. Instance
// members parse as method_or_prop_defn whose property_or_ident name node
// holds the member name in its `method` field (the `instance` field is the
// self identifier, e.g. `this`); static members and `member val` properties
// carry a bare identifier; abstract members parse as member_signature.
func fsharpMemberName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "method_or_prop_defn":
			if name := child.ChildByFieldName("name"); validNode(name) {
				return fsharpPropertyOrIdentName(name, src)
			}
		case "property_or_ident":
			return fsharpPropertyOrIdentName(child, src)
		case "member_signature":
			return firstNameDescendant(child, src)
		}
	}
	return ""
}

func fsharpPropertyOrIdentName(node *sitter.Node, src []byte) string {
	if m := node.ChildByFieldName("method"); validNode(m) {
		return strings.TrimSpace(m.Content(src))
	}
	return firstNameDescendant(node, src)
}

// fsharpSignature returns a bounded declaration header for F# node types whose
// tree keeps the body as unlabeled sibling children (modules, members), where
// the generic body-field cut cannot apply.
func fsharpSignature(node *sitter.Node, src []byte) string {
	start := node.StartByte()
	var end uint32
	switch node.Type() {
	case "named_module":
		if name := node.ChildByFieldName("name"); validNode(name) {
			end = name.EndByte()
		}
	case "module_defn":
		if id := firstNamedChildOfType(node, "identifier"); validNode(id) {
			end = id.EndByte()
		}
	case "member_defn":
		// Cut before the trailing body expression (the last named child of the
		// method_or_prop_defn), keeping `member this.Name args`.
		if defn := firstNamedChildOfType(node, "method_or_prop_defn"); validNode(defn) {
			if n := int(defn.NamedChildCount()); n >= 2 {
				if body := defn.NamedChild(n - 1); validNode(body) {
					end = body.StartByte()
				}
			}
		}
	case "type_definition":
		// Cut before the type body (the concrete *_type_defn child's first
		// `block` field: record fields, union cases, or member elements),
		// keeping attributes, name, and primary constructor arguments.
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if !validNode(child) || !strings.HasSuffix(child.Type(), "_type_defn") {
				continue
			}
			if block := child.ChildByFieldName("block"); validNode(block) {
				end = block.StartByte()
			}
			break
		}
	}
	if end <= start || int(end) > len(src) {
		return ""
	}
	signature := strings.Join(strings.Fields(string(src[start:end])), " ")
	return strings.TrimSpace(strings.TrimRight(signature, "=: \t\r\n"))
}

// fieldEntities extracts struct/class field declarations as field symbols, one
// per declared name, qualified under the containing type's scope. It returns
// false for non-field nodes and for declarations outside a container (so local
// variables and parameters are never treated as fields). This pass handles Go
// struct fields (field_declaration -> field_identifier); TypeScript/Java/C#
// fields are added later.
func fieldEntities(node *sitter.Node, src []byte, language, scope string, inFunc bool) ([]Entity, bool) {
	if scope == "" {
		return nil, false
	}
	if node.Type() == "field_declaration" && (language == "C" || language == "C++") {
		return nil, false
	}
	switch node.Type() {
	case "field_declaration", // Go/Rust/Java/C#/C/C++ struct & class fields
		"public_field_definition", "field_definition", // TS/JS class fields
		"property_signature",   // TS interface/type-literal fields
		"property_declaration": // C# properties and Kotlin class-body properties (mapped to the canonical field kind)
	default:
		return nil, false
	}
	// tree-sitter-kotlin also emits property_declaration for locals inside
	// function bodies (and for members of object literals declared there); only
	// declarations directly inside a class/interface body, outside any function,
	// are members (evidence: ktor's `public val application: Application` on the
	// ApplicationCall interface, which typed-property receiver resolution needs).
	if language == "Kotlin" {
		parent := node.Parent()
		if inFunc || !validNode(parent) || parent.Type() != "class_body" {
			return nil, false
		}
	}
	// A TS/JS class field initialised with a function value
	// (`method = (x) => …` / `static create = function () {…}`) is a callable
	// member, not data: it is called like a method and must be a call target and
	// part of the method inventory. Classify it as a method, named like one.
	switch node.Type() {
	case "public_field_definition", "field_definition":
		if functionLikeValue(node.ChildByFieldName("value")) {
			if names := fieldDeclNames(node, src); len(names) == 1 {
				return []Entity{{
					Kind:        "method",
					Name:        qualify(scope, names[0]),
					Signature:   signatureFromNode(node, src),
					StartLine:   int(node.StartPoint().Row) + 1,
					EndLine:     int(node.EndPoint().Row) + 1,
					BodyHash:    hash(normalize(node.Content(src))),
					Fingerprint: hash(normalize(signatureFromNode(node, src))),
				}}, true
			}
		}
	}
	typeText := fieldTypeText(node, src)
	names := fieldDeclNames(node, src)
	if len(names) == 0 {
		// Embedded field (e.g. Go `io.Reader`) or an unsupported shape; the
		// declaration extractor does not synthesize a name for these.
		return nil, false
	}
	start := int(node.StartPoint().Row) + 1
	end := int(node.EndPoint().Row) + 1
	out := make([]Entity, 0, len(names))
	for _, name := range names {
		signature := name
		if typeText != "" {
			signature = name + " " + typeText
		}
		out = append(out, Entity{
			Kind:        "field",
			Name:        qualify(scope, name),
			Signature:   signature,
			StartLine:   start,
			EndLine:     end,
			BodyHash:    hash(typeText),
			Fingerprint: hash(normalize(signature)),
		})
	}
	return out, true
}

// fieldDeclNames extracts the declared member names from a field/property node
// across languages: field_identifier (Go/Rust/C++), variable_declarator (Java)
// or variable_declaration>variable_declarator (C#), and property_identifier /
// name field (TypeScript, C# properties).
func fieldDeclNames(node *sitter.Node, src []byte) []string {
	switch node.Type() {
	case "public_field_definition", "field_definition", "property_signature", "property_declaration":
		if name := node.ChildByFieldName("name"); validNode(name) {
			return []string{name.Content(src)}
		}
		if name := firstChildOfType(node, src, "property_identifier", "field_identifier"); name != "" {
			return []string{name}
		}
		// Kotlin property_declaration: the name is the simple_identifier of the
		// variable_declaration child (`val listener: Listener` / `var x = ...`).
		if decl := firstNamedChildOfType(node, "variable_declaration"); validNode(decl) {
			if name := firstChildOfType(decl, src, "simple_identifier"); name != "" {
				return []string{name}
			}
		}
		return nil
	}
	// field_declaration: collect every declared name.
	var names []string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "field_identifier":
			names = append(names, child.Content(src))
		case "variable_declarator":
			if name := variableDeclaratorName(child, src); name != "" {
				names = append(names, name)
			}
		case "variable_declaration": // C# wraps declarators in variable_declaration
			for j := 0; j < int(child.NamedChildCount()); j++ {
				if decl := child.NamedChild(j); decl.Type() == "variable_declarator" {
					if name := variableDeclaratorName(decl, src); name != "" {
						names = append(names, name)
					}
				}
			}
		}
	}
	return names
}

func variableDeclaratorName(node *sitter.Node, src []byte) string {
	if name := node.ChildByFieldName("name"); validNode(name) {
		return name.Content(src)
	}
	return firstChildOfType(node, src, "identifier", "field_identifier")
}

func firstChildOfType(node *sitter.Node, src []byte, types ...string) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		for _, t := range types {
			if child.Type() == t {
				return child.Content(src)
			}
		}
	}
	return ""
}

// fieldTypeText returns the field's type text when the grammar exposes it,
// best-effort (signature/type text is optional metadata).
func fieldTypeText(node *sitter.Node, src []byte) string {
	if typeNode := node.ChildByFieldName("type"); validNode(typeNode) {
		return strings.TrimSpace(typeNode.Content(src))
	}
	// C# field_declaration nests the type under variable_declaration; Kotlin
	// property_declaration nests the declared type there too, as the sibling of
	// the name (`val listener: Listener` -> variable_declaration
	// [simple_identifier, user_type]).
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if child := node.NamedChild(i); child.Type() == "variable_declaration" {
			if typeNode := child.ChildByFieldName("type"); validNode(typeNode) {
				return strings.TrimSpace(typeNode.Content(src))
			}
			for j := 0; j < int(child.NamedChildCount()); j++ {
				switch typeNode := child.NamedChild(j); typeNode.Type() {
				case "user_type", "nullable_type":
					return strings.TrimSpace(typeNode.Content(src))
				}
			}
		}
	}
	// tree-sitter-swift property_declaration carries the declared type in a
	// type_annotation child (`var fileio: FileIO { ... }`); its named child is
	// the type node. Without this, Swift field symbols carry no type text, so
	// property-chain receiver typing has no cross-file source. No other
	// routed grammar puts a type_annotation directly under a field node, so
	// the fallback stays inert elsewhere.
	if ann := firstNamedChildOfType(node, "type_annotation"); validNode(ann) && ann.NamedChildCount() > 0 {
		return strings.TrimSpace(ann.NamedChild(0).Content(src))
	}
	return ""
}

var kotlinClassDeclarationRe = regexp.MustCompile(`(?m)\b(?:data\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
var kotlinPrimaryConstructorPropertyRe = regexp.MustCompile(`\b(?:val|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([^=]+)`)

func kotlinPrimaryConstructorFieldEntities(content string) []Entity {
	var entities []Entity
	seen := map[string]bool{}
	for _, loc := range kotlinClassDeclarationRe.FindAllStringSubmatchIndex(content, -1) {
		if len(loc) < 4 {
			continue
		}
		className := content[loc[2]:loc[3]]
		open := strings.LastIndex(content[loc[0]:loc[1]], "(")
		if open < 0 {
			continue
		}
		open += loc[0]
		close := matchingDelimiterOffset(content, open, '(', ')')
		if close < 0 {
			continue
		}
		params := content[open+1 : close]
		for _, param := range splitTopLevelCommaSpans(params) {
			text := strings.TrimSpace(param.Text)
			match := kotlinPrimaryConstructorPropertyRe.FindStringSubmatch(text)
			if match == nil {
				continue
			}
			name := match[1]
			typeText := strings.TrimSpace(match[2])
			if idx := strings.Index(typeText, "="); idx >= 0 {
				typeText = strings.TrimSpace(typeText[:idx])
			}
			typeText = strings.TrimSpace(strings.TrimRight(typeText, ","))
			qualifiedName := qualify(className, name)
			if seen[qualifiedName] {
				continue
			}
			seen[qualifiedName] = true
			start := open + 1 + param.Start
			end := open + 1 + param.End
			signature := name
			if typeText != "" {
				signature = name + " " + typeText
			}
			block := content[start:end]
			entities = append(entities, Entity{
				Kind:        "field",
				Name:        qualifiedName,
				Signature:   signature,
				StartLine:   countLinesBefore(content, start) + 1,
				EndLine:     countLinesBefore(content, end) + 1,
				BodyHash:    hash(normalize(block)),
				Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: qualifiedName, Signature: signature}, block))),
			})
		}
	}
	return entities
}

type commaSpan struct {
	Text       string
	Start, End int
}

func splitTopLevelCommaSpans(value string) []commaSpan {
	var spans []commaSpan
	start := 0
	depth := 0
	inString := byte(0)
	escaped := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				spans = append(spans, commaSpan{Text: value[start:i], Start: start, End: i})
				start = i + 1
			}
		}
	}
	if start <= len(value) {
		spans = append(spans, commaSpan{Text: value[start:], Start: start, End: len(value)})
	}
	return spans
}

// pythonOverloadStub reports whether a Python function_definition is decorated
// with @typing.overload (in any alias: `@overload`, `@t.overload`,
// `@typing.overload`). tree-sitter-python nests a decorated function under a
// `decorated_definition` whose `decorator` children precede the definition.
func pythonOverloadStub(node *sitter.Node, src []byte) bool {
	parent := node.Parent()
	if !validNode(parent) || parent.Type() != "decorated_definition" {
		return false
	}
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		c := parent.NamedChild(i)
		if !validNode(c) || c.Type() != "decorator" {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Content(src)), "@"))
		if idx := strings.IndexAny(name, "(\t "); idx >= 0 {
			name = name[:idx] // drop any call args, e.g. @overload(...)
		}
		if dot := strings.LastIndex(name, "."); dot >= 0 {
			name = name[dot+1:] // last dotted component: t.overload -> overload
		}
		if name == "overload" {
			return true
		}
	}
	return false
}

func entityFromNode(node *sitter.Node, src []byte, language, scope string) (Entity, bool) {
	var kind string
	var name string
	switch node.Type() {
	case "class", "class_definition", "class_declaration", "class_specifier", "mixin_declaration",
		"abstract_class_declaration":
		// tree-sitter-typescript emits a distinct node type for `abstract class X`.
		// Without this case the class symbol is dropped and — because scope flows
		// from the class kind — its methods are never qualified under it (empty
		// container_id), so this/self call resolution can't fire for them.
		//
		// tree-sitter-objc reuses `class_declaration` for `@class Foo;` forward
		// declarations (headers are full of them); the class symbol comes from
		// class_interface / class_implementation, so forward decls are skipped.
		if language == "Objective-C" && node.Type() == "class_declaration" {
			return Entity{}, false
		}
		// tree-sitter-swift reuses `class_declaration` for `extension Foo { ... }`
		// blocks. An extension declares no new type — emitting it produced a
		// duplicate class symbol per extension (same name as the primary
		// struct/class), which forced #sig-suffixed IDs, pointed members'
		// container_id at the extension instead of the real type, and broke the
		// "globally unique name" gate in call resolution for every extended type.
		// The extension is a pure scope: walkEntitiesScoped qualifies its members
		// under the extended type name (see the swiftExtensionTypeName hook).
		if language == "Swift" && swiftExtensionDeclaration(node) {
			return Entity{}, false
		}
		kind = "class"
		name = nodeName(node, src)
	case "method_signature", "getter_signature", "setter_signature":
		// Dart class members (declaration head; body is a sibling node). Gated to
		// Dart because `method_signature` also denotes TypeScript interface
		// members, where extracting them as methods would change TS behavior.
		if language != "Dart" {
			return Entity{}, false
		}
		// A method_signature wrapping a function/getter/setter signature is just
		// packaging: the walk extracts the inner signature (whose name node is the
		// member name), while nodeName on the wrapper picks the first identifier —
		// the return type (`Future<Response> head(...)` -> a bogus "Future" method).
		if node.Type() == "method_signature" && validNode(dartInnerSignature(node)) {
			return Entity{}, false
		}
		kind = "method"
		name = nodeName(node, src)
		if scope != "" {
			name = qualify(scope, name)
		}
	case "function_signature":
		// Dart top-level / local function declaration head. Gated to Dart because
		// `function_signature` also denotes TypeScript ambient declarations.
		if language != "Dart" {
			return Entity{}, false
		}
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "subroutine_declaration_statement":
		// Perl `sub name { ... }` (name field is a bareword). Gated to Perl so
		// the generic extraction of other languages is unchanged. Perl subs are
		// plain functions regardless of the enclosing package (packages are
		// namespaces, not classes), so the kind stays "function"; a sub inside a
		// `package Name { ... }` block is qualified under that package's scope.
		if language != "Perl" {
			return Entity{}, false
		}
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			name = qualify(scope, name)
		}
	case "package_statement":
		// Perl `package Name;` / `package Name { ... }` namespace declaration
		// (name field is a package node such as Mojo::Util).
		if language != "Perl" {
			return Entity{}, false
		}
		kind = "module"
		name = nodeName(node, src)
	case "function", "bind":
		// Haskell function equations (`f x = …`) and simple variable bindings
		// (`f = …`). Gated to Haskell because the node types are generic words
		// other grammars could reuse. A multi-equation definition parses as one
		// `function` node per equation; only the first consecutive equation of a
		// name emits, so each top-level name yields a single symbol.
		if language != "Haskell" {
			return Entity{}, false
		}
		name = haskellBindingName(node, src)
		if name == "" {
			// Pattern bindings (`(a, b) = …`) have no single LHS name.
			return Entity{}, false
		}
		if prev := haskellPrevDeclSibling(node); validNode(prev) &&
			(prev.Type() == "function" || prev.Type() == "bind") &&
			haskellBindingName(prev, src) == name {
			return Entity{}, false // later equation of the same binding
		}
		kind = "function"
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "signature":
		// Haskell type signature (`f :: …`). It only carries the symbol when no
		// binding follows (e.g. a class method without a default implementation);
		// otherwise the binding right after it emits, avoiding duplicates. Gated
		// to Haskell: `function` type arrows nest inside signatures too, but they
		// have no name field so haskellBindingName rejects them above.
		if language != "Haskell" {
			return Entity{}, false
		}
		name = haskellBindingName(node, src)
		if name == "" {
			return Entity{}, false
		}
		if next := haskellNextDeclSibling(node); validNode(next) &&
			(next.Type() == "function" || next.Type() == "bind") &&
			haskellBindingName(next, src) == name {
			return Entity{}, false // the binding itself emits the symbol
		}
		kind = "function"
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "data_type", "newtype", "type_synomym":
		// Haskell `data`/`newtype`/`type` declarations ("type_synomym" is the
		// grammar's spelling). Gated to Haskell so the generic node names cannot
		// leak into other grammars.
		if language != "Haskell" {
			return Entity{}, false
		}
		kind = "type"
		name = nodeName(node, src)
	case "fun_decl":
		// Erlang function definition form (`name(Args) -> Body.`). Gated to
		// Erlang and handled by a dedicated builder because tree-sitter-erlang
		// parses each clause of a multi-clause function as its own fun_decl form,
		// so consecutive same-name/arity clauses fold into one function symbol.
		if language != "Erlang" {
			return Entity{}, false
		}
		return erlangFunDeclEntity(node, src)
	case "module_attribute":
		// Erlang `-module(name).` attribute names the compilation unit.
		if language != "Erlang" {
			return Entity{}, false
		}
		kind = "module"
		name = nodeName(node, src)
	case "record_decl":
		// Erlang `-record(name, {...}).` — the language's struct-like form.
		if language != "Erlang" {
			return Entity{}, false
		}
		kind = "struct"
		name = nodeName(node, src)
	case "named_module", "module_defn":
		// F# module declarations: `module A.B.C` heading a file (named_module) or
		// a nested `module Name =` block (module_defn). Gated to F# per the
		// language-promotion pattern so no other grammar's node types shift.
		if language != "F#" {
			return Entity{}, false
		}
		kind = "module"
		name = fsharpModuleName(node, src)
	case "function_or_value_defn":
		// F# `let` binding: a binding with a parameter list is a function; a bare
		// value binding is a variable (only surfaced outside function bodies, so
		// the pervasive function-local `let x = ...` stays out of the symbol set).
		if language != "F#" {
			return Entity{}, false
		}
		kind, name = fsharpLetBinding(node, src)
		if kind == "" {
			return Entity{}, false
		}
	case "member_defn":
		// F# type member (`member this.Name ...`, `static member`, `member val`,
		// `abstract member`), qualified under the enclosing type via scope.
		if language != "F#" {
			return Entity{}, false
		}
		kind = "method"
		name = fsharpMemberName(node, src)
		if scope != "" {
			name = qualify(scope, name)
		}
	case "module_definition":
		kind = "module"
		name = nodeName(node, src)
	case "module":
		// Ruby `module Name ... end` (tree-sitter-ruby's node type; the name
		// field is a constant). Modules are namespaces/mixins on par with
		// classes, and "module" scopes children, so nested methods qualify under
		// the module name. Gated to Ruby because the bare node type is a word
		// other grammars could reuse.
		if language != "Ruby" {
			return Entity{}, false
		}
		kind = "module"
		name = nodeName(node, src)
	case "binary_operator":
		// R defines functions by assignment: `name <- function(args) ...` is a
		// binary_operator whose value side is a function_definition. Gated to R
		// so languages whose grammars also emit binary_operator nodes are
		// unchanged.
		if language != "R" {
			return Entity{}, false
		}
		var ok bool
		kind, name, ok = rAssignmentEntity(node, src, scope)
		if !ok {
			return Entity{}, false
		}
	case "function_definition":
		// In R, function_definition is anonymous — the name lives on the
		// enclosing assignment (the binary_operator case above); extracting
		// here would invent a name from the first parameter.
		if language == "R" {
			return Entity{}, false
		}
		// Julia long-form `function name(args) ... end`. The name needs the
		// signature walk instead of generic nodeName: a qualified extension
		// method (`function Base.show(io, x)`) must keep its dotted path (the
		// generic descent would stop at "Base"), and a callable-object
		// definition (`function (obj::T)(x)`) binds no name at all.
		if language == "Julia" {
			kind = "function"
			name = juliaDefinitionName(firstNamedChildOfType(node, "signature"), src)
			if name == "" {
				return Entity{}, false
			}
			if scope != "" && !strings.Contains(name, ".") {
				kind = "method"
				name = qualify(scope, name)
			}
			break
		}
		// A @typing.overload-decorated def is a type-only stub, not a real
		// definition (replaced at runtime by the implementation of the same
		// name), so it must not be emitted as its own symbol — the impl carries
		// the symbol.
		if pythonOverloadStub(node, src) {
			return Entity{}, false
		}
		kind = "function"
		name = nodeName(node, src)
		if language == "Objective-C" || language == "C" {
			// A C function routinely returns a typedef'd type
			// (`CURLcode curl_easy_perform(...)`, `static NSString * Escape(...)`
			// in a .m file), whose type_identifier is the first name node in
			// pre-order, so nodeName would misname the function after its
			// return type. Take the identifier from the declarator field
			// instead. Gated to C and Objective-C so C++ extraction (qualified
			// names, destructors) is unchanged.
			if declarator := node.ChildByFieldName("declarator"); validNode(declarator) {
				if id := firstDescendantOfType(declarator, "identifier"); validNode(id) {
					name = strings.TrimSpace(id.Content(src))
				}
			}
		}
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "macro_definition":
		// Julia `macro name(args) ... end`. The kind vocabulary has no macro
		// kind, so macros join the callable inventory as functions. Gated to
		// Julia because tree-sitter-rust also emits `macro_definition` (for
		// `macro_rules!`), which stays unextracted.
		if language != "Julia" {
			return Entity{}, false
		}
		kind = "function"
		name = juliaDefinitionName(firstNamedChildOfType(node, "signature"), src)
		if name == "" {
			return Entity{}, false
		}
		if scope != "" && !strings.Contains(name, ".") {
			kind = "method"
			name = qualify(scope, name)
		}
	case "struct_definition":
		// Julia `struct Name ... end` / `mutable struct Name ... end`. The name
		// is the first identifier of the type head, past type parameters and the
		// `<:` supertype clause. Gated to Julia so the node type cannot leak
		// into other grammars.
		if language != "Julia" {
			return Entity{}, false
		}
		kind = "struct"
		name = nodeName(node, src)
	case "abstract_definition", "primitive_definition":
		// Julia `abstract type Name end` / `primitive type Name N end`.
		if language != "Julia" {
			return Entity{}, false
		}
		kind = "type"
		name = nodeName(node, src)
	case "assignment":
		// Julia short-form function definition `name(args) = expr` — an
		// assignment whose left-hand side is a call. Plain assignments
		// (variable bindings, tuple destructuring, indexed stores) bind no
		// callable and stay unextracted, as do assignment nodes in every other
		// language.
		if language != "Julia" {
			return Entity{}, false
		}
		name = juliaDefinitionName(node.NamedChild(0), src)
		if name == "" {
			return Entity{}, false
		}
		kind = "function"
		if scope != "" && !strings.Contains(name, ".") {
			kind = "method"
			name = qualify(scope, name)
		}
	case "function_declaration", "function_item":
		kind = "function"
		name = nodeName(node, src)
		if language == "Kotlin" {
			// tree-sitter-kotlin has no name field on function_declaration, and on
			// an extension function (`fun Call<T>.awaitResponse()`) the receiver
			// type precedes the name, so nodeName's pre-order descent latches onto
			// the receiver's type_identifier ("Call") instead of the function
			// name. The function's own name is always the direct simple_identifier
			// child (the receiver's identifiers sit inside a user_type subtree).
			if id := firstNamedChildOfType(node, "simple_identifier"); validNode(id) {
				name = strings.TrimSpace(id.Content(src))
			}
		}
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "function_signature_item":
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "method_declaration":
		// An Objective-C method_declaration is a prototype in an @interface /
		// category head; the @implementation's method_definition carries the
		// symbol. It also has no name field, so nodeName's descent would latch
		// onto the return type instead of the selector.
		if language == "Objective-C" {
			return Entity{}, false
		}
		kind = "method"
		name = nodeName(node, src)
		if receiver := goReceiverName(node, src); receiver != "" {
			name = qualify(receiver, name)
		} else if scope != "" {
			name = qualify(scope, name)
		}
	case "method_definition":
		kind = "method"
		if language == "Objective-C" {
			// tree-sitter-objc has no name field on method_definition; the
			// selector's segments are bare identifier children following the
			// return method_type, so nodeName's pre-order descent would return
			// the return type. Use the first selector segment (colon-free),
			// e.g. `initWithBaseURL:sessionConfiguration:` -> initWithBaseURL.
			name = objcSelectorName(node, src)
		} else {
			name = nodeName(node, src)
		}
		if scope != "" {
			name = qualify(scope, name)
		}
	case "class_interface", "class_implementation":
		// Objective-C @interface / @implementation (node types unique to
		// tree-sitter-objc). Both declare the class; the first identifier child
		// is the class name (a category `@interface Foo (Bar)` still names Foo).
		kind = "class"
		name = nodeName(node, src)
	case "protocol_declaration":
		// Objective-C `@protocol Foo <NSObject> ... @end` (the first identifier
		// child is the protocol name) and Swift `protocol Foo { ... }` — both
		// grammars emit this node type. ObjC protocols are its interface
		// analogue; Swift protocols keep their own kind (regression: an
		// ObjC-only gate here silently dropped every Swift protocol). Forward
		// declarations (`@protocol Foo;`) are a distinct
		// protocol_forward_declaration node and stay unextracted.
		switch language {
		case "Objective-C":
			kind = "interface"
		case "Swift":
			kind = "protocol"
		default:
			return Entity{}, false
		}
		name = nodeName(node, src)
	case "method":
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "type_definition", "type_spec", "type_alias_declaration":
		kind = "type"
		if language == "F#" {
			// The declared name lives on the type_name node of the concrete
			// *_type_defn child; the generic name descent would instead latch onto
			// a leading attribute ([<CustomEquality>] type SemVerInfo -> "CustomEquality").
			name = fsharpTypeName(node, src)
		} else if (language == "C" || language == "C++") && node.Type() == "type_definition" {
			if alias := cFamilyTypedefAliasName(node, src); alias != "" {
				name = alias
			} else {
				name = nodeName(node, src)
			}
		} else {
			name = nodeName(node, src)
		}
	case "interface_declaration", "interface_definition":
		kind = "interface"
		name = nodeName(node, src)
	case "record_declaration":
		// C# `record` / `record struct` types. Without this case a record is
		// invisible: no type symbol, and its properties/methods get no container
		// (dropping e.g. every EF Core `*Dependencies` parameter object), so
		// property-typed receiver calls through a record can never resolve.
		// Gated to C# so the same node name in other grammars (e.g. Java
		// records, currently unextracted) keeps its existing behavior.
		if language != "C#" {
			return Entity{}, false
		}
		kind = "class"
		name = nodeName(node, src)
	case "struct_item", "struct_specifier", "struct_declaration":
		// Zig struct literals are anonymous (`const Point = struct {...}`); the
		// symbol is extracted at the enclosing variable_declaration, which carries
		// the name. Extracting here would latch onto the first container field.
		if language == "Zig" {
			return Entity{}, false
		}
		// tree-sitter-objc reuses `struct_declaration` for @property and ivar
		// declarations (`AFHTTPSessionManager *sessionManager;`), whose first
		// name node is the property's *type* — emitting those would flood the
		// snapshot with type names masquerading as structs. Skip them for
		// Objective-C; real `struct x` usages still surface as struct_specifier.
		if language == "Objective-C" && node.Type() == "struct_declaration" {
			return Entity{}, false
		}
		kind = "struct"
		name = nodeName(node, src)
	case "enum_item", "enum_declaration", "enum_specifier":
		// Same as struct_declaration: Zig enums are anonymous literals named by
		// the enclosing variable_declaration.
		if language == "Zig" {
			return Entity{}, false
		}
		kind = "enum"
		name = nodeName(node, src)
	case "variable_declaration":
		// Zig type declarations are `const Name = struct/union/enum {...}`. Gated
		// to Zig so other grammars' variable declarations stay unextracted. Plain
		// value bindings (locals, imports) are not symbols and are skipped.
		if language != "Zig" {
			return Entity{}, false
		}
		kind = zigContainerKind(node)
		if kind == "" {
			return Entity{}, false
		}
		if id := firstNamedChildOfType(node, "identifier"); validNode(id) {
			name = strings.TrimSpace(id.Content(src))
		}
	case "object_definition":
		// Scala `object Name` singleton — with or without an extends clause
		// (`object SQLExecution extends Logging`); both parse as
		// object_definition with the identifier in the name field, but the node
		// type had no case, so objects never emitted (companion objects only
		// appeared to extract because the same-named class carried the symbol).
		// An object is a singleton class, so it emits kind "class", which also
		// scopes nested defs under the object name. Gated to Scala so grammars
		// reusing the node type are unchanged.
		if language != "Scala" {
			return Entity{}, false
		}
		kind = "class"
		name = nodeName(node, src)
	case "trait_definition", "trait_item":
		kind = "trait"
		name = nodeName(node, src)
	case "value_definition":
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "function_statement":
		kind = "function"
		name = luaFunctionName(node, src)
		if scope != "" && name != "" && !strings.Contains(name, ".") {
			kind = "method"
			name = qualify(scope, name)
		}
	case "block":
		kind = "block"
		name = hclBlockName(node, src)
	case "field":
		kind = "field"
		name = cueFieldName(node, src)
	case "message":
		kind = "message"
		name = nodeName(node, src)
	case "service":
		kind = "service"
		name = nodeName(node, src)
	case "rpc":
		kind = "rpc"
		name = nodeName(node, src)
		if scope != "" {
			name = qualify(scope, name)
		}
	case "create_table":
		kind = "table"
		name = sqlObjectName(node, src)
	case "create_function":
		kind = "function"
		name = sqlObjectName(node, src)
	case "create_view":
		kind = "view"
		name = sqlObjectName(node, src)
	case "create_materialized_view":
		kind = "view"
		name = sqlObjectName(node, src)
	case "create_index":
		kind = "index"
		name = sqlIndexName(node, src)
	case "create_trigger":
		kind = "trigger"
		name = sqlObjectName(node, src)
	case "statement":
		var ok bool
		kind, name, ok = sqlStatementEntity(node, src)
		if !ok {
			return Entity{}, false
		}
	case "call":
		var ok bool
		kind, name, ok = elixirCallEntity(node, src, scope)
		if !ok {
			return Entity{}, false
		}
	case "list_lit":
		// Clojure def-forms. tree-sitter-clojure has no semantic node types
		// (defn is just a list), so extract by list-head inspection. Gated to
		// Clojure/ClojureScript so no other grammar's node types are affected.
		if language != "Clojure" && language != "ClojureScript" {
			return Entity{}, false
		}
		return clojureListEntity(node, src)
	case "variable_declarator":
		value := node.ChildByFieldName("value")
		if functionLikeValue(value) {
			kind = "function"
		} else if isExportedTopLevelJSVariable(node, language) {
			kind = "variable"
		} else {
			return Entity{}, false
		}
		name = nodeName(node, src)
	default:
		return Entity{}, false
	}
	if name == "" {
		return Entity{}, false
	}
	kind = refineKind(kind, node, src)

	block := node.Content(src)
	entity := Entity{
		Kind:        kind,
		Name:        name,
		Signature:   signatureFromNode(node, src),
		StartLine:   int(node.StartPoint().Row) + 1,
		EndLine:     int(node.EndPoint().Row) + 1,
		BodyHash:    hash(normalize(block)),
		Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signatureFromNode(node, src)}, block))),
	}
	// F# modules and members carry their declarations/body as direct siblings of
	// the name (no `body` field or body-like wrapper node), so signatureFromNode
	// would collapse the whole remaining file (module) or member body into the
	// signature. Cap it to the declaration header.
	if language == "F#" {
		if sig := fsharpSignature(node, src); sig != "" {
			entity.Signature = sig
			entity.Fingerprint = hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: sig}, block)))
		}
	}
	// Dart declaration heads (function/method/getter/setter signatures) keep the
	// body as a *sibling* function_body node, so the node range covers only the
	// head. Extend the entity to the body: without this, the symbol block that
	// call scanning reads contains no call sites, so Dart emitted zero CALLS.
	if language == "Dart" {
		if body := dartSignatureBody(node); validNode(body) && int(body.EndByte()) <= len(src) {
			entity.EndLine = int(body.EndPoint().Row) + 1
			full := string(src[node.StartByte():body.EndByte()])
			entity.BodyHash = hash(normalize(full))
			entity.Fingerprint = hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: entity.Signature}, full)))
		}
	}
	return entity, true
}

// dartInnerSignature returns the function/getter/setter signature wrapped by a
// Dart method_signature node, if any.
func dartInnerSignature(node *sitter.Node) *sitter.Node {
	for _, inner := range []string{"function_signature", "getter_signature", "setter_signature"} {
		if child := firstNamedChildOfType(node, inner); validNode(child) {
			return child
		}
	}
	return nil
}

// dartSignatureBody returns the function_body sibling that carries a Dart
// declaration head's body. For a class member the head is wrapped in a
// method_signature and the body is the *wrapper's* next sibling; for a
// top-level function it directly follows the function_signature. Abstract
// members have no body sibling and return nil.
func dartSignatureBody(node *sitter.Node) *sitter.Node {
	switch node.Type() {
	case "function_signature", "getter_signature", "setter_signature", "method_signature":
	default:
		return nil
	}
	holder := node
	if parent := node.Parent(); validNode(parent) && parent.Type() == "method_signature" {
		holder = parent
	}
	if sibling := holder.NextNamedSibling(); validNode(sibling) && sibling.Type() == "function_body" {
		return sibling
	}
	return nil
}

func refineKind(kind string, node *sitter.Node, src []byte) string {
	if kind != "class" {
		return kind
	}
	// Grammars that fold several declaration forms into one class-like node
	// (tree-sitter-kotlin parses `interface X` as class_declaration) are told
	// apart by the declaration keyword. That keyword may sit behind
	// annotations and modifiers (`@OptIn(...) public sealed interface X`,
	// `fun interface X`), so skip those before matching (evidence: ktor's
	// `public interface HttpClientEngine : CoroutineScope, Closeable` was
	// emitted as a class).
	switch declarationKeyword(strings.TrimSpace(node.Content(src))) {
	case "struct":
		return "struct"
	case "enum":
		return "enum"
	case "interface":
		return "interface"
	case "protocol":
		return "interface"
	default:
		return kind
	}
}

// declKeywordModifiers are declaration modifiers that may precede the
// class/interface/struct/enum keyword across the class-like grammars. `fun` is
// a modifier only in Kotlin's `fun interface`; a plain function never reaches
// refineKind because its node kind is not class-like.
var declKeywordModifiers = map[string]bool{
	"public": true, "private": true, "protected": true, "internal": true,
	"abstract": true, "final": true, "open": true, "sealed": true,
	"static": true, "inner": true, "data": true, "value": true,
	"expect": true, "actual": true, "external": true, "partial": true,
	"fun": true, "export": true, "default": true,
}

// declarationKeyword returns the first word of a declaration after skipping
// leading annotations (`@Name` / `@Name(...)`) and declaration modifiers, "" if
// the text runs out before a non-modifier word.
func declarationKeyword(content string) string {
	for content != "" {
		content = strings.TrimLeft(content, " \t\r\n")
		if strings.HasPrefix(content, "@") {
			end := 1
			for end < len(content) && (isWordByte(content[end]) || content[end] == '.') {
				end++
			}
			rest := strings.TrimLeft(content[end:], " \t\r\n")
			if strings.HasPrefix(rest, "(") {
				if close := matchingParen(rest, 0); close > 0 {
					rest = rest[close+1:]
				} else {
					return ""
				}
			}
			content = rest
			continue
		}
		end := 0
		for end < len(content) && isWordByte(content[end]) {
			end++
		}
		if end == 0 {
			return ""
		}
		word := content[:end]
		if word == "interface" {
			// Dart spells class modifiers with the same words (`abstract
			// interface class Client` declares a class); only a bare
			// `interface X` declaration keyword counts.
			if rest := strings.TrimLeft(content[end:], " \t\r\n"); strings.HasPrefix(rest, "class ") {
				content = content[end:]
				continue
			}
		}
		if !declKeywordModifiers[word] {
			return word
		}
		content = content[end:]
	}
	return ""
}

func isWordByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}

var postgresGeneratedColumnPattern = regexp.MustCompile(`(?is)\bgenerated\s+always\s+as\s*\([^;]*?\)\s+stored`)
var postgresLineCommentPattern = regexp.MustCompile(`(?m)--[^\n\r]*`)
var postgresBlockCommentPattern = regexp.MustCompile(`(?is)/\*.*?\*/`)
var postgresVectorTypePattern = regexp.MustCompile(`(?i)\bvector\s*\([^)]*\)`)
var postgresTimestamptzTypePattern = regexp.MustCompile(`(?i)\btimestamptz\b`)
var postgresBigserialTypePattern = regexp.MustCompile(`(?i)\bbigserial\b`)
var postgresTypeCastPattern = regexp.MustCompile(`(?i)::\s*[a-z_][a-z0-9_]*(?:\[\])?`)
var postgresCurrentDatePattern = regexp.MustCompile(`(?i)\bcurrent_date\b`)
var postgresOnConflictPattern = regexp.MustCompile(`(?is)\bon\s+conflict\b[^;]*`)
var postgresGrantRevokePattern = regexp.MustCompile(`(?is)\b(?:grant|revoke)\b[^;]*;`)
var postgresNotifyPattern = regexp.MustCompile(`(?is)\bnotify\b[^;]*;`)
var postgresDeletePattern = regexp.MustCompile(`(?is)\bdelete\s+from\b[^;]*;`)
var postgresDropFunctionPattern = regexp.MustCompile(`(?is)\bdrop\s+function\b[^;]*;`)
var postgresAlterTablePattern = regexp.MustCompile(`(?is)\balter\s+table\b[^;]*;`)
var postgresAlterFunctionPattern = regexp.MustCompile(`(?is)\balter\s+function\b[^;]*;`)
var postgresPsqlMetaCommandPattern = regexp.MustCompile(`(?im)^\s*(?:\.[^\n\r]*|\\[a-z]+[^\n\r]*)`)
var postgresSubstringFromPattern = regexp.MustCompile(`(?is)\bsubstring\s*\([^()]*\s+from\s+'[^']*'\)`)
var postgresIsDistinctFromPattern = regexp.MustCompile(`(?i)\bis\s+distinct\s+from\b`)
var postgresCrossJoinLateralPattern = regexp.MustCompile(`(?i)\bcross\s+join\s+lateral\b`)
var postgresWithOrdinalityPattern = regexp.MustCompile(`(?i)\s+with\s+ordinality\b`)
var postgresOnDeleteUpdatePattern = regexp.MustCompile(`(?i)\s+on\s+(?:delete|update)\s+(?:cascade|restrict|set\s+null|set\s+default|no\s+action)\b`)
var postgresVectorOperatorClassPattern = regexp.MustCompile(`(?i)\s+vector_[a-z0-9_]+_ops\b`)
var postgresIndexMethodPattern = regexp.MustCompile(`(?i)\s+using\s+[a-z0-9_]+\b`)

// The function/procedure header (between the CREATE keyword and the AS body
// clause) is matched with `[^;]*?` instead of `.*?` so a match cannot run across
// a statement boundary: with `.*?`, a file that opens with LANGUAGE C functions
// (`AS '@MODULE_PATHNAME@', ...`) and later defines a dollar-quoted function let
// the match start at the first CREATE and end at the later body, swallowing every
// definition in between (timescaledb sql/time_bucket.sql). After the closing
// dollar-quote a statement may carry trailing attribute clauses — `LANGUAGE ...`,
// `SET search_path TO ...`, volatility/strictness/parallel markers — before the
// terminating `;` (timescaledb ends most plpgsql bodies with `$BODY$ SET
// search_path TO pg_catalog, pg_temp;`).
const postgresFunctionTrailer = `(?:\s+(?:language|set|immutable|stable|volatile|strict|called|returns|leakproof|not|external|security|parallel|cost|rows|window|support|transform)\b[^;]*)?;`

var postgresCreateFunctionPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?(?:function|procedure)\b[^;]*?\bas\s+\$[a-z0-9_]*\$.*?\$[a-z0-9_]*\$` + postgresFunctionTrailer)
var postgresCreateExternalFunctionPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?(?:function|procedure)\b[^;]*?\bas\s+'[^']+'(?:\s*,\s*'[^']+')?` + postgresFunctionTrailer)
var postgresCreateDomainCastPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:domain|cast)\b[^;]*;`)

// Declarative partitioning the grammar rejects. `PARTITION BY {RANGE|LIST|HASH}
// (...)` trails a normal column list, so blanking it to the statement end leaves
// a valid `CREATE TABLE t (...)`. `PARTITION OF parent FOR VALUES ...` replaces
// the column list entirely, so a bare `CREATE TABLE t` would be left with no body;
// substitute a dummy body so the statement still parses (the real table symbol is
// still extracted from the original bytes).
var postgresPartitionByPattern = regexp.MustCompile(`(?is)\s+partition\s+by\b[^;]*`)
var postgresPartitionOfPattern = regexp.MustCompile(`(?is)\s+partition\s+of\b[^;]*`)

// COPY is a psql/dump construct the grammar rejects. `COPY t (...) FROM stdin;`
// is followed by tab-delimited data terminated by a `\.` line (pg_dump output);
// mask the whole block. Other COPY statements (TO/FROM file) mask as one statement.
var postgresCopyStdinBlockPattern = regexp.MustCompile(`(?ism)^[ \t]*copy\b[^;]*\bfrom\s+stdin\b[^;]*;.*?^\\\.[ \t]*$`)
var postgresCopyStatementPattern = regexp.MustCompile(`(?im)^[ \t]*copy\b[^;]*;`)

// Extension-template placeholders like @extschema@ / @extschema:cube@ in .sql.in
// files are not valid SQL scalars; replace each with a same-width identifier so
// the surrounding statement parses (e.g. `AS @extschema:cube@.cube`).
var postgresExtschemaPlaceholderPattern = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_:]*@`)
var postgresDoBlockPattern = regexp.MustCompile(`(?is)\bdo\s+\$[a-z0-9_]*\$.*?\$[a-z0-9_]*\$;`)
var postgresDropTriggerPattern = regexp.MustCompile(`(?is)\bdrop\s+trigger\b[^;]*;`)
var postgresDropPolicyPattern = regexp.MustCompile(`(?is)\bdrop\s+policy\b[^;]*;`)
var postgresRowLevelSecurityPattern = regexp.MustCompile(`(?is)\balter\s+table\b[^;]*\brow\s+level\s+security\s*;`)
var postgresFunctionSetPattern = regexp.MustCompile(`(?im)^\s*set\s+search_path\s*=\s*[^;\n]+`)
var postgresLoadPattern = regexp.MustCompile(`(?im)^\s*load\s+'[^']+'\s*;?`)
var postgresInlinePsqlMetaCommandPattern = regexp.MustCompile(`(?im)\\(?:gset|gexec|gdesc|watch|if|elif|else|endif|quit|q)\b[^\n\r]*`)
var postgresExtensionDDLPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:access\s+method|operator(?:\s+class|\s+family)?|type|aggregate|text\s+search\s+(?:configuration|dictionary|parser|template)|collation|statistics|transform)\b[^;]*;`)
var postgresAlterExtensionPattern = regexp.MustCompile(`(?is)\balter\s+extension\b[^;]*;`)

// PostgreSQL-specific statements the SQL grammar does not parse: the EXPLAIN
// options parenthetical (`EXPLAIN (COSTS OFF, ...) <stmt>` — mask only the
// option list so the inner statement still parses), ALTER OPERATOR FAMILY/CLASS,
// and COMMENT ON for non-table objects (access method, operator, type, ...).
var postgresExplainOptionsPattern = regexp.MustCompile(`(?i)\bexplain\s*\([^)]*\)`)
var postgresAlterOperatorPattern = regexp.MustCompile(`(?is)\balter\s+operator\s+(?:family|class)\b[^;]*;`)
var postgresAlterOperatorGenericPattern = regexp.MustCompile(`(?is)\balter\s+operator\s+[^\s(]+\s*\([^)]*\)[^;]*;`)
var postgresForeignAndTriggerDDLPattern = regexp.MustCompile(`(?is)\b(?:create|alter|drop)\s+(?:foreign\s+data\s+wrapper|server|user\s+mapping|event\s+trigger|publication|subscription)\b[^;]*;`)
var postgresAlterTextSearchPattern = regexp.MustCompile(`(?is)\balter\s+text\s+search\s+(?:configuration|dictionary|parser|template)\b[^;]*;`)
var postgresCommentOnObjectPattern = regexp.MustCompile(`(?is)\bcomment\s+on\s+(?:access\s+method|operator(?:\s+(?:family|class))?|aggregate|type|domain|collation|text\s+search\s+\w+|transform|extension|cast|function|procedure|language|server|publication|subscription)\b[^;]*;`)

func maskPostgresUnsupportedSyntax(content string) string {
	masked := []byte(content)
	for _, loc := range postgresVectorTypePattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "text")
	}
	for _, loc := range postgresTimestamptzTypePattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "timestamp")
	}
	for _, loc := range postgresBigserialTypePattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "bigint")
	}
	for _, loc := range postgresTypeCastPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresCurrentDatePattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "now()")
	}
	for _, loc := range postgresLineCommentPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresBlockCommentPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresSubstringFromPattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "null")
	}
	for _, loc := range postgresIsDistinctFromPattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "<>")
	}
	for _, loc := range postgresCrossJoinLateralPattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], "cross join")
	}
	for _, loc := range postgresPsqlMetaCommandPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresInlinePsqlMetaCommandPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresGeneratedColumnPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	maskPostgresCheckConstraints(masked, content)
	for _, loc := range postgresCreateFunctionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresCreateExternalFunctionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDoBlockPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresExtensionDDLPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterExtensionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresExplainOptionsPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterOperatorPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterOperatorGenericPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresForeignAndTriggerDDLPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterTextSearchPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresExtschemaPlaceholderPattern.FindAllStringIndex(content, -1) {
		replaceBytesPreservingWidth(masked, loc[0], loc[1], strings.Repeat("a", loc[1]-loc[0]))
	}
	for _, loc := range postgresCreateDomainCastPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresCommentOnObjectPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresLoadPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDropTriggerPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDropPolicyPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresRowLevelSecurityPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDropFunctionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterTablePattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresAlterFunctionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresGrantRevokePattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresNotifyPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDeletePattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresFunctionSetPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresWithOrdinalityPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresOnDeleteUpdatePattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresPartitionOfPattern.FindAllStringIndex(content, -1) {
		replaceRangePreservingNewlines(masked, loc[0], loc[1], " (partition_dummy int)")
	}
	for _, loc := range postgresPartitionByPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresCopyStdinBlockPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresCopyStatementPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	maskPostgresTableConstraints(masked, content)
	for _, loc := range postgresIndexMethodPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresOnConflictPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresVectorOperatorClassPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	lower := asciiLowerString(content)
	offset := 0
	for {
		rel := strings.Index(lower[offset:], "create policy")
		if rel < 0 {
			break
		}
		start := offset + rel
		end := sqlStatementEnd(content, start)
		maskBytesPreservingNewlines(masked, start, end)
		offset = end
	}
	return string(masked)
}

func replaceBytesPreservingWidth(src []byte, start, end int, replacement string) {
	if start < 0 {
		start = 0
	}
	if end > len(src) {
		end = len(src)
	}
	for i := start; i < end; i++ {
		src[i] = ' '
	}
	copy(src[start:end], replacement)
}

func replaceRangePreservingNewlines(src []byte, start, end int, replacement string) {
	if start < 0 {
		start = 0
	}
	if end > len(src) {
		end = len(src)
	}
	for i := start; i < end; i++ {
		switch src[i] {
		case '\n', '\r':
		default:
			src[i] = ' '
		}
	}
	copy(src[start:min(end, start+len(replacement))], replacement)
}

func maskBytesPreservingNewlines(src []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(src) {
		end = len(src)
	}
	for i := start; i < end; i++ {
		switch src[i] {
		case '\n', '\r':
		default:
			src[i] = ' '
		}
	}
}

func maskPostgresCheckConstraints(masked []byte, content string) {
	lower := asciiLowerString(content)
	offset := 0
	for {
		rel := strings.Index(lower[offset:], "check")
		if rel < 0 {
			return
		}
		start := offset + rel
		beforeOK := start == 0 || !isSQLIdentifierByte(lower[start-1])
		after := start + len("check")
		afterOK := after >= len(lower) || !isSQLIdentifierByte(lower[after])
		if !beforeOK || !afterOK {
			offset = after
			continue
		}
		open := after
		for open < len(content) && (content[open] == ' ' || content[open] == '\t' || content[open] == '\n' || content[open] == '\r') {
			open++
		}
		if open >= len(content) || content[open] != '(' {
			offset = after
			continue
		}
		depth := 0
		inString := false
		for i := open; i < len(content); i++ {
			switch content[i] {
			case '\'':
				if inString && i+1 < len(content) && content[i+1] == '\'' {
					i++
					continue
				}
				inString = !inString
			case '(':
				if !inString {
					depth++
				}
			case ')':
				if !inString {
					depth--
					if depth == 0 {
						if before := previousNonSpace(content, start); before >= 0 && (content[before] == ',' || content[before] == '(') {
							replaceRangePreservingNewlines(masked, start, i+1, "check_dummy text")
						} else {
							maskBytesPreservingNewlines(masked, start, i+1)
						}
						offset = i + 1
						goto next
					}
				}
			}
		}
		return
	next:
	}
}

// asciiLowerString lowercases only ASCII A-Z, byte for byte, leaving every other
// byte (including multi-byte UTF-8 sequences) untouched. Unlike strings.ToLower,
// the result has exactly the same byte length as the input, so byte offsets from
// a keyword search on the lowered copy stay valid when applied back to the
// original content. SQL keywords are ASCII, so this finds the same matches —
// strings.ToLower can grow the byte length (e.g. 'İ' -> "i̇"), which made these
// offsets run past the end of content and panic on large SQL files.
func asciiLowerString(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

func maskPostgresTableConstraints(masked []byte, content string) {
	lower := asciiLowerString(content)
	for _, keyword := range []string{"primary key", "foreign key", "unique", "constraint"} {
		offset := 0
		for {
			rel := strings.Index(lower[offset:], keyword)
			if rel < 0 {
				break
			}
			start := offset + rel
			before := previousNonSpace(content, start)
			if before >= 0 && content[before] != ',' && content[before] != '(' {
				offset = start + len(keyword)
				continue
			}
			end := tableConstraintEnd(content, start)
			replaceRangePreservingNewlines(masked, start, end, "constraint_dummy text")
			offset = end
		}
	}
}

func previousNonSpace(content string, index int) int {
	for i := index - 1; i >= 0; i-- {
		switch content[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return i
		}
	}
	return -1
}

func tableConstraintEnd(content string, start int) int {
	depth := 0
	inString := false
	seenParen := false
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '\'':
			if inString && i+1 < len(content) && content[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
		case '(':
			if !inString {
				depth++
				seenParen = true
			}
		case ')':
			if !inString {
				if depth == 0 && seenParen {
					return i
				}
				depth--
				if seenParen && depth == 0 {
					return i + 1
				}
			}
		case ',':
			if !inString && !seenParen {
				return i + 1
			}
		case '\n', '\r':
			if !inString && !seenParen {
				return i
			}
		}
	}
	return len(content)
}

func isSQLIdentifierByte(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_'
}

func postgresFunctionEntities(src []byte) []Entity {
	content := string(src)
	var entities []Entity
	for _, pattern := range []*regexp.Regexp{postgresCreateFunctionPattern, postgresCreateExternalFunctionPattern} {
		for _, loc := range pattern.FindAllStringIndex(content, -1) {
			block := content[loc[0]:loc[1]]
			if name := matchSQLCreateFunctionName(block); name != "" {
				signature := strings.TrimSpace(block)
				if index := strings.IndexByte(signature, '\n'); index >= 0 {
					signature = signature[:index]
				}
				entity := Entity{
					Kind:        "function",
					Name:        name,
					Signature:   strings.TrimSpace(strings.TrimRight(signature, "{:; \t\r\n")),
					StartLine:   countLinesBefore(content, loc[0]) + 1,
					EndLine:     countLinesBefore(content, loc[1]) + 1,
					BodyHash:    hash(normalize(block)),
					Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
				}
				entities = append(entities, entity)
			}
		}
	}
	return entities
}

func postgresPolicyEntities(src []byte) []Entity {
	content := string(src)
	lower := asciiLowerString(content)
	var entities []Entity
	offset := 0
	for {
		rel := strings.Index(lower[offset:], "create policy")
		if rel < 0 {
			break
		}
		start := offset + rel
		end := sqlStatementEnd(content, start)
		block := content[start:end]
		if name := matchSQLCreatePolicyName(block); name != "" {
			signature := strings.TrimSpace(block)
			if index := strings.IndexByte(signature, '\n'); index >= 0 {
				signature = signature[:index]
			}
			entity := Entity{
				Kind:        "policy",
				Name:        name,
				Signature:   strings.TrimSpace(strings.TrimRight(signature, "{:; \t\r\n")),
				StartLine:   countLinesBefore(content, start) + 1,
				EndLine:     countLinesBefore(content, end) + 1,
				BodyHash:    hash(normalize(block)),
				Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
			}
			entities = append(entities, entity)
		}
		offset = end
	}
	return entities
}

var (
	graphqlResolverRootPattern       = regexp.MustCompile(`(?m)\b([A-Z][A-Za-z0-9_]*)\s*:\s*\{`)
	graphqlResolverExportRootPattern = regexp.MustCompile(`(?m)\b(?:export\s+)?(?:const|let|var)\s+([A-Z][A-Za-z0-9_]*)\s*=\s*\{`)
	graphqlResolverContextPattern    = regexp.MustCompile(`(?i)\b(graphql|resolvers?)\b`)
	graphqlResolverFieldPattern      = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	graphqlResolverReferencePattern  = regexp.MustCompile(`^([A-Za-z_$][A-Za-z0-9_$]*)(?:\s*\.\s*[A-Za-z_$][A-Za-z0-9_$]*)*(?:\s*\([^{};]*\))?\s*(?:,|$)`)
	graphqlResolverObjectPattern     = regexp.MustCompile(`(?m)\b(?:subscribe|resolve)\s*:`)
	graphqlSchemaDefinitionPattern   = regexp.MustCompile(`(?m)\bschema\b[^{]*\{`)
	graphqlSchemaOperationPattern    = regexp.MustCompile(`(?m)\b(query|mutation|subscription)\s*:\s*([_A-Za-z][_0-9A-Za-z]*)\b`)
	graphqlSchemaTypePattern         = regexp.MustCompile(`(?m)\b(?:extend\s+)?type\s+([_A-Za-z][_0-9A-Za-z]*)\b[^{]*\{`)
	graphqlSchemaFieldNamePattern    = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
)

func graphqlSchemaEntities(content string) []Entity {
	var entities []Entity
	seen := map[string]bool{}
	operationRoots := graphqlSchemaOperationRoots(content)
	for _, loc := range graphqlSchemaTypePattern.FindAllStringSubmatchIndex(content, -1) {
		typeName := content[loc[2]:loc[3]]
		open := strings.LastIndex(content[loc[0]:loc[1]], "{")
		if open < 0 {
			continue
		}
		open += loc[0]
		close := matchingBraceOffset(content, open)
		if close < 0 {
			continue
		}
		body := content[open+1 : close]
		for _, field := range graphqlSchemaFields(body) {
			rootName := operationRoots[typeName]
			if rootName == "" {
				rootName = typeName
			}
			name := typeName + "." + field.Name
			if seen[name] {
				continue
			}
			seen[name] = true
			start := open + 1 + field.Start
			end := open + 1 + field.End
			block := content[start:end]
			signature := "GraphQL schema " + strings.ToLower(rootName) + " " + field.Name
			entities = append(entities, Entity{
				Kind:        "graphql_schema_field",
				Name:        name,
				Signature:   signature,
				StartLine:   countLinesBefore(content, start) + 1,
				EndLine:     countLinesBefore(content, end) + 1,
				BodyHash:    hash(normalize(block)),
				Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
			})
		}
	}
	return entities
}

func graphqlSchemaOperationRoots(content string) map[string]string {
	roots := map[string]string{}
	for _, loc := range graphqlSchemaDefinitionPattern.FindAllStringSubmatchIndex(content, -1) {
		open := strings.LastIndex(content[loc[0]:loc[1]], "{")
		if open < 0 {
			continue
		}
		open += loc[0]
		close := matchingBraceOffset(content, open)
		if close < 0 {
			continue
		}
		body := content[open+1 : close]
		for _, match := range graphqlSchemaOperationPattern.FindAllStringSubmatch(body, -1) {
			if len(match) < 3 {
				continue
			}
			roots[match[2]] = strings.ToLower(match[1])
		}
	}
	return roots
}

func graphqlSchemaFields(body string) []graphqlResolverField {
	var fields []graphqlResolverField
	for i := 0; i < len(body); i++ {
		i = skipGraphQLIgnored(body, i)
		if i >= len(body) {
			break
		}
		if !isGraphQLNameStart(body[i]) {
			continue
		}
		nameStart := i
		i++
		for i < len(body) && isGraphQLNamePart(body[i]) {
			i++
		}
		name := body[nameStart:i]
		if !graphqlSchemaFieldNamePattern.MatchString(name) {
			continue
		}
		cursor := skipGraphQLIgnored(body, i)
		if cursor < len(body) && body[cursor] == '(' {
			close := matchingDelimiterOffset(body, cursor, '(', ')')
			if close < 0 {
				continue
			}
			cursor = skipGraphQLIgnored(body, close+1)
		}
		if cursor >= len(body) || body[cursor] != ':' {
			i = maxInt(i, cursor)
			continue
		}
		end := graphqlSchemaFieldEnd(body, cursor+1)
		fields = append(fields, graphqlResolverField{Name: name, Start: nameStart, End: end})
		i = maxInt(i, end-1)
	}
	return fields
}

func skipGraphQLIgnored(value string, index int) int {
	for index < len(value) {
		switch {
		case value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r' || value[index] == ',':
			index++
		case value[index] == '#':
			for index < len(value) && value[index] != '\n' && value[index] != '\r' {
				index++
			}
		case strings.HasPrefix(value[index:], `"""`):
			index += 3
			if end := strings.Index(value[index:], `"""`); end >= 0 {
				index += end + 3
			} else {
				return len(value)
			}
		case value[index] == '"':
			index++
			for index < len(value) {
				if value[index] == '\\' {
					index += 2
					continue
				}
				if value[index] == '"' {
					index++
					break
				}
				index++
			}
		default:
			return index
		}
	}
	return index
}

func graphqlSchemaFieldEnd(body string, start int) int {
	seenType := false
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '[':
			depth++
			seenType = true
		case ']':
			if depth > 0 {
				depth--
			}
			seenType = true
		case '#':
			if seenType && depth == 0 {
				return i
			}
			for i < len(body) && body[i] != '\n' && body[i] != '\r' {
				i++
			}
		case '\n', '\r':
			if seenType && depth == 0 {
				return i
			}
		case ' ', '\t':
		default:
			seenType = true
		}
	}
	return len(body)
}

func isGraphQLNameStart(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_'
}

func isGraphQLNamePart(ch byte) bool {
	return isGraphQLNameStart(ch) || (ch >= '0' && ch <= '9')
}

func graphqlResolverEntities(path, content string) []Entity {
	var entities []Entity
	seen := map[string]bool{}
	for _, loc := range graphqlResolverRootPattern.FindAllStringSubmatchIndex(content, -1) {
		typeName := content[loc[2]:loc[3]]
		entities = appendGraphQLResolverRootEntities(entities, seen, path, content, typeName, loc[0], loc[1])
	}
	for _, loc := range graphqlResolverExportRootPattern.FindAllStringSubmatchIndex(content, -1) {
		typeName := content[loc[2]:loc[3]]
		entities = appendGraphQLResolverRootEntities(entities, seen, path, content, typeName, loc[0], loc[1])
	}
	return entities
}

func appendGraphQLResolverRootEntities(entities []Entity, seen map[string]bool, path, content, typeName string, locStart, locEnd int) []Entity {
	open := strings.LastIndex(content[locStart:locEnd], "{")
	if open < 0 {
		return entities
	}
	open += locStart
	close := matchingBraceOffset(content, open)
	if close < 0 {
		return entities
	}
	if !graphqlResolverContext(path, content, locStart, close) {
		return entities
	}
	body := content[open+1 : close]
	for _, field := range graphqlResolverFields(body) {
		name := typeName + "." + field.Name
		if seen[name] {
			continue
		}
		seen[name] = true
		start := open + 1 + field.Start
		end := open + 1 + field.End
		block := content[start:end]
		signature := "GraphQL resolver " + strings.ToLower(typeName) + " " + field.Name
		entities = append(entities, Entity{
			Kind:        "graphql_resolver",
			Name:        name,
			Signature:   signature,
			StartLine:   countLinesBefore(content, start) + 1,
			EndLine:     countLinesBefore(content, end) + 1,
			BodyHash:    hash(normalize(block)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
		})
	}
	return entities
}

func graphqlResolverContext(path, content string, start, end int) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "resolver") || strings.Contains(base, "graphql") || strings.Contains(base, "schema") {
		return true
	}
	from := maxInt(0, start-300)
	to := minInt(len(content), end+80)
	return graphqlResolverContextPattern.MatchString(content[from:to])
}

type graphqlResolverField struct {
	Name  string
	Start int
	End   int
}

func graphqlResolverFields(body string) []graphqlResolverField {
	var fields []graphqlResolverField
	depth := 0
	inString := byte(0)
	escaped := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
			continue
		case '{', '(', '[':
			depth++
			continue
		case '}', ')', ']':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 || !isJSIdentifierStart(ch) {
			continue
		}
		nameStart := i
		i++
		for i < len(body) && isJSIdentifierPart(body[i]) {
			i++
		}
		name := body[nameStart:i]
		if !graphqlResolverFieldPattern.MatchString(name) {
			continue
		}
		cursor := skipSpace(body, i)
		if cursor >= len(body) {
			continue
		}
		if body[cursor] == '(' {
			end := graphqlResolverFieldEnd(body, cursor)
			fields = append(fields, graphqlResolverField{Name: name, Start: nameStart, End: end})
			i = maxInt(i, end-1)
			continue
		}
		if body[cursor] != ':' {
			continue
		}
		valueStart := skipSpace(body, cursor+1)
		if !looksLikeGraphQLResolverValue(body[valueStart:]) {
			continue
		}
		end := graphqlResolverFieldEnd(body, valueStart)
		fields = append(fields, graphqlResolverField{Name: name, Start: nameStart, End: end})
		i = maxInt(i, end-1)
	}
	return fields
}

func looksLikeGraphQLResolverValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(trimmed, "async function"), strings.HasPrefix(trimmed, "function"):
		return true
	case strings.HasPrefix(trimmed, "async ("), strings.HasPrefix(trimmed, "("):
		return strings.Contains(trimmed, "=>")
	case strings.HasPrefix(trimmed, "{"):
		end := matchingBraceOffset(trimmed, 0)
		if end < 0 {
			return false
		}
		return graphqlResolverObjectPattern.MatchString(trimmed[:end])
	default:
		arrow := strings.Index(trimmed, "=>")
		return (arrow > 0 && arrow < 80) || looksLikeGraphQLResolverReference(trimmed)
	}
}

func looksLikeGraphQLResolverReference(value string) bool {
	match := graphqlResolverReferencePattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return false
	}
	switch match[1] {
	case "false", "null", "true", "undefined":
		return false
	default:
		return true
	}
}

func graphqlResolverFieldEnd(body string, start int) int {
	depth := 0
	inString := byte(0)
	escaped := false
	for i := start; i < len(body); i++ {
		ch := body[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	return len(body)
}

func matchingBraceOffset(content string, open int) int {
	if open < 0 || open >= len(content) || content[open] != '{' {
		return -1
	}
	depth := 0
	inString := byte(0)
	escaped := false
	for i := open; i < len(content); i++ {
		ch := content[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func matchingDelimiterOffset(content string, open int, openCh, closeCh byte) int {
	if open < 0 || open >= len(content) || content[open] != openCh {
		return -1
	}
	depth := 0
	inString := byte(0)
	escaped := false
	for i := open; i < len(content); i++ {
		ch := content[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			inString = ch
		case openCh:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func skipSpace(value string, index int) int {
	for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r') {
		index++
	}
	return index
}

func isJSIdentifierStart(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' || ch == '$'
}

func isJSIdentifierPart(ch byte) bool {
	return isJSIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}

func countLinesBefore(content string, end int) int {
	if end > len(content) {
		end = len(content)
	}
	return strings.Count(content[:end], "\n")
}

// sqlStatementEnd returns the offset just past the ';' that terminates the SQL
// statement starting at start, ignoring ';' that appear inside single-quoted
// strings (” is an embedded quote) or dollar-quoted strings ($$...$$ /
// $tag$...$tag$, used by function bodies). It returns len(content) when no
// terminating ';' is found. This prevents a semicolon inside a literal (e.g. a
// CREATE POLICY USING clause) from truncating the statement and silently
// dropping every entity that follows it.
func sqlStatementEnd(content string, start int) int {
	n := len(content)
	for i := start; i < n; {
		switch content[i] {
		case '\'':
			i++
			for i < n {
				if content[i] == '\'' {
					if i+1 < n && content[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case '$':
			if tag, ok := dollarQuoteTag(content, i); ok {
				rest := content[i+len(tag):]
				closeRel := strings.Index(rest, tag)
				if closeRel < 0 {
					return n
				}
				i = i + len(tag) + closeRel + len(tag)
			} else {
				i++
			}
		case ';':
			return i + 1
		default:
			i++
		}
	}
	return n
}

// dollarQuoteTag reports whether content[i] begins a PostgreSQL dollar-quote and
// returns the full opening tag (e.g. "$$" or "$body$"). A tag is '$', an optional
// identifier (letters/underscore, then alnum/underscore), then '$'.
func dollarQuoteTag(content string, i int) (string, bool) {
	if i >= len(content) || content[i] != '$' {
		return "", false
	}
	for j := i + 1; j < len(content); j++ {
		c := content[j]
		if c == '$' {
			return content[i : j+1], true
		}
		isFirst := j == i+1
		switch {
		case c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
		case !isFirst && c >= '0' && c <= '9':
		default:
			return "", false
		}
	}
	return "", false
}

// stripSQLComments blanks -- line comments and /* */ block comments with spaces
// (preserving length and newlines so reported line numbers stay correct), while
// leaving comment markers that appear inside single-quoted or dollar-quoted
// literals untouched. It is used to feed the regex entity extractors source that
// cannot match commented-out DDL.
func stripSQLComments(content string) string {
	b := []byte(content)
	n := len(b)
	for i := 0; i < n; {
		switch b[i] {
		case '\'':
			i++
			for i < n {
				if b[i] == '\'' {
					if i+1 < n && b[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case '$':
			if tag, ok := dollarQuoteTag(content, i); ok {
				closeRel := strings.Index(content[i+len(tag):], tag)
				if closeRel < 0 {
					return string(b)
				}
				i = i + len(tag) + closeRel + len(tag)
			} else {
				i++
			}
		case '-':
			if i+1 < n && b[i+1] == '-' {
				for i < n && b[i] != '\n' {
					b[i] = ' '
					i++
				}
			} else {
				i++
			}
		case '/':
			if i+1 < n && b[i+1] == '*' {
				b[i] = ' '
				b[i+1] = ' '
				i += 2
				for i < n {
					if b[i] == '*' && i+1 < n && b[i+1] == '/' {
						b[i] = ' '
						b[i+1] = ' '
						i += 2
						break
					}
					if b[i] != '\n' && b[i] != '\r' {
						b[i] = ' '
					}
					i++
				}
			} else {
				i++
			}
		default:
			i++
		}
	}
	return string(b)
}

func sqlObjectName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if validNode(child) && child.Type() == "object_reference" {
			if name := sqlReferenceName(child, src); name != "" {
				return name
			}
		}
	}
	return nodeName(node, src)
}

func sqlIndexName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "object_reference":
			// In CREATE INDEX, the first object_reference after ON is the table.
			continue
		case "identifier", "literal":
			if name := sqlIdentifierContent(child, src); name != "" {
				return name
			}
		}
	}
	return sqlObjectName(node, src)
}

func sqlReferenceName(node *sitter.Node, src []byte) string {
	var parts []string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "identifier", "literal":
			if part := sqlIdentifierContent(child, src); part != "" {
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, ".")
}

func sqlIdentifierContent(node *sitter.Node, src []byte) string {
	content := strings.TrimSpace(node.Content(src))
	content = strings.Trim(content, "`\"")
	return content
}

func sqlStatementEntity(node *sitter.Node, src []byte) (string, string, bool) {
	content := strings.TrimSpace(node.Content(src))
	lower := asciiLowerString(content)
	if !strings.HasPrefix(lower, "create ") {
		return "", "", false
	}
	if name := matchSQLCreatePolicyName(content); name != "" {
		return "policy", name, true
	}
	return "", "", false
}

// sqlCreateFunctionHeadPattern anchors on the whole CREATE FUNCTION/PROCEDURE
// header. Matching the keyword as part of the header (rather than searching for
// the substring "function") supports CREATE PROCEDURE and avoids keying off
// "function" embedded in a schema name (e.g. `CREATE PROCEDURE
// _timescaledb_functions.rebuild_columnstore(...)` used to yield the garbage
// name "s.rebuild_columnstore").
var sqlCreateFunctionHeadPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?(?:function|procedure)\s+(?:if\s+not\s+exists\s+)?`)

func matchSQLCreateFunctionName(content string) string {
	loc := sqlCreateFunctionHeadPattern.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	rest := strings.TrimSpace(content[loc[1]:])
	if rest == "" {
		return ""
	}
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return ""
	}
	return normalizeSQLDottedName(rest[:open])
}

func matchSQLCreatePolicyName(content string) string {
	lower := asciiLowerString(content)
	idx := strings.Index(lower, "create policy")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(content[idx+len("create policy"):])
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			return rest[1 : end+1]
		}
		return ""
	}
	tokens := sqlStatementTokens(rest)
	if len(tokens) > 0 {
		return tokens[0]
	}
	return ""
}

func normalizeSQLDottedName(content string) string {
	var parts []string
	for _, part := range strings.Split(content, ".") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "`\"")
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ".")
}

func sqlStatementTokens(content string) []string {
	var tokens []string
	for _, field := range strings.Fields(content) {
		token := strings.Trim(field, "`\"(),;")
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func nodeName(node *sitter.Node, src []byte) string {
	for _, field := range []string{"name", "property"} {
		child := node.ChildByFieldName(field)
		if validNode(child) {
			if name := nameFromNode(child, src); name != "" {
				return name
			}
		}
	}
	return firstNameDescendant(node, src)
}

func firstNameDescendant(node *sitter.Node, src []byte) string {
	if !validNode(node) {
		return ""
	}
	if isNameNode(node.Type()) {
		return strings.TrimSpace(node.Content(src))
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		// A declaration's own name is never inside its leading modifiers,
		// annotations, or generic type parameters. Skipping those subtrees
		// stops the pre-order search from latching onto an annotation name
		// (e.g. Kotlin `@OptIn class Koin` -> "OptIn") or a type parameter
		// (e.g. `fun <T> get()` -> "T") before reaching the real name.
		if skipForNameDescent(child.Type()) {
			continue
		}
		if name := firstNameDescendant(child, src); name != "" {
			return name
		}
	}
	return ""
}

func skipForNameDescent(nodeType string) bool {
	switch nodeType {
	case "modifiers", "annotation", "type_parameters":
		return true
	default:
		return false
	}
}

func nameFromNode(node *sitter.Node, src []byte) string {
	if !validNode(node) {
		return ""
	}
	content := strings.TrimSpace(node.Content(src))
	if isNameNode(node.Type()) && content != "" {
		return content
	}
	if name := firstNameDescendant(node, src); name != "" {
		return name
	}
	return content
}

func isNameNode(nodeType string) bool {
	switch nodeType {
	case "alias", "bare_key", "class_name", "constant", "field_identifier", "field_name", "identifier", "id_name", "message_name", "module_name", "name", "package_identifier", "property_identifier", "rpc_name", "service_name", "simple_identifier", "template_literal", "type_constructor", "type_identifier", "value_name", "word":
		return true
	default:
		return false
	}
}

func signatureFromNode(node *sitter.Node, src []byte) string {
	start := node.StartByte()
	end := node.EndByte()
	if body := node.ChildByFieldName("body"); validNode(body) {
		end = body.StartByte()
	} else if body := firstBodyLikeChild(node); validNode(body) {
		end = body.StartByte()
	}
	if end <= start || int(end) > len(src) {
		end = node.EndByte()
	}
	signature := strings.TrimSpace(string(src[start:end]))
	// Collapse a multi-line declaration header into one line rather than cutting
	// at the first newline: a class whose generic parameter list spans several
	// lines carries its `extends`/`implements` clause only after the break, and
	// dropping it loses the supertype (so inheritance-aware call resolution and
	// EXTENDS/INHERITS edges silently disappear for that type).
	signature = strings.Join(strings.Fields(signature), " ")
	return strings.TrimSpace(strings.TrimRight(signature, "{:; \t\r\n"))
}

func firstBodyLikeChild(node *sitter.Node) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "block", "statement_block", "class_body", "declaration_list", "field_declaration_list", "interface_body", "compound_statement", "closure", "do_block", "function_body", "message_body", "service_body", "template_body":
			return child
		}
	}
	return nil
}

func functionLikeValue(node *sitter.Node) bool {
	if !validNode(node) {
		return false
	}
	switch node.Type() {
	case "arrow_function", "function", "function_definition", "function_expression", "generator_function", "lambda":
		return true
	default:
		return false
	}
}

var jsExportedVariablePattern = regexp.MustCompile(`(?m)^\s*export\s+(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`)
var jsAssignmentMethodPattern = regexp.MustCompile(`(?m)^\s*((?:[A-Za-z_$][A-Za-z0-9_$]*\s*\.\s*)+[A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s+)?function(?:\s+[A-Za-z_$][A-Za-z0-9_$]*)?\s*\(`)
var luaFunctionLinePattern = regexp.MustCompile(`(?m)^[ \t]*(?:local[ \t]+)?function[ \t]+([A-Za-z_][A-Za-z0-9_]*(?:(?:[.:])[A-Za-z_][A-Za-z0-9_]*)*)[ \t]*\(`)
var luaBlockTokenPattern = regexp.MustCompile(`\b(function|if|for|while|repeat|do|end|until)\b`)
var objectiveCMethodNamePattern = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)`)

func javascriptExportedVariableEntities(content string) []Entity {
	matches := jsExportedVariablePattern.FindAllStringSubmatchIndex(content, -1)
	entities := make([]Entity, 0, len(matches))
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		name := content[match[2]:match[3]]
		lineStart := strings.LastIndexByte(content[:match[0]], '\n') + 1
		lineEndRel := strings.IndexByte(content[match[0]:], '\n')
		lineEnd := len(content)
		if lineEndRel >= 0 {
			lineEnd = match[0] + lineEndRel
		}
		signature := strings.TrimSpace(content[lineStart:lineEnd])
		startLine := strings.Count(content[:match[0]], "\n") + 1
		entities = append(entities, Entity{
			Kind:        "variable",
			Name:        name,
			Signature:   signature,
			StartLine:   startLine,
			EndLine:     startLine,
			BodyHash:    hash(normalize(signature)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, signature))),
		})
	}
	return entities
}

func javascriptAssignmentMethodEntities(content string) []Entity {
	matches := jsAssignmentMethodPattern.FindAllStringSubmatchIndex(content, -1)
	entities := make([]Entity, 0, len(matches))
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		name := strings.Join(regexp.MustCompile(`\s*\.\s*`).Split(strings.TrimSpace(content[match[2]:match[3]]), -1), ".")
		if name == "" || strings.HasPrefix(name, "module.exports.") || strings.HasPrefix(name, "exports.") {
			// Export alias properties are useful exports, but not object/prototype
			// method declarations with a stable receiver.
			continue
		}
		lineStart := strings.LastIndexByte(content[:match[0]], '\n') + 1
		lineEndRel := strings.IndexByte(content[match[0]:], '\n')
		lineEnd := len(content)
		if lineEndRel >= 0 {
			lineEnd = match[0] + lineEndRel
		}
		startLine := countLinesBefore(content, match[0]) + 1
		endLine := startLine
		openBrace := strings.IndexByte(content[match[0]:], '{')
		if openBrace >= 0 {
			openBrace += match[0]
			if closeBrace := matchingDelimiterOffset(content, openBrace, '{', '}'); closeBrace >= 0 {
				endLine = countLinesBefore(content, closeBrace) + 1
			}
		}
		signature := strings.TrimSpace(content[lineStart:lineEnd])
		blockEnd := lineEnd
		if endLine > startLine && openBrace >= 0 {
			if closeBrace := matchingDelimiterOffset(content, openBrace, '{', '}'); closeBrace >= 0 {
				blockEnd = closeBrace + 1
			}
		}
		block := content[match[0]:minInt(blockEnd, len(content))]
		entities = append(entities, Entity{
			Kind:        "method",
			Name:        name,
			Signature:   signature,
			StartLine:   startLine,
			EndLine:     endLine,
			BodyHash:    hash(normalize(block)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
		})
	}
	return entities
}

func luaFunctionEntities(content string) []Entity {
	matches := luaFunctionLinePattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	entities := make([]Entity, 0, len(matches))
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		name := strings.ReplaceAll(strings.TrimSpace(content[match[2]:match[3]]), ":", ".")
		if name == "" {
			continue
		}
		lineStart := strings.LastIndexByte(content[:match[0]], '\n') + 1
		lineEndRel := strings.IndexByte(content[match[0]:], '\n')
		lineEnd := len(content)
		if lineEndRel >= 0 {
			lineEnd = match[0] + lineEndRel
		}
		signature := strings.TrimSpace(content[lineStart:lineEnd])
		startLine := countLinesBefore(content, match[0]) + 1
		endLine := luaFunctionEndLine(lines, startLine)
		block := luaBlock(lines, startLine, endLine)
		entities = append(entities, Entity{
			Kind:        "function",
			Name:        name,
			Signature:   signature,
			StartLine:   startLine,
			EndLine:     endLine,
			BodyHash:    hash(normalize(block)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
		})
	}
	return entities
}

func luaBlock(lines []string, startLine, endLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) || endLine < startLine {
		return ""
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}

func luaFunctionEndLine(lines []string, startLine int) int {
	depth := 0
	for i := startLine - 1; i < len(lines); i++ {
		code := stripLuaLineForBlockScan(lines[i])
		previous := ""
		for _, match := range luaBlockTokenPattern.FindAllStringSubmatch(code, -1) {
			if len(match) < 2 {
				continue
			}
			token := match[1]
			switch token {
			case "function", "if", "for", "while", "repeat":
				depth++
			case "do":
				if previous != "for" && previous != "while" {
					depth++
				}
			case "end", "until":
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
			previous = token
		}
	}
	return len(lines)
}

func stripLuaLineForBlockScan(line string) string {
	bytes := []byte(line)
	quote := byte(0)
	for i := 0; i < len(bytes); i++ {
		if quote != 0 {
			if bytes[i] == '\\' {
				i++
				continue
			}
			if bytes[i] == quote {
				quote = 0
			}
			bytes[i] = ' '
			continue
		}
		switch bytes[i] {
		case '"', '\'':
			quote = bytes[i]
			bytes[i] = ' '
		case '-':
			if i+1 < len(bytes) && bytes[i+1] == '-' {
				for j := i; j < len(bytes); j++ {
					bytes[j] = ' '
				}
				return string(bytes)
			}
		}
	}
	return string(bytes)
}

func objectiveCMethodEntities(content string) []Entity {
	lines := strings.SplitAfter(content, "\n")
	offsets := make([]int, len(lines))
	offset := 0
	for i, line := range lines {
		offsets[i] = offset
		offset += len(line)
	}

	var entities []Entity
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "- (") && !strings.HasPrefix(trimmed, "+ (") {
			continue
		}
		startLine := i + 1
		startByte := offsets[i] + strings.Index(lines[i], strings.TrimLeft(lines[i], " \t"))
		headerParts := []string{strings.TrimSpace(strings.TrimRight(lines[i], "\r\n"))}
		openByte := -1
		prototype := false
		for j := i; j < len(lines) && j < i+40; j++ {
			if j > i {
				headerParts = append(headerParts, strings.TrimSpace(strings.TrimRight(lines[j], "\r\n")))
			}
			line := lines[j]
			brace := strings.IndexByte(line, '{')
			semi := strings.IndexByte(line, ';')
			if semi >= 0 && (brace < 0 || semi < brace) {
				prototype = true
				break
			}
			if brace >= 0 {
				openByte = offsets[j] + brace
				break
			}
		}
		if prototype || openByte < 0 {
			continue
		}
		header := strings.Join(headerParts, " ")
		name := objectiveCMethodHeaderName(header)
		if name == "" {
			continue
		}
		endLine := startLine
		closeByte := matchingDelimiterOffset(content, openByte, '{', '}')
		if closeByte >= 0 {
			endLine = countLinesBefore(content, closeByte) + 1
		}
		signature := strings.TrimSpace(strings.TrimSuffix(header[:strings.Index(header, "{")+1], "{"))
		blockEnd := minInt(len(content), openByte+1)
		if closeByte >= 0 {
			blockEnd = closeByte + 1
		}
		block := content[startByte:blockEnd]
		entities = append(entities, Entity{
			Kind:        "method",
			Name:        name,
			Signature:   signature,
			StartLine:   startLine,
			EndLine:     endLine,
			BodyHash:    hash(normalize(block)),
			Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
		})
	}
	return entities
}

func objectiveCMethodHeaderName(header string) string {
	closeReturnType := strings.IndexByte(header, ')')
	if closeReturnType < 0 || closeReturnType+1 >= len(header) {
		return ""
	}
	rest := header[closeReturnType+1:]
	match := objectiveCMethodNamePattern.FindStringSubmatch(rest)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func appendMissingEntities(entities []Entity, candidates ...Entity) []Entity {
	seen := make(map[string]bool, len(entities))
	for _, entity := range entities {
		seen[entity.Kind+"\x00"+entity.Name] = true
	}
	for _, candidate := range candidates {
		key := candidate.Kind + "\x00" + candidate.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		entities = append(entities, candidate)
	}
	return entities
}

var cPlusPlusUsingAliasLineRe = regexp.MustCompile(`^\s*(?:template\s*<[^>\n]+>\s*)?using\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^;]+);`)

func cPlusPlusTypeAliasEntities(content string) []Entity {
	lines := strings.SplitAfter(content, "\n")
	var entities []Entity
	braceDepth := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := cPlusPlusUsingAliasLineRe.FindStringSubmatch(line); match != nil && braceDepth <= 1 {
			signature := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
			name := match[1]
			block := strings.TrimSpace(line)
			entities = append(entities, Entity{
				Kind:        "type",
				Name:        name,
				Signature:   signature,
				StartLine:   i + 1,
				EndLine:     i + 1,
				BodyHash:    hash(normalize(block)),
				Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
			})
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			braceDepth += strings.Count(line, "{")
			braceDepth -= strings.Count(line, "}")
			if braceDepth < 0 {
				braceDepth = 0
			}
		}
	}
	return entities
}

func isExportedTopLevelJSVariable(node *sitter.Node, language string) bool {
	if language != "JavaScript" && language != "TypeScript" {
		return false
	}
	parent := node.Parent()
	if !validNode(parent) {
		return false
	}
	if parent.Type() != "lexical_declaration" && parent.Type() != "variable_declaration" {
		return false
	}
	grandparent := parent.Parent()
	if !validNode(grandparent) || grandparent.Type() != "export_statement" {
		return false
	}
	root := grandparent.Parent()
	return validNode(root) && root.Type() == "program"
}

func scopesChildren(kind string) bool {
	switch kind {
	// "type" scopes children so Go struct fields qualify under the struct
	// (Go structs parse as type_spec -> kind "type"). Interface/alias bodies
	// have no field declarations, so this only affects struct fields.
	case "class", "interface", "message", "module", "service", "struct", "trait", "type":
		return true
	default:
		return false
	}
}

// rAssignmentEntity extracts an R definition from an assignment. The r-lib
// grammar represents `name <- function(args) ...` as a binary_operator with
// lhs/operator/rhs fields; `=`, `<<-`, and the right-assign forms
// (`function(...) ... -> name`) are the same node with a different operator.
// A function_definition value (including the `\(x)` lambda shorthand) yields a
// function; an R6Class/setRefClass call value yields a class (the trivial
// S4/R6 idiom). The name side may be an identifier or a string, as in
// syntactic names like `"myop<-" <- function(x, value) ...`.
func rAssignmentEntity(node *sitter.Node, src []byte, scope string) (string, string, bool) {
	operator := node.ChildByFieldName("operator")
	if !validNode(operator) {
		return "", "", false
	}
	target := node.ChildByFieldName("lhs")
	value := node.ChildByFieldName("rhs")
	switch operator.Type() {
	case "<-", "<<-", "=":
	case "->", "->>":
		target, value = value, target
	default:
		return "", "", false
	}
	if !validNode(value) {
		return "", "", false
	}
	name := rAssignmentTargetName(target, src)
	if name == "" {
		return "", "", false
	}
	switch value.Type() {
	case "function_definition":
		if scope != "" {
			return "method", qualify(scope, name), true
		}
		return "function", name, true
	case "call":
		if fn := value.ChildByFieldName("function"); validNode(fn) {
			callee := strings.TrimSpace(fn.Content(src))
			if callee == "R6Class" || callee == "R6::R6Class" || callee == "setRefClass" || callee == "methods::setRefClass" {
				return "class", name, true
			}
		}
	}
	return "", "", false
}

// rAssignmentTargetName returns the defined name from the target side of an R
// assignment: a plain identifier or a quoted string (syntactic names).
func rAssignmentTargetName(node *sitter.Node, src []byte) string {
	if !validNode(node) {
		return ""
	}
	switch node.Type() {
	case "identifier":
		return strings.TrimSpace(node.Content(src))
	case "string":
		if content := firstNamedChildOfType(node, "string_content"); validNode(content) {
			return strings.TrimSpace(content.Content(src))
		}
	}
	return ""
}

// erlangFunDeclEntity builds the function symbol for an Erlang fun_decl form.
// tree-sitter-erlang parses each clause of a multi-clause function
// (`f(1) -> a;\nf(2) -> b.`) as its own fun_decl form, so a run of consecutive
// fun_decl siblings with the same name/arity is folded into a single symbol:
// the first clause emits an entity spanning the whole run and the later
// clauses emit nothing.
func erlangFunDeclEntity(node *sitter.Node, src []byte) (Entity, bool) {
	name, arity := erlangFunClauseNameArity(node, src)
	if name == "" {
		return Entity{}, false
	}
	if prev := node.PrevNamedSibling(); validNode(prev) && prev.Type() == "fun_decl" {
		if prevName, prevArity := erlangFunClauseNameArity(prev, src); prevName == name && prevArity == arity {
			return Entity{}, false // continuation clause; the run's first clause carries the symbol
		}
	}
	last := node
	for next := last.NextNamedSibling(); validNode(next) && next.Type() == "fun_decl"; next = next.NextNamedSibling() {
		if nextName, nextArity := erlangFunClauseNameArity(next, src); nextName != name || nextArity != arity {
			break
		}
		last = next
	}
	start, end := node.StartByte(), last.EndByte()
	if end <= start || int(end) > len(src) {
		end = node.EndByte()
	}
	block := string(src[start:end])
	signature := name
	if clause := firstNamedChildOfType(node, "function_clause"); validNode(clause) {
		signature = strings.TrimSpace(strings.TrimSuffix(signatureFromNode(clause, src), "->"))
	}
	return Entity{
		Kind:        "function",
		Name:        name,
		Signature:   signature,
		StartLine:   int(node.StartPoint().Row) + 1,
		EndLine:     int(last.EndPoint().Row) + 1,
		BodyHash:    hash(normalize(block)),
		Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
	}, true
}

// erlangFunClauseNameArity returns the function name and arity of a fun_decl's
// first clause. Erlang identifies functions by name/arity, so both are needed
// to decide whether adjacent fun_decl forms are clauses of the same function.
func erlangFunClauseNameArity(node *sitter.Node, src []byte) (string, int) {
	clause := firstNamedChildOfType(node, "function_clause")
	if !validNode(clause) {
		return "", 0
	}
	name := nameFromNode(clause.ChildByFieldName("name"), src)
	arity := 0
	if args := clause.ChildByFieldName("args"); validNode(args) {
		arity = int(args.NamedChildCount())
	}
	return name, arity
}

func elixirCallEntity(node *sitter.Node, src []byte, scope string) (string, string, bool) {
	head := firstNamedChildOfType(node, "identifier")
	if !validNode(head) {
		return "", "", false
	}
	switch strings.TrimSpace(head.Content(src)) {
	case "defmodule":
		if alias := firstDescendantOfType(node, "alias"); validNode(alias) {
			return "module", strings.TrimSpace(alias.Content(src)), true
		}
	case "def", "defp":
		args := firstNamedChildOfType(node, "arguments")
		if !validNode(args) {
			return "", "", false
		}
		for i := 0; i < int(args.NamedChildCount()); i++ {
			child := args.NamedChild(i)
			if !validNode(child) || child.Type() != "call" {
				continue
			}
			if nameNode := firstNamedChildOfType(child, "identifier"); validNode(nameNode) {
				name := strings.TrimSpace(nameNode.Content(src))
				if scope != "" {
					return "method", qualify(scope, name), true
				}
				return "function", name, true
			}
		}
	}
	return "", "", false
}

// juliaDefinitionName returns the name a Julia callable definition binds, given
// its head node: the `signature` child of a long-form `function`/`macro`
// definition, or the left-hand side of a short-form `name(args) = expr`
// assignment. Return-type (`f(x)::T`) and type-parameter (`f(x) where T`)
// wrappers are looked through; a qualified extension method (`Base.show`,
// `Base.:(==)`) keeps its dotted path; a parametric constructor
// (`Foo{T}(x) where T`) binds the type name. Callable-object definitions
// (`function (obj::T)(x)`) and non-definition assignments bind no callable
// name and yield "".
func juliaDefinitionName(head *sitter.Node, src []byte) string {
	inSignature := false
	for validNode(head) {
		switch head.Type() {
		case "signature":
			inSignature = true
			head = head.NamedChild(0)
		case "where_expression", "typed_expression":
			head = head.NamedChild(0)
		case "identifier":
			// A bare identifier signature is a `function name end` zero-method
			// declaration; a bare-identifier assignment LHS is a variable
			// binding, not a callable definition.
			if inSignature {
				return strings.TrimSpace(head.Content(src))
			}
			return ""
		case "call_expression":
			callee := head.NamedChild(0)
			if !validNode(callee) {
				return ""
			}
			switch callee.Type() {
			case "identifier", "operator", "field_expression":
				return strings.TrimSpace(callee.Content(src))
			case "curly_expression", "parametrized_type_expression":
				// Parametric constructor definition, e.g. `Foo{T}(x) where T`.
				if id := firstNamedChildOfType(callee, "identifier"); validNode(id) {
					return strings.TrimSpace(id.Content(src))
				}
				return ""
			default:
				return ""
			}
		default:
			return ""
		}
	}
	return ""
}

// clojureDefKinds maps a Clojure def-form head symbol (its final segment, so
// namespaced heads like `mu/defn` or `methodical/defmulti` match too — a
// common idiom for instrumented/extended def macros) to the symbol kind.
var clojureDefKinds = map[string]string{
	"defn":         "function",
	"defn-":        "function",
	"defmacro":     "function",
	"defmulti":     "function",
	"defmethod":    "function",
	"def":          "variable",
	"defonce":      "variable",
	"defrecord":    "class",
	"deftype":      "class",
	"defprotocol":  "interface",
	"definterface": "interface",
}

// clojureListEntity extracts a symbol from a Clojure `(defX name ...)` list.
// tree-sitter-clojure parses every form as a generic list_lit, so the def-form
// is recognized by inspecting the list head: the head symbol's final segment
// (after any namespace qualifier) must be a known def macro, and the symbol
// name is the next symbol value in the list (metadata attaches to the sym_lit
// node itself in this grammar, and docstrings follow the name, so neither
// interferes).
func clojureListEntity(node *sitter.Node, src []byte) (Entity, bool) {
	values := clojureListValues(node)
	if len(values) < 2 {
		return Entity{}, false
	}
	head := values[0]
	if head.Type() != "sym_lit" {
		return Entity{}, false
	}
	kind, ok := clojureDefKinds[clojureSymName(head, src)]
	if !ok {
		return Entity{}, false
	}
	nameNode := values[1]
	if nameNode.Type() != "sym_lit" {
		return Entity{}, false
	}
	name := clojureSymName(nameNode, src)
	if name == "" {
		return Entity{}, false
	}
	// The header — through the params vector when one directly follows the
	// name — is the signature; the whole form would collapse a large function
	// body into one line.
	sigEnd := nameNode.EndByte()
	if len(values) > 2 && values[2].Type() == "vec_lit" {
		sigEnd = values[2].EndByte()
	}
	signature := node.Content(src)
	if int(sigEnd) > int(node.StartByte()) && int(sigEnd) <= len(src) {
		signature = string(src[node.StartByte():sigEnd])
	}
	signature = strings.Join(strings.Fields(signature), " ")

	block := node.Content(src)
	return Entity{
		Kind:        kind,
		Name:        name,
		Signature:   signature,
		StartLine:   int(node.StartPoint().Row) + 1,
		EndLine:     int(node.EndPoint().Row) + 1,
		BodyHash:    hash(normalize(block)),
		Fingerprint: hash(normalize(entityFingerprintSource(Entity{Name: name, Signature: signature}, block))),
	}, true
}

// clojureListValues returns the value children of a list_lit, skipping
// comments, discard expressions (#_), and metadata attached to the list.
func clojureListValues(node *sitter.Node) []*sitter.Node {
	var values []*sitter.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "comment", "dis_expr", "meta_lit", "old_meta_lit":
			continue
		}
		values = append(values, child)
	}
	return values
}

// clojureSymName returns a symbol's name without its namespace qualifier
// (`mu/defn` -> `defn`, `clojure.core/def` -> `def`).
func clojureSymName(node *sitter.Node, src []byte) string {
	if name := node.ChildByFieldName("name"); validNode(name) {
		return strings.TrimSpace(name.Content(src))
	}
	text := strings.TrimSpace(node.Content(src))
	if idx := strings.LastIndex(text, "/"); idx >= 0 && idx+1 < len(text) {
		return text[idx+1:]
	}
	return text
}

func hclBlockName(node *sitter.Node, src []byte) string {
	var parts []string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if !validNode(child) {
			continue
		}
		switch child.Type() {
		case "identifier", "string_lit":
			if value := hclBlockPart(child, src); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.Join(parts, ".")
}

func hclBlockPart(node *sitter.Node, src []byte) string {
	if node.Type() == "identifier" {
		return strings.TrimSpace(node.Content(src))
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if validNode(child) && child.Type() == "template_literal" {
			return strings.TrimSpace(child.Content(src))
		}
	}
	return strings.Trim(strings.TrimSpace(node.Content(src)), `"`)
}

func cueFieldName(node *sitter.Node, src []byte) string {
	label := firstNamedChildOfType(node, "label")
	if !validNode(label) {
		return ""
	}
	return strings.TrimSpace(label.Content(src))
}

func luaFunctionName(node *sitter.Node, src []byte) string {
	nameNode := firstNamedChildOfType(node, "function_name")
	if !validNode(nameNode) {
		return nodeName(node, src)
	}
	var parts []string
	for i := 0; i < int(nameNode.NamedChildCount()); i++ {
		child := nameNode.NamedChild(i)
		if validNode(child) && child.Type() == "identifier" {
			parts = append(parts, strings.TrimSpace(child.Content(src)))
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(nameNode.Content(src))
	}
	return strings.Join(parts, ".")
}

func firstNamedChildOfType(node *sitter.Node, nodeType string) *sitter.Node {
	if !validNode(node) {
		return nil
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if validNode(child) && child.Type() == nodeType {
			return child
		}
	}
	return nil
}

func firstDescendantOfType(node *sitter.Node, nodeType string) *sitter.Node {
	if !validNode(node) {
		return nil
	}
	if node.Type() == nodeType {
		return node
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if found := firstDescendantOfType(node.NamedChild(i), nodeType); validNode(found) {
			return found
		}
	}
	return nil
}

func goReceiverName(node *sitter.Node, src []byte) string {
	signature := signatureFromNode(node, src)
	receiverStart := strings.Index(signature, "func (")
	if receiverStart < 0 {
		return ""
	}
	receiver := signature[receiverStart+len("func ("):]
	receiverEnd := strings.Index(receiver, ")")
	if receiverEnd < 0 {
		return ""
	}
	receiver = strings.TrimSpace(receiver[:receiverEnd])
	fields := strings.Fields(receiver)
	if len(fields) == 0 {
		return ""
	}
	name := strings.Trim(fields[len(fields)-1], "*[]")
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	return strings.TrimSpace(name)
}

func qualify(scope, name string) string {
	if scope == "" || name == "" || strings.HasPrefix(name, scope+".") {
		return name
	}
	return scope + "." + name
}

func validNode(node *sitter.Node) bool {
	return node != nil && !node.IsNull()
}

func normalize(value string) string {
	fields := strings.Fields(value)
	return strings.Join(fields, " ")
}

func entityFingerprintSource(entity Entity, block string) string {
	lines := strings.Split(block, "\n")
	if len(lines) <= 1 {
		return strings.ReplaceAll(entity.Signature, entity.Name, "<name>")
	}
	return strings.Join(lines[1:], "\n")
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
