package sem

import "testing"

// A nested field assignment (`a.cfg.value = 1`) matches at the intermediate
// chain segment `cfg`, whose true receiver is `a.cfg`. Emitting {cfg, value}
// could resolve against a same-named but differently-typed local, so nested
// setter assignments must produce no receiver call (recall loss OK; precision
// first). A flat `request.bodyFields = body` still yields the setter call.
func TestDartSetterAssignmentRejectsNestedReceiver(t *testing.T) {
	if got := dartSetterAssignmentCalls("a.cfg.value = 1"); len(got) != 0 {
		t.Fatalf("nested field assignment must emit no receiver call, got %#v", got)
	}
	// Optional-chained and cascade-nested receivers are rejected the same way.
	for _, src := range []string{"a?.cfg.value = 1", "a..cfg.value = 1", "a . cfg . value = 1"} {
		if got := dartSetterAssignmentCalls(src); len(got) != 0 {
			t.Fatalf("nested receiver %q must emit no receiver call, got %#v", src, got)
		}
	}

	flat := dartSetterAssignmentCalls("request.bodyFields = body")
	if len(flat) != 1 || flat[0].Receiver != "request" || flat[0].Method != "bodyFields" {
		t.Fatalf("flat property assignment should emit {request, bodyFields}, got %#v", flat)
	}
	if !flat[0].SetterAssign {
		t.Fatalf("property-assignment call must be marked SetterAssign, got %#v", flat[0])
	}
}

// A read-write property declares a same-named getter and setter. Both are Dart
// `method` symbols with the same short name, so the container's method index
// collapses to whichever is declared last. A property-assignment call invokes
// the SETTER, so it must resolve to the setter accessor regardless of
// declaration order — not to the getter about half the time.
func TestDartReadWritePropertyAssignmentResolvesToSetter(t *testing.T) {
	// setter declared first (getter last) is the order that previously mis-resolved
	// to the getter; the getter-first order is the determinism control.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"setter-first", "  set value(int v) {}\n  int get value => 0;\n"},
		{"getter-first", "  int get value => 0;\n  set value(int v) {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "lib/box.dart", "class Box {\n"+tc.body+"}\n\n"+
				"void go() {\n  var b = Box();\n  b.value = 1;\n}\n")

			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}
			setterID := ""
			for _, s := range snapshot.Symbols {
				if s.Name == "value" && dartIsSetterAccessor(s.Signature, "value") {
					setterID = s.ID
				}
			}
			if setterID == "" {
				t.Fatal("no setter symbol found for property value")
			}
			edges := runCallsFrom(snapshot, "go")
			if len(edges) != 1 {
				t.Fatalf("expected exactly one CALLS edge from go, got %#v", edges)
			}
			if edges[0].ToID != setterID {
				t.Fatalf("property-assignment must resolve to the setter %s, got %s", setterID, edges[0].ToID)
			}
		})
	}
}
