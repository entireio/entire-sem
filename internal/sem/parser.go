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

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/groovy"
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
	"github.com/suhaanthayyil/entire-sem/internal/sem/pgsql"
)

type languageSpec struct {
	language      string
	grammar       *sitter.Language
	inventoryOnly bool
}

var treeSitterLanguages = map[string]languageSpec{
	".bash":       {language: "Bash", grammar: bash.GetLanguage()},
	".c":          {language: "C", grammar: c.GetLanguage()},
	".cc":         {language: "C++", grammar: cpp.GetLanguage()},
	".cpp":        {language: "C++", grammar: cpp.GetLanguage()},
	".cs":         {language: "C#", grammar: csharp.GetLanguage()},
	".cue":        {language: "CUE", grammar: cue.GetLanguage()},
	".cxx":        {language: "C++", grammar: cpp.GetLanguage()},
	".ex":         {language: "Elixir", grammar: elixir.GetLanguage()},
	".exs":        {language: "Elixir", grammar: elixir.GetLanguage()},
	".go":         {language: "Go", grammar: golang.GetLanguage()},
	".gradle":     {language: "Groovy", grammar: groovy.GetLanguage()},
	".groovy":     {language: "Groovy", grammar: groovy.GetLanguage()},
	".h":          {language: "C", grammar: c.GetLanguage()},
	".hcl":        {language: "HCL", grammar: hcl.GetLanguage()},
	".html":       {language: "HTML"},
	".hh":         {language: "C++", grammar: cpp.GetLanguage()},
	".hpp":        {language: "C++", grammar: cpp.GetLanguage()},
	".hxx":        {language: "C++", grammar: cpp.GetLanguage()},
	".java":       {language: "Java", grammar: java.GetLanguage()},
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
	".ml":         {language: "OCaml", grammar: ocaml.GetLanguage()},
	".mli":        {language: "OCaml", grammar: ocaml.GetLanguage()},
	".php":        {language: "PHP", grammar: php.GetLanguage()},
	".proto":      {language: "Protocol Buffers", grammar: protobuf.GetLanguage()},
	".py":         {language: "Python", grammar: python.GetLanguage()},
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
	".zsh":        {language: "Bash", grammar: bash.GetLanguage()},
	".dockerfile": {language: "Dockerfile"},
}

type TreeSitterParser struct{}

type ParseStatus struct {
	ParseError bool
	Detail     string
}

func (TreeSitterParser) Parse(path, content string) ([]Entity, string) {
	entities, language, _ := TreeSitterParser{}.ParseWithStatus(path, content)
	return entities, language
}

func (TreeSitterParser) ParseWithStatus(path, content string) ([]Entity, string, ParseStatus) {
	spec, ok := languageForPath(path)
	if !ok {
		return nil, "", ParseStatus{}
	}
	if spec.language == "Kustomize" && looksLikeFluxKustomizationManifest(content) {
		spec = treeSitterLanguages[".yaml"]
	}
	if strings.EqualFold(filepath.Ext(path), ".h") && looksLikeObjectiveC(content) {
		spec = languageSpec{language: "Objective-C", inventoryOnly: true}
	}
	if spec.language == "SQL" {
		spec.grammar = pgsql.GetLanguage()
	}
	if spec.grammar == nil {
		return fallbackEntities(path, content, spec.language), spec.language, ParseStatus{}
	}
	src := []byte(content)
	parseSrc := src
	if spec.language == "SQL" {
		parseSrc = []byte(maskPostgresUnsupportedSyntax(content))
	}
	if spec.language == "Java" {
		parseSrc = []byte(maskJavaUnsupportedSyntax(content))
	}
	if spec.language == "Groovy" {
		parseSrc = []byte(maskGroovyUnsupportedSyntax(content))
	}
	if spec.language == "Kotlin" {
		parseSrc = []byte(maskKotlinUnsupportedSyntax(path, content))
	}
	if spec.language == "YAML" {
		parseSrc = []byte(maskYAMLUnsupportedSyntax(content))
	}
	if spec.language == "TypeScript" && !strings.EqualFold(filepath.Ext(path), ".tsx") {
		parseSrc = []byte(maskTypeScriptUnsupportedSyntax(content))
	}
	root, err := sitter.ParseCtx(context.Background(), parseSrc, spec.grammar)
	if err != nil || root == nil || root.IsNull() {
		detail := "tree-sitter parse failed"
		if err != nil {
			detail = err.Error()
		}
		return nil, spec.language, ParseStatus{ParseError: true, Detail: detail}
	}
	if spec.language == "YAML" {
		status := ParseStatus{}
		if root.HasError() {
			status = ParseStatus{ParseError: true, Detail: parseErrorDetail(root, src)}
		}
		return yamlEntities(path, content), spec.language, status
	}

	var entities []Entity
	walkEntities(root, src, spec.language, "", &entities)
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
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].StartLine == entities[j].StartLine {
			return entities[i].Name < entities[j].Name
		}
		return entities[i].StartLine < entities[j].StartLine
	})
	status := ParseStatus{}
	if root.HasError() {
		status = ParseStatus{ParseError: true, Detail: parseErrorDetail(root, src)}
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
	groovyQuotedMethodPattern = regexp.MustCompile(`\b(def|void)\s+"[^"\n]+"\s*\(`)
	groovyJavaCastPattern     = regexp.MustCompile(`\([A-Za-z_][A-Za-z0-9_]*\)\s+[A-Za-z_$]`)
)

