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

func TestTreeSitterParserCMasksBSDMacros(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("server.c", `#include "tmux.h"

TAILQ_HEAD(clients, client);
RB_GENERATE_STATIC(args_tree, args_entry, entry,
    args_cmp);
static TAILQ_HEAD(, window) alerts_list = TAILQ_HEAD_INITIALIZER(alerts_list);
static struct utf8_width_cache utf8_width_cache =
    RB_INITIALIZER(utf8_width_cache);

enum key_code {
	KEYC_MOUSE,
	KEYC_MOUSE_KEYS(MOUSEMOVE),
	KEYC_NONE,
};

static const char *client_rows[] = {
	"Features " WINDOW_CLIENT_FEATURE(256) " " WINDOW_CLIENT_FEATURE(RGB),
};

void printflike(3, 4)
cmd_log_argv(int argc, char **argv, const char *fmt, ...)
{
}

static void
alerts_timer(__unused int fd, __unused short events, __unused void *arg)
{
}

static int
server_loop(void)
{
	struct client *c, *tmp;
	if (timercmp(&c->activity_time, &tmp->activity_time, >))
		return 1;
	TAILQ_FOREACH(c, &clients, entry) {
		notify_client(c);
	}
	TAILQ_FOREACH_SAFE(c, &clients, entry, tmp) {
		notify_client(c);
	}
	RB_FOREACH(c, args_tree, &args->tree) {
		notify_client(c);
	}
	RB_FOREACH(c, args_tree, &args->tree)
		notify_client(c);
	RB_FOREACH_SAFE(c, args_tree, &args->tree,
	    tmp) {
		notify_client(c);
	}
	return 0;
}
`)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for _, name := range []string{"cmd_log_argv", "server_loop"} {
		if seen[name].Name == "" {
			t.Fatalf("missing C entity %q in %#v", name, entities)
		}
	}
}

func TestTreeSitterParserCMasksPostgresMacros(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("verify_nbtree.c", `#include "postgres.h"

PG_MODULE_MAGIC_EXT(
					.name = "amcheck",
					.version = PG_VERSION
);

PG_FUNCTION_INFO_V1(bt_index_check);

static void
check_all(List *items)
{
	ListCell *cell;

	foreach(cell, items)
		check_item(lfirst(cell));
}
`)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	if seen["check_all"].Name == "" {
		t.Fatalf("missing PostgreSQL C entity in %#v", entities)
	}
}

func TestTreeSitterParserCMasksMultilineAttributes(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("xmalloc.h", `#ifndef XMALLOC_H
#define XMALLOC_H

int xasprintf(char **, const char *, ...)
		__attribute__((__format__ (printf, 2, 3)))
		__attribute__((__nonnull__ (2)));
int xsnprintf(char *, size_t, const char *, ...)
		__attribute__((__format__ (printf, 3, 4)))
		__attribute__((__nonnull__ (3)))
		__attribute__((__bounded__ (__string__, 1, 2)));
__attribute__((weak)) extern int LLVMFuzzerInitialize(int *argc, char ***argv);

#endif
`)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
}

func TestTreeSitterParserCMasksPreprocessorAlternates(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("utf8.c", `static void
utf8_add_to_width_cache(void)
{
#ifdef HAVE_UTF8PROC
	if (utf8proc_mbtowc() <= 0) {
#else
	if (mbtowc() <= 0) {
#endif
		return;
	}
}
`)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
}

