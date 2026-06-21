package sem

import "testing"

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