func maskGroovyUnsupportedSyntax(content string) string {
	content = groovyQuotedMethodPattern.ReplaceAllStringFunc(content, func(match string) string {
		open := strings.LastIndex(match, "(")
		quote := strings.Index(match, "\"")
		if open <= quote || quote < 0 {
			return match
		}
		prefix := match[:quote]
		placeholder := "quotedFeature"
		spaceCount := open - quote - len(placeholder)
		if spaceCount < 1 {
			placeholder = "q"
			spaceCount = open - quote - len(placeholder)
		}
		if spaceCount < 0 {
			return match
		}
		return prefix + placeholder + strings.Repeat(" ", spaceCount) + "("
	})
	return groovyJavaCastPattern.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat(" ", len(match)-1) + match[len(match)-1:]
	})
}

var (
	kotlinSuspendLambdaPattern = regexp.MustCompile(`\bsuspend\s+\{`)
	kotlinMultiDollarString    = regexp.MustCompile(`\$+\s*"`)
)

func maskKotlinUnsupportedSyntax(path, content string) string {
	content = kotlinSuspendLambdaPattern.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat(" ", len(match)-1) + "{"
	})
	content = kotlinMultiDollarString.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat(" ", len(match)-1) + "\""
	})
	if strings.EqualFold(filepath.Ext(path), ".kts") {
		content = maskKotlinGradleOptionValueAssignments(content)
		content = maskKotlinGradleWhenGetOrElse(content)
	}
	return content
}

func maskYAMLUnsupportedSyntax(content string) string {
	// Antora playbooks commonly use @PLACEHOLDER@ values before templating.
	// Bare YAML scalars cannot start with "@", but replacing it in parse-only
	// input preserves line and column positions while leaving entity extraction
	// on the original source.
	return strings.Map(func(r rune) rune {
		if r == '@' {
			return 'x'
		}
		return r
	}, content)
}

func maskKotlinGradleOptionValueAssignments(content string) string {
	return maskKotlinGradleBlocks(content, ".value =", "maskedGradleOptionValue()")
}

