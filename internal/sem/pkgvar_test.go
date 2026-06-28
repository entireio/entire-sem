package sem

import "testing"

func TestCollectPackageVarTypes(t *testing.T) {
	content := `package zerolog

import "example.com/m/internal/json"

var (
	_   encoder = (*json.Encoder)(nil)
	enc         = json.Encoder{}
)

var solo = &json.Encoder{}

func useEnc() {
	local := json.Encoder{} // inside a func body: must NOT be collected as a package var
	_ = local
}
`
	got := collectPackageVarTypes(content)
	if qt, ok := got["enc"]; !ok || qt.alias != "json" || qt.typeName != "Encoder" {
		t.Fatalf("enc: got %+v ok=%v", got["enc"], ok)
	}
	if qt, ok := got["solo"]; !ok || qt.alias != "json" || qt.typeName != "Encoder" {
		t.Fatalf("solo: got %+v ok=%v", got["solo"], ok)
	}
	if _, ok := got["local"]; ok {
		t.Fatalf("function-body var must not be collected as a package var")
	}
}

func TestResolveQualifiedType(t *testing.T) {
	jsonEnc := SymbolRecord{ID: "m:Go:internal/json/enc.go:type:Encoder", Name: "Encoder", Kind: "type", FilePath: "internal/json/enc.go"}
	cborEnc := SymbolRecord{ID: "m:Go:internal/cbor/enc.go:type:Encoder", Name: "Encoder", Kind: "type", FilePath: "internal/cbor/enc.go"}
	idx := map[string][]SymbolRecord{"Encoder": {jsonEnc, cborEnc}}

	// json.Encoder must resolve to the Encoder in the json/ directory, not cbor's.
	got, ok := resolveQualifiedType(pkgQualType{alias: "json", typeName: "Encoder"}, idx)
	if !ok || got.ID != jsonEnc.ID {
		t.Fatalf("expected json Encoder, got %+v ok=%v", got, ok)
	}

	// An alias matching no package directory resolves to nothing (not a wrong guess).
	if _, ok := resolveQualifiedType(pkgQualType{alias: "msgpack", typeName: "Encoder"}, idx); ok {
		t.Fatalf("unknown alias must not resolve")
	}
}