func TestTreeSitterParserCMasksBSDTreeTables(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("utf8.c", `#include "tmux.h"

struct utf8_width_item {
	wchar_t wc;
	u_int width;
	int allocated;
	RB_ENTRY(utf8_width_item) entry;
};

static int
utf8_width_cache_cmp(struct utf8_width_item *uw1, struct utf8_width_item *uw2)
{
	return (0);
}
RB_HEAD(utf8_width_cache, utf8_width_item);
RB_GENERATE_STATIC(utf8_width_cache, utf8_width_item, entry,
    utf8_width_cache_cmp);
static struct utf8_width_cache utf8_width_cache =
    RB_INITIALIZER(utf8_width_cache);

static struct utf8_width_item utf8_default_width_cache[] = {
	{ .wc = 0x0261D, .width = 2 },
	{ .wc = 0x1FAF8, .width = 2 }
};

struct utf8_item {
	RB_ENTRY(utf8_item) index_entry;
	u_int index;
	RB_ENTRY(utf8_item) data_entry;
	char data[UTF8_SIZE];
	u_char size;
};
RB_HEAD(utf8_data_tree, utf8_item);
RB_GENERATE_STATIC(utf8_data_tree, utf8_item, data_entry, utf8_data_cmp);
static struct utf8_data_tree utf8_data_tree = RB_INITIALIZER(utf8_data_tree);

#define UTF8_GET_SIZE(uc) (((uc) >> 24) & 0x1f)
#define UTF8_SET_SIZE(size) (((utf8_char)(size)) << 24)

static struct utf8_item *
utf8_item_by_data(const u_char *data, size_t size)
{
	struct utf8_item ui;
	return (RB_FIND(utf8_data_tree, &utf8_data_tree, &ui));
}

static void
utf8_insert_width_cache(wchar_t wc, u_int width)
{
	struct utf8_width_item *uw, *old;
	old = RB_INSERT(utf8_width_cache, &utf8_width_cache, uw);
	if (old != NULL) {
		RB_REMOVE(utf8_width_cache, &utf8_width_cache, old);
		RB_INSERT(utf8_width_cache, &utf8_width_cache, uw);
	}
}

static void
utf8_add_to_width_cache(const char *s)
{
}
`)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
}

func TestTreeSitterParserBashMasksHereDocPipelines(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("regress.sh", `#!/bin/sh

cat <<EOF|cmp -s $TMP - || exit 1
expected
EOF

(cat <<EOF|cmp -s - $OUT) || exit 1
TERM=$TERM
EOF

query_decrpm() {
	${_setup:+printf '$_setup'; sleep 0.2}
	printf '\033[%s\$p' "$_mode"
}
`)
	if language != "Bash" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	if seen["query_decrpm"].Name == "" {
		t.Fatalf("missing shell function in %#v", entities)
	}
}

func TestTreeSitterParserZshParsesCompletionExpansions(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("completion.zsh", `#compdef base-test

__base-test_complete() {
    local -ar non_empty_completions=("${@:#(|:*)}")
    local -ar empty_completions=("${(M)@:#(|:*)}")
    _describe -V '' non_empty_completions -- empty_completions -P $'\'\''
}

__base-test_custom_complete() {
    local -a completions
    completions=("${(@f)"$("${command_name}" "${@}" "${command_line[@]}")"}")
    if [[ "${#completions[@]}" -gt 1 ]]; then
        __base-test_complete "${completions[@]:0:-1}"
    fi
}

__base-test_cursor_index_in_current_word() {
    printf %s "${#${(z)LBUFFER}[-1]}"
}
`)
	if language != "Zsh" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}
	for _, name := range []string{"__base-test_complete", "__base-test_custom_complete", "__base-test_cursor_index_in_current_word"} {
		if seen[name].Name == "" {
			t.Fatalf("missing shell function %q in %#v", name, entities)
		}
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

func TestTreeSitterParserKotlinMasksModernCallAndAnnotationSyntax(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("KoinModern.kt", `import kotlin.reflect.KClass

@Target(AnnotationTarget.CLASS, AnnotationTarget.FUNCTION)
public annotation class Single(val binds: Array<KClass<*>> = [], val createdAtStart: Boolean = false)

class WorkManagerActivity {
    fun onCreate() {
        findViewById<TextView>(R.id.workmanager_message).text = "Work Manager is starting."
        module {
            single<Simple.ComponentInterface1> { Simple.Component1() } withOptions {
                override()
            }
        }
    }
}

private fun Koin.checkDefinition(allParameters: ParametersBinding) {
    val parameters: ParametersHolder = allParameters.parametersCreators[
        CheckedComponent(
            definition.qualifier,
            definition.primaryType,
        ),
    ]?.invoke(definition.qualifier)
}
`)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected Kotlin entities after masking modern syntax")
	}
}

