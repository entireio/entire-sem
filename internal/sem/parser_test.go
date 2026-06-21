package sem

import (
	"strings"
	"testing"
)

func TestTreeSitterParserPythonEntities(t *testing.T) {
	input := `class Token:
    pass

def validate_token(token: str) -> bool:
    return bool(token)

async def refresh_token(token):
    return token
`
	entities, language := TreeSitterParser{}.Parse("auth.py", input)
	if language != "Python" {
		t.Fatalf("language = %q", language)
	}
	if len(entities) != 3 {
		t.Fatalf("entities = %#v", entities)
	}
	if entities[0].Kind != "class" || entities[0].Name != "Token" {
		t.Fatalf("first entity = %#v", entities[0])
	}
	if entities[1].Kind != "function" || entities[1].Name != "validate_token" {
		t.Fatalf("second entity = %#v", entities[1])
	}
	if entities[2].Kind != "function" || entities[2].Name != "refresh_token" {
		t.Fatalf("third entity = %#v", entities[2])
	}
}

func TestCompareSignatureBodyRenameAddRemove(t *testing.T) {
	before, _ := TreeSitterParser{}.Parse("auth.py", `def validate_token(token):
    return bool(token)

def old_name(value):
    return value + 1

def removed():
    return False
`)
	after, _ := TreeSitterParser{}.Parse("auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)

def new_name(value):
    return value + 1

def added():
    return True
`)
	changes := Compare(before, after)
	seen := map[string]bool{}
	for _, change := range changes {
		seen[change.Type+":"+change.Name] = true
		if change.Type == "renamed" {
			seen["renamed:"+change.OldName+"->"+change.NewName] = true
		}
	}
	for _, want := range []string{
		"signature_changed:validate_token",
		"renamed:old_name->new_name",
		"removed:removed",
		"added:added",
	} {
		if !seen[want] {
			t.Fatalf("missing %s in %#v", want, changes)
		}
	}
}

func TestTreeSitterParserMultiLanguageEntities(t *testing.T) {
	tests := []struct {
		path     string
		input    string
		language string
		names    []string
	}{
		{
			path:     "main.go",
			language: "Go",
			input: `package main
type User struct { Name string }
func (u User) Validate(value string) bool { return value != "" }
func Format() {}
`,
			names: []string{"User", "User.Validate", "Format"},
		},
		{
			path:     "app.ts",
			language: "TypeScript",
			input: `interface Foo { value: string }
type Bar = string
class User { validate(value: string) { return value } }
const build = (value: number) => value + 1
`,
			names: []string{"Foo", "Bar", "User", "User.validate", "build"},
		},
		{
			path:     "lib.rs",
			language: "Rust",
			input: `pub struct User { name: String }
pub fn validate(value: &str) -> bool { true }
trait Run { fn run(&self); }
`,
			names: []string{"User", "validate", "Run", "Run.run"},
		},
		{
			path:     ".github/workflows/ci.yml",
			language: "YAML",
			input: `name: CI
on:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
  deploy:
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    steps:
      - run: ./scripts/deploy.sh
`,
			names: []string{"ci", "on", "permissions", "jobs.test", "jobs.deploy"},
		},
	}

	for _, tt := range tests {
		entities, language := TreeSitterParser{}.Parse(tt.path, tt.input)
		if language != tt.language {
			t.Fatalf("%s language = %q", tt.path, language)
		}
		seen := map[string]bool{}
		for _, entity := range entities {
			seen[entity.Name] = true
		}
		for _, name := range tt.names {
			if !seen[name] {
				t.Fatalf("%s missing entity %q in %#v", tt.path, name, entities)
			}
		}
	}
}

func TestTreeSitterParserTypeScriptExportedFactoryVariables(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("slice.ts", `function buildCreateSlice() {
  return function createSlice() {}
}

export const createSlice = buildCreateSlice()
const internalSlice = buildCreateSlice()
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	createSlice, ok := seen["createSlice"]
	if !ok {
		t.Fatalf("missing exported factory variable in %#v", entities)
	}
	if createSlice.Kind != "variable" {
		t.Fatalf("createSlice kind = %q, want variable", createSlice.Kind)
	}
	if _, ok := seen["internalSlice"]; ok {
		t.Fatalf("non-exported factory variable was promoted: %#v", entities)
	}
}

func TestTreeSitterParserJavaScriptAssignmentMethodEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("application.js", `var app = exports = module.exports = {};

app.init = function init() {
  this.ready = true;
};

res.json = function json(obj) {
  return this.send(obj);
};
`)
	if language != "JavaScript" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for _, name := range []string{"app.init", "res.json"} {
		entity, ok := seen[name]
		if !ok {
			t.Fatalf("missing assignment method %q in %#v", name, entities)
		}
		if entity.Kind != "method" {
			t.Fatalf("%s kind = %q, want method", name, entity.Kind)
		}
		if entity.EndLine <= entity.StartLine {
			t.Fatalf("%s should span its function body: %#v", name, entity)
		}
	}
}