func maskKotlinGradleWhenGetOrElse(content string) string {
	return maskKotlinGradleBlocks(content, ".getOrElse(when (", ".getOrElse(\"masked\")")
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

func looksLikeObjectiveC(content string) bool {
	return regexp.MustCompile(`(?m)^\s*@(?:interface|implementation|protocol|class|end)\b`).MatchString(content) ||
		regexp.MustCompile(`(?m)^\s*#import\s+[<"]`).MatchString(content)
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
	if !validNode(node) {
		return
	}
	// Field/property declarations emit one entity per declared name and are not
	// descended into (their name nodes would otherwise look like field accesses).
	if fields, ok := fieldEntities(node, src, language, scope); ok {
		*entities = append(*entities, fields...)
		return
	}
	entity, ok := entityFromNode(node, src, language, scope)
	childScope := scope
	if ok {
		*entities = append(*entities, entity)
		if scopesChildren(entity.Kind) {
			childScope = entity.Name
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		walkEntities(node.NamedChild(i), src, language, childScope, entities)
	}
}

// fieldEntities extracts struct/class field declarations as field symbols, one
// per declared name, qualified under the containing type's scope. It returns
// false for non-field nodes and for declarations outside a container (so local
// variables and parameters are never treated as fields). This pass handles Go
// struct fields (field_declaration -> field_identifier); TypeScript/Java/C#
// fields are added later.
func fieldEntities(node *sitter.Node, src []byte, language, scope string) ([]Entity, bool) {
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
		"property_declaration": // C# properties (mapped to the canonical field kind)
	default:
		return nil, false
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
	// C# field_declaration nests the type under variable_declaration.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if child := node.NamedChild(i); child.Type() == "variable_declaration" {
			if typeNode := child.ChildByFieldName("type"); validNode(typeNode) {
				return strings.TrimSpace(typeNode.Content(src))
			}
		}
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

func entityFromNode(node *sitter.Node, src []byte, language, scope string) (Entity, bool) {
	var kind string
	var name string
	switch node.Type() {
	case "class", "class_definition", "class_declaration", "class_specifier":
		kind = "class"
		name = nodeName(node, src)
	case "module_definition":
		kind = "module"
		name = nodeName(node, src)
	case "function_definition":
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "function_declaration", "function_item":
		kind = "function"
		name = nodeName(node, src)
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
		kind = "method"
		name = nodeName(node, src)
		if receiver := goReceiverName(node, src); receiver != "" {
			name = qualify(receiver, name)
		} else if scope != "" {
			name = qualify(scope, name)
		}
	case "method_definition":
		kind = "method"
		name = nodeName(node, src)
		if scope != "" {
			name = qualify(scope, name)
		}
	case "method":
		kind = "function"
		name = nodeName(node, src)
		if scope != "" {
			kind = "method"
			name = qualify(scope, name)
		}
	case "type_definition", "type_spec", "type_alias_declaration":
		kind = "type"
		name = nodeName(node, src)
	case "interface_declaration", "interface_definition":
		kind = "interface"
		name = nodeName(node, src)
	case "struct_item", "struct_specifier", "struct_declaration":
		kind = "struct"
		name = nodeName(node, src)
	case "enum_item", "enum_declaration", "enum_specifier":
		kind = "enum"
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
	return entity, true
}

func refineKind(kind string, node *sitter.Node, src []byte) string {
	if kind != "class" {
		return kind
	}
	content := strings.TrimSpace(node.Content(src))
	switch {
	case strings.HasPrefix(content, "struct "):
		return "struct"
	case strings.HasPrefix(content, "enum "):
		return "enum"
	case strings.HasPrefix(content, "interface "):
		return "interface"
	case strings.HasPrefix(content, "protocol "):
		return "interface"
	default:
		return kind
	}
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
var postgresCreateFunctionPattern = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?function\b.*?\bas\s+\$[a-z0-9_]*\$.*?\$[a-z0-9_]*\$(?:\s+language\b[^;]*)?;`)
var postgresDoBlockPattern = regexp.MustCompile(`(?is)\bdo\s+\$[a-z0-9_]*\$.*?\$[a-z0-9_]*\$;`)
var postgresDropTriggerPattern = regexp.MustCompile(`(?is)\bdrop\s+trigger\b[^;]*;`)
var postgresDropPolicyPattern = regexp.MustCompile(`(?is)\bdrop\s+policy\b[^;]*;`)
var postgresRowLevelSecurityPattern = regexp.MustCompile(`(?is)\balter\s+table\b[^;]*\brow\s+level\s+security\s*;`)
var postgresFunctionSetPattern = regexp.MustCompile(`(?im)^\s*set\s+search_path\s*=\s*[^;\n]+`)

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
	for _, loc := range postgresGeneratedColumnPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	maskPostgresCheckConstraints(masked, content)
	for _, loc := range postgresCreateFunctionPattern.FindAllStringIndex(content, -1) {
		maskBytesPreservingNewlines(masked, loc[0], loc[1])
	}
	for _, loc := range postgresDoBlockPattern.FindAllStringIndex(content, -1) {
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
	lower := strings.ToLower(content)
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
	lower := strings.ToLower(content)
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

func maskPostgresTableConstraints(masked []byte, content string) {
	lower := strings.ToLower(content)
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
	for _, loc := range postgresCreateFunctionPattern.FindAllStringIndex(content, -1) {
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
	return entities
}

func postgresPolicyEntities(src []byte) []Entity {
	content := string(src)
	lower := strings.ToLower(content)
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
	lower := strings.ToLower(content)
	if !strings.HasPrefix(lower, "create ") {
		return "", "", false
	}
	if name := matchSQLCreatePolicyName(content); name != "" {
		return "policy", name, true
	}
	return "", "", false
}

func matchSQLCreateFunctionName(content string) string {
	lower := strings.ToLower(content)
	idx := strings.Index(lower, "function")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(content[idx+len("function"):])
	if rest == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(rest), "if not exists") {
		rest = strings.TrimSpace(rest[len("if not exists"):])
	}
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return ""
	}
	return normalizeSQLDottedName(rest[:open])
}

func matchSQLCreatePolicyName(content string) string {
	lower := strings.ToLower(content)
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
		if name := firstNameDescendant(child, src); name != "" {
			return name
		}
	}
	return ""
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
	if index := strings.IndexByte(signature, '\n'); index >= 0 {
		signature = signature[:index]
	}
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