func TestTreeSitterParserKotlinMasksGradleAllOpenBlock(t *testing.T) {
	_, language, status := TreeSitterParser{}.ParseWithStatus("benchmark.gradle.kts", `plugins {
    kotlin("jvm")
}

allOpen {
    annotation("org.openjdk.jmh.annotations.State")
}

benchmark {
    targets {
        register("jvm")
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

func TestTreeSitterParserYAMLMasksQuotedMappingKeys(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("mkdocs.yml", `nav:
  - API Documentation:
      - macros:
          - 'NLOHMANN_DEFINE_DERIVED_TYPE_INTRUSIVE, NLOHMANN_DEFINE_DERIVED_TYPE_INTRUSIVE_WITH_DEFAULT, NLOHMANN_DEFINE_DERIVED_TYPE_INTRUSIVE_ONLY_SERIALIZE, NLOHMANN_DEFINE_DERIVED_TYPE_NON_INTRUSIVE, NLOHMANN_DEFINE_DERIVED_TYPE_NON_INTRUSIVE_WITH_DEFAULT, NLOHMANN_DEFINE_DERIVED_TYPE_NON_INTRUSIVE_ONLY_SERIALIZE': api/macros/nlohmann_define_derived_type.md
          - 'NLOHMANN_DEFINE_TYPE_INTRUSIVE, NLOHMANN_DEFINE_TYPE_INTRUSIVE_WITH_DEFAULT, NLOHMANN_DEFINE_TYPE_INTRUSIVE_ONLY_SERIALIZE': api/macros/nlohmann_define_type_intrusive.md
`)
	if language != "YAML" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected YAML section entities after masking quoted mapping keys")
	}
}

func TestTreeSitterParserCSharpMasksPreprocessorAndPrimaryConstructors(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("WrappedReaderTests.cs", "\ufeff"+`#if !NET5_0_OR_GREATER
namespace System.Diagnostics.CodeAnalysis
{
    internal sealed class NotNullWhenAttribute : Attribute {}
}
#endif

using System;

#if NET8_0_OR_GREATER
namespace Dapper.Tests;
#endif

public class WrappedReaderTests
{
    private static readonly int[] ErrZeroRows = [];
    static readonly Hashtable s_ReadViaGetFieldValueCache = [];

#if DEBUG
    [Obsolete(nameof(Read))]
#endif
    public void Read() {}

    class DummyDbException(string message) : DbException(message);

    public void Add(Type type, TypeMapEntry value, Dictionary<Type, TypeMapEntry> snapshot)
    {
        SetTypeMap(new Dictionary<Type, TypeMapEntry>(snapshot) { [type] = value });
        typeHandlers = [];
        locals ??= [];
    }
}
`)
	if language != "C#" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected C# entities after masking preprocessor directives")
	}
}

func TestTreeSitterParserSwiftMasksModernSyntax(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("CommandParser.swift", `struct Runner {
  @Option() var generateCompletionScript: String

  mutating func parse(
    arguments: [String]
  ) async throws(CommandError) -> ParsableCommand {
    for try await line in try fileHandle.bytes.lines {
      if let prefix {
        print(prefix)
      }
    }
    let suggestion = arguments
      .filter({
        $0.synopsisString.editDistance(to: name.synopsisString)
          < kSimilarityFloor
      })
    if !flags.isEmpty
      || !options.isEmpty
    {
      print("complete")
    }
    XCTAssert(
      type(of: command.commandStack[0])
        == NestedDefaultSubcommandHelp.Type.self)
    XCTAssert(
      error.description == """
        Multiple arguments are named \"--foo\".
        """
        || error.description == """
          Multiple arguments are named \"--bar\".
          """
    )
    return command
  }
}
`)
	if language != "Swift" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	if len(entities) == 0 {
		t.Fatalf("expected Swift entities after masking modern syntax")
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

template <typename T, typename Char>
FMT_VISIBILITY("hidden")
FMT_CONSTEXPR auto invoke_parse(parse_context<Char>& ctx) -> const Char* {
  return nullptr;
}

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
  EXPECT_FALSE(fmt::is_formattable<int(s::*)>::value);
}

TEST(module_test, errors) {
  EXPECT_THROW(throw fmt::format_error("oops"), std::exception);
  EXPECT_NONFATAL_FAILURE(
      EXPECT_THROW_MSG(throw runtime_error("a"), runtime_error, "b"), "");
  EXPECT_CALL(streambuf, xsputn(data, static_cast<std::streamsize>(n)))
      .WillOnce(testing::Return(max_streamsize));
  if (auto result = std::scan<std::string, int>("answer = 42", "{} = {}")) {
    FAIL();
  }
}

#if defined(__cpp_lib_ranges) && __cpp_lib_ranges >= 202207L
TEST(ranges_test, nested_ranges) {
  auto r = std::views::iota(0, 3) | std::views::transform([](auto i) {
             return std::views::take(std::ranges::subrange(l), i);
           }) |
           std::views::transform(std::views::reverse);
}
#endif

GTEST_DISABLE_MSC_WARNINGS_PUSH_(4251 \
/* class A needs to have dll-interface to be used by clients of class B */)

GTEST_DEFINE_bool_(catch_exceptions,
                   internal::BoolFromGTestEnv("catch_exceptions", true),
                   "True if and only if " GTEST_NAME_
                   " should catch exceptions and treat them as test failures.");

GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(::std::string, "");
GTEST_COMPILE_ASSERT_(!std::is_reference<Result>::value,
                      Result_cannot_be_a_reference_type);
GTEST_DISALLOW_COPY_AND_ASSIGN_(Impl);

GTEST_REPEATER_METHOD_(OnTestProgramStart, UnitTest)
GTEST_REVERSE_REPEATER_METHOD_(OnEnvironmentsSetUpEnd, UnitTest)
GTEST_IMPL_FORMAT_C_STRING_AS_POINTER_(const char)
GTEST_IMPL_FORMAT_C_STRING_AS_STRING_(char, ::std::string)

GTEST_ATTRIBUTE_PRINTF_(2, 3)
static void ColoredPrintf(GTestColor color, const char *fmt, ...) {}

GTEST_INTERNAL_DEPRECATED(
    "INSTANTIATE_TEST_CASE_P is deprecated, please use "
    "INSTANTIATE_TEST_SUITE_P")
constexpr bool InstantiateTestCase_P_IsDeprecated() { return true; }

template <GTEST_TEMPLATE_ Fixture, class TestSel, typename Types>
class TypeParameterizedTest {
  typedef typename GTEST_BIND_(TestSel, Type) TestClass;
};

std::string OsStackTraceGetter::CurrentStackTrace(int max_depth, int skip_count)
    GTEST_LOCK_EXCLUDED_(mutex_) {
#if GTEST_HAS_ABSL
  return "";
#else
  static_cast<void>(max_depth);
  return "";
#endif
}

using ReturnType =
    decltype((std::declval<Class*>()->*std::declval<MethodPtr>())());

template <typename R, R* = nullptr>
internal::ReturnRefAction<R> ReturnRef(R&&) = delete;

int Run() GTEST_MUST_USE_RESULT_;
class Matcher {
template <typename T>
explicit Matcher(
    const MatcherInterface<T>* impl,
    typename std::enable_if<!std::is_same<T, const T&>::value>::type* =
        nullptr) {}
};
typedef GTEST_REMOVE_REFERENCE_AND_CONST_(T) RawT;
GTEST_IMPL_CMP_HELPER_(NE, !=)
using LosslessArithmeticConvertible =
    LosslessArithmeticConvertibleImpl<GMOCK_KIND_OF_(From), From,
                                      GMOCK_KIND_OF_(To), To>;
template <typename E = std::enable_if<sizeof...(Ts) == 1>,
          typename E::type* = nullptr>
explicit MatcherBaseImpl(Ts... params) {}
template <
    typename T1, typename T2,
    typename std::enable_if<!std::is_integral<T1>::value ||
                            !std::is_pointer<T2>::value>::type* = nullptr>
static AssertionResult Compare(const char* lhs_expression,
                               const char* rhs_expression, const T1& lhs,
                               const T2& rhs) {}
const FieldType Class::*field_;
struct span_input_adapter {
template<class IteratorType,
         typename std::enable_if<
             std::is_same<typename iterator_traits<IteratorType>::iterator_category, std::random_access_iterator_tag>::value,
             int>::type = 0>
span_input_adapter(IteratorType first, IteratorType last) {}
};
template<typename NumberType, typename std::enable_if<
             std::is_floating_point<NumberType>::value, int>::type = 0>
void write_number_with_ubjson_prefix(const NumberType n) {}
template<typename InputIt>
using require_input_iter = typename std::enable_if<std::is_convertible<typename std::iterator_traits<InputIt>::iterator_category,
    std::input_iterator_tag>::value>::type;
template<typename InputIt, typename = require_input_iter<InputIt>>
void insert(InputIt first, InputIt last) {}
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

enum : ullong { is_unpacked_bit = 1ULL << 63 };

extern template FMT_API auto thousands_sep_impl<char>(locale_ref)
    -> thousands_sep_result<char>;

template class file_access<file_access_tag, std::filebuf,
                           &std::filebuf::_Myfile>;

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

template <typename T, typename Enable = void>
struct is_std_string_like : std::false_type {};
template <typename T>
struct is_std_string_like<T, void_t<decltype(std::declval<T>().find_first_of(
                                 typename T::value_type(), 0))>>
    : std::is_convertible<decltype(std::declval<T>().data()),
                          const typename T::value_type*> {};

template <size_t... Is>
static auto check(index_sequence<Is...>) -> decltype(all_true(
    index_sequence<Is...>{},
    integer_sequence<bool, (Is >= 0)...>{}));

template <bool IS_CONSTEXPR, typename T, typename Ptr = const T*>
FMT_CONSTEXPR auto find(Ptr first, Ptr last, T value, Ptr& out) -> bool {
  return false;
}

template <typename T>
std::string namespace_name(std::string ns, T* /*unused*/ = nullptr) {
  return ns;
}

class MutationDispatcher {
  struct Mutator {
    size_t (MutationDispatcher::*Fn)(uint8_t *Data, size_t Size, size_t Max);
  };
  size_t Mutate(uint8_t *Data, size_t Size, size_t MaxSize) {
    auto M = Mutator{};
    size_t NewSize = (this->*(M.Fn))(Data, Size, MaxSize);
    return NewSize;
  }
};

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
std::partial_ordering operator<=>(const_reference rhs) const noexcept
{
    const_reference lhs = *this;
    JSON_IMPLEMENT_OPERATOR(<=>,
                            std::partial_ordering::equivalent,
                            std::partial_ordering::unordered,
                            lhs_type <=> rhs_type)
}

CHECK_THROWS_WITH_AS([&]()
{
    [[maybe_unused]] auto result = json::from_msgpack(empty_data);
    return true;
}
(),
"parse error",
json::parse_error&);

struct DOCTEST_INTERFACE_DECL IsNaN
{
    bool flipped;
};

struct Approx
{
    template <typename T>
    explicit Approx(const T& value,
                    typename detail::types::enable_if<std::is_constructible<double, T>::value>::type* =
                            static_cast<T*>(nullptr)) {
        *this = static_cast<double>(value);
    }
};

template <typename L>
struct Expression_lhs {
    operator L() const { return lhs; }
    L lhs;
};
struct ExpressionDecomposer {
    template <typename L,typename types::enable_if<!doctest::detail::types::is_rvalue_reference<L>::value,void >::type* = nullptr>
    Expression_lhs<const L&> operator<<(const L &operand) { return Expression_lhs<const L&>(operand, m_at); }
};
struct ResultBuilder {
    template <int comparison, typename L, typename R>
    DOCTEST_NOINLINE bool binary_assert(const DOCTEST_REF_WRAP(L) lhs,
                                        const DOCTEST_REF_WRAP(R) rhs) {
        if (!lhs) {
            return { false };
        }
        return { true, (DOCTEST_STRINGIFY(lhs)) };
    }
};

ATTRIBUTE_TARGET_POPCNT
bool MergeFrom(ValueBitMap &Other) { return true; }
class TimerQ {
 public:
  TimerQ() : TimerQueue(NULL) {};
  ~TimerQ() {
    DeleteTimerQueueEx(TimerQueue, NULL);
  };
};
extern "C" {
__attribute__((visibility("default")))
void __sanitizer_cov_trace_pc_guard(uint32_t *Guard) {}
}  // extern "C"

template<class B, class... Bn>
struct conjunction<B, Bn...>
: std::conditional<static_cast<bool>(B::value), conjunction<Bn...>, B>::type {};
template<class B> struct negation : std::integral_constant < bool, !B::value > { };
template<typename T>
struct is_c_string : bool_constant<impl::is_c_string<T>()> {};
template<typename T>
void sax_static_asserts() {
    (void)detail::is_sax_static_asserts<T, T> {};
}

DOCTEST_NORETURN void throw_exception(int e) {
    throw e;
}
void color_to_stream(std::ostream&, Color::Enum) DOCTEST_BRANCH_ON_DISABLED({}, ;)
DOCTEST_INTERFACE String toString(double long in);
String toString(char signed in);
String toString(char unsigned in);
String toString(short unsigned in);
String toString(long unsigned in);
String toString(long long unsigned in);
template struct DOCTEST_INTERFACE_DEF IsNaN<long double>;
template <typename F>
IsNaN<F>::operator bool() const {
    return std::isnan(value) ^ flipped;
}

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

func TestTreeSitterParserPostgresExtensionDDL(t *testing.T) {
	input := `LOAD 'pg_plan_advice';

CREATE FUNCTION bt_index_check(index regclass, heapallindexed boolean)
RETURNS void
AS 'MODULE_PATHNAME', 'bt_index_check'
LANGUAGE C STRICT PARALLEL RESTRICTED;

ALTER EXTENSION pg_stat_statements UPDATE TO '1.11';

CREATE ACCESS METHOD bloom TYPE INDEX HANDLER blhandler;

CREATE TYPE seg (
  internallength = variable,
  input = seg_in,
  output = seg_out
);

CREATE OPERATOR CLASS int4_ops
DEFAULT FOR TYPE int4 USING bloom AS
  OPERATOR 1 =(int4, int4),
  FUNCTION 1 hashint4(int4);

SELECT 1 AS ok \gset
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("contrib/amcheck/amcheck--1.0.sql", input)
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
	if seen["bt_index_check"] != "function" {
		t.Fatalf("missing external C function entity in %#v", entities)
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
			path:     "auth.zsh",
			language: "Zsh",
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

func TestTreeSitterParserCSharpBOMPreservesSymbolNames(t *testing.T) {
	// A leading UTF-8 BOM must not shift symbol byte offsets. The BOM mask is
	// byte-length-preserving (3-byte BOM -> 3 spaces); a 3->1 byte replacement
	// previously drifted every name by -2 (e.g. "WidgetFactory" -> "s WidgetFacto").
	src := "\ufeffnamespace Acme.Demo\n{\n    public class WidgetFactory\n    {\n        public int CounterValue;\n        public string BuildWidget(string name) { return name; }\n    }\n}\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("Widget.cs", src)
	if language != "C#" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	want := map[string]string{
		"WidgetFactory":              "class",
		"WidgetFactory.CounterValue": "field",
		"WidgetFactory.BuildWidget":  "method",
	}
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for name, kind := range want {
		if seen[name] != kind {
			t.Fatalf("BOM-prefixed C# name %q kind = %q, want %q; corrupted names present in %#v", name, seen[name], kind, entities)
		}
	}
}

func TestTreeSitterParserKotlinNamesSkipAnnotationsAndTypeParameters(t *testing.T) {
	// A declaration's own name is never inside its leading annotations/modifiers
	// or its generic type parameters. The fallback name search must skip those:
	// "@OptIn class Koin" must be "Koin" (not "OptIn"), and "fun <T> get()" must
	// be "get" (not the type parameter "T").
	src := "@OptIn(KoinInternalApi::class)\nclass Koin {\n    fun <T> get(): T { return resolve() }\n    inline fun <reified T> getOrNull(): T? { return null }\n}\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("Koin.kt", src)
	if language != "Kotlin" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	want := map[string]string{
		"Koin":           "class",
		"Koin.get":       "method",
		"Koin.getOrNull": "method",
	}
	seen := map[string]string{}
	for _, entity := range entities {
		seen[entity.Name] = entity.Kind
	}
	for name, kind := range want {
		if seen[name] != kind {
			t.Fatalf("Kotlin name %q kind = %q, want %q; annotation/type-parameter leak in %#v", name, seen[name], kind, entities)
		}
	}
	if _, leaked := seen["OptIn"]; leaked {
		t.Fatalf("annotation name leaked as a symbol: %#v", entities)
	}
	if _, leaked := seen["T"]; leaked {
		t.Fatalf("type parameter leaked as a symbol name: %#v", entities)
	}
}

func TestTreeSitterParserCMasksExportAndAttributeMacros(t *testing.T) {
	// C runtime export/attribute macros (PGDLLIMPORT, PG_USED_FOR_ASSERTS_ONLY,
	// pg_attribute_*) prefix or annotate declarations and break the C grammar,
	// cascading parse errors onto following tokens. Masking them must let the
	// real declarations parse with correct names.
	src := "extern PGDLLIMPORT volatile sig_atomic_t InterruptPending;\n" +
		"PGDLLIMPORT int log_min_messages = 0;\n" +
		"static int counter pg_attribute_unused() = 0;\n" +
		"int real_function(int value)\n{\n    return value + log_min_messages;\n}\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("guc.c", src)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking export macros: %s", status.Detail)
	}
	found := false
	for _, e := range entities {
		if e.Name == "real_function" && (e.Kind == "function" || e.Kind == "method") {
			found = true
		}
	}
	if !found {
		t.Fatalf("real_function not extracted (macro cascade not contained): %#v", entities)
	}
}

func TestTreeSitterParserCMasksCatalogStructMacros(t *testing.T) {
	// PostgreSQL system-catalog headers wrap a valid C struct body in
	// BEGIN_CATALOG_STRUCT / CATALOG(name,oid,...) BKI_... { ... } macros that
	// break the grammar and cascade onto the field declarations. Masking the
	// macro scaffolding must let the struct and a following real function parse.
	src := "BEGIN_CATALOG_STRUCT\n" +
		"CATALOG(pg_authid,1260,AuthIdRelationId) BKI_SHARED_RELATION BKI_ROWTYPE_OID(2842,X)\n" +
		"{\n" +
		"\tOid oid;\n" +
		"\tbool rolsuper;\n" +
		"\tint32 rolconnlimit BKI_DEFAULT(-1);\n" +
		"} FormData_pg_authid;\n" +
		"END_CATALOG_STRUCT\n\n" +
		"int role_count(int n)\n{\n\treturn n + 1;\n}\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("pg_authid.h", src)
	if language != "C" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking catalog macros: %s", status.Detail)
	}
	found := false
	for _, e := range entities {
		if e.Name == "role_count" && (e.Kind == "function" || e.Kind == "method") {
			found = true
		}
	}
	if !found {
		t.Fatalf("real function after catalog struct not extracted: %#v", entities)
	}
}