func TestTreeSitterParserTypeScriptExportedVariablesSurviveParseRecovery(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("slice.ts", `type Broken = <

export const createSlice = /* @__PURE__ */ buildCreateSlice()
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if !status.ParseError {
		t.Fatalf("expected parse recovery status")
	}
	for _, entity := range entities {
		if entity.Name == "createSlice" && entity.Kind == "variable" {
			return
		}
	}
	t.Fatalf("missing exported variable after parse recovery: %#v", entities)
}

func TestTreeSitterParserParseErrorDetailIncludesLocation(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("slice.ts", `type Broken = <

export const createSlice = /* @__PURE__ */ buildCreateSlice()
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if !status.ParseError {
		t.Fatalf("expected parse recovery status")
	}
	if !strings.Contains(status.Detail, "line ") || !strings.Contains(status.Detail, "near ") {
		t.Fatalf("parse detail should include location and snippet, got %q", status.Detail)
	}
}

func TestTreeSitterParserTypeScriptMasksGeneratedKeywordProperties(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("types.generated.ts", `export type IdQueryType = {
  equals?: Maybe<Scalars['ID']>
  in?: Maybe<Scalars['ID']>
  notIn?: Maybe<Scalars['ID']>
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected type entity from generated type")
	}
}

func TestTreeSitterParserTypeScriptMasksStaticAccessorMethod(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("index.d.ts", `export class AxiosHeaders {
  static from(thing?: AxiosHeaders | string): AxiosHeaders;
  static accessor(header: string | string[]): AxiosHeaders;
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	for _, entity := range entities {
		if entity.Name == "AxiosHeaders" && entity.Kind == "class" {
			return
		}
	}
	t.Fatalf("missing class entity after masking static accessor method: %#v", entities)
}

func TestTreeSitterParserTypeScriptMasksGenericCallableTypeSignatures(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("callable.ts", `export interface TakePattern<State> {
  <Predicate extends AnyListenerPredicate<State>>(
    predicate: Predicate,
  ): TakePatternOutputWithoutTimeout<State, Predicate>
  <Predicate extends AnyListenerPredicate<State>>(
    predicate: Predicate,
    timeout: number,
  ): TakePatternOutputWithTimeout<State, Predicate>
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptMasksCallableSelectorReturnSignatures(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("combineSlices.ts", `export interface CombinedSliceReducer<DeclaredState, InitialState> {
  selector: {
    <Selector extends (state: DeclaredState, ...args: any[]) => unknown>(
      selectorFn: Selector,
    ): (
      state: WithOptionalProp<
        Parameters<Selector>[0],
        Exclude<keyof DeclaredState, keyof InitialState>
      >,
      ...args: Tail<Parameters<Selector>>
    ) => ReturnType<Selector>
  }
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptMasksNamedGenericTypeMembers(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("createAsyncThunk.ts", `type CreateAsyncThunk<CurriedThunkApiConfig extends AsyncThunkConfig> =
  CreateAsyncThunkFunction<CurriedThunkApiConfig> & {
    withTypes<ThunkApiConfig extends AsyncThunkConfig>(): CreateAsyncThunk<
      OverrideThunkApiConfigs<CurriedThunkApiConfig, ThunkApiConfig>
    >
  }
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptMasksAdjacentGenericCallableTypeAliases(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("createAsyncThunk.ts", `export type CreateAsyncThunkFunction<
  CurriedThunkApiConfig extends AsyncThunkConfig,
> = {
  <Returned, ThunkArg = void>(
    typePrefix: string,
    payloadCreator: AsyncThunkPayloadCreator<
      Returned,
      ThunkArg,
      CurriedThunkApiConfig
    >,
    options?: AsyncThunkOptions<ThunkArg, CurriedThunkApiConfig>,
  ): AsyncThunk<Returned, ThunkArg, CurriedThunkApiConfig>

  <Returned, ThunkArg, ThunkApiConfig extends AsyncThunkConfig>(
    typePrefix: string,
    payloadCreator: AsyncThunkPayloadCreator<
      Returned,
      ThunkArg,
      OverrideThunkApiConfigs<CurriedThunkApiConfig, ThunkApiConfig>
    >,
    options?: AsyncThunkOptions<
      ThunkArg,
      OverrideThunkApiConfigs<CurriedThunkApiConfig, ThunkApiConfig>
    >,
  ): AsyncThunk<
    Returned,
    ThunkArg,
    OverrideThunkApiConfigs<CurriedThunkApiConfig, ThunkApiConfig>
  >
}

type CreateAsyncThunk<CurriedThunkApiConfig extends AsyncThunkConfig> =
  CreateAsyncThunkFunction<CurriedThunkApiConfig> & {
    withTypes<ThunkApiConfig extends AsyncThunkConfig>(): CreateAsyncThunk<
      OverrideThunkApiConfigs<CurriedThunkApiConfig, ThunkApiConfig>
    >
  }
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptMasksNestedGenericCallableOverloads(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("listenerMiddleware/types.ts", `export type AddListenerOverloads<Return, AdditionalOptions = unknown> = {
  <
    MiddlewareActionType extends UnknownAction,
    ListenerPredicateType extends ListenerPredicate<MiddlewareActionType>,
  >(
    options: {
      predicate: ListenerPredicateType
      effect: ListenerEffect<
        ListenerPredicateGuardedActionType<ListenerPredicateType>,
        StateType,
        DispatchType,
        ExtraArgument
      >
    } & AdditionalOptions,
  ): Return

  <ActionCreatorType extends TypedActionCreatorWithMatchFunction<any>>(
    options: {
      actionCreator: ActionCreatorType
      effect: ListenerEffect<
        ReturnType<ActionCreatorType>,
        StateType,
        DispatchType,
        ExtraArgument
      >
    } & AdditionalOptions,
  ): Return
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptDoesNotMaskRuntimeGenericArrows(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("runtime.ts", `export const prepareAutoBatched =
  <T>() =>
  (payload: T): { payload: T; meta: unknown } => ({
    payload,
    meta: {},
  })

const memoizeSpy = vi.fn(
  <F extends (...args: any[]) => any>(fn: F, param?: boolean) => fn,
)
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserTypeScriptDoesNotMaskTemplateHTML(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("docusaurus.config.ts", "const config = {\n  html: `\n    <a href=\"https://www.netlify.com\">\n      <img src=\"badge.svg\" />\n    </a>\n  `,\n}\n")
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserJavaMasksAnnotatedVarargs(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("ClassUtils.java", `package org.junit.platform.commons.util;

class ClassUtils {
  public static String nullSafeToString(@Nullable Class<?> @Nullable... classes) {
    return "";
  }
}
`)
	if language != "Java" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	for _, entity := range entities {
		if entity.Name == "ClassUtils.nullSafeToString" && entity.Kind == "method" {
			return
		}
	}
	t.Fatalf("missing method entity after masking annotated varargs: %#v", entities)
}

func TestTreeSitterParserJavaMasksModuleImports(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("JUnitRunModule.java", `package p;

import module org.junit.jupiter.api;

class MultiplicationTests {
  @Test
  void multiplication() {
    Assertions.assertEquals(4, 2 * 2);
  }
}
`)
	if language != "Java" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	for _, entity := range entities {
		if entity.Name == "MultiplicationTests.multiplication" && entity.Kind == "method" {
			return
		}
	}
	t.Fatalf("missing method entity after masking module import: %#v", entities)
}

func TestTreeSitterParserGroovyMasksQuotedMethodNames(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("GroovyAssertEqualsTests.groovy", `package org.junit.jupiter.api

class GroovyAssertEqualsTests {
    @Test
    void "null references can be passed to assertEquals"() {
        assert true
    }

    def "regular"() {
        expect:
        true
    }
}
`)
	if language != "Groovy" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected entities after masking quoted Groovy methods")
	}
}

func TestTreeSitterParserGroovyMasksJavaStyleCasts(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("PrimitiveAndWrapperTypeHelpers.groovy", `package org.junit.jupiter.api

class PrimitiveAndWrapperTypeHelpers {
    static char c(int number) {
        return (char) number
    }

    static Character C(int number) {
        return Character.valueOf((char) number)
    }
}
`)
	if language != "Groovy" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected entities after masking Groovy Java-style casts")
	}
}

func TestTreeSitterParserKotlinMasksSuspendLambdaInvocation(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("KotlinAssertionsTests.kt", `import kotlinx.coroutines.runBlocking

fun expectedContextExceptionTesting() =
    runBlocking<Unit> {
        assertThrows<AssertionError>("Should fail async") {
            suspend { fail("Should fail async") }()
        }
    }
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected entities after masking Kotlin suspend lambda invocation")
	}
}

func TestTreeSitterParserKotlinMasksMultiDollarStrings(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("GenerateJreRelatedSourceCode.kt", `fun render(year: String): String {
    return licenseHeader.replace($$"$YEAR", year)
}
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserKotlinMasksGradleListOptionAssignment(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("documentation.gradle.kts", `tasks.withType<Javadoc>().configureEach {
    (this as StandardJavadocDocletOptions).apply {
        addMultilineStringsOption("tag").value = listOf(
            "apiNote:a:API Note:",
            "implNote:a:Implementation Note:"
        )
        use(true)
    }
}
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserKotlinMasksGradleOptionValueMapAssignment(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("documentation.gradle.kts", `tasks.withType<Javadoc>().configureEach {
    (this as StandardJavadocDocletOptions).apply {
        addStringsOption("-module", ",").value = modularProjects.map { it.javaModuleName }
        use(true)
    }
}
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserKotlinMasksGradleWhenGetOrElse(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("junitbuild.publishing-conventions.gradle.kts", `val jupiterProjects = listOf<Project>()

group = buildParameters.publishing.group
    .getOrElse(when (project) {
        in jupiterProjects -> "org.junit.jupiter"
        else -> "org.junit"
    })
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserYAMLMasksAntoraPlaceholders(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("antora-playbook.yml", `antora:
  extensions:
  - '@antora/collector-extension'
content:
  sources:
  - url: @GIT_REPO_ROOT@
    branches: @GIT_BRANCH_NAME@
ui:
  supplemental_files:
    - path: css/vendor/tabs.css
      contents: @GIT_REPO_ROOT@/documentation/node_modules/@asciidoctor/tabs/dist/css/tabs.css
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected YAML section entities after masking Antora placeholders")
	}
}

func TestTreeSitterParserDetectsCPlusPlusHeaders(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("args.h", `#ifndef FMT_ARGS_H_
#define FMT_ARGS_H_

#include <memory>

namespace detail {
template <typename T> struct is_reference_wrapper : std::false_type {};

class dynamic_arg_list {
 public:
  template <typename T, typename Arg> auto push(const Arg& arg) -> const T&;
};
}
#endif
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected C++ header entities")
	}
}

func TestTreeSitterParserCPlusPlusMasksFmtMacros(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("format.cc", `#if __has_include(<cxxabi.h>)
#  include <cxxabi.h>
#endif

FMT_PRAGMA_GCC(push_options)
FMT_BEGIN_NAMESPACE

template <typename T> auto unwrap(const T& v) -> const T& { return v; }

FMT_BEGIN_EXPORT
enum class color : uint32_t { red };
FMT_END_EXPORT

FMT_END_NAMESPACE
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusMasksAnnotationMacros(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("base.h", `#if FMT_CPLUSPLUS > 201703L && FMT_HAS_INCLUDE(<version>)
#  include <version>
#endif

class FMT_API format_error : public std::runtime_error {
 public:
  FMT_CONSTEXPR explicit format_error(const char* message) : std::runtime_error(message) {}
};

template <typename T>
FMT_CONSTEXPR20 auto unwrap(const T& v) -> const T& { return v; }

class GTEST_API_ ScopedFakeTestPartResultReporter
    : public TestPartResultReporterInterface {};
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected C++ entities after masking annotation macros")
	}
}

func TestTreeSitterParserCPlusPlusMasksComplexPreprocessorForms(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("os.h", `#  if FMT_HAS_INCLUDE(<xlocale.h>)
#    include <xlocale.h>
#  endif

#  define FMT_ASSERT(condition, message)                                    \
    ((condition) ? void() : report_error(message))

inline auto get() -> int { return 1; }
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusMasksFunctionLikeMacros(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("format.h", `template <typename To, typename From, FMT_ENABLE_IF(sizeof(To) > sizeof(From))>
auto convert(From value) -> To {
  return static_cast<To>(value);
}

template <typename OutputIt,
          FMT_ENABLE_IF(is_back_insert_iterator<OutputIt>::value&&
                            is_contiguous<typename OutputIt::container>::value)>
auto reserve(OutputIt it, size_t n) -> typename OutputIt::value_type* {
  return nullptr;
}

class FMT_SO_VISIBILITY("default") format_error : public std::runtime_error {
};

class basic_memory_buffer {
  FMT_NO_UNIQUE_ADDRESS Allocator alloc_;
};

extern "C" __declspec(dllimport) int __stdcall WriteConsoleW(void*);
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusMasksTestDeclarationMacros(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("base-test.cc", `template <typename T> struct mock_buffer final : fmt::detail::buffer<T> {
  MOCK_METHOD(size_t, do_grow, (size_t));
  static void grow(fmt::detail::buffer<T>& buf, size_t capacity) {}
};

#define VISIT_TYPE(type_, visit_type_) \
  template <> struct visit_type<type_> { using type = visit_type_; }
VISIT_TYPE(signed char, int);

GMOCK_DECLARE_KIND_(bool, kBool);

TEST(buffer_test, indestructible) {
  static_assert(true, "ok");
}
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusMasksFmtExceptionAndCasts(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("format-inl.h", `void format_system_error(int error_code) {
  FMT_TRY {
    auto ec = std::error_code(error_code, std::generic_category());
  }
  FMT_CATCH(...) {}
}

void set_fill_size(size_t size) {
  data_ = (data_ & ~fill_size_mask) | (unsigned(size) << fill_size_shift);
}
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusMasksMoreFmtForms(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("base.h", `FMT_TYPE_CONSTANT(int, int_type)
FMT_FORMAT_AS(signed char, int);

enum : unsigned {
  type_mask = 0x00007,
};

extern template FMT_API auto thousands_sep_impl<char>(locale_ref)
    -> thousands_sep_result<char>;

void operator_call(bool value) {
  operator()<bool>(value);
}

void vformat_to(locale_ref loc = {}) {}

auto read(scan_iterator it, T& value, const format_specs& specs = {})
    -> scan_iterator {
  return it;
}

auto write(OutputIt out, monostate, format_specs = {}, locale_ref = {})
    -> OutputIt {
  return out;
}

export module fmt;

void inline_try() {
  FMT_TRY { flush(); }
  FMT_CATCH(...) {}
}

template <typename, typename OutputIt> void write(OutputIt, foo) = delete;

struct derived {
  static auto get(const T& v) -> all {
    return {v.*(&getter::c)};
  }
};

template <typename Tuple, FMT_ENABLE_IF(is_tuple_like<Tuple>::value)>
auto join(const Tuple& tuple FMT_LIFETIMEBOUND, string_view sep) -> int {
  return 0;
}

class max_size_allocator {
 public:
  using typename Allocator::value_type;
};
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserCPlusPlusExtractsTrailingReturnTemplateFunction(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("format.h", `template <size_t SIZE>
FMT_NODISCARD auto to_string(const basic_memory_buffer<char, SIZE>& buf)
    -> std::string {
  return {buf.data(), buf.size()};
}
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	found := false
	for _, entity := range entities {
		if entity.Kind == "function" && entity.Name == "to_string" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected to_string function entity, got %#v", entities)
	}
}

func TestTreeSitterParserCPlusPlusMasksNlohmannMacros(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("json.hpp", `NLOHMANN_JSON_NAMESPACE_BEGIN

JSON_HEDLEY_NON_NULL(1)
JSON_HEDLEY_RETURNS_NON_NULL
void grisu2(char* buf, int& len);

bool check(bool value) {
  return JSON_HEDLEY_UNLIKELY(!value);
}

template <typename BasicJsonType>
void dependent_call(BasicJsonType&& j) {
  j.template get<int>();
  j->operator[]("key");
}

template <typename BasicJsonType>
struct adl_serializer {
  template <typename TargetType>
  static void from_json(BasicJsonType&& j, TargetType& val) {
    val = j.template get<TargetType>();
  }

  template <typename TargetType>
  auto sfinae(BasicJsonType&& j) -> decltype(j.template get<TargetType>(), void()) {
    return;
  }

  template <typename TargetType>
  auto multi_line_sfinae(BasicJsonType&& j)
  -> decltype(
    j.template get<TargetType>(),
    void())
  {
    return;
  }
};

NLOHMANN_BASIC_JSON_TPL_DECLARATION
class basic_json {};
using basic_json_t = NLOHMANN_BASIC_JSON_TPL;

template <typename T>
bool compare(const T& lhs, const T& rhs) {
  return lhs <=> rhs;
}

JSON_PRIVATE_UNLESS_TESTED:
void private_helper();

NLOHMANN_DEFINE_TYPE_NON_INTRUSIVE(person, name, address, age)

enum class choice { yes, no };
NLOHMANN_JSON_SERIALIZE_ENUM(choice, {
  {choice::yes, "yes"},
  {choice::no, "no"},
})

NLOHMANN_JSON_NAMESPACE_END
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	found := false
	for _, entity := range entities {
		if entity.Kind == "struct" && entity.Name == "adl_serializer" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected C++ entities after masking nlohmann macros, got %#v", entities)
	}
}

func TestTreeSitterParserCPlusPlusMasksDoctestMacros(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("unit.cpp", `DOCTEST_MSVC_SUPPRESS_WARNING(4189)
TEST_CASE("modifiers" * doctest::test_suite("json"))
{
    SECTION("clear()")
    {
        CAPTURE(1);
        CHECK(json::parse("{}").is_object());
        CHECK_THROWS_AS(json::parse("["), json::parse_error&);
    }
    SECTION("compile error in from_json converting to container "
            "with std::pair")
    {
        CHECK(true);
    }
}

void assert_invariant(bool check_parents) {
  JSON_TRY
  {
    JSON_ASSERT(!check_parents || std::all_of(begin(), end(), [](const auto& j)
    {
      return true;
    }));
  }
  JSON_CATCH(...) {}
}

int at(bool ok) {
  if (ok) {
    JSON_TRY
    {
      return 1;
    }
    JSON_CATCH (std::out_of_range&)
    {
      JSON_THROW(type_error::create(304, "bad"));
    }
  }
  return 0;
}

TEST_CASE_TEMPLATE("checking forward-iterators", T,
                   std::vector<int>, std::string, nlohmann::json)
{
    auto it1 = typename T::iterator{};
    CHECK(it1 == it1);
}

TEST_CASE_TEMPLATE_INVOKE(value_in_range_of_test,
                          trait_test_arg<std::size_t, std::int32_t, false, true>,
                          trait_test_arg<std::size_t, std::uint32_t, true, true>);

template <typename C = char,
          enable_if_t<std::is_unsigned<C>::value>* = nullptr>
void sfinae_default() {}

class DOCTEST_INTERFACE String {
 public:
  using size_type = DOCTEST_CONFIG_STRING_SIZE_TYPE;
 private:
  static DOCTEST_CONSTEXPR size_type len = 24;
};

template <typename T, std::size_t N,
          typename Array = T (&)[N]>
Array array_ref(T (&v)[N]) {
  return v;
}

struct explicit_value {
  JSON_EXPLICIT operator int() const { return 0; }
};

JSON_IMPLEMENT_OPERATOR( ==, true, false, false)

auto ns = STRINGIZE(NLOHMANN_JSON_NAMESPACE);
DOCTEST_MSVC_SUPPRESS_WARNING_POP
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected C++ entities after masking doctest macros")
	}
}

func TestTreeSitterParserCPlusPlusExtractsUsingAliases(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("json_fwd.hpp", `namespace nlohmann {
template<typename>
class basic_json;
using json = basic_json<>;
using ordered_json = basic_json<nlohmann::ordered_map>;
}
`)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		if entity.Kind == "type" {
			seen[entity.Name] = entity
		}
	}
	for _, name := range []string{"json", "ordered_json"} {
		entity, ok := seen[name]
		if !ok {
			t.Fatalf("missing C++ using alias %q in %#v", name, entities)
		}
		if entity.Signature == "" || entity.StartLine == 0 {
			t.Fatalf("incomplete alias entity for %q: %#v", name, entity)
		}
	}
}

func TestTreeSitterParserTypeScriptMasksTypeofDynamicImportTypeArgument(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("configureStore.test.ts", `vi.doMock('redux', async (importOriginal) => {
  const redux = await importOriginal<typeof import('redux')>()
  return redux
})
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
}

func TestTreeSitterParserObjectiveCInventoryFallback(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("AppDelegate.h", `#import <RCTAppDelegate.h>
#import <UIKit/UIKit.h>

@interface AppDelegate : RCTAppDelegate

@end
`)
	if language != "Objective-C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 || entities[0].Kind != "document" {
		t.Fatalf("expected Objective-C inventory entity, got %#v", entities)
	}

	entities, language, status = TreeSitterParser{}.ParseWithStatus("AppDelegate.mm", `#import "AppDelegate.h"

@implementation AppDelegate

@end
`)
	if language != "Objective-C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 || entities[0].Kind != "document" {
		t.Fatalf("expected Objective-C++ inventory entity, got %#v", entities)
	}
}

func TestTreeSitterParserTypeScriptGraphQLResolverEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("src/resolvers.ts", `export const resolvers = {
  Query: {
    user: (_parent, args) => ({ id: args.id }),
    viewer(_parent, _args, ctx) {
      return ctx.viewer
    },
    namedUser: getUser,
    memberUser: userResolvers.findUser,
    wrappedUser: withAuth(getUser),
    disabled: true,
  },
  Mutation: {
    createUser: async (_parent, args) => ({ id: args.input.id }),
  },
  Subscription: {
    userCreated: {
      subscribe: (_parent, _args, ctx) => ctx.pubsub.asyncIterator("USER_CREATED"),
    },
  },
  User: {
    id: (user) => user.id,
  },
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		if entity.Kind == "graphql_resolver" {
			seen[entity.Name] = entity
		}
	}
	for _, name := range []string{"Query.user", "Query.viewer", "Query.namedUser", "Query.memberUser", "Query.wrappedUser", "Mutation.createUser", "Subscription.userCreated", "User.id"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing GraphQL resolver entity %s in %#v", name, entities)
		}
	}
	if _, ok := seen["Query.disabled"]; ok {
		t.Fatalf("literal boolean was misreported as GraphQL resolver entity: %#v", seen["Query.disabled"])
	}
	if seen["Query.user"].Signature != "GraphQL resolver query user" {
		t.Fatalf("resolver signature = %q", seen["Query.user"].Signature)
	}
}

func TestTreeSitterParserTypeScriptGraphQLModularResolverEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("src/user.resolvers.ts", `export const Query = {
  user: getUser,
}

const Mutation = {
  createUser: mutationResolvers.createUser,
}

const QueryIgnored = {
  disabled: true,
}

const helper = {
  user: getUser,
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		if entity.Kind == "graphql_resolver" {
			seen[entity.Name] = entity
		}
	}
	for _, name := range []string{"Query.user", "Mutation.createUser"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing modular GraphQL resolver entity %s in %#v", name, entities)
		}
	}
	for _, name := range []string{"QueryIgnored.disabled", "helper.user"} {
		if _, ok := seen[name]; ok {
			t.Fatalf("unexpected modular GraphQL resolver entity %s in %#v", name, entities)
		}
	}
}

func TestTreeSitterParserKotlinPrimaryConstructorFieldEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("User.kt", `package com.acme

data class User(
  val id: String,
  var displayName: String = "anonymous",
  private val age: Int,
)
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for _, want := range []struct {
		name      string
		signature string
	}{
		{name: "User.id", signature: "id String"},
		{name: "User.displayName", signature: "displayName String"},
		{name: "User.age", signature: "age Int"},
	} {
		entity, ok := seen[want.name]
		if !ok {
			t.Fatalf("missing Kotlin primary constructor field %s in %#v", want.name, entities)
		}
		if entity.Kind != "field" || entity.Signature != want.signature {
			t.Fatalf("unexpected Kotlin field entity %s: %#v", want.name, entity)
		}
	}
}

func TestGraphQLSchemaFieldEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("schema.graphql", `schema {
  query: Query
}

type Query {
  user(id: ID!): User!
  viewer: User
  search(
    term: String!
    limit: Int = 10
  ): [User!]!
}

extend type Mutation {
  createUser(input: CreateUserInput!): User!
}

type User {
  id: ID!
}
`)
	if language != "GraphQL" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for _, name := range []string{"schema", "Query.user", "Query.viewer", "Query.search", "Mutation.createUser", "User.id"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing GraphQL schema entity %s in %#v", name, entities)
		}
	}
	if seen["Query.user"].Kind != "graphql_schema_field" || seen["Query.user"].Signature != "GraphQL schema query user" {
		t.Fatalf("unexpected Query.user entity: %#v", seen["Query.user"])
	}
	if seen["User.id"].Kind != "graphql_schema_field" || seen["User.id"].Signature != "GraphQL schema user id" {
		t.Fatalf("unexpected User.id entity: %#v", seen["User.id"])
	}
	if seen["Query.search"].Kind != "graphql_schema_field" || seen["Query.search"].Signature != "GraphQL schema query search" {
		t.Fatalf("unexpected Query.search entity: %#v", seen["Query.search"])
	}
}

func TestGraphQLSchemaAliasedRootEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("schema.graphql", `schema {
  query: RootQuery
  mutation: RootMutation
}

type RootQuery {
  user(id: ID!): User!
}

type RootMutation {
  createUser(input: CreateUserInput!): User!
}
`)
	if language != "GraphQL" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	if seen["RootQuery.user"].Kind != "graphql_schema_field" || seen["RootQuery.user"].Signature != "GraphQL schema query user" {
		t.Fatalf("query alias entity = %#v", seen["RootQuery.user"])
	}
	if seen["RootMutation.createUser"].Kind != "graphql_schema_field" || seen["RootMutation.createUser"].Signature != "GraphQL schema mutation createUser" {
		t.Fatalf("mutation alias entity = %#v", seen["RootMutation.createUser"])
	}
}

func TestTreeSitterParserSupportsYAMLWorkflowExtensions(t *testing.T) {
	if !Supported(".github/workflows/ci.yml") {
		t.Fatal(".yml workflow should be supported")
	}
	if !Supported(".github/workflows/deploy.yaml") {
		t.Fatal(".yaml workflow should be supported")
	}

	entities, language := TreeSitterParser{}.Parse(".github/workflows/deploy.yaml", `name: Deploy
on: workflow_dispatch
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - run: echo deploy
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for name, kind := range map[string]string{
		"deploy":       "workflow",
		"on":           "section",
		"jobs.publish": "job",
	} {
		if seen[name] != kind {
			t.Fatalf("%s kind = %q, want %q in %#v", name, seen[name], kind, entities)
		}
	}
}

func TestTreeSitterParserDoesNotTreatEveryYAMLFileAsWorkflow(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("pnpm-workspace.yaml", `packages:
  - apps/*
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	for _, entity := range entities {
		if entity.Kind == "workflow" {
			t.Fatalf("unexpected workflow entity in %#v", entities)
		}
	}
}

func TestTreeSitterParserDockerComposeServiceEntities(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("docker-compose.yml", `services:
  api:
    image: example/api:latest
  db:
    image: postgres:16
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for _, name := range []string{"compose.service.api", "compose.service.db"} {
		if seen[name] != "resource" {
			t.Fatalf("%s kind = %q, want resource in %#v", name, seen[name], entities)
		}
	}
}

func TestTreeSitterParserKubernetesMultiDocumentResources(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("k8s/resources.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
---
apiVersion: v1
kind: Secret
metadata:
  name: api-secret
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		if entity.Kind == "resource" {
			seen[entity.Name] = entity
		}
	}
	for _, name := range []string{"ConfigMap.api-config", "Secret.api-secret"} {
		if seen[name].Name == "" {
			t.Fatalf("missing %s resource in %#v", name, entities)
		}
	}
	if seen["ConfigMap.api-config"].EndLine >= seen["Secret.api-secret"].StartLine {
		t.Fatalf("resource source ranges overlap: %#v", seen)
	}
}

func TestTreeSitterParserPostgresMigrationEntities(t *testing.T) {
	input := `create extension if not exists vector;

create table public.contracts (
  id uuid primary key,
  embedding vector(1536),
  created_at timestamptz not null default now(),
  status text generated always as ('active') stored,
  check (status in ('active', 'archived'))
);

create or replace function public.touch_contract()
returns trigger
language plpgsql
as $$
begin
  new.created_at := now();
  return new;
end;
$$;

create trigger contracts_touch
before update on public.contracts
for each row execute function public.touch_contract();

create index contracts_embedding_idx on public.contracts using ivfflat (embedding vector_cosine_ops);

create policy "contracts are readable"
on public.contracts for select
using (true);

insert into public.contracts (id)
values ('00000000-0000-0000-0000-000000000000')
on conflict (id) do update set created_at = excluded.created_at;
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("supabase/migrations/202605090001_phase2_contract.sql", input)
	if language != "SQL" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("parse status = %#v; entities = %#v", status, entities)
	}
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for name, kind := range map[string]string{
		"public.contracts":        "table",
		"public.touch_contract":   "function",
		"contracts_touch":         "trigger",
		"contracts_embedding_idx": "index",
		"contracts are readable":  "policy",
	} {
		if seen[name] != kind {
			t.Fatalf("%s kind = %q, want %q in %#v", name, seen[name], kind, entities)
		}
	}
}

func TestTreeSitterParserExpandedLanguageEntities(t *testing.T) {
	tests := []struct {
		path     string
		input    string
		language string
		names    []string
	}{
		{
			path:     "main.c",
			language: "C",
			input: `typedef struct User { int id; } User;
int validate(int token) { return token; }
`,
			names: []string{"User", "validate"},
		},
		{
			path:     "main.cpp",
			language: "C++",
			input: `class User { public: void run() {} };
int validate(int token) { return token; }
`,
			names: []string{"User", "User.run", "validate"},
		},
		{
			path:     "User.cs",
			language: "C#",
			input: `class User { public bool Validate(string token) { return true; } }
interface IRun { void Run(); }
`,
			names: []string{"User", "User.Validate", "IRun", "IRun.Run"},
		},
		{
			path:     "User.java",
			language: "Java",
			input: `class User { boolean validate(String token) { return true; } }
interface Run { void run(); }
`,
			names: []string{"User", "User.validate", "Run", "Run.run"},
		},
		{
			path:     "User.kt",
			language: "Kotlin",
			input: `class User { fun validate(token: String): Boolean { return true } }
interface Run { fun run() }
fun top() {}
`,
			names: []string{"User", "User.validate", "Run", "Run.run", "top"},
		},
		{
			path:     "auth.rb",
			language: "Ruby",
			input: `class User
  def validate(token)
    true
  end
end
def top; end
`,
			names: []string{"User", "User.validate", "top"},
		},
		{
			path:     "auth.php",
			language: "PHP",
			input: `<?php
class User { function validate($token) { return true; } }
function top() {}
interface Run { public function run(); }
`,
			names: []string{"User", "User.validate", "top", "Run", "Run.run"},
		},
		{
			path:     "Auth.swift",
			language: "Swift",
			input: `struct User { func validate(token: String) -> Bool { true } }
class Runner {}
func top() {}
`,
			names: []string{"User", "User.validate", "Runner", "top"},
		},
		{
			path:     "Auth.scala",
			language: "Scala",
			input: `class User { def validate(token: String): Boolean = true }
trait Run { def run(): Unit }
def top(): Unit = {}
`,
			names: []string{"User", "User.validate", "Run", "Run.run", "top"},
		},
		{
			path:     "auth.ex",
			language: "Elixir",
			input: `defmodule User do
  def validate(token), do: true
  defp secret(), do: :ok
end
`,
			names: []string{"User", "User.validate", "User.secret"},
		},
		{
			path:     "auth.sh",
			language: "Bash",
			input: `validate_token() { echo ok; }
function run_task { echo run; }
`,
			names: []string{"validate_token", "run_task"},
		},
		{
			path:     "main.tf",
			language: "HCL",
			input: `resource "aws_instance" "web" { ami = "x" }
variable "name" {}
`,
			names: []string{"resource.aws_instance.web", "variable.name"},
		},
		{
			path:     "auth.ml",
			language: "OCaml",
			input: `type user = { name: string }
let validate token = true
module Auth = struct let run x = x end
`,
			names: []string{"user", "validate", "Auth", "Auth.run"},
		},
		{
			path:     "auth.lua",
			language: "Lua",
			input: `function validate(token) return true end
local function helper() end
User = {}
function User:run() end
`,
			names: []string{"validate", "helper", "User.run"},
		},
		{
			path:     "schema.sql",
			language: "SQL",
			input: `CREATE TABLE users (id INT);
CREATE FUNCTION validate_token(token TEXT) RETURNS BOOLEAN AS $$ SELECT true; $$ LANGUAGE SQL;
`,
			names: []string{"users", "validate_token"},
		},
		{
			path:     "schema.cue",
			language: "CUE",
			input: `#User: { name: string }
validate: true
`,
			names: []string{"#User", "validate"},
		},
		{
			path:     "Auth.groovy",
			language: "Groovy",
			input: `class User { boolean validate(String token) { true } }
def top() { }
`,
			names: []string{"User", "User.validate", "top"},
		},
		{
			path:     "auth.proto",
			language: "Protocol Buffers",
			input: `syntax = "proto3";
message User { string name = 1; }
service Auth { rpc Validate(User) returns (User); }
`,
			names: []string{"User", "Auth", "Auth.Validate"},
		},
	}

	for _, tt := range tests {
		entities, language := TreeSitterParser{}.Parse(tt.path, tt.input)
		if language != tt.language {
			t.Fatalf("%s language = %q", tt.path, language)
		}
		seen := map[string]bool{}
		for _, entity := range entities {
			seen[entity.Name] = true
		}
		for _, name := range tt.names {
			if !seen[name] {
				t.Fatalf("%s missing entity %q in %#v", tt.path, name, entities)
			}
		}
	}
}

func TestFastCFamilyEntitiesTopLevelInventory(t *testing.T) {
	entities := fastCFamilyEntities("main.c", `#include "user.h"
typedef struct User {
	int id;
} User;

static inline int helper(int token)
{
	return token + 1;
}

int validate(int token) {
	if (token) {
		return helper(token);
	}
	return 0;
}
`, "C")
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for name, kind := range map[string]string{
		"User":     "type",
		"helper":   "function",
		"validate": "function",
	} {
		if seen[name] != kind {
			t.Fatalf("%s kind = %q, want %q in %#v", name, seen[name], kind, entities)
		}
	}
	if seen["if"] != "" {
		t.Fatalf("control statement parsed as function: %#v", entities)
	}
}
