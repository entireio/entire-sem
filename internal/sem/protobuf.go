package sem

// The tree-sitter-protobuf grammar bundled by go-tree-sitter is a proto3-only
// grammar: it requires a `syntax` declaration, accepts only the "proto3"
// literal, and rejects the proto2 `required`/`optional` labels and `group`
// declarations. Proto2 remains common in long-lived production repositories,
// where omitting `syntax` means proto2 by specification.
//
// prepareProtocolBuffersParseSource builds a position-preserving proto3 view
// for tree-sitter while keeping a parallel source view containing the original
// bytes. The parser consumes the first string; entity extraction consumes the
// second. Replacements therefore improve grammar compatibility without
// rewriting signatures, body hashes, or source locations. When a synthetic
// syntax line is needed, both views receive it and callers subtract lineOffset
// from extracted locations.

import (
	"regexp"
	"strings"
)

const protobufSyntheticSyntax = "syntax = \"proto3\";\n"

var (
	protobufSyntaxDeclarationPattern = regexp.MustCompile(`(?m)^[\t ]*syntax[\t ]*=`)
	protobufProto2LiteralPattern     = regexp.MustCompile(`"proto2"`)
	protobufFieldLabelPattern        = regexp.MustCompile(`\b(?:required|optional)\b`)
	protobufGroupPattern             = regexp.MustCompile(`\b(?:required|optional|repeated)[\t ]+group[\t ]+([A-Za-z_][A-Za-z0-9_]*)[\t ]*=[\t ]*[0-9]+[\t ]*\{`)
)

func prepareProtocolBuffersParseSource(content string) (parseSource, entitySource string, lineOffset int) {
	entitySource = content
	if !protobufSyntaxDeclarationPattern.MatchString(stripCodeLiteralsAndComments(content)) {
		entitySource = protobufSyntheticSyntax + content
		lineOffset = 1
	}

	parseBytes := []byte(entitySource)
	structure := stripCodeLiteralsAndComments(entitySource)
	if syntax := protobufSyntaxDeclarationPattern.FindStringIndex(structure); syntax != nil {
		lineEnd := strings.IndexByte(entitySource[syntax[0]:], '\n')
		if lineEnd < 0 {
			lineEnd = len(entitySource) - syntax[0]
		}
		line := parseBytes[syntax[0] : syntax[0]+lineEnd]
		if literal := protobufProto2LiteralPattern.FindIndex(line); literal != nil {
			copy(line[literal[0]:literal[1]], `"proto3"`)
		}
	}

	// Proto2 field labels are all eight bytes long, as is `repeated`, so the
	// grammar-compatible substitution leaves every later byte at its original
	// offset. The original label remains visible in entitySource.
	for _, bounds := range protobufFieldLabelPattern.FindAllStringIndex(structure, -1) {
		copy(parseBytes[bounds[0]:bounds[1]], "repeated")
	}

	// A proto2 group is structurally a nested message with an implicit field.
	// Present the declaration as `message <Name> {` while preserving the name
	// and opening-brace offsets. Entity extraction then emits the original group
	// declaration as a message-shaped container rather than dropping the whole
	// file as unparseable.
	for _, bounds := range protobufGroupPattern.FindAllStringSubmatchIndex(structure, -1) {
		start, end := bounds[0], bounds[1]
		nameStart, nameEnd := bounds[2], bounds[3]
		braceRelative := strings.LastIndexByte(structure[start:end], '{')
		if braceRelative < 0 || start+len("message") > nameStart {
			continue
		}
		brace := start + braceRelative
		for index := start; index < nameStart; index++ {
			parseBytes[index] = ' '
		}
		copy(parseBytes[start:start+len("message")], "message")
		for index := nameEnd; index < brace; index++ {
			parseBytes[index] = ' '
		}
	}

	return string(parseBytes), entitySource, lineOffset
}
