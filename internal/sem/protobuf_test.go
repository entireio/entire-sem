package sem

import (
	"strings"
	"testing"
)

func TestProtocolBuffersProto2WithoutSyntaxParsesLegacyGroups(t *testing.T) {
	content := `package protoexample;

enum FOO {X=17;};

message Test {
   required string label = 1;
   optional int32 type = 2[default=77];
   repeated int64 reps = 3;
   optional group OptionalGroup = 4{
     required string RequiredField = 5;
   }
}
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("test.proto", content)
	if language != "Protocol Buffers" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("valid proto2 reported a parse failure: %+v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for name, kind := range map[string]string{
		"FOO":           "enum",
		"Test":          "message",
		"OptionalGroup": "message",
	} {
		entity, ok := seen[name]
		if !ok {
			t.Fatalf("missing %s entity %q in %#v", kind, name, entities)
		}
		if entity.Kind != kind {
			t.Fatalf("%s kind = %q, want %q", name, entity.Kind, kind)
		}
	}
	if seen["FOO"].StartLine != 3 || seen["Test"].StartLine != 5 || seen["OptionalGroup"].StartLine != 9 {
		t.Fatalf("synthetic syntax line leaked into source locations: %#v", entities)
	}
	if !strings.Contains(seen["OptionalGroup"].Signature, "optional group OptionalGroup") {
		t.Fatalf("group signature was rewritten instead of preserving source: %q", seen["OptionalGroup"].Signature)
	}
}

func TestProtocolBuffersExplicitProto2PreservesSourceAndRPCs(t *testing.T) {
	content := `syntax = "proto2";
package auth;
message Request { required string token = 1; }
message Reply { optional bool valid = 1 [default = true]; }
service Auth { rpc Validate(Request) returns (Reply); }
`
	parseSource, entitySource, offset := prepareProtocolBuffersParseSource(content)
	if offset != 0 || entitySource != content {
		t.Fatalf("explicit proto2 source binding changed: offset=%d source=%q", offset, entitySource)
	}
	if !strings.Contains(parseSource, `syntax = "proto3";`) || !strings.Contains(parseSource, "repeated string token") {
		t.Fatalf("proto3 compatibility view missing expected substitutions: %q", parseSource)
	}
	entities, _, status := TreeSitterParser{}.ParseWithStatus("auth.proto", content)
	if status.ParseError {
		t.Fatalf("valid explicit proto2 reported a parse failure: %+v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for name, kind := range map[string]string{
		"Request":       "message",
		"Reply":         "message",
		"Auth":          "service",
		"Auth.Validate": "rpc",
	} {
		if seen[name].Kind != kind {
			t.Fatalf("%s = %#v, want kind %s (all=%#v)", name, seen[name], kind, entities)
		}
	}
	wantRequestBody := "message Request { required string token = 1; }"
	if seen["Request"].BodyHash != hash(normalize(wantRequestBody)) {
		t.Fatalf("original proto2 body was rewritten: hash=%q want=%q", seen["Request"].BodyHash, hash(normalize(wantRequestBody)))
	}
}

func TestProtocolBuffersCompatibilityDoesNotRewriteCommentsOrStringOptions(t *testing.T) {
	content := `syntax = "proto2";
// optional group CommentOnly = 7 { required string nope = 1; }
message Note {
  optional string text = 1 [default = "required optional group"];
}
`
	parseSource, _, _ := prepareProtocolBuffersParseSource(content)
	if !strings.Contains(parseSource, "optional group CommentOnly") {
		t.Fatalf("comment text was rewritten: %q", parseSource)
	}
	if !strings.Contains(parseSource, `"required optional group"`) {
		t.Fatalf("string option was rewritten: %q", parseSource)
	}
	_, _, status := TreeSitterParser{}.ParseWithStatus("note.proto", content)
	if status.ParseError {
		t.Fatalf("valid proto2 string option reported a parse failure: %+v", status)
	}
}

func TestProtocolBuffersCompatibilityStillReportsMalformedProto2(t *testing.T) {
	content := `syntax = "proto2";
message Broken {
  required string value = 1;
`
	_, _, status := TreeSitterParser{}.ParseWithStatus("broken.proto", content)
	if !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("malformed proto2 failure = %+v, want visible E_PARSE_ERROR", status)
	}
}
