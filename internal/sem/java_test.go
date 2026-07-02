package sem

import (
	"reflect"
	"strings"
	"testing"
)

// Java call idioms the generic scanners miss (evidence: on square/retrofit the
// focus method Retrofit.Builder.build resolved 0/5 inbound and 1/3 outbound
// CALLS edges): nested-type fluent constructor chains
// (`new Retrofit.Builder().baseUrl(...).build()`) and declared-type locals
// (`BuiltInFactories b = Platform.builtInFactories;`).
func TestJavaBuilderChainAndTypedLocalCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "retrofit/Retrofit.java", `package retrofit2;

public final class Retrofit {
  public static final class Builder {
    public Builder baseUrl(String baseUrl) {
      return this;
    }

    public Builder addConverterFactory(Factory factory) {
      return this;
    }

    public Retrofit build() {
      Executor callbackExecutor = this.callbackExecutor;
      BuiltInFactories builtInFactories = Platform.builtInFactories;
      List<Factory> defaultCallAdapterFactories =
          builtInFactories.createDefaultCallAdapterFactories(callbackExecutor);
      List<Factory> defaultConverterFactories =
          builtInFactories.createDefaultConverterFactories();
      return new Retrofit();
    }
  }
}
`)
	writeFile(t, repo, "retrofit/BuiltInFactories.java", `package retrofit2;

class BuiltInFactories {
  List<Factory> createDefaultCallAdapterFactories(Executor callbackExecutor) {
    return null;
  }

  List<Factory> createDefaultConverterFactories() {
    return null;
  }
}
`)
	writeFile(t, repo, "retrofit/Platform.java", `package retrofit2;

final class Platform {
  static final BuiltInFactories builtInFactories = new BuiltInFactories();
}
`)
	// A same-named nested Builder in another outer class: the spelled
	// `new Retrofit.Builder()` qualifier must pick the right one.
	writeFile(t, repo, "samples/AnnotatedConverterFactory.java", `package samples;

public class AnnotatedConverterFactory {
  public static final class Builder {
    public Builder add(Class<?> cls) {
      return this;
    }

    public AnnotatedConverterFactory create() {
      return new AnnotatedConverterFactory();
    }
  }
}
`)
	writeFile(t, repo, "samples/SimpleService.java", `package samples;

public final class SimpleService {
  public static void main(String... args) {
    Retrofit retrofit =
        new Retrofit.Builder()
            .baseUrl(API_URL)
            .addConverterFactory(GsonConverterFactory.create())
            .build();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}
	// Fluent chain: every chained method resolves against the constructed
	// nested type, including the terminal build() past nested call arguments.
	for _, target := range []string{"Builder.baseUrl", "Builder.addConverterFactory", "Builder.build"} {
		r, ok := calls["SimpleService.main->"+target]
		if !ok {
			t.Fatalf("fluent constructor chain call to %s not resolved: %#v", target, calls)
		}
		if r.Reason != "method call resolved via fluent constructor chain type" {
			t.Fatalf("chain call to %s resolved through %q", target, r.Reason)
		}
		if to := findSymbolByID(snapshot.Symbols, r.ToID); to.FilePath != "retrofit/Retrofit.java" {
			t.Fatalf("chain call to %s resolved into %s, want retrofit/Retrofit.java", target, to.FilePath)
		}
	}
	// Declared-type local receiver.
	for _, target := range []string{"BuiltInFactories.createDefaultCallAdapterFactories", "BuiltInFactories.createDefaultConverterFactories"} {
		if _, ok := calls["Builder.build->"+target]; !ok {
			t.Fatalf("typed-local receiver call to %s not resolved: %#v", target, calls)
		}
	}
}

func findSymbolByID(symbols []SymbolRecord, id string) SymbolRecord {
	for _, s := range symbols {
		if s.ID == id {
			return s
		}
	}
	return SymbolRecord{}
}

func TestJavaLocalVarTypes(t *testing.T) {
	block := `{
      Executor callbackExecutor = this.callbackExecutor;
      final BuiltInFactories builtInFactories = Platform.builtInFactories;
      List<CallAdapter.Factory> callAdapterFactories = new ArrayList<>(this.callAdapterFactories);
      okhttp3.Call.Factory callFactory = this.callFactory;
      int count = 3;
      String message = "Type decoy = fake;";
      Executor other;
      if (ready) other = fallback;
      Conflicted a = one();
      Different a = two();
    }`
	got := javaLocalVarTypes(block)
	want := map[string]string{
		"callbackExecutor":     "Executor",
		"builtInFactories":     "BuiltInFactories",
		"callAdapterFactories": "List",
		"other":                "Executor",
		"message":              "String",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("javaLocalVarTypes = %#v, want %#v", got, want)
	}
}

func TestJavaConstructorChainCalls(t *testing.T) {
	block := `{
    Retrofit retrofit =
        new Retrofit.Builder()
            .baseUrl(server.url("/"))
            .addConverterFactory(new AnnotatedConverterFactory.Builder().add(Moshi.class).create())
            .build();
    Plain p = new Plain().first();
  }`
	chains := javaConstructorChainCalls(block)
	byDetail := map[string]javaCtorChainCall{}
	for _, chain := range chains {
		byDetail[chain.Qualifier+"."+chain.TypeName] = chain
	}
	outer, ok := byDetail["Retrofit.Builder"]
	if !ok || !reflect.DeepEqual(outer.Methods, []string{"baseUrl", "addConverterFactory", "build"}) {
		t.Fatalf("outer chain wrong: %#v", chains)
	}
	inner, ok := byDetail["AnnotatedConverterFactory.Builder"]
	if !ok || !reflect.DeepEqual(inner.Methods, []string{"add", "create"}) {
		t.Fatalf("inner chain wrong: %#v", chains)
	}
	plain, ok := byDetail[".Plain"]
	if !ok || plain.Qualifier != "" || !reflect.DeepEqual(plain.Methods, []string{"first"}) {
		t.Fatalf("plain chain wrong: %#v", chains)
	}
	if strings.Contains(block, "\t") {
		t.Fatal("unexpected tab")
	}
}