func TestTreeSitterParserCPlusPlusMasksNamespaceAndAnnotationMacros(t *testing.T) {
	// Library namespace-opening macros (asmjit ASMJIT_BEGIN_(SUB_)NAMESPACE) and
	// Julia annotation macros (JL_NOTSAFEPOINT) break the C++ grammar. Masking
	// them must let the enclosed declarations parse.
	src := "ASMJIT_BEGIN_SUB_NAMESPACE(a64)\n\n" +
		"class Assembler {\n" +
		"public:\n" +
		"  void emit() JL_NOTSAFEPOINT;\n" +
		"  int realMethod(int n) { return n; }\n" +
		"};\n\n" +
		"ASMJIT_END_SUB_NAMESPACE\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("assembler.cpp", src)
	if language != "C++" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking C++ macros: %s", status.Detail)
	}
	gotClass, gotMethod := false, false
	for _, e := range entities {
		if e.Name == "Assembler" && e.Kind == "class" {
			gotClass = true
		}
		if e.Name == "realMethod" || e.Name == "Assembler.realMethod" {
			gotMethod = true
		}
	}
	if !gotClass || !gotMethod {
		t.Fatalf("class/method not extracted inside namespace macro: %#v", entities)
	}
}

func TestTreeSitterParserOCamlInterfaceValSignatures(t *testing.T) {
	// .mli interface files use `val NAME : type` signatures the implementation
	// grammar cannot parse. Masking rewrites top-level vals to `let NAME = ()`
	// so they parse and NAME is still extracted; single- and multi-line type
	// signatures are both handled.
	src := "(** doc *)\n\n" +
		"val fundecl : Mach.fundecl -> Mach.fundecl\n\n" +
		"val instrument_initialiser\n" +
		"   : Cmm.expression\n" +
		"  -> (unit -> Debuginfo.t)\n" +
		"  -> Cmm.expression\n"
	entities, language, status := TreeSitterParser{}.ParseWithStatus("afl.mli", src)
	if language != "OCaml" {
		t.Fatalf("language = %q", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking .mli signatures: %s", status.Detail)
	}
	names := map[string]bool{}
	for _, e := range entities {
		names[e.Name] = true
	}
	if !names["fundecl"] || !names["instrument_initialiser"] {
		t.Fatalf("val names not extracted from .mli: %#v", entities)
	}
}
