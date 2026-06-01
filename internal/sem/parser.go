package sem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
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
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/swift"
	treesittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	treesitterts "github.com/smacker/go-tree-sitter/typescript/typescript"
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
	".sql":    {language: "SQL", grammar: sql.GetLanguage()},
	".swift":  {language: "Swift", grammar: swift.GetLanguage()},
	".tf":     {language: "HCL", grammar: hcl.GetLanguage()},
	".tfvars": {language: "HCL", grammar: hcl.GetLanguage()},
	".ts":     {language: "TypeScript", grammar: treesitterts.GetLanguage()},
	".tsx":    {language: "TypeScript", grammar: treesittertsx.GetLanguage()},
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
	src := []byte(content)
	root, err := sitter.ParseCtx(context.Background(), src, spec.grammar)
	if err != nil || root == nil || root.IsNull() {
		detail := "tree-sitter parse failed"
		if err != nil {
			detail = err.Error()
		}
		return nil, spec.language, ParseStatus{ParseError: true, Detail: detail}
	}

	var entities []Entity
	walkEntities(root, src, "", &entities)
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

func walkEntities(node *sitter.Node, src []byte, scope string, entities *[]Entity) {
	if !validNode(node) {
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
		walkEntities(node.NamedChild(i), src, childScope, entities)
	}
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
		name = nodeName(node, src)
	case "create_function":
		kind = "function"
		name = nodeName(node, src)
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
	case "class", "interface", "message", "module", "service", "struct", "trait":
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
