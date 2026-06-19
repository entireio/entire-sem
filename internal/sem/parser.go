package sem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	language string
	grammar  *sitter.Language
}

var treeSitterLanguages = map[string]languageSpec{
	".bash":   {language: "Bash", grammar: bash.GetLanguage()},
	".c":      {language: "C", grammar: c.GetLanguage()},
	".cc":     {language: "C++", grammar: cpp.GetLanguage()},
	".cpp":    {language: "C++", grammar: cpp.GetLanguage()},
	".cs":     {language: "C#", grammar: csharp.GetLanguage()},
	".cue":    {language: "CUE", grammar: cue.GetLanguage()},
	".cxx":    {language: "C++", grammar: cpp.GetLanguage()},
	".ex":     {language: "Elixir", grammar: elixir.GetLanguage()},
	".exs":    {language: "Elixir", grammar: elixir.GetLanguage()},
	".go":     {language: "Go", grammar: golang.GetLanguage()},
	".gradle": {language: "Groovy", grammar: groovy.GetLanguage()},
	".groovy": {language: "Groovy", grammar: groovy.GetLanguage()},
	".h":      {language: "C", grammar: c.GetLanguage()},
	".hcl":    {language: "HCL", grammar: hcl.GetLanguage()},
	".hh":     {language: "C++", grammar: cpp.GetLanguage()},
	".hpp":    {language: "C++", grammar: cpp.GetLanguage()},
	".hxx":    {language: "C++", grammar: cpp.GetLanguage()},
	".java":   {language: "Java", grammar: java.GetLanguage()},
	".js":     {language: "JavaScript", grammar: javascript.GetLanguage()},
	".jsx":    {language: "JavaScript", grammar: treesittertsx.GetLanguage()},
	".kt":     {language: "Kotlin", grammar: kotlin.GetLanguage()},
	".kts":    {language: "Kotlin", grammar: kotlin.GetLanguage()},
	".lua":    {language: "Lua", grammar: lua.GetLanguage()},
	".ml":     {language: "OCaml", grammar: ocaml.GetLanguage()},
	".mli":    {language: "OCaml", grammar: ocaml.GetLanguage()},
	".php":    {language: "PHP", grammar: php.GetLanguage()},
	".proto":  {language: "Protocol Buffers", grammar: protobuf.GetLanguage()},
	".py":     {language: "Python", grammar: python.GetLanguage()},
	".rb":     {language: "Ruby", grammar: ruby.GetLanguage()},
	".rs":     {language: "Rust", grammar: rust.GetLanguage()},
	".sbt":    {language: "Scala", grammar: scala.GetLanguage()},
	".scala":  {language: "Scala", grammar: scala.GetLanguage()},
	".sc":     {language: "Scala", grammar: scala.GetLanguage()},
	".sh":     {language: "Bash", grammar: bash.GetLanguage()},
	".sql":    {language: "SQL"},
	".swift":  {language: "Swift", grammar: swift.GetLanguage()},
	".tf":     {language: "HCL", grammar: hcl.GetLanguage()},
	".tfvars": {language: "HCL", grammar: hcl.GetLanguage()},
	".ts":     {language: "TypeScript", grammar: treesitterts.GetLanguage()},
	".tsx":    {language: "TypeScript", grammar: treesittertsx.GetLanguage()},
	".yaml":   {language: "YAML", grammar: treesitteryaml.GetLanguage()},
	".yml":    {language: "YAML", grammar: treesitteryaml.GetLanguage()},
	".zsh":    {language: "Bash", grammar: bash.GetLanguage()},
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
	if spec.language == "SQL" {
		spec.grammar = pgsql.GetLanguage()
	}
	src := []byte(content)
	parseSrc := src
	if spec.language == "SQL" {
		parseSrc = []byte(maskPostgresUnsupportedSyntax(content))
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
			status = ParseStatus{ParseError: true, Detail: "tree-sitter syntax error nodes present"}
		}
		return yamlEntities(path, content), spec.language, status
	}

	var entities []Entity
	walkEntities(root, src, spec.language, "", &entities)
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
		status = ParseStatus{ParseError: true, Detail: "tree-sitter syntax error nodes present"}
	}
	return entities, spec.language, status
}

func Supported(path string) bool {
	_, ok := languageForPath(path)
	return ok
}

func languageForPath(path string) (languageSpec, bool) {
	spec, ok := treeSitterLanguages[strings.ToLower(filepath.Ext(path))]
	return spec, ok
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
	entity, ok := entityFromNode(node, src, scope)
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

func entityFromNode(node *sitter.Node, src []byte, scope string) (Entity, bool) {
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
		if !functionLikeValue(value) {
			return Entity{}, false
		}
		kind = "function"
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
