package sem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestBuildProviderSnapshotEmitsContractRecords(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", `import json

class AuthService:
    def execute_tool_handler(self):
        return {"tool": "execute", "schema": {}}

def validate_token(token):
    return bool(token)

def check_token(token):
    return validate_token(token)
`)
	writeFile(t, repo, "server.ts", `export function register(app) {
  app.get("/users/{id}", handleRoute)
}

export function handleRoute() {
  return "ok"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.SchemaVersion != "1.1" {
		t.Fatalf("schema version = %q", snapshot.Header.SchemaVersion)
	}
	if snapshot.Header.Provider != ProviderName {
		t.Fatalf("provider = %q", snapshot.Header.Provider)
	}
	if snapshot.Header.Stats.CompletenessLevel != "ok" {
		t.Fatalf("completeness = %q", snapshot.Header.Stats.CompletenessLevel)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("files = %#v", snapshot.Files)
	}
	for _, file := range snapshot.Files {
		if file.ID == "" {
			t.Fatalf("file record missing id: %#v", file)
		}
	}

	var validate SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == "validate_token" {
			validate = symbol
		}
	}
	if validate.ID == "" {
		t.Fatalf("missing validate_token in %#v", snapshot.Symbols)
	}
	if validate.StableIDVersion != StableSymbolIDVersion {
		t.Fatalf("stable id version = %q", validate.StableIDVersion)
	}
	if !strings.Contains(validate.ID, "local/") || !strings.Contains(validate.ID, ":Python:auth.py:function:validate_token") {
		t.Fatalf("stable id = %q", validate.ID)
	}

	seenRelations := map[string]bool{}
	for _, relation := range snapshot.Relations {
		seenRelations[relation.Type] = true
		if relation.WarningCodes == nil {
			t.Fatalf("warning_codes should be an array in %#v", relation)
		}
		if relation.Confidence <= 0 || relation.Reason == "" {
			t.Fatalf("relation missing confidence/reason: %#v", relation)
		}
	}
	for _, want := range []string{"DEFINES", "CONTAINS", "IMPORTS", "CALLS", "HANDLES_ROUTE", "HANDLES_TOOL"} {
		if !seenRelations[want] {
			t.Fatalf("missing %s in %#v", want, snapshot.Relations)
		}
	}
	if symbolByKindAndName(snapshot.Symbols, "tool", "AuthService.execute_tool_handler").ID == "" {
		t.Fatalf("missing tool boundary symbol in %#v", snapshot.Symbols)
	}
	if len(snapshot.Externals) == 0 {
		t.Fatalf("missing external endpoint records")
	}
}

func TestGoModuleImportsResolveThroughGoMod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/acme/service\n\ngo 1.22\n")
	writeFile(t, repo, "cmd/api/main.go", `package main

import "example.com/acme/service/internal/auth"

func main() {
	auth.Validate()
}
`)
	writeFile(t, repo, "internal/auth/auth.go", `package auth

func Validate() {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "internal/auth/auth.go")
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:cmd/api/main.go") && relation.ToID == target {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing go.mod resolved import to %s in %#v", target, snapshot.Relations)
	}
	if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.9 {
		t.Fatalf("unexpected go.mod import metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "go_mod_import" {
		t.Fatalf("unexpected go.mod import evidence: %#v", found.Evidence)
	}
}

func TestTypeScriptManifestImportsResolveThroughPackageAndTSConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "package.json", `{"name":"@acme/app"}`)
	writeFile(t, repo, "tsconfig.json", `{
  "compilerOptions": {
    "paths": {
      "@lib/*": ["src/lib/*"]
    }
  }
}`)
	writeFile(t, repo, "src/app.ts", `import { helper } from "@lib/helper"
import { Widget } from "@acme/app/src/components/widget"

export function run(): string {
  return helper(new Widget().name)
}
`)
	writeFile(t, repo, "src/lib/helper.ts", `export function helper(value: string): string {
  return value
}
`)
	writeFile(t, repo, "src/components/widget.ts", `export class Widget {
  name = "widget"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"src/lib/helper.ts":        "tsconfig_paths_import",
		"src/components/widget.ts": "package_json_import",
	}
	for targetPath, evidenceKind := range targets {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/app.ts") && relation.ToID == target {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing JS/TS manifest resolved import to %s in %#v", target, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
			t.Fatalf("unexpected JS/TS import metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != evidenceKind {
			t.Fatalf("unexpected JS/TS import evidence for %s: %#v", targetPath, found.Evidence)
		}
	}
}

func TestTypeScriptRelativeImportedCallsResolveWithoutFieldFalsePositive(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/app.ts", `import { helper } from './helper'

interface Options {
  middleware?: () => void
}

export function run(options: Options): string {
  options.middleware?.()
  return helper()
}
`)
	writeFile(t, repo, "src/helper.ts", `export function helper(): string {
  return 'ok'
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "helper") {
		t.Fatalf("missing imported helper call relation: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "Options.middleware") {
		t.Fatalf("interface field was treated as a callee: %#v", snapshot.Relations)
	}
}

func TestCPlusPlusLocalIncludesResolveToFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "include/fmt/format.h", `#pragma once
template <typename T>
auto format(T value) -> std::string {
  return {};
}
`)
	writeFile(t, repo, "include/fmt/args.h", `#pragma once
#include "format.h"

class dynamic_format_arg_store {};
`)
	writeFile(t, repo, "src/fmt.cc", `#include "fmt/format.h"
#include <string>

auto use_format() -> std::string {
  return format(1);
}
`)
	writeFile(t, repo, "test/gtest-extra.h", `#pragma once
class extra_test {};
`)
	writeFile(t, repo, "test/format-test.cc", `#include "fmt/format.h"
#include "gtest-extra.h"
#include <string>

void test_format() {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	formatTarget := fileID(snapshot.Header.RepoKey, "include/fmt/format.h")
	extraTarget := fileID(snapshot.Header.RepoKey, "test/gtest-extra.h")
	for _, want := range []struct {
		from   string
		target string
		detail string
	}{
		{"include/fmt/args.h", formatTarget, "format.h"},
		{"src/fmt.cc", formatTarget, "fmt/format.h"},
		{"test/format-test.cc", formatTarget, "fmt/format.h"},
		{"test/format-test.cc", extraTarget, "gtest-extra.h"},
	} {
		if !hasImportRelationWithEvidence(snapshot.Relations, want.from, want.target, want.detail, "import_statement") {
			t.Fatalf("missing C/C++ local include %s -> %s (%s) in %#v", want.from, want.target, want.detail, snapshot.Relations)
		}
	}
	if hasImportRelation(snapshot.Relations, "src/fmt.cc", fileID(snapshot.Header.RepoKey, "test/format-test.cc")) {
		t.Fatalf("standard <string> include resolved to an unrelated local file: %#v", snapshot.Relations)
	}
	for _, file := range snapshot.Files {
		if file.Path == "include/fmt/format.h" && file.Language != "C++" {
			t.Fatalf("C++ header file language = %q, want C++", file.Language)
		}
	}
}

func TestTypeScriptWorkspacePackageReexportCallsResolveToSource(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "packages/toolkit/package.json", `{
  "name": "@acme/toolkit",
  "exports": { ".": "./dist/index.js" }
}
`)
	writeFile(t, repo, "packages/toolkit/src/index.ts", `export {
  // js
  configureStore,
} from './configureStore'
`)
	writeFile(t, repo, "packages/toolkit/src/configureStore.ts", `export function configureStore(): string {
  return 'ok'
}
`)
	writeFile(t, repo, "examples/app/src/store.ts", `import { configureStore } from '@acme/toolkit'

const store = configureStore()

export function boot(): string {
  return configureStore()
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "boot", "configureStore", "import_resolved") {
		t.Fatalf("missing workspace package re-export call relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "examples/app/src/store.ts", "configureStore", "import_resolved") {
		t.Fatalf("missing top-level workspace package call relation: %#v", snapshot.Relations)
	}
}

func TestJavaScriptReceiverCallsResolveUniqueAssignmentMethods(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/application.js", `var app = exports = module.exports = {};

app.init = function init() {
  this.ready = true;
};

app.handle = function handle(req, res, next) {
  next();
};
`)
	writeFile(t, repo, "lib/express.js", `var proto = require('./application');

function createApplication() {
  var app = function(req, res, next) {
    app.handle(req, res, next);
  };

  mixin(app, proto, false);
  app.init();
  return app;
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "createApplication", "app.init", "name_only") {
		t.Fatalf("missing receiver-qualified call createApplication -> app.init in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "createApplication", "app.handle", "name_only") {
		t.Fatalf("missing receiver-qualified call createApplication -> app.handle in %#v", snapshot.Relations)
	}
}

func TestTypeScriptManifestImportsResolveThroughExtendedTSConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "package.json", `{"name":"@acme/app"}`)
	writeFile(t, repo, "tsconfig.json", `{
  "extends": "./config/tsconfig.base.json",
  "compilerOptions": {
    "paths": {
      "@override/*": ["src/override/*"]
    }
  }
}`)
	writeFile(t, repo, "config/tsconfig.base.json", `{
  "compilerOptions": {
    "paths": {
      "@base/*": ["../src/base/*"],
      "@override/*": ["../src/base-override/*"]
    }
  }
}`)
	writeFile(t, repo, "src/app.ts", `import { base } from "@base/helper"
import { override } from "@override/helper"

export const value = base() + override()
`)
	writeFile(t, repo, "src/base/helper.ts", `export function base(): string {
  return "base"
}
`)
	writeFile(t, repo, "src/base-override/helper.ts", `export function override(): string {
  return "base-override"
}
`)
	writeFile(t, repo, "src/override/helper.ts", `export function override(): string {
  return "override"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		targetPath string
		detail     string
	}{
		{targetPath: "src/base/helper.ts", detail: "@base/helper"},
		{targetPath: "src/override/helper.ts", detail: "@override/helper"},
	} {
		target := fileID(snapshot.Header.RepoKey, want.targetPath)
		if !hasImportRelationWithEvidence(snapshot.Relations, "src/app.ts", target, want.detail, "tsconfig_paths_import") {
			t.Fatalf("missing extended tsconfig import %s -> %s in %#v", want.detail, want.targetPath, snapshot.Relations)
		}
	}
	if hasImportRelation(snapshot.Relations, "src/app.ts", fileID(snapshot.Header.RepoKey, "src/base-override/helper.ts")) {
		t.Fatalf("child tsconfig paths should override inherited duplicate pattern: %#v", snapshot.Relations)
	}
}

func TestTypeScriptManifestImportsResolveThroughTSConfigBaseURL(t *testing.T) {
	t.Run("inherited baseUrl", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "package.json", `{"name":"@acme/app"}`)
		writeFile(t, repo, "tsconfig.json", `{
  "extends": "./config/tsconfig.base.json"
}`)
		writeFile(t, repo, "config/tsconfig.base.json", `{
  "compilerOptions": {
    "baseUrl": "../src"
  }
}`)
		writeFile(t, repo, "app/main.ts", `import { helper } from "core/helper"

export const value = helper()
`)
		writeFile(t, repo, "src/core/helper.ts", `export function helper(): string {
  return "base"
}
`)

		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		target := fileID(snapshot.Header.RepoKey, "src/core/helper.ts")
		if !hasImportRelationWithEvidence(snapshot.Relations, "app/main.ts", target, "core/helper", "tsconfig_baseurl_import") {
			t.Fatalf("missing inherited tsconfig baseUrl import to %s in %#v", target, snapshot.Relations)
		}
	})

	t.Run("child baseUrl overrides parent", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "package.json", `{"name":"@acme/app"}`)
		writeFile(t, repo, "tsconfig.json", `{
  "extends": "./config/tsconfig.base.json",
  "compilerOptions": {
    "baseUrl": "app"
  }
}`)
		writeFile(t, repo, "config/tsconfig.base.json", `{
  "compilerOptions": {
    "baseUrl": "../src"
  }
}`)
		writeFile(t, repo, "app/main.ts", `import { feature } from "feature/widget"

export const value = feature()
`)
		writeFile(t, repo, "src/feature/widget.ts", `export function feature(): string {
  return "parent"
}
`)
		writeFile(t, repo, "app/feature/widget.ts", `export function feature(): string {
  return "child"
}
`)

		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		childTarget := fileID(snapshot.Header.RepoKey, "app/feature/widget.ts")
		if !hasImportRelationWithEvidence(snapshot.Relations, "app/main.ts", childTarget, "feature/widget", "tsconfig_baseurl_import") {
			t.Fatalf("missing child tsconfig baseUrl import to %s in %#v", childTarget, snapshot.Relations)
		}
		if hasImportRelation(snapshot.Relations, "app/main.ts", fileID(snapshot.Header.RepoKey, "src/feature/widget.ts")) {
			t.Fatalf("child tsconfig baseUrl should override inherited parent baseUrl: %#v", snapshot.Relations)
		}
	})
}

func TestTypeScriptComputedRuntimeImportsResolveToLocalFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/app.ts", `const helper = require(path.posix.join(".", "lib", "helper"))

export async function run(): Promise<string> {
  const feature = await import([".", "feature"].join("/"))
  return helper.value + feature.value
}
`)
	writeFile(t, repo, "src/lib/helper.ts", `export const value = "helper"
`)
	writeFile(t, repo, "src/feature.ts", `export const value = "feature"
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		targetPath string
		detail     string
	}{
		{targetPath: "src/lib/helper.ts", detail: "./lib/helper"},
		{targetPath: "src/feature.ts", detail: "./feature"},
	} {
		target := fileID(snapshot.Header.RepoKey, want.targetPath)
		if !hasImportRelationWithEvidence(snapshot.Relations, "src/app.ts", target, want.detail, "import_statement") {
			t.Fatalf("missing computed runtime import %s -> %s in %#v", want.detail, want.targetPath, snapshot.Relations)
		}
	}
}

func TestTypeScriptManifestImportsResolveThroughNestedPackageJSON(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "package.json", `{"private": true, "workspaces": ["packages/*"]}`)
	writeFile(t, repo, "packages/utils/package.json", `{
  "name": "@acme/utils",
  "exports": {
    ".": "./src/index.ts",
    "./extra": "./src/extra.ts"
  }
}`)
	writeFile(t, repo, "packages/ui/package.json", `{"name":"@acme/ui"}`)
	writeFile(t, repo, "apps/web/src/app.ts", `import { util } from "@acme/utils"
import { extra } from "@acme/utils/extra"
import { Button } from "@acme/ui/button"

export const value = util() + extra() + new Button().label
`)
	writeFile(t, repo, "packages/utils/src/index.ts", `export function util(): string {
  return "util"
}
`)
	writeFile(t, repo, "packages/utils/src/extra.ts", `export function extra(): string {
  return "extra"
}
`)
	writeFile(t, repo, "packages/ui/button.ts", `export class Button {
  label = "button"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"packages/utils/src/index.ts": "package_workspace_exports_import",
		"packages/utils/src/extra.ts": "package_workspace_exports_import",
		"packages/ui/button.ts":       "package_workspace_import",
	}
	for targetPath, evidenceKind := range targets {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:apps/web/src/app.ts") && relation.ToID == target {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing nested package import to %s in %#v", targetPath, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
			t.Fatalf("unexpected nested package import metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != evidenceKind {
			t.Fatalf("unexpected nested package import evidence for %s: %#v", targetPath, found.Evidence)
		}
	}
}

func TestTypeScriptManifestImportsResolveThroughExportsImportsAndImportMap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "package.json", `{
  "name": "@acme/pkg",
  "exports": {
    ".": "./src/index.ts",
    "./feature": {
      "import": "./src/feature.ts",
      "default": "./dist/feature.js"
    }
  },
  "imports": {
    "#config": "./src/config.ts"
  }
}`)
	writeFile(t, repo, "import-map.json", `{
  "imports": {
    "@shared/util": "./src/shared/util.ts"
  }
}`)
	writeFile(t, repo, "src/app.ts", `import { feature } from "@acme/pkg/feature"
import { config } from "#config"
import { util } from "@shared/util"

export const value = feature(config.name) + util()
`)
	writeFile(t, repo, "src/feature.ts", `export function feature(value: string): string {
  return value
}
`)
	writeFile(t, repo, "src/config.ts", `export const config = { name: "app" }
`)
	writeFile(t, repo, "src/shared/util.ts", `export function util(): string {
  return "util"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"src/feature.ts":     "package_exports_import",
		"src/config.ts":      "package_imports_import",
		"src/shared/util.ts": "import_map_import",
	}
	for targetPath, evidenceKind := range targets {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/app.ts") && relation.ToID == target {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing JS/TS manifest resolved import to %s in %#v", target, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
			t.Fatalf("unexpected JS/TS import metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != evidenceKind {
			t.Fatalf("unexpected JS/TS import evidence for %s: %#v", targetPath, found.Evidence)
		}
	}
}

func TestTypeScriptManifestImportsResolveThroughScopedImportMap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "import-map.json", `{
  "imports": {
    "@shared/util": "./src/global/util.ts"
  },
  "scopes": {
    "./src/app/": {
      "@shared/util": "./src/app/util.ts",
      "@scoped/": "./src/scoped/"
    }
  }
}`)
	writeFile(t, repo, "src/app/main.ts", `import { util } from "@shared/util"
import { feature } from "@scoped/feature"

export const value = util() + feature()
`)
	writeFile(t, repo, "src/other.ts", `import { util } from "@shared/util"

export const value = util()
`)
	writeFile(t, repo, "src/app/util.ts", `export function util(): string {
  return "app"
}
`)
	writeFile(t, repo, "src/global/util.ts", `export function util(): string {
  return "global"
}
`)
	writeFile(t, repo, "src/scoped/feature.ts", `export function feature(): string {
  return "feature"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	assertImport := func(fromPath, targetPath, evidenceKind string) {
		t.Helper()
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:"+fromPath) && relation.ToID == target {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing scoped import-map import %s -> %s in %#v", fromPath, targetPath, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
			t.Fatalf("unexpected scoped import-map metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != evidenceKind {
			t.Fatalf("unexpected scoped import-map evidence: %#v", found.Evidence)
		}
	}
	assertImport("src/app/main.ts", "src/app/util.ts", "import_map_scoped_import")
	assertImport("src/app/main.ts", "src/scoped/feature.ts", "import_map_scoped_import")
	assertImport("src/other.ts", "src/global/util.ts", "import_map_import")
	if hasImportRelation(snapshot.Relations, "src/app/main.ts", fileID(snapshot.Header.RepoKey, "src/global/util.ts")) {
		t.Fatalf("scoped import-map override also resolved to global target: %#v", snapshot.Relations)
	}
}

func TestPackageJSONTargetsDoNotInventRootEntries(t *testing.T) {
	exports := parsePackageJSONTargets(`{
  "exports": {
    "./feature": "./src/feature.ts"
  },
  "imports": {
    "#config": "./src/config.ts"
  }
}`, "exports")
	if _, ok := exports["."]; ok {
		t.Fatalf("subpath-only exports invented a root target: %#v", exports)
	}
	if got := exports["./feature"]; got != "./src/feature.ts" {
		t.Fatalf("missing subpath export target: %#v", exports)
	}

	imports := parsePackageJSONTargets(`{
  "imports": {
    "#config": "./src/config.ts"
  }
}`, "imports")
	if _, ok := imports["."]; ok {
		t.Fatalf("package imports invented a root target: %#v", imports)
	}
	if got := imports["#config"]; got != "./src/config.ts" {
		t.Fatalf("missing package imports target: %#v", imports)
	}

	rootExport := parsePackageJSONTargets(`{
  "exports": {
    "import": "./src/index.ts",
    "default": "./dist/index.js"
  }
}`, "exports")
	if got := rootExport["."]; got != "./src/index.ts" {
		t.Fatalf("conditional root export did not resolve: %#v", rootExport)
	}
}

func TestPythonManifestImportsResolveThroughProjectMetadata(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pyproject.toml", `[project]
name = "acme-app"
`)
	writeFile(t, repo, "src/acme_app/__init__.py", "")
	writeFile(t, repo, "src/acme_app/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "src/acme_app/consumer.py", `import acme_app.service

def call():
    return acme_app.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "src/acme_app/service.py")
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/acme_app/consumer.py") && relation.ToID == target {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing Python manifest resolved import to %s in %#v", target, snapshot.Relations)
	}
	if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
		t.Fatalf("unexpected Python import metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "python_project_import" {
		t.Fatalf("unexpected Python import evidence: %#v", found.Evidence)
	}
}

func TestPythonRuntimeLiteralImportsResolveToLocalFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/acme_runtime/__init__.py", "")
	writeFile(t, repo, "src/acme_runtime/plugin.py", `def run():
    return "plugin"
`)
	writeFile(t, repo, "src/acme_runtime/legacy.py", `def run():
    return "legacy"
`)
	writeFile(t, repo, "src/acme_runtime/extra.py", `def run():
    return "extra"
`)
	writeFile(t, repo, "src/acme_runtime/joined.py", `def run():
    return "joined"
`)
	writeFile(t, repo, "src/acme_runtime/direct.py", `def run():
    return "direct"
`)
	writeFile(t, repo, "src/acme_runtime/aliased.py", `def run():
    return "aliased"
`)
	writeFile(t, repo, "src/acme_runtime/module_alias.py", `def run():
    return "module_alias"
`)
	writeFile(t, repo, "src/acme_runtime/formatted.py", `def run():
    return "formatted"
`)
	writeFile(t, repo, "src/acme_runtime/loader.py", `import importlib
import importlib as il
from importlib import import_module, import_module as load_module

PLUGIN = "acme_runtime.extra"
PREFIX = "acme_runtime"
JOINED = PREFIX + ".joined"
ALIASED = PREFIX + ".aliased"
MODULE_ALIAS = PREFIX + ".module_alias"

def load():
    plugin = importlib.import_module("acme_runtime.plugin")
    legacy = __import__("acme_runtime.legacy")
    extra = importlib.import_module(PLUGIN)
    joined = __import__(JOINED)
    direct = import_module("acme_runtime.direct")
    aliased = load_module(ALIASED)
    module_alias = il.import_module(MODULE_ALIAS)
    formatted = importlib.import_module(f"{PREFIX}.formatted")
    return plugin, legacy, extra, joined, direct, aliased, module_alias, formatted
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, targetPath := range []string{
		"src/acme_runtime/plugin.py",
		"src/acme_runtime/legacy.py",
		"src/acme_runtime/extra.py",
		"src/acme_runtime/joined.py",
		"src/acme_runtime/direct.py",
		"src/acme_runtime/aliased.py",
		"src/acme_runtime/module_alias.py",
		"src/acme_runtime/formatted.py",
	} {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		if !hasImportRelation(snapshot.Relations, "src/acme_runtime/loader.py", target) {
			t.Fatalf("missing Python runtime literal import to %s in %#v", target, snapshot.Relations)
		}
	}
}

func TestPythonNamespaceImportsResolveThroughDiscoveredSourceRoots(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "services/api/src/acme_ns/widgets/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "services/api/src/acme_ns/widgets/consumer.py", `import acme_ns.widgets.service

def call():
    return acme_ns.widgets.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "services/api/src/acme_ns/widgets/service.py")
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:services/api/src/acme_ns/widgets/consumer.py") && relation.ToID == target {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing Python namespace import to %s in %#v", target, snapshot.Relations)
	}
	if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
		t.Fatalf("unexpected Python namespace metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "python_namespace_import" {
		t.Fatalf("unexpected Python namespace evidence: %#v", found.Evidence)
	}
}

func TestPythonImportsResolveThroughConfiguredPackageFindRoots(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pyproject.toml", `[tool.setuptools.packages.find]
where = ["lib"]
`)
	writeFile(t, repo, "lib/acme_lib/__init__.py", "")
	writeFile(t, repo, "lib/acme_lib/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "lib/acme_lib/consumer.py", `import acme_lib.service

def call():
    return acme_lib.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "lib/acme_lib/service.py")
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:lib/acme_lib/consumer.py") && relation.ToID == target {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing configured-root Python import to %s in %#v", target, snapshot.Relations)
	}
	if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.88 {
		t.Fatalf("unexpected configured-root Python import metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "python_module_import" {
		t.Fatalf("unexpected configured-root Python import evidence: %#v", found.Evidence)
	}
}

func TestPythonImportsResolveThroughPyProjectPackageDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pyproject.toml", `[tool.setuptools]
package-dir = {"" = "lib"}
`)
	writeFile(t, repo, "lib/acme_pkg/__init__.py", "")
	writeFile(t, repo, "lib/acme_pkg/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "lib/acme_pkg/consumer.py", `import acme_pkg.service

def call():
    return acme_pkg.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "lib/acme_pkg/service.py")
	if !hasImportRelationWithEvidence(snapshot.Relations, "lib/acme_pkg/consumer.py", target, "acme_pkg.service", "python_module_import") {
		t.Fatalf("missing pyproject package-dir Python import to %s in %#v", target, snapshot.Relations)
	}
}

func TestPythonImportsResolveThroughSetupCFGPackageDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "setup.cfg", `[options]
package_dir =
    = python
`)
	writeFile(t, repo, "python/acme_cfg/__init__.py", "")
	writeFile(t, repo, "python/acme_cfg/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "python/acme_cfg/consumer.py", `import acme_cfg.service

def call():
    return acme_cfg.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "python/acme_cfg/service.py")
	if !hasImportRelationWithEvidence(snapshot.Relations, "python/acme_cfg/consumer.py", target, "acme_cfg.service", "python_module_import") {
		t.Fatalf("missing setup.cfg package_dir Python import to %s in %#v", target, snapshot.Relations)
	}
}

func TestPythonImportsResolveThroughPackageSpecificPyProjectPackageDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pyproject.toml", `[tool.setuptools.package-dir]
"acme_pkg" = "lib/acme_pkg"
`)
	writeFile(t, repo, "lib/acme_pkg/__init__.py", "")
	writeFile(t, repo, "lib/acme_pkg/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "lib/acme_pkg/consumer.py", `import acme_pkg.service

def call():
    return acme_pkg.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "lib/acme_pkg/service.py")
	if !hasImportRelationWithEvidence(snapshot.Relations, "lib/acme_pkg/consumer.py", target, "acme_pkg.service", "python_module_import") {
		t.Fatalf("missing pyproject package-specific package-dir Python import to %s in %#v", target, snapshot.Relations)
	}
}

func TestPythonImportsResolveThroughPackageSpecificSetupCFGPackageDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "setup.cfg", `[options.package_dir]
acme_cfg = python/acme_cfg
`)
	writeFile(t, repo, "python/acme_cfg/__init__.py", "")
	writeFile(t, repo, "python/acme_cfg/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "python/acme_cfg/consumer.py", `import acme_cfg.service

def call():
    return acme_cfg.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "python/acme_cfg/service.py")
	if !hasImportRelationWithEvidence(snapshot.Relations, "python/acme_cfg/consumer.py", target, "acme_cfg.service", "python_module_import") {
		t.Fatalf("missing setup.cfg package-specific package_dir Python import to %s in %#v", target, snapshot.Relations)
	}
}

func TestPythonFromPackageImportModuleReceiverCallsResolveToLocalSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/acme/__init__.py", "")
	writeFile(t, repo, "src/acme/sessions.py", `class Session:
    pass
`)
	writeFile(t, repo, "src/acme/api.py", `from . import sessions

def request():
    return sessions.Session()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "src/acme/sessions.py")
	if !hasImportRelationWithEvidence(snapshot.Relations, "src/acme/api.py", target, ".sessions", "import_statement") {
		t.Fatalf("missing from-package module import to %s in %#v", target, snapshot.Relations)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "request", "Session", "import_resolved") {
		t.Fatalf("missing imported module receiver call to Session in %#v", snapshot.Relations)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && relation.FromID != "" && strings.HasSuffix(relation.FromID, ":function:request") && strings.HasPrefix(relation.ToID, "external:symbol:sessions.Session") {
			t.Fatalf("local module receiver call also emitted external fallback: %#v", relation)
		}
	}
}

func TestSetupCFGNameParsingNormalizesPythonPackage(t *testing.T) {
	name := parseSetupCFGName(`[metadata]
Name = acme-tools
`)
	if name != "acme-tools" {
		t.Fatalf("setup.cfg name = %q", name)
	}
	names := normalizePythonPackageNames([]string{name})
	if len(names) != 1 || names[0] != "acme_tools" {
		t.Fatalf("normalized package names = %#v", names)
	}
}

func TestJavaPackageImportsResolveToLocalTypeFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/main/java/com/acme/api/Handler.java", `package com.acme.api;

import com.acme.lib.Service;
import static com.acme.lib.Util.check;

class Handler {
  Service service;
}
`)
	writeFile(t, repo, "src/main/java/com/acme/lib/Service.java", `package com.acme.lib;

class Service {}
`)
	writeFile(t, repo, "src/main/java/com/acme/lib/Util.java", `package com.acme.lib;

class Util {
  static boolean check() { return true; }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"src/main/java/com/acme/lib/Service.java": "com.acme.lib.Service",
		"src/main/java/com/acme/lib/Util.java":    "com.acme.lib.Util.check",
	}
	for targetPath, imported := range targets {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/main/java/com/acme/api/Handler.java") && relation.ToID == target && relation.Evidence[0].Detail == imported {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Java resolved import %s to %s in %#v", imported, target, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
			t.Fatalf("unexpected Java import metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "jvm_package_import" {
			t.Fatalf("unexpected Java import evidence: %#v", found.Evidence)
		}
	}
}

func TestJavaMavenPackageIdentityResolvesLocalTypeFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pom.xml", `<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.acme</groupId>
  <artifactId>worker-lib</artifactId>
</project>
`)
	writeFile(t, repo, "src/main/java/app/Handler.java", `package app;

import com.acme.worker.lib.service.Service;

class Handler {
  Service service;
}
`)
	writeFile(t, repo, "src/main/java/service/Service.java", `package service;

class Service {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "src/main/java/service/Service.java")
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/main/java/app/Handler.java") && relation.ToID == target && relation.Evidence[0].Detail == "com.acme.worker.lib.service.Service" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing Maven identity Java import to %s in %#v", target, snapshot.Relations)
	}
	if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.89 {
		t.Fatalf("unexpected Maven identity import metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "jvm_manifest_package_import" {
		t.Fatalf("unexpected Maven identity import evidence: %#v", found.Evidence)
	}
}

func TestJavaGradlePackageIdentityResolvesLocalTypeFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "settings.gradle", `rootProject.name = 'analytics-lib'
`)
	writeFile(t, repo, "build.gradle", `plugins {
    id 'java-library'
}

group = 'io.acme'
`)
	writeFile(t, repo, "src/main/java/app/Handler.java", `package app;

import io.acme.analytics.lib.service.Service;

class Handler {
  Service service;
}
`)
	writeFile(t, repo, "src/main/java/service/Service.java", `package service;

class Service {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "src/main/java/service/Service.java")
	if !hasImportRelationWithEvidence(snapshot.Relations, "src/main/java/app/Handler.java", target, "io.acme.analytics.lib.service.Service", "jvm_manifest_package_import") {
		t.Fatalf("missing Gradle identity Java import to %s in %#v", target, snapshot.Relations)
	}
}

func TestCSharpProjectNamespaceResolvesLocalNamespaceFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Worker.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <RootNamespace>Acme.WorkerLib</RootNamespace>
  </PropertyGroup>
</Project>
`)
	writeFile(t, repo, "App/Handler.cs", `using Acme.WorkerLib.Services;

namespace App;

class Handler {
  Service service;
}
`)
	writeFile(t, repo, "Services/Service.cs", `namespace Services;

class Service {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "Services/Service.cs")
	if !hasImportRelationWithEvidence(snapshot.Relations, "App/Handler.cs", target, "Acme.WorkerLib.Services", "csharp_csproj_namespace_import") {
		t.Fatalf("missing .csproj identity C# import to %s in %#v", target, snapshot.Relations)
	}
}

func TestCSharpNamespaceImportSkipsAmbiguousNamespace(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "App/Handler.cs", `using Shared.Services;

namespace App;

class Handler {}
`)
	writeFile(t, repo, "Services/First.cs", `namespace Shared.Services;

class First {}
`)
	writeFile(t, repo, "Services/Second.cs", `namespace Shared.Services;

class Second {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" &&
			strings.HasSuffix(relation.FromID, "file:App/Handler.cs") &&
			relation.Resolution == "import_resolved" {
			t.Fatalf("ambiguous C# namespace should not resolve to one file: %#v", relation)
		}
	}
}

func TestPHPComposerPSR4ImportResolvesLocalTypeFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "composer.json", `{
  "autoload": {
    "psr-4": {
      "App\\": "app/"
    }
  }
}
`)
	writeFile(t, repo, "routes/web.php", `<?php

use App\Services\UserService;

function boot() {
    return new UserService();
}
`)
	writeFile(t, repo, "app/Services/UserService.php", `<?php

namespace Services;

class UserService {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	target := fileID(snapshot.Header.RepoKey, "app/Services/UserService.php")
	if !hasImportRelationWithEvidence(snapshot.Relations, "routes/web.php", target, `App\Services\UserService`, "composer_psr4_import") {
		t.Fatalf("missing composer PSR-4 PHP import to %s in %#v", target, snapshot.Relations)
	}
}

func TestPHPNamespaceImportSkipsAmbiguousType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes/web.php", `<?php

use Shared\Services\UserService;
`)
	writeFile(t, repo, "src/First.php", `<?php

namespace Shared\Services;

class UserService {}
`)
	writeFile(t, repo, "src/Second.php", `<?php

namespace Shared\Services;

class UserService {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "IMPORTS" &&
			strings.HasSuffix(relation.FromID, "file:routes/web.php") &&
			relation.Resolution == "import_resolved" {
			t.Fatalf("ambiguous PHP type should not resolve to one file: %#v", relation)
		}
	}
}

func TestRustCargoImportsResolveToLocalModuleFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Cargo.toml", `[package]
name = "acme-tools"
version = "0.1.0"
`)
	writeFile(t, repo, "src/lib.rs", `pub mod config;
pub mod engine;
mod internal;
pub use crate::internal::SettingsAlias;
#[path = "generated/client.rs"]
pub mod client;
`)
	writeFile(t, repo, "src/config.rs", `pub struct Settings;
`)
	writeFile(t, repo, "src/engine/mod.rs", `pub struct Runner;
`)
	writeFile(t, repo, "src/internal.rs", `pub struct SettingsAlias;
`)
	writeFile(t, repo, "src/generated/client.rs", `pub struct Client;
`)
	writeFile(t, repo, "src/main.rs", `use crate::config::Settings;
use crate::SettingsAlias;
use acme_tools::engine::Runner;
use acme_tools::client::Client;

fn main() {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"src/config.rs":           "rust_crate_import",
		"src/internal.rs":         "rust_crate_import",
		"src/engine/mod.rs":       "cargo_package_import",
		"src/generated/client.rs": "cargo_package_import",
	}
	for targetPath, evidenceKind := range targets {
		target := fileID(snapshot.Header.RepoKey, targetPath)
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:src/main.rs") && relation.ToID == target {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Rust resolved import to %s in %#v", target, snapshot.Relations)
		}
		if found.Resolution != "import_resolved" || found.RelationScope != "module" || found.TargetKind != "file" || found.Confidence < 0.87 {
			t.Fatalf("unexpected Rust import metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != evidenceKind {
			t.Fatalf("unexpected Rust import evidence for %s: %#v", targetPath, found.Evidence)
		}
	}
}

func TestResourceDependsOnGraph(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "main.tf", `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "web" {
  vpc_id = aws_vpc.main.id
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var deps [][2]string
	for _, r := range snapshot.Relations {
		if r.Type == "RESOURCE_DEPENDS_ON" {
			deps = append(deps, [2]string{lastSegment(r.FromID), lastSegment(r.ToID)})
		}
	}
	if len(deps) != 1 {
		t.Fatalf("want exactly one dependency (subnet->vpc), got %v", deps)
	}
	if deps[0][0] != "resource.aws_subnet.web" || deps[0][1] != "resource.aws_vpc.main" {
		t.Fatalf("unexpected dependency %v", deps[0])
	}
}

func TestDockerfileStageResourceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Dockerfile", `FROM golang:1.22 AS builder
RUN go build -o /out/app ./cmd/app

FROM alpine:3.20 AS runtime
COPY --from=builder /out/app /usr/local/bin/app
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "runtime", "builder") {
		t.Fatalf("missing runtime->builder dependency in %#v", snapshot.Relations)
	}
}

func TestKubernetesResourceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: web
spec:
  template:
    spec:
      runtimeClassName: sandboxed
      priorityClassName: critical
      serviceAccountName: api-runner
      imagePullSecrets:
        - name: registry-creds
      volumes:
        - name: credentials
          secret:
            secretName: api-secret
        - name: cache
          persistentVolumeClaim:
            claimName: api-cache
        - name: projected-config
          projected:
            sources:
              - configMap:
                  name: api-projected-config
              - secret:
                  name: api-projected-secret
      containers:
        - name: api
          image: example/api:latest
          ports:
            - containerPort: 8080
          env:
            - name: LOG_LEVEL
              value: debug
            - name: FEATURE_FLAG
              valueFrom:
                configMapKeyRef:
                  name: api-key-config
                  key: FEATURE_FLAG
            - name: API_TOKEN
              valueFrom:
                secretKeyRef:
                  name: api-key-secret
                  key: token
          envFrom:
            - configMapRef:
                name: api-config
            - secretRef:
                name: api-env
`)
	writeFile(t, repo, "k8s/vpa.yaml", `apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: api
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api
`)
	writeFile(t, repo, "k8s/namespace.yaml", `apiVersion: v1
kind: Namespace
metadata:
  name: web
`)
	writeFile(t, repo, "k8s/configmap.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-projected-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-key-config
`)
	writeFile(t, repo, "k8s/secret.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: api-secret
---
apiVersion: v1
kind: Secret
metadata:
  name: api-env
---
apiVersion: v1
kind: Secret
metadata:
  name: api-projected-secret
---
apiVersion: v1
kind: Secret
metadata:
  name: registry-creds
---
apiVersion: v1
kind: Secret
metadata:
  name: api-key-secret
`)
	writeFile(t, repo, "k8s/service-account.yaml", `apiVersion: v1
kind: ServiceAccount
metadata:
  name: api-runner
`)
	writeFile(t, repo, "k8s/runtime-class.yaml", `apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: sandboxed
handler: runsc
`)
	writeFile(t, repo, "k8s/priority-class.yaml", `apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: critical
value: 100000
`)
	writeFile(t, repo, "k8s/pvc.yaml", `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: api-cache
spec:
  storageClassName: fast
  volumeName: api-cache-pv
`)
	writeFile(t, repo, "k8s/snapshot.yaml", `apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: api-cache-snapshot
spec:
  source:
    persistentVolumeClaimName: api-cache
`)
	writeFile(t, repo, "k8s/snapshot-content.yaml", `apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotContent
metadata:
  name: api-cache-snapshot-content
spec:
  volumeSnapshotRef:
    name: api-cache-snapshot
    namespace: default
`)
	writeFile(t, repo, "k8s/restore-pvc.yaml", `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-cache
spec:
  dataSource:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: api-cache-snapshot
`)
	writeFile(t, repo, "k8s/clone-pvc.yaml", `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: cloned-cache
spec:
  dataSourceRef:
    kind: PersistentVolumeClaim
    name: api-cache
`)
	writeFile(t, repo, "k8s/storage-class.yaml", `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast
provisioner: example.com/fast
`)
	writeFile(t, repo, "k8s/pv.yaml", `apiVersion: v1
kind: PersistentVolume
metadata:
  name: api-cache-pv
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"external:config:kubernetes/configmap/api-config",
		"external:config:kubernetes/configmap/web/api-config",
		"external:config:kubernetes/configmap/api-key-config",
		"external:config:kubernetes/configmap/web/api-key-config",
		"external:config:kubernetes/secret/api-secret",
		"external:config:kubernetes/secret/web/api-secret",
		"external:config:kubernetes/secret/api-env",
		"external:config:kubernetes/secret/web/api-env",
		"external:config:kubernetes/secret/api-key-secret",
		"external:config:kubernetes/secret/web/api-key-secret",
		"external:config:kubernetes/secret/api-projected-secret",
		"external:config:kubernetes/secret/web/api-projected-secret",
		"external:config:kubernetes/secret/registry-creds",
		"external:config:kubernetes/secret/web/registry-creds",
		"external:config:kubernetes/serviceaccount/api-runner",
		"external:config:kubernetes/serviceaccount/web/api-runner",
		"external:config:kubernetes/configmap/api-projected-config",
		"external:config:kubernetes/configmap/web/api-projected-config",
		"external:config:kubernetes/persistentvolumeclaim/api-cache",
		"external:config:kubernetes/persistentvolumeclaim/web/api-cache",
		"external:config:kubernetes/storageclass/fast",
		"external:config:kubernetes/persistentvolume/api-cache-pv",
		"external:config:kubernetes/runtimeclass/sandboxed",
		"external:config:kubernetes/priorityclass/critical",
		"external:config:kubernetes/namespace/web",
		"external:config:kubernetes/deployment/api",
	} {
		if !hasRelationTo(snapshot.Relations, "RESOURCE_DEPENDS_ON", target) {
			t.Fatalf("missing Kubernetes dependency to %s in %#v", target, snapshot.Relations)
		}
	}
	for _, target := range []string{
		"external:config:kubernetes/env/LOG_LEVEL",
		"external:config:kubernetes/image/example/api:latest",
		"external:config:kubernetes/port/8080",
	} {
		if !hasRelationTo(snapshot.Relations, "CONFIGURES", target) {
			t.Fatalf("missing Kubernetes config fact to %s in %#v", target, snapshot.Relations)
		}
	}
	for _, target := range []string{
		"ConfigMap.api-config",
		"ConfigMap.api-key-config",
		"ConfigMap.api-projected-config",
		"Secret.api-secret",
		"Secret.api-env",
		"Secret.api-key-secret",
		"Secret.api-projected-secret",
		"Secret.registry-creds",
		"ServiceAccount.api-runner",
		"PersistentVolumeClaim.api-cache",
		"RuntimeClass.sandboxed",
		"PriorityClass.critical",
		"Namespace.web",
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Deployment.api", target) {
			t.Fatalf("missing exact Kubernetes resource dependency Deployment.api -> %s in %#v", target, snapshot.Relations)
		}
	}
	for _, target := range []string{
		"StorageClass.fast",
		"PersistentVolume.api-cache-pv",
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PersistentVolumeClaim.api-cache", target) {
			t.Fatalf("missing exact Kubernetes resource dependency PersistentVolumeClaim.api-cache -> %s in %#v", target, snapshot.Relations)
		}
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PersistentVolumeClaim.restored-cache", "VolumeSnapshot.api-cache-snapshot") {
		t.Fatalf("missing exact Kubernetes PVC dataSource dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PersistentVolumeClaim.cloned-cache", "PersistentVolumeClaim.api-cache") {
		t.Fatalf("missing exact Kubernetes PVC dataSourceRef dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "VolumeSnapshot.api-cache-snapshot", "PersistentVolumeClaim.api-cache") {
		t.Fatalf("missing exact Kubernetes VolumeSnapshot source dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "VolumeSnapshotContent.api-cache-snapshot-content", "VolumeSnapshot.api-cache-snapshot") {
		t.Fatalf("missing exact Kubernetes VolumeSnapshotContent ref dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "VerticalPodAutoscaler.api", "Deployment.api") {
		t.Fatalf("missing exact Kubernetes VPA targetRef dependency in %#v", snapshot.Relations)
	}
}

func TestKubernetesResourceReferencesIncludeNamespaceQualifiedExternalNames(t *testing.T) {
	refs := kubernetesResourceReferences(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: web
spec:
  template:
    spec:
      envFrom:
        - configMapRef:
            name: api-config
`)
	var found bool
	for _, ref := range refs {
		if ref.Kind == "configmap" && ref.Name == "api-config" && ref.ExternalName == "web/api-config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing namespaced configmap ref in %#v", refs)
	}
}

func TestKubernetesReloaderAnnotationsDependOnConfigResources(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: web
  annotations:
    configmap.reloader.stakater.com/reload: api-config, feature-flags
spec:
  template:
    metadata:
      annotations:
        secret.reloader.stakater.com/reload: "api-secret; api-token"
    spec:
      containers:
        - name: api
          image: example/api:latest
`)
	writeFile(t, repo, "k8s/config.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
`)
	writeFile(t, repo, "k8s/secret.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: api-secret
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"external:config:kubernetes/configmap/api-config",
		"external:config:kubernetes/configmap/web/api-config",
		"external:config:kubernetes/configmap/feature-flags",
		"external:config:kubernetes/configmap/web/feature-flags",
		"external:config:kubernetes/secret/api-secret",
		"external:config:kubernetes/secret/web/api-secret",
		"external:config:kubernetes/secret/api-token",
		"external:config:kubernetes/secret/web/api-token",
	} {
		if !hasRelationTo(snapshot.Relations, "RESOURCE_DEPENDS_ON", target) {
			t.Fatalf("missing Reloader annotation dependency to %s in %#v", target, snapshot.Relations)
		}
	}
	for _, target := range []string{
		"ConfigMap.api-config",
		"Secret.api-secret",
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Deployment.api", target) {
			t.Fatalf("missing exact Reloader annotation dependency Deployment.api -> %s in %#v", target, snapshot.Relations)
		}
	}
}

func TestKubernetesServiceSelectorDependsOnWorkload(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    metadata:
      labels:
        app: api
        tier: backend
    spec:
      containers:
        - name: api
          image: example/api:latest
`)
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: api
spec:
  selector:
    app: api
    tier: backend
  ports:
    - port: 80
`)
	writeFile(t, repo, "k8s/pdb.yaml", `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: api
spec:
  selector:
    matchLabels:
      app: api
`)
	writeFile(t, repo, "k8s/network-policy.yaml", `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: api-policy
spec:
  podSelector:
    matchLabels:
      tier: backend
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Service.api", "Deployment.api") {
		t.Fatalf("missing Service.api -> Deployment.api selector dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PodDisruptionBudget.api", "Deployment.api") {
		t.Fatalf("missing PodDisruptionBudget.api -> Deployment.api selector dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "NetworkPolicy.api-policy", "Deployment.api") {
		t.Fatalf("missing NetworkPolicy.api-policy -> Deployment.api selector dependency in %#v", snapshot.Relations)
	}
}

func TestKubernetesSelectorsDependOnCronJobWorkload(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/cronjob.yaml", `apiVersion: batch/v1
kind: CronJob
metadata:
  name: cleanup
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        metadata:
          labels:
            app: cleanup
            tier: batch
        spec:
          restartPolicy: OnFailure
          containers:
            - name: cleanup
              image: example/cleanup:latest
`)
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: cleanup
spec:
  selector:
    app: cleanup
    tier: batch
  ports:
    - port: 80
`)
	writeFile(t, repo, "k8s/pdb.yaml", `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: cleanup
spec:
  selector:
    matchLabels:
      app: cleanup
`)
	writeFile(t, repo, "k8s/network-policy.yaml", `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: cleanup
spec:
  podSelector:
    matchLabels:
      tier: batch
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"Service.cleanup", "CronJob.cleanup"},
		{"PodDisruptionBudget.cleanup", "CronJob.cleanup"},
		{"NetworkPolicy.cleanup", "CronJob.cleanup"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Kubernetes selector dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKubernetesSelectorsDependOnRolloutWorkload(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/rollout.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: api
spec:
  template:
    metadata:
      labels:
        app: api
        tier: frontend
    spec:
      containers:
        - name: api
          image: example/api:latest
`)
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: api
spec:
  selector:
    app: api
    tier: frontend
  ports:
    - port: 80
`)
	writeFile(t, repo, "k8s/pod-monitor.yaml", `apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: api
spec:
  selector:
    matchLabels:
      app: api
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"Service.api", "Rollout.api"},
		{"PodMonitor.api", "Rollout.api"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Kubernetes selector dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKubernetesMatchExpressionSelectorsDependOnWorkloads(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    metadata:
      labels:
        app: api
        tier: backend
    spec:
      containers:
        - name: api
          image: example/api:latest
`)
	writeFile(t, repo, "k8s/pdb.yaml", `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: api
spec:
  selector:
    matchExpressions:
      - key: app
        operator: In
        values:
          - api
      - key: tier
        operator: Exists
`)
	writeFile(t, repo, "k8s/network-policy.yaml", `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: api-policy
spec:
  podSelector:
    matchExpressions:
      - key: tier
        operator: NotIn
        values: ["frontend"]
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PodDisruptionBudget.api", "Deployment.api") {
		t.Fatalf("missing PDB matchExpressions dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "NetworkPolicy.api-policy", "Deployment.api") {
		t.Fatalf("missing NetworkPolicy matchExpressions dependency in %#v", snapshot.Relations)
	}
}

func TestKubernetesMatchExpressionSelectorsDependOnCronJobWorkload(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/cronjob.yaml", `apiVersion: batch/v1
kind: CronJob
metadata:
  name: cleanup
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        metadata:
          labels:
            app: cleanup
            tier: batch
        spec:
          restartPolicy: OnFailure
          containers:
            - name: cleanup
              image: example/cleanup:latest
`)
	writeFile(t, repo, "k8s/pdb.yaml", `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: cleanup
spec:
  selector:
    matchExpressions:
      - key: app
        operator: In
        values:
          - cleanup
      - key: tier
        operator: Exists
`)
	writeFile(t, repo, "k8s/network-policy.yaml", `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: cleanup
spec:
  podSelector:
    matchExpressions:
      - key: tier
        operator: NotIn
        values: ["frontend"]
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PodDisruptionBudget.cleanup", "CronJob.cleanup") {
		t.Fatalf("missing PDB matchExpressions dependency on CronJob in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "NetworkPolicy.cleanup", "CronJob.cleanup") {
		t.Fatalf("missing NetworkPolicy matchExpressions dependency on CronJob in %#v", snapshot.Relations)
	}
}

func TestKubernetesMonitorSelectorsDependOnTargets(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: example/api:latest
`)
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: api
  labels:
    app: api
spec:
  selector:
    app: api
  ports:
    - port: 80
`)
	writeFile(t, repo, "k8s/service-monitor.yaml", `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: api
spec:
  selector:
    matchLabels:
      app: api
`)
	writeFile(t, repo, "k8s/pod-monitor.yaml", `apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: api
spec:
  selector:
    matchLabels:
      app: api
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "ServiceMonitor.api", "Service.api") {
		t.Fatalf("missing ServiceMonitor.api -> Service.api selector dependency in %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "ServiceMonitor.api", "Deployment.api") {
		t.Fatalf("ServiceMonitor selector incorrectly matched workload target: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PodMonitor.api", "Deployment.api") {
		t.Fatalf("missing PodMonitor.api -> Deployment.api selector dependency in %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "PodMonitor.api", "Service.api") {
		t.Fatalf("PodMonitor selector incorrectly matched Service target: %#v", snapshot.Relations)
	}
}

func TestKubernetesPrometheusMonitorSecretDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/service-monitor.yaml", `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: api
spec:
  endpoints:
    - port: http
      bearerTokenSecret:
        name: scrape-token
        key: token
      basicAuth:
        username:
          name: scrape-basic
          key: username
        password:
          name: scrape-basic
          key: password
      authorization:
        credentials:
          name: scrape-credentials
          key: credentials
`)
	writeFile(t, repo, "k8s/pod-monitor.yaml", `apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: api-pods
spec:
  podMetricsEndpoints:
    - port: http
      tlsConfig:
        keySecret:
          name: scrape-key
          key: tls.key
`)
	for _, name := range []string{"scrape-token", "scrape-basic", "scrape-credentials", "scrape-key"} {
		writeFile(t, repo, "k8s/"+name+".yaml", `apiVersion: v1
kind: Secret
metadata:
  name: `+name+`
`)
	}

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"ServiceMonitor.api", "Secret.scrape-token"},
		{"ServiceMonitor.api", "Secret.scrape-basic"},
		{"ServiceMonitor.api", "Secret.scrape-credentials"},
		{"PodMonitor.api-pods", "Secret.scrape-key"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Prometheus monitor secret dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKubernetesRbacIngressAndScaleTargetDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
`)
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: api
---
apiVersion: v1
kind: Service
metadata:
  name: grpc-api
---
apiVersion: v1
kind: Service
metadata:
  name: secure-api
`)
	writeFile(t, repo, "k8s/gateway.yaml", `apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public
spec:
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        certificateRefs:
          - name: public-cert
          - name: ignored-cert-config
            kind: ConfigMap
`)
	writeFile(t, repo, "k8s/gateway-cert.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: public-cert
`)
	writeFile(t, repo, "k8s/ignored-cert-config.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored-cert-config
`)
	writeFile(t, repo, "k8s/reference-grant.yaml", `apiVersion: gateway.networking.k8s.io/v1
kind: ReferenceGrant
metadata:
  name: ignored-parent
`)
	writeFile(t, repo, "k8s/service-account.yaml", `apiVersion: v1
kind: ServiceAccount
metadata:
  name: api-runner
`)
	writeFile(t, repo, "k8s/ingress-class.yaml", `apiVersion: networking.k8s.io/v1
kind: IngressClass
metadata:
  name: edge
spec:
  controller: example.com/edge
`)
	writeFile(t, repo, "k8s/role.yaml", `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: api-reader
`)
	writeFile(t, repo, "k8s/cluster-role.yaml", `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: api-admin
`)
	writeFile(t, repo, "k8s/hpa.yaml", `apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: api
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api
`)
	writeFile(t, repo, "k8s/ingress.yaml", `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api
spec:
  ingressClassName: edge
  rules:
    - http:
        paths:
          - path: /
            backend:
              service:
                name: api
                port:
                  number: 80
`)
	writeFile(t, repo, "k8s/role-binding.yaml", `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: api-readers
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: api-reader
subjects:
  - kind: ServiceAccount
    name: api-runner
    namespace: default
`)
	writeFile(t, repo, "k8s/cluster-role-binding.yaml", `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: api-admins
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: api-admin
subjects:
  - kind: ServiceAccount
    name: api-runner
    namespace: default
`)
	writeFile(t, repo, "k8s/job.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: api-migrate
  ownerReferences:
    - apiVersion: apps/v1
      kind: Deployment
      name: api
`)
	writeFile(t, repo, "k8s/http-route.yaml", `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: api
spec:
  parentRefs:
    - name: public
    - name: ignored-parent
      kind: ReferenceGrant
  rules:
    - backendRefs:
        - name: api
          kind: Service
          port: 80
`)
	writeFile(t, repo, "k8s/grpc-route.yaml", `apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata:
  name: grpc-api
spec:
  parentRefs:
    - name: public
  rules:
    - backendRefs:
        - group: ""
          kind: Service
          name: grpc-api
          port: 50051
        - kind: BackendTLSPolicy
          name: ignored-policy
`)
	writeFile(t, repo, "k8s/tls-route.yaml", `apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata:
  name: secure-api
spec:
  parentRefs:
    - name: public
  rules:
    - backendRefs:
        - name: secure-api
          port: 443
`)
	writeFile(t, repo, "k8s/backend-policy.yaml", `apiVersion: gateway.networking.k8s.io/v1alpha3
kind: BackendTLSPolicy
metadata:
  name: ignored-policy
spec:
  targetRefs:
    - kind: Service
      name: grpc-api
`)
	writeFile(t, repo, "k8s/gateway-policy.yaml", `apiVersion: gateway.networking.k8s.io/v1alpha2
kind: BackendTrafficPolicy
metadata:
  name: public-policy
spec:
  targetRef:
    kind: Gateway
    name: public
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"HorizontalPodAutoscaler.api", "Deployment.api"},
		{"Ingress.api", "Service.api"},
		{"Ingress.api", "IngressClass.edge"},
		{"HTTPRoute.api", "Service.api"},
		{"HTTPRoute.api", "Gateway.public"},
		{"GRPCRoute.grpc-api", "Service.grpc-api"},
		{"GRPCRoute.grpc-api", "Gateway.public"},
		{"TLSRoute.secure-api", "Service.secure-api"},
		{"TLSRoute.secure-api", "Gateway.public"},
		{"Gateway.public", "Secret.public-cert"},
		{"BackendTLSPolicy.ignored-policy", "Service.grpc-api"},
		{"BackendTrafficPolicy.public-policy", "Gateway.public"},
		{"RoleBinding.api-readers", "Role.api-reader"},
		{"RoleBinding.api-readers", "ServiceAccount.api-runner"},
		{"ClusterRoleBinding.api-admins", "ClusterRole.api-admin"},
		{"ClusterRoleBinding.api-admins", "ServiceAccount.api-runner"},
		{"Job.api-migrate", "Deployment.api"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing exact Kubernetes dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "HTTPRoute.api", "ReferenceGrant.ignored-parent") {
		t.Fatalf("HTTPRoute parentRefs should not treat explicit non-Gateway parent as Gateway dependency: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "GRPCRoute.grpc-api", "BackendTLSPolicy.ignored-policy") {
		t.Fatalf("Gateway API backendRefs should not treat explicit non-Service backend as Service dependency: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Gateway.public", "ConfigMap.ignored-cert-config") {
		t.Fatalf("Gateway certificateRefs should not treat explicit non-Secret certificate as Secret dependency: %#v", snapshot.Relations)
	}
}

func TestKubernetesScaledObjectDefaultScaleTargetDependsOnDeployment(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker
spec:
  template:
    metadata:
      labels:
        app: worker
    spec:
      containers:
        - name: worker
          image: example/worker:latest
`)
	writeFile(t, repo, "k8s/scaled-object.yaml", `apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: worker
spec:
  scaleTargetRef:
    name: worker
  triggers:
    - type: cpu
      authenticationRef:
        name: api-trigger-auth
      metadata:
        type: Utilization
        value: "80"
    - type: cron
      authenticationRef:
        name: cluster-trigger-auth
        kind: ClusterTriggerAuthentication
      metadata:
        timezone: UTC
        start: "0 * * * *"
        end: "30 * * * *"
        desiredReplicas: "2"
`)
	writeFile(t, repo, "k8s/trigger-auth.yaml", `apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata:
  name: api-trigger-auth
spec:
  secretTargetRef:
    - parameter: token
      name: api-token
      key: token
`)
	writeFile(t, repo, "k8s/cluster-trigger-auth.yaml", `apiVersion: keda.sh/v1alpha1
kind: ClusterTriggerAuthentication
metadata:
  name: cluster-trigger-auth
spec:
  podIdentity:
    provider: aws
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "ScaledObject.worker", "Deployment.worker") {
		t.Fatalf("missing ScaledObject.worker -> Deployment.worker default scale target dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "ScaledObject.worker", "TriggerAuthentication.api-trigger-auth") {
		t.Fatalf("missing ScaledObject.worker -> TriggerAuthentication.api-trigger-auth dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "ScaledObject.worker", "ClusterTriggerAuthentication.cluster-trigger-auth") {
		t.Fatalf("missing ScaledObject.worker -> ClusterTriggerAuthentication.cluster-trigger-auth dependency in %#v", snapshot.Relations)
	}
}

func TestKubernetesCustomControllerReferenceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/cert.yaml", `apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: api-cert
spec:
  secretName: api-tls
  issuerRef:
    name: letsencrypt
    kind: ClusterIssuer
`)
	writeFile(t, repo, "k8s/issuer.yaml", `apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
      - dns01:
          cloudflare:
            apiTokenSecretRef:
              name: cloudflare-api-token
              key: api-token
`)
	writeFile(t, repo, "k8s/issuer-secrets.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: letsencrypt-account-key
---
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-api-token
`)
	writeFile(t, repo, "k8s/external-secret.yaml", `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: api-secrets
spec:
  secretStoreRef:
    name: vault
    kind: ClusterSecretStore
  target:
    name: api-runtime
`)
	writeFile(t, repo, "k8s/secret-store.yaml", `apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: vault
`)
	writeFile(t, repo, "k8s/external-secret-target.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: api-runtime
`)
	writeFile(t, repo, "k8s/cluster-external-secret.yaml", `apiVersion: external-secrets.io/v1beta1
kind: ClusterExternalSecret
metadata:
  name: shared-secrets
spec:
  secretStoreRef:
    name: vault
    kind: ClusterSecretStore
  externalSecretSpec:
    target:
      name: shared-runtime
`)
	writeFile(t, repo, "k8s/cluster-external-secret-target.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: shared-runtime
`)
	writeFile(t, repo, "k8s/sealed-secret.yaml", `apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: sealed-api
spec:
  encryptedData:
    token: AgBy...
  template:
    metadata:
      name: api-sealed-runtime
`)
	writeFile(t, repo, "k8s/sealed-secret-fallback.yaml", `apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: worker-sealed-runtime
spec:
  encryptedData:
    token: AgBy...
`)
	writeFile(t, repo, "k8s/sealed-secret-targets.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: api-sealed-runtime
---
apiVersion: v1
kind: Secret
metadata:
  name: worker-sealed-runtime
`)
	writeFile(t, repo, "k8s/cron-workflow.yaml", `apiVersion: argoproj.io/v1alpha1
kind: CronWorkflow
metadata:
  name: nightly-report
spec:
  workflowSpec:
    workflowTemplateRef:
      name: report-template
    templates:
      - name: run
        steps:
          - - name: render
              templateRef:
                name: shared-template
                clusterScope: true
                template: render
`)
	writeFile(t, repo, "k8s/workflow-template.yaml", `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: report-template
---
apiVersion: argoproj.io/v1alpha1
kind: ClusterWorkflowTemplate
metadata:
  name: shared-template
`)
	writeFile(t, repo, "k8s/pipeline-run.yaml", `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: api-build
spec:
  pipelineRef:
    name: build-pipeline
`)
	writeFile(t, repo, "k8s/pipeline.yaml", `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: build-pipeline
spec:
  tasks:
    - name: image
      taskRef:
        name: kaniko
        kind: ClusterTask
`)
	writeFile(t, repo, "k8s/cluster-task.yaml", `apiVersion: tekton.dev/v1
kind: ClusterTask
metadata:
  name: kaniko
`)
	writeFile(t, repo, "k8s/rollout.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: api
spec:
  strategy:
    canary:
      analysis:
        templates:
          - templateName: success-rate
            args:
              - name: service-name
                value: api
          - templateName: global-slo
            clusterScope: true
`)
	writeFile(t, repo, "k8s/analysis-run.yaml", `apiVersion: argoproj.io/v1alpha1
kind: AnalysisRun
metadata:
  name: manual-analysis
spec:
  templates:
    - templateName: success-rate
`)
	writeFile(t, repo, "k8s/analysis-templates.yaml", `apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: success-rate
---
apiVersion: argoproj.io/v1alpha1
kind: ClusterAnalysisTemplate
metadata:
  name: global-slo
`)
	writeFile(t, repo, "k8s/argo-events-sensor.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Sensor
metadata:
  name: order-created
spec:
  eventBusName: platform-bus
  dependencies:
    - name: webhook-order
      eventSourceName: webhook
      eventName: order
`)
	writeFile(t, repo, "k8s/argo-events-eventsource.yaml", `apiVersion: argoproj.io/v1alpha1
kind: EventSource
metadata:
  name: webhook
spec:
  eventBusName: platform-bus
  webhook:
    order:
      endpoint: /orders
      method: POST
`)
	writeFile(t, repo, "k8s/argo-events-eventbus.yaml", `apiVersion: argoproj.io/v1alpha1
kind: EventBus
metadata:
  name: platform-bus
`)
	writeFile(t, repo, "k8s/argocd-application.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  project: platform
  source:
    repoURL: https://example.com/acme/web.git
    path: deploy
  destination:
    server: https://kubernetes.default.svc
    namespace: web
`)
	writeFile(t, repo, "k8s/argocd-applicationset.yaml", `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: workers
spec:
  template:
    metadata:
      name: worker
    spec:
      project: workloads
`)
	writeFile(t, repo, "k8s/argocd-projects.yaml", `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
---
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: workloads
`)
	writeFile(t, repo, "k8s/service-binding.yaml", `apiVersion: servicebinding.io/v1
kind: ServiceBinding
metadata:
  name: api-database
spec:
  service:
    apiVersion: database.example.com/v1
    kind: PostgreSQL
    name: user-db
  workload:
    apiVersion: apps/v1
    kind: Deployment
    name: api
`)
	writeFile(t, repo, "k8s/database.yaml", `apiVersion: database.example.com/v1
kind: PostgreSQL
metadata:
  name: user-db
`)
	writeFile(t, repo, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
`)
	writeFile(t, repo, "k8s/knative-trigger.yaml", `apiVersion: eventing.knative.dev/v1
kind: Trigger
metadata:
  name: user-created
spec:
  broker: default
  subscriber:
    ref:
      apiVersion: serving.knative.dev/v1
      kind: Service
      name: event-handler
`)
	writeFile(t, repo, "k8s/knative-broker.yaml", `apiVersion: eventing.knative.dev/v1
kind: Broker
metadata:
  name: default
`)
	writeFile(t, repo, "k8s/knative-service.yaml", `apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: event-handler
`)
	writeFile(t, repo, "k8s/knative-channel.yaml", `apiVersion: messaging.knative.dev/v1
kind: InMemoryChannel
metadata:
  name: user-events
`)
	writeFile(t, repo, "k8s/knative-subscription.yaml", `apiVersion: messaging.knative.dev/v1
kind: Subscription
metadata:
  name: user-events-to-handler
spec:
  channel:
    apiVersion: messaging.knative.dev/v1
    kind: InMemoryChannel
    name: user-events
  subscriber:
    ref:
      apiVersion: serving.knative.dev/v1
      kind: Service
      name: event-handler
  reply:
    ref:
      apiVersion: eventing.knative.dev/v1
      kind: Broker
      name: default
`)
	writeFile(t, repo, "k8s/knative-route.yaml", `apiVersion: serving.knative.dev/v1
kind: Route
metadata:
  name: user-api
spec:
  traffic:
    - revisionName: user-api-00001
    - configurationName: user-api
`)
	writeFile(t, repo, "k8s/knative-configuration.yaml", `apiVersion: serving.knative.dev/v1
kind: Configuration
metadata:
  name: user-api
`)
	writeFile(t, repo, "k8s/knative-revision.yaml", `apiVersion: serving.knative.dev/v1
kind: Revision
metadata:
  name: user-api-00001
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"Certificate.api-cert", "ClusterIssuer.letsencrypt"},
		{"ClusterIssuer.letsencrypt", "Secret.letsencrypt-account-key"},
		{"ClusterIssuer.letsencrypt", "Secret.cloudflare-api-token"},
		{"ExternalSecret.api-secrets", "ClusterSecretStore.vault"},
		{"ExternalSecret.api-secrets", "Secret.api-runtime"},
		{"ClusterExternalSecret.shared-secrets", "ClusterSecretStore.vault"},
		{"ClusterExternalSecret.shared-secrets", "Secret.shared-runtime"},
		{"SealedSecret.sealed-api", "Secret.api-sealed-runtime"},
		{"SealedSecret.worker-sealed-runtime", "Secret.worker-sealed-runtime"},
		{"CronWorkflow.nightly-report", "WorkflowTemplate.report-template"},
		{"CronWorkflow.nightly-report", "ClusterWorkflowTemplate.shared-template"},
		{"PipelineRun.api-build", "Pipeline.build-pipeline"},
		{"Pipeline.build-pipeline", "ClusterTask.kaniko"},
		{"Rollout.api", "AnalysisTemplate.success-rate"},
		{"Rollout.api", "ClusterAnalysisTemplate.global-slo"},
		{"AnalysisRun.manual-analysis", "AnalysisTemplate.success-rate"},
		{"Sensor.order-created", "EventSource.webhook"},
		{"Sensor.order-created", "EventBus.platform-bus"},
		{"EventSource.webhook", "EventBus.platform-bus"},
		{"Application.web", "AppProject.platform"},
		{"ApplicationSet.workers", "AppProject.workloads"},
		{"ServiceBinding.api-database", "PostgreSQL.user-db"},
		{"ServiceBinding.api-database", "Deployment.api"},
		{"Trigger.user-created", "Broker.default"},
		{"Trigger.user-created", "Service.event-handler"},
		{"Subscription.user-events-to-handler", "InMemoryChannel.user-events"},
		{"Subscription.user-events-to-handler", "Service.event-handler"},
		{"Subscription.user-events-to-handler", "Broker.default"},
		{"Route.user-api", "Revision.user-api-00001"},
		{"Route.user-api", "Configuration.user-api"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing custom-controller dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKubernetesIstioServiceMeshReferenceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: reviews
`)
	writeFile(t, repo, "k8s/gateway.yaml", `apiVersion: networking.istio.io/v1beta1
kind: Gateway
metadata:
  name: public-gateway
`)
	writeFile(t, repo, "k8s/virtual-service.yaml", `apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: reviews
spec:
  hosts:
    - reviews.example.com
  gateways:
    - mesh
    - public-gateway
  http:
    - route:
        - destination:
            host: reviews.default.svc.cluster.local
            subset: v1
`)
	writeFile(t, repo, "k8s/destination-rule.yaml", `apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: reviews
spec:
  host: reviews.default.svc.cluster.local
  subsets:
    - name: v1
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"VirtualService.reviews", "Service.reviews"},
		{"VirtualService.reviews", "Gateway.public-gateway"},
		{"DestinationRule.reviews", "Service.reviews"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Istio dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "VirtualService.reviews", "Gateway.mesh") {
		t.Fatalf("Istio special mesh gateway should not emit a resource dependency: %#v", snapshot.Relations)
	}
}

func TestKubernetesFluxSourceReferenceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/helm-repository.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: podinfo
spec:
  url: https://stefanprodan.github.io/podinfo
`)
	writeFile(t, repo, "k8s/git-repository.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: platform-config
spec:
  interval: 1m
  url: https://example.com/platform/config.git
`)
	writeFile(t, repo, "k8s/helm-chart.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata:
  name: podinfo-chart
spec:
  chart: podinfo
  sourceRef:
    kind: HelmRepository
    name: podinfo
`)
	writeFile(t, repo, "k8s/redis-release.yaml", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: redis
spec:
  chart:
    spec:
      chart: redis
      sourceRef:
        kind: HelmRepository
        name: podinfo
`)
	writeFile(t, repo, "k8s/helm-release.yaml", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
spec:
  dependsOn:
    - name: redis
  valuesFrom:
    - kind: ConfigMap
      name: podinfo-values
      valuesKey: values.yaml
    - kind: Secret
      name: podinfo-secret-values
      valuesKey: values.yaml
  chartRef:
    kind: HelmChart
    name: podinfo-chart
  chart:
    spec:
      chart: podinfo
      sourceRef:
        kind: HelmRepository
        name: podinfo
`)
	writeFile(t, repo, "k8s/podinfo-values.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: podinfo-values
data:
  values.yaml: |
    replicaCount: 2
`)
	writeFile(t, repo, "k8s/podinfo-secret-values.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: podinfo-secret-values
stringData:
  values.yaml: |
    token: test
`)
	writeFile(t, repo, "k8s/base-kustomization.yaml", `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: base
spec:
  sourceRef:
    kind: GitRepository
    name: platform-config
  path: ./base
`)
	writeFile(t, repo, "k8s/kustomization.yaml", `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
spec:
  dependsOn:
    - name: base
  sourceRef:
    kind: GitRepository
    name: platform-config
  path: ./clusters/prod
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"HelmChart.podinfo-chart", "HelmRepository.podinfo"},
		{"HelmRelease.redis", "HelmRepository.podinfo"},
		{"HelmRelease.podinfo", "HelmRepository.podinfo"},
		{"HelmRelease.podinfo", "HelmChart.podinfo-chart"},
		{"HelmRelease.podinfo", "HelmRelease.redis"},
		{"HelmRelease.podinfo", "ConfigMap.podinfo-values"},
		{"HelmRelease.podinfo", "Secret.podinfo-secret-values"},
		{"Kustomization.base", "GitRepository.platform-config"},
		{"Kustomization.platform", "GitRepository.platform-config"},
		{"Kustomization.platform", "Kustomization.base"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Flux source dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKubernetesCrossplaneReferenceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "k8s/provider-config.yaml", `apiVersion: aws.upbound.io/v1beta1
kind: ProviderConfig
metadata:
  name: aws-default
spec:
  credentials:
    source: InjectedIdentity
`)
	writeFile(t, repo, "k8s/composition.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: webapp
spec:
  compositeTypeRef:
    apiVersion: platform.example.org/v1alpha1
    kind: XWebApp
`)
	writeFile(t, repo, "k8s/composite.yaml", `apiVersion: platform.example.org/v1alpha1
kind: XWebApp
metadata:
  name: frontend-abc
spec: {}
`)
	writeFile(t, repo, "k8s/bucket.yaml", `apiVersion: s3.aws.upbound.io/v1beta1
kind: Bucket
metadata:
  name: logs
spec:
  providerConfigRef:
    name: aws-default
  writeConnectionSecretToRef:
    name: logs-connection
`)
	writeFile(t, repo, "k8s/bucket-secret.yaml", `apiVersion: v1
kind: Secret
metadata:
  name: logs-connection
`)
	writeFile(t, repo, "k8s/webapp-claim.yaml", `apiVersion: platform.example.org/v1alpha1
kind: WebApp
metadata:
  name: frontend
spec:
  compositionRef:
    name: webapp
  resourceRef:
    apiVersion: platform.example.org/v1alpha1
    kind: XWebApp
    name: frontend-abc
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"Bucket.logs", "ProviderConfig.aws-default"},
		{"Bucket.logs", "Secret.logs-connection"},
		{"WebApp.frontend", "Composition.webapp"},
		{"WebApp.frontend", "XWebApp.frontend-abc"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", edge[0], edge[1]) {
			t.Fatalf("missing Crossplane dependency %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
}

func TestKustomizeResourceDependencies(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "overlays/prod/kustomization.yaml", `resources:
  - ../../base/deployment.yaml
  - service.yaml
patches:
  - patch-replicas.yaml
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"external:config:kustomize/file/../../base/deployment.yaml",
		"external:config:kustomize/file/service.yaml",
		"external:config:kustomize/file/patch-replicas.yaml",
	} {
		if !hasRelationTo(snapshot.Relations, "RESOURCE_DEPENDS_ON", target) {
			t.Fatalf("missing Kustomize dependency to %s in %#v", target, snapshot.Relations)
		}
	}
}

func TestDockerComposeServiceDependenciesAndConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "docker-compose.yml", `services:
  api:
    image: example/api:latest
    depends_on:
      - redis
      - db
    ports:
      - "8080:80"
    environment:
      LOG_LEVEL: debug
      FEATURE_FLAG: "true"
  db:
    image: postgres:16
  redis:
    image: redis:7
  base:
    image: example/base:latest
  worker:
    image: example/worker:latest
    links:
      - redis:cache
    extends:
      service: base
    network_mode: "service:db"
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, service := range []string{"compose.service.api", "compose.service.db", "compose.service.redis", "compose.service.base", "compose.service.worker"} {
		found := false
		for _, symbol := range snapshot.Symbols {
			if symbol.Kind == "resource" && symbol.QualifiedName == service {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing Compose service symbol %s in %#v", service, snapshot.Symbols)
		}
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "compose.service.api", "compose.service.db") {
		t.Fatalf("missing api->db Compose dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "compose.service.api", "compose.service.redis") {
		t.Fatalf("missing api->redis Compose dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "compose.service.worker", "compose.service.redis") {
		t.Fatalf("missing worker->redis Compose link dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "compose.service.worker", "compose.service.base") {
		t.Fatalf("missing worker->base Compose extends dependency in %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "compose.service.worker", "compose.service.db") {
		t.Fatalf("missing worker->db Compose network_mode dependency in %#v", snapshot.Relations)
	}
	for _, target := range []string{
		"external:config:compose/service/api",
		"external:config:compose/image/example/api:latest",
		"external:config:compose/env/FEATURE_FLAG",
		"external:config:compose/env/LOG_LEVEL",
		"external:config:compose/port/8080:80",
	} {
		if !hasRelationTo(snapshot.Relations, "CONFIGURES", target) {
			t.Fatalf("missing Compose config fact to %s in %#v", target, snapshot.Relations)
		}
	}
}

func TestChannelEventsShareNode(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "bus.js", `function publish(bus) {
  bus.emit("order.placed", {})
}

function subscribe(bus) {
  bus.on("order.placed", handle)
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var emits, listens RelationRecord
	for _, r := range snapshot.Relations {
		switch r.Type {
		case "EMITS":
			emits = r
		case "LISTENS_ON":
			listens = r
		}
	}
	if emits.ToID == "" || listens.ToID == "" {
		t.Fatalf("missing channel edges in %#v", snapshot.Relations)
	}
	if emits.ToID != listens.ToID {
		t.Fatalf("emit/listen should share a channel node: %q vs %q", emits.ToID, listens.ToID)
	}
	if !strings.HasSuffix(emits.ToID, "channel:order.placed") {
		t.Fatalf("unexpected channel node %q", emits.ToID)
	}
	if !contains(emits.WarningCodes, "WEAK_PATTERN") {
		t.Fatalf("channel edge should carry WEAK_PATTERN: %#v", emits)
	}
}

func TestTestsRelationLinksTestToUnit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "math.go", `package m

func Add(a int, b int) int { return a + b }
`)
	writeFile(t, repo, "math_test.go", `package m

import "testing"

func TestAdd(t *testing.T) {
	_ = Add(1, 2)
}

func TestNothingHere(t *testing.T) {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var tests [][2]string
	for _, r := range snapshot.Relations {
		if r.Type == "TESTS" {
			tests = append(tests, [2]string{lastSegment(r.FromID), lastSegment(r.ToID)})
		}
	}
	if len(tests) != 1 {
		t.Fatalf("want exactly one TESTS edge (TestAdd->Add), got %v", tests)
	}
	if tests[0][0] != "TestAdd" || tests[0][1] != "Add" {
		t.Fatalf("unexpected TESTS edge %v", tests[0])
	}
	// TestNothingHere has no matching subject -> no edge.
}

func TestProfilesControlRelationOutputAndHeader(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.go", `package auth

import "strings"

type Token struct {
	Value string
}

func (t *Token) Valid() bool {
	return strings.TrimSpace(t.Value) != ""
}

func Check(v string) bool {
	tok := Token{Value: v}
	return tok.Valid()
}
`)

	run := func(profile Profile) (SnapshotHeader, map[string]int) {
		var header SnapshotHeader
		byType := map[string]int{}
		err := StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: profile}, func(rec any) error {
			switch r := rec.(type) {
			case SnapshotHeader:
				header = r
			case RelationRecord:
				byType[r.Type]++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return header, byType
	}

	fullHeader, full := run(ProfileFull)
	if fullHeader.Profile != "full" || fullHeader.ProfileLimits.Evidence != "full" {
		t.Fatalf("full header = %#v", fullHeader)
	}
	if full["USES_TYPE"] == 0 || full["READS_FIELD"] == 0 {
		t.Fatalf("full profile should include deep relations: %v", full)
	}

	fastHeader, fast := run(ProfileFast)
	if fastHeader.Profile != "fast" || fastHeader.ProfileLimits.CallResolution != "shallow" {
		t.Fatalf("fast header = %#v", fastHeader)
	}
	if len(fastHeader.SkippedRelations) == 0 {
		t.Fatalf("fast header should list skipped relation families")
	}
	for _, deep := range []string{"USES_TYPE", "READS_FIELD", "EXTENDS", "SIMILAR_TO"} {
		if fast[deep] != 0 {
			t.Fatalf("fast profile must omit %s: %v", deep, fast)
		}
	}

	synHeader, syn := run(ProfileSyntaxOnly)
	if synHeader.Profile != "syntax-only" {
		t.Fatalf("syntax-only header = %#v", synHeader)
	}
	for relType := range syn {
		if relType != "DEFINES" && relType != "CONTAINS" {
			t.Fatalf("syntax-only emitted unexpected relation %q: %v", relType, syn)
		}
	}

	// Capabilities advertises the per-profile relation sets.
	caps := Capabilities()
	for _, p := range []string{"full", "fast", "syntax-only"} {
		if len(caps.RelationSupportByProfile[p]) == 0 {
			t.Fatalf("capabilities missing relation support for profile %q", p)
		}
	}
	if !contains(caps.RelationSupportByProfile["syntax-only"], "DEFINES") || contains(caps.RelationSupportByProfile["syntax-only"], "CALLS") {
		t.Fatalf("syntax-only profile relation set wrong: %v", caps.RelationSupportByProfile["syntax-only"])
	}
}

func TestMaxParseBytesSkipsOversizedFileWithPartialFailure(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "big.go", "package big\n\nfunc Large() {}\n"+strings.Repeat("// generated register mask\n", 20))

	var files int
	var symbols int
	var summary SnapshotSummary
	err := StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{MaxParseBytes: 64}, func(rec any) error {
		switch rec.(type) {
		case FileRecord:
			files++
		case SymbolRecord:
			symbols++
		case SnapshotSummary:
			summary = rec.(SnapshotSummary)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("files = %d, want 1", files)
	}
	if symbols != 0 {
		t.Fatalf("symbols = %d, want 0", symbols)
	}
	if summary.Stats.Files != 1 || summary.Stats.ParsedFiles != 0 {
		t.Fatalf("summary stats = %#v, want file emitted but not parsed", summary.Stats)
	}
	if len(summary.PartialFailures) != 1 || summary.PartialFailures[0].Code != "E_FILE_TOO_LARGE" {
		t.Fatalf("partial failures = %#v, want E_FILE_TOO_LARGE", summary.PartialFailures)
	}
}

func TestGoStructFieldsEmittedAsSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "account.go", `package bank

type Account struct {
	ID      string
	Balance int
	owner   string
}

func Open(name string, initial int) Account {
	normalized := name
	return Account{ID: normalized, Balance: initial}
}

func (a *Account) Deposit(amount int) {
	updated := a.Balance + amount
	a.Balance = updated
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	fields := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		if s.Kind == "field" {
			fields[s.QualifiedName] = s
		}
	}

	// Positive: each declared field is emitted, qualified under and contained by
	// the struct.
	var account SymbolRecord
	for _, s := range snapshot.Symbols {
		if s.Kind == "type" && s.QualifiedName == "Account" {
			account = s
		}
	}
	if account.ID == "" {
		t.Fatalf("Account type symbol missing")
	}
	for _, name := range []string{"Account.ID", "Account.Balance", "Account.owner"} {
		f, ok := fields[name]
		if !ok {
			t.Fatalf("missing field %q in %v", name, keysOfFields(fields))
		}
		if f.ContainerID != account.ID {
			t.Fatalf("field %q container = %q, want %q", name, f.ContainerID, account.ID)
		}
		if !strings.HasSuffix(f.ID, ":field:"+name) {
			t.Fatalf("field %q unstable/odd id %q", name, f.ID)
		}
		if f.Signature == "" {
			t.Fatalf("field %q missing signature/type text", name)
		}
	}

	// Negative: function params (name, initial, amount) and locals (normalized,
	// updated) must NOT be emitted as fields.
	for _, notField := range []string{"name", "initial", "amount", "normalized", "updated",
		"Open.name", "Open.initial", "Deposit.amount", "Deposit.normalized", "Deposit.updated"} {
		if _, ok := fields[notField]; ok {
			t.Fatalf("param/local %q was wrongly emitted as a field", notField)
		}
	}
	if len(fields) != 3 {
		t.Fatalf("want exactly 3 fields, got %d: %v", len(fields), keysOfFields(fields))
	}
}

func TestFieldAccessRelations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "account.go", `package bank

type Account struct {
	Balance int
}

func (a *Account) Deposit(amount int) {
	x := a.Balance + amount
	a.Balance = x
}

func leak(other *Account, raw map[string]int) {
	_ = other.Balance
	_ = raw.Balance
	other.Mystery = 1
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	reads, writes := map[string]RelationRecord{}, map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		switch r.Type {
		case "READS_FIELD":
			reads[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		case "WRITES_FIELD":
			writes[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
			if r.Confidence < 0.85 {
				t.Fatalf("field write confidence too low: %#v", r)
			}
		}
	}

	// a.Balance read and write inside Deposit resolve via the Go receiver.
	if _, ok := reads["Account.Deposit->Account.Balance"]; !ok {
		t.Fatalf("missing READS_FIELD Deposit->Balance: %v", reads)
	}
	if _, ok := writes["Account.Deposit->Account.Balance"]; !ok {
		t.Fatalf("missing WRITES_FIELD Deposit->Balance: %v", writes)
	}
	if r, ok := reads["leak->Account.Balance"]; !ok || r.Confidence != 0.83 {
		t.Fatalf("typed-parameter READS_FIELD leak->Balance not resolved (0.83): %#v", reads)
	}
	// raw.Balance (raw is a map, not Account) and other.Mystery (no such field)
	// must not produce edges — the field is not a known member of the receiver.
	for edge := range reads {
		if strings.HasPrefix(edge, "leak->") && edge != "leak->Account.Balance" {
			t.Fatalf("unresolved/dynamic access produced READS_FIELD: %s", edge)
		}
	}
	for edge := range writes {
		if strings.HasPrefix(edge, "leak->") {
			t.Fatalf("unresolved/dynamic access produced WRITES_FIELD: %s", edge)
		}
	}
}

func TestFieldsAcrossLanguages(t *testing.T) {
	cases := []struct {
		file      string
		source    string
		want      []string // qualified field names that must be present
		notFields []string // params/locals that must NOT be fields
	}{
		{
			file: "C.java",
			source: `class C {
  private int count;
  public String name;
  void m(int p) { int local = p; }
}
`,
			want:      []string{"C.count", "C.name"},
			notFields: []string{"p", "local", "C.p", "C.local", "m"},
		},
		{
			file: "C.cs",
			source: `namespace N { class C {
  public int Count;
  public string Name { get; set; }
  void M(int p) { int local = p; }
} }
`,
			want:      []string{"C.Count", "C.Name"},
			notFields: []string{"p", "local", "C.p", "C.local"},
		},
		{
			file: "c.ts",
			source: `export class C {
  count: number = 0
  private name: string
  go(p: number) { const local = p }
}
export interface I { size: number }
`,
			want:      []string{"C.count", "C.name", "I.size"},
			notFields: []string{"p", "local", "C.p", "C.local"},
		},
	}

	for _, tc := range cases {
		repo := t.TempDir()
		writeFile(t, repo, tc.file, tc.source)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		fields := map[string]SymbolRecord{}
		for _, s := range snapshot.Symbols {
			if s.Kind == "field" {
				fields[s.QualifiedName] = s
			}
		}
		for _, name := range tc.want {
			f, ok := fields[name]
			if !ok {
				t.Fatalf("%s: missing field %q in %v", tc.file, name, keysOfFields(fields))
			}
			if f.ContainerID == "" {
				t.Fatalf("%s: field %q has no container_id", tc.file, name)
			}
		}
		for _, name := range tc.notFields {
			if _, ok := fields[name]; ok {
				t.Fatalf("%s: %q wrongly emitted as a field", tc.file, name)
			}
		}
	}
}

func TestGoFieldIDsStableAcrossMethodBodyEdits(t *testing.T) {
	repo := t.TempDir() // same repo dir for both builds, so repo_key is constant
	build := func(body string) map[string]string {
		writeFile(t, repo, "account.go", `package bank

type Account struct {
	ID      string
	Balance int
}

func (a *Account) Touch() {
`+body+`
}
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, s := range snapshot.Symbols {
			if s.Kind == "field" {
				ids[s.QualifiedName] = s.ID
			}
		}
		return ids
	}

	before := build("\t_ = a.Balance")
	after := build("\tx := a.Balance\n\ty := x + 1\n\t_ = y")
	if len(before) != 2 {
		t.Fatalf("expected 2 fields, got %v", before)
	}
	for name, id := range before {
		if after[name] != id {
			t.Fatalf("field %q id changed across method body edit: %q -> %q", name, id, after[name])
		}
	}
}

// mapReader adapts an in-memory content map to a contentReader for tests.
func mapReader(contentByFile map[string]string) contentReader {
	return func(path string) (string, bool) {
		content, ok := contentByFile[path]
		return content, ok
	}
}

func keysOfFields(m map[string]SymbolRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestUsesTypeLinksSignatureTypes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "shop.go", `package shop

type Cart struct{ n int }

type Receipt struct{ total int }

func Checkout(cart Cart) Receipt {
	return Receipt{}
}

func label(name string) string {
	return name
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	uses := map[string]bool{}
	for _, r := range snapshot.Relations {
		if r.Type == "USES_TYPE" {
			uses[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = true
			if r.TargetKind != "symbol" {
				t.Fatalf("USES_TYPE should target a symbol: %#v", r)
			}
		}
	}
	if !uses["Checkout->Cart"] || !uses["Checkout->Receipt"] {
		t.Fatalf("Checkout should use Cart and Receipt: %v", uses)
	}
	// A signature of only primitives links to no local type.
	for key := range uses {
		if strings.HasPrefix(key, "label->") {
			t.Fatalf("primitive-only signature linked a type: %s", key)
		}
	}
}

func TestSimilarToLinksNearDuplicates(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "clones.go", `package c

func alpha(values []int) int {
	total := 0
	for _, value := range values {
		total += value * value
	}
	return total
}

func beta(values []int) int {
	total := 0
	for _, value := range values {
		total += value * value
	}
	return total
}

func unrelated(name string) bool {
	return len(name) > 0
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var pairs [][2]string
	for _, r := range snapshot.Relations {
		if r.Type != "SIMILAR_TO" {
			continue
		}
		if r.Confidence < 0.82 {
			t.Fatalf("SIMILAR_TO below threshold: %#v", r)
		}
		pairs = append(pairs, [2]string{lastSegment(r.FromID), lastSegment(r.ToID)})
	}
	if len(pairs) != 1 {
		t.Fatalf("want exactly one near-duplicate pair, got %v", pairs)
	}
	a, b := pairs[0][0], pairs[0][1]
	if !((a == "alpha" && b == "beta") || (a == "beta" && b == "alpha")) {
		t.Fatalf("unexpected pair %v", pairs[0])
	}
	if a == "unrelated" || b == "unrelated" {
		t.Fatalf("unrelated function linked: %v", pairs[0])
	}
}

func TestHTTPCallsDetectionAndRouteSeparation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "client.js", `function register(app) {
  app.get("/server-route", show)
}

function ping() {
  return axios.get("/client-route")
}

function external() {
  return fetch("https://api.example.com/v1/items")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	routes := map[string]bool{}
	httpCallByPath := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		switch r.Type {
		case "HANDLES_ROUTE":
			routes[lastSegment(r.ToID)] = true
		case "HTTP_CALLS":
			httpCallByPath[lastSegment(r.ToID)] = r
		}
	}

	// Server registration is a route, not a client call.
	if !routes["/server-route"] {
		t.Fatalf("server route missing: %v", routes)
	}
	if _, isCall := httpCallByPath["/server-route"]; isCall {
		t.Fatalf("server route misclassified as HTTP_CALLS")
	}
	// Client calls are HTTP_CALLS, not routes.
	if _, isRoute := routes["/client-route"]; isRoute {
		t.Fatalf("axios.get misclassified as a route")
	}
	if _, ok := httpCallByPath["/client-route"]; !ok {
		t.Fatalf("axios.get not detected as HTTP_CALLS: %v", httpCallByPath)
	}
	// Absolute URL reduces to its path at lower confidence.
	ext, ok := httpCallByPath["/v1/items"]
	if !ok {
		t.Fatalf("absolute-URL fetch not detected (path /v1/items): %v", httpCallByPath)
	}
	if ext.Confidence != 0.6 {
		t.Fatalf("absolute-URL call confidence = %v, want 0.6", ext.Confidence)
	}
}

func TestHTTPCallsBridgeToLocalRouteHandler(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", `export function register(app: any): void {
  app.get("/health", health)
}

export function health(): string {
  return "ok"
}

export async function ping(): Promise<unknown> {
  return fetch("/health")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/health") {
		t.Fatalf("missing HTTP_CALLS endpoint relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("missing HANDLES_ROUTE endpoint relation: %#v", snapshot.Relations)
	}
	var bridge RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && lastSegment(relation.FromID) == "ping" && lastSegment(relation.ToID) == "health" {
			bridge = relation
			break
		}
	}
	if bridge.FromID == "" {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
	if bridge.Resolution != "pattern" || bridge.TargetKind != "symbol" || bridge.Confidence > 0.72 {
		t.Fatalf("unexpected bridge metadata: %#v", bridge)
	}
}

func TestGoHTTPHandleFuncResolvesRouteHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import (
	"net/http"
)

const apiPrefix = "/api"
const healthRoute = apiPrefix + "/health"

func register(mux *http.ServeMux) {
	http.HandleFunc(healthRoute, health)
	mux.Handle("/ready", http.HandlerFunc(ready))
}

func health(w http.ResponseWriter, r *http.Request) {}

func ready(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/health")
	http.Get("http://localhost/ready")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/api/health") {
		t.Fatalf("missing Go HandleFunc route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "ready", "/ready") {
		t.Fatalf("missing Go HandlerFunc wrapper route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/api/health") {
		t.Fatalf("registration function was misclassified as route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "ready") {
		t.Fatalf("missing route bridge CALLS ping->ready: %#v", snapshot.Relations)
	}
}

func TestGoRouterMethodResolvesChiGinHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

const userRoute = "/api/users/{id}"

type Router interface {
	Get(string, http.HandlerFunc)
}

func register(r Router) {
	r.Get(userRoute, showUser)
}

func showUser(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/users/{id}")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/{id}") {
		t.Fatalf("missing Go router method route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/api/users/{id}") {
		t.Fatalf("registration function was misclassified as router handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestGoRouterSelectorHandlerResolvesUniqueMethodAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

type Router interface {
	Get(string, http.HandlerFunc)
}

type apiHandlers struct{}

func register(r Router, handlers apiHandlers) {
	r.Get("/api/users/{id}", handlers.ShowUser)
	http.HandleFunc("/api/health", handlers.Health)
}

func (apiHandlers) ShowUser(w http.ResponseWriter, r *http.Request) {}

func (apiHandlers) Health(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/users/{id}")
	http.Get("http://localhost/api/health")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "apiHandlers.ShowUser", "/api/users/{id}") {
		t.Fatalf("missing Go selector router method route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "apiHandlers.Health", "/api/health") {
		t.Fatalf("missing Go selector HandleFunc route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "apiHandlers.ShowUser") {
		t.Fatalf("missing route bridge CALLS ping->apiHandlers.ShowUser: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "apiHandlers.Health") {
		t.Fatalf("missing route bridge CALLS ping->apiHandlers.Health: %#v", snapshot.Relations)
	}
}

func TestGoRouterSelectorHandlerSkipsAmbiguousMethod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

type Router interface {
	Get(string, http.HandlerFunc)
}

type apiHandlers struct{}
type adminHandlers struct{}

func register(r Router, handlers apiHandlers) {
	r.Get("/api/users/{id}", handlers.ShowUser)
}

func (apiHandlers) ShowUser(w http.ResponseWriter, r *http.Request) {}

func (adminHandlers) ShowUser(w http.ResponseWriter, r *http.Request) {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "apiHandlers.ShowUser", "/api/users/{id}") ||
		hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "adminHandlers.ShowUser", "/api/users/{id}") {
		t.Fatalf("ambiguous Go selector route handler should not resolve: %#v", snapshot.Relations)
	}
}

func TestGoRouterGroupPrefixComposesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

const apiPrefix = "/api"

type Echo interface {
	Group(string) Group
}

type Group interface {
	GET(string, http.HandlerFunc)
}

func register(e Echo) {
	api := e.Group(apiPrefix)
	api.GET("/users/:id", showUser)
}

func showUser(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing Go grouped router route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/users/:id") {
		t.Fatalf("grouped Go route emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestGoChainedRouterGroupPrefixComposesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

const apiPrefix = "/api"

type App interface {
	Group(string) Group
}

type Group interface {
	Get(string, http.HandlerFunc)
}

func register(app App) {
	app.Group(apiPrefix).Get("/teams/{teamID}", showTeam)
}

func showTeam(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/teams/{teamID}")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showTeam", "/api/teams/{teamID}") {
		t.Fatalf("missing chained Go grouped router route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showTeam", "/teams/{teamID}") {
		t.Fatalf("chained grouped Go route emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showTeam") {
		t.Fatalf("missing route bridge CALLS ping->showTeam: %#v", snapshot.Relations)
	}
}

func TestGoNestedRouterGroupPrefixComposesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package server

import "net/http"

type Echo interface {
	Group(string) Group
}

type Group interface {
	Group(string) Group
	GET(string, http.HandlerFunc)
}

func register(e Echo) {
	api := e.Group("/api")
	v1 := api.Group("/v1")
	v1.GET("/users/:id", showUser)
	api.Group("/admin").GET("/audit", showAudit)
}

func showUser(w http.ResponseWriter, r *http.Request) {}

func showAudit(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/api/v1/users/:id")
	http.Get("http://localhost/api/admin/audit")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing nested Go grouped router route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showAudit", "/api/admin/audit") {
		t.Fatalf("missing nested chained Go grouped router route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/v1/users/:id") {
		t.Fatalf("nested grouped Go route emitted partially mounted route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showAudit") {
		t.Fatalf("missing route bridge CALLS ping->showAudit: %#v", snapshot.Relations)
	}
}

func TestStaticConstantRouteComposition(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", `const apiPrefix = "/api"

export function register(app: any): void {
  app.get(apiPrefix + "/health", health)
}

export function health(): string {
  return "ok"
}

export async function ping(): Promise<unknown> {
  return fetch("/api/health")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/api/health") {
		t.Fatalf("missing static-composed route: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("suffix literal was misreported as standalone route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
}

func TestComputedRouteExpressionComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", "const apiPrefix = \"/api\"\n"+
		"const versionPrefix = `${apiPrefix}/v1`\n"+
		"const usersRoute = versionPrefix + \"/users/:id\"\n\n"+
		"export function register(app: any): void {\n"+
		"  app.get(usersRoute, showUser)\n"+
		"}\n\n"+
		"export function showUser(): string {\n"+
		"  return \"ok\"\n"+
		"}\n\n"+
		"export async function ping(): Promise<unknown> {\n"+
		"  return fetch(usersRoute)\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing computed route expression: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/users/:id") {
		t.Fatalf("computed suffix was misreported as standalone route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestStaticArrayJoinRouteExpressionComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", "const apiPrefix = \"/api\"\n"+
		"const version = \"v1\"\n"+
		"const usersRoute = [apiPrefix, version, \"users\", \":id\"].join(\"/\")\n\n"+
		"export function register(app: any): void {\n"+
		"  app.get(usersRoute, showUser)\n"+
		"}\n\n"+
		"export function showUser(): string {\n"+
		"  return \"ok\"\n"+
		"}\n\n"+
		"export async function ping(): Promise<unknown> {\n"+
		"  return fetch([apiPrefix, version, \"users\", \":id\"].join(\"/\"))\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing static array-join route expression: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/v1/users/:id") {
		t.Fatalf("missing static array-join HTTP call relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing array-join route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestStaticPathJoinRouteExpressionComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", "const apiPrefix = \"/api\"\n"+
		"const version = \"v1\"\n"+
		"const usersRoute = path.posix.join(apiPrefix, version, \"users\", \":id\")\n\n"+
		"export function register(app: any): void {\n"+
		"  app.get(usersRoute, showUser)\n"+
		"}\n\n"+
		"export function showUser(): string {\n"+
		"  return \"ok\"\n"+
		"}\n\n"+
		"export async function ping(): Promise<unknown> {\n"+
		"  return fetch(path.join(apiPrefix, version, \"users\", \":id\"))\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing static path.join route expression: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/v1/users/:id") {
		t.Fatalf("missing static path.join HTTP call relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing path.join route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestStringRawTemplateRouteExpressionComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", "const version = \"v1\"\n\n"+
		"export function register(app: any): void {\n"+
		"  app.get(String.raw`/api/${version}/users/:id`, showUser)\n"+
		"}\n\n"+
		"export function showUser(): string {\n"+
		"  return \"ok\"\n"+
		"}\n\n"+
		"export async function ping(): Promise<unknown> {\n"+
		"  return fetch(String.raw`/api/${version}/users/:id`)\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing String.raw route expression: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/v1/users/:id") {
		t.Fatalf("missing String.raw HTTP call relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing String.raw route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestURLPathnameRouteConstantComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", "const base = \"https://example.invalid\"\n"+
		"const version = \"v1\"\n"+
		"const usersRoute = new URL(`/api/${version}/users/:id`, base).pathname\n\n"+
		"export function register(app: any): void {\n"+
		"  app.get(usersRoute, showUser)\n"+
		"}\n\n"+
		"export function showUser(): string {\n"+
		"  return \"ok\"\n"+
		"}\n\n"+
		"export async function ping(): Promise<unknown> {\n"+
		"  const route = new URL(\"/api/\" + version + \"/users/:id\", base).pathname\n"+
		"  return fetch(route)\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/v1/users/:id") {
		t.Fatalf("missing URL pathname route expression: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/v1/users/:id") {
		t.Fatalf("missing URL pathname HTTP call relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing URL pathname route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestFastifyDirectRouteResolvesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", `const userRoute = "/api/users/:id"

export function register(fastify: any): void {
  fastify.get(userRoute, showUser)
}

export function showUser(): string {
  return "ok"
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing Fastify direct route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/api/users/:id") {
		t.Fatalf("registration function was misclassified as Fastify handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestJavaScriptDirectRouteSelectorHandlerResolvesUniqueMemberAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", `const handlers = {}

export function register(app: any): void {
  app.get("/api/users/:id", handlers.showUser)
}

export function showUser(): string {
  return "ok"
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing JS selector route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestJavaScriptImportedRouteSelectorHandlerResolvesUniqueMemberAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const handlers = {}
export const usersRouter = Router()

usersRouter.get("/:id", handlers.showUser)

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { usersRouter } from "./routes"

export function register(app: any): void {
  app.use("/api/users", usersRouter)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing imported JS selector route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestRouteHandlerResolverSkipsAmbiguousSelectorMember(t *testing.T) {
	handlers := map[string]SymbolRecord{
		"apiHandlers.showUser":   {ID: "handler:api", Name: "showUser", QualifiedName: "apiHandlers.showUser"},
		"adminHandlers.showUser": {ID: "handler:admin", Name: "showUser", QualifiedName: "adminHandlers.showUser"},
	}
	if handler, ok := resolveRouteHandlerSymbol(handlers, "handlers.showUser"); ok {
		t.Fatalf("ambiguous selector should not resolve, got %#v", handler)
	}
}

func TestFastifyImportedPluginPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "users.ts", `const detailRoute = "/:id"

export async function usersRoutes(fastify: any): Promise<void> {
  fastify.get(detailRoute, showUser)
}

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { usersRoutes } from "./users"

const apiPrefix = "/api"
const userPrefix = apiPrefix + "/users"

export async function register(app: any): Promise<void> {
  app.register(usersRoutes, { prefix: userPrefix })
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing Fastify plugin route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestFastifyCommonJSExportedPluginPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "users.js", `const detailRoute = "/:id"

async function usersRoutes(fastify) {
  fastify.get(detailRoute, showUser)
}

function showUser() {
  return "ok"
}

module.exports = usersRoutes
`)
	writeFile(t, repo, "app.js", `const usersRoutes = require("./users")

const userPrefix = "/api/users"

async function register(app) {
  app.register(usersRoutes, { prefix: userPrefix })
}

async function ping() {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing CommonJS Fastify plugin route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "api.ts", `const usersRouter = Router()

export function register(app: any): void {
  usersRouter.get("/:id", showUser)
  app.use("/api/users", usersRouter)
}

export function showUser(): string {
  return "ok"
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/api/users/:id") {
		t.Fatalf("missing composed Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "register") {
		t.Fatalf("missing route bridge CALLS ping->register: %#v", snapshot.Relations)
	}
}

func TestExpressImportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const usersRouter = Router()

usersRouter.get("/:id", showUser)

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { usersRouter } from "./routes"

export function register(app: any): void {
  app.use("/api/users", usersRouter)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing cross-file Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressDefaultImportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `const usersRouter = Router()

usersRouter.get("/:id", showUser)

export function showUser(): string {
  return "ok"
}

export default usersRouter
`)
	writeFile(t, repo, "app.ts", `import usersRouter from "./routes"

export function register(app: any): void {
  app.use("/api/users", usersRouter)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing default-imported Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressCommonJSExportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.js", `const usersRouter = Router()

usersRouter.get("/:id", showUser)

function showUser() {
  return "ok"
}

module.exports = usersRouter
`)
	writeFile(t, repo, "app.js", `const usersRouter = require("./routes")

function register(app) {
  app.use("/api/users", usersRouter)
}

async function ping() {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing CommonJS-exported Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestHonoImportedRouteMountComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const users = new Hono()

users.get("/:id", showUser)

export function showUser(): Response {
  return new Response("ok")
}
`)
	writeFile(t, repo, "app.ts", `import { users } from "./routes"

export function register(app: any): void {
  app.route("/api/users", users)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing Hono route mount route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestKoaImportedRouterMountComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const users = new Router()

users.get("/:id", showUser)

export function showUser(ctx: any): void {
  ctx.body = "ok"
}
`)
	writeFile(t, repo, "app.ts", `import mount from "koa-mount"
import { users } from "./routes"

export function register(app: any): void {
  app.use(mount("/api/users", users.routes()))
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing Koa router mount route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestKoaRouterConstructorPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `const prefix = "/users"
export const users = new Router({ prefix })

users.get("/:id", showUser)

export function showUser(ctx: any): void {
  ctx.body = "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { users } from "./routes"

export function register(app: any): void {
  app.use(users.routes())
}

export async function ping(): Promise<unknown> {
  return fetch("/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/users/:id") {
		t.Fatalf("missing Koa constructor-prefix route: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/:id") {
		t.Fatalf("Koa constructor-prefix route emitted unprefixed child route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/users/:id") {
		t.Fatalf("missing matching HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressAliasedImportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const usersRouter = Router()

usersRouter.get("/:id", showUser)

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { usersRouter as routes } from "./routes"

export function register(app: any): void {
  app.use("/api/users", routes)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing aliased imported Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressNamespaceImportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `export const usersRouter = Router()

usersRouter.get("/:id", showUser)

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import * as routeModule from "./routes"

export function register(app: any): void {
  app.use("/api/users", routeModule.usersRouter)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing namespace imported Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestExpressStaticConstantRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.ts", `const detailRoute = "/:id"
export const usersRouter = Router()

usersRouter.get(detailRoute, showUser)

export function showUser(): string {
  return "ok"
}
`)
	writeFile(t, repo, "app.ts", `import { usersRouter } from "./routes"

const apiPrefix = "/api"
const userPrefix = apiPrefix + "/users"

export function register(app: any): void {
  app.use(userPrefix, usersRouter)
}

export async function ping(): Promise<unknown> {
  return fetch("/api/users/:id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "showUser", "/api/users/:id") {
		t.Fatalf("missing static-constant imported Express router route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
}

func TestNestJSControllerRoutesComposeAndBridgeHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "users.controller.ts", `import { Controller, Get, Post } from "@nestjs/common"

@Controller({ path: "api/users" })
export class UsersController {
  @Get(":id")
  showUser() {
    return "ok"
  }

  @Post()
  createUser() {
    return "created"
  }

  async ping() {
    return fetch("/api/users/:id")
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.showUser", "/api/users/:id") {
		t.Fatalf("missing composed NestJS GET route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.createUser", "/api/users") {
		t.Fatalf("missing composed NestJS POST route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "UsersController.ping", "/api/users/:id") {
		t.Fatalf("missing NestJS HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UsersController.ping", "UsersController.showUser") {
		t.Fatalf("missing route bridge CALLS ping->showUser: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UsersController", "/api/users/:id") {
		t.Fatalf("controller class was misclassified as route handler: %#v", snapshot.Relations)
	}
}

func TestPythonRouteDecoratorsBridgeToHTTPClients(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `from fastapi import FastAPI
import requests

app = FastAPI()

@app.get("/users/{id}")
def show_user(id: str):
    return {"id": id}

@app.route("/health", methods=["GET"])
def health():
    return "ok"

def ping():
    return requests.get("/health")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/users/{id}") {
		t.Fatalf("missing FastAPI decorator route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("missing Flask route decorator: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/health") {
		t.Fatalf("missing Python requests HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
}

func TestPythonAddAPIRouteResolvesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `from fastapi import FastAPI
import requests

app = FastAPI()
user_route = "/users/{id}"

def show_user(id: str):
    return {"id": id}

def register():
    app.add_api_route(user_route, show_user, methods=["GET"])

def ping():
    return requests.get("/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/users/{id}") {
		t.Fatalf("missing FastAPI add_api_route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/users/{id}") {
		t.Fatalf("registration function was misclassified as add_api_route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "show_user") {
		t.Fatalf("missing route bridge CALLS ping->show_user: %#v", snapshot.Relations)
	}
}

func TestFlaskAddURLRuleResolvesHandlersAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `from flask import Flask
import requests

app = Flask(__name__)
health_route = "/health"

def health():
    return "ok"

def status():
    return "up"

def register():
    app.add_url_rule(health_route, "health", health)
    app.add_url_rule("/status", view_func=status)

def ping_health():
    return requests.get("/health")

def ping_status():
    return requests.get("/status")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		handler string
		route   string
	}{
		{"health", "/health"},
		{"status", "/status"},
	} {
		if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", want.handler, want.route) {
			t.Fatalf("missing Flask add_url_rule route %s -> %s in %#v", want.handler, want.route, snapshot.Relations)
		}
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/health") {
		t.Fatalf("registration function was misclassified as Flask add_url_rule handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping_health", "health") {
		t.Fatalf("missing route bridge CALLS ping_health->health: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping_status", "status") {
		t.Fatalf("missing route bridge CALLS ping_status->status: %#v", snapshot.Relations)
	}
}

func TestFlaskMethodViewAddURLRuleResolvesClassAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `from flask import Flask
from flask.views import MethodView
import requests

app = Flask(__name__)
route = "/users/<id>"

class UserView(MethodView):
    def get(self, id):
        return {"id": id}

def register():
    app.add_url_rule(route, view_func=UserView.as_view("user_view"))

def ping_user():
    return requests.get("/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UserView", "/users/{id}") {
		t.Fatalf("missing Flask MethodView add_url_rule route: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "register", "/users/{id}") {
		t.Fatalf("registration function was misclassified as Flask MethodView handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping_user", "UserView") {
		t.Fatalf("missing route bridge CALLS ping_user->UserView: %#v", snapshot.Relations)
	}
}

func TestPythonAddAPIRouteSelectorHandlerResolvesUniqueMemberAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `from fastapi import FastAPI
import requests

app = FastAPI()
handlers = object()

def show_user(id: str):
    return {"id": id}

def register():
    app.add_api_route("/users/{id}", handlers.show_user, methods=["GET"])

def ping():
    return requests.get("/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/users/{id}") {
		t.Fatalf("missing FastAPI add_api_route selector handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "show_user") {
		t.Fatalf("missing route bridge CALLS ping->show_user: %#v", snapshot.Relations)
	}
}

func TestPythonTornadoRouteTupleBridgesToHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `import requests
import tornado.web

class UserHandler(tornado.web.RequestHandler):
    def get(self, user_id):
        self.write({"id": user_id})

class HealthHandler(tornado.web.RequestHandler):
    def get(self):
        self.write("ok")

def make_app():
    return tornado.web.Application([
        (r"/users/(?P<user_id>[^/]+)", UserHandler),
        ("/health", HealthHandler),
    ])

def ping():
    requests.get("/users/{user_id}")
    return requests.get("/health")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UserHandler", "/users/{user_id}") {
		t.Fatalf("missing Tornado handler route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "HealthHandler", "/health") {
		t.Fatalf("missing Tornado health route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "UserHandler") {
		t.Fatalf("missing route bridge CALLS ping->UserHandler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "HealthHandler") {
		t.Fatalf("missing route bridge CALLS ping->HealthHandler: %#v", snapshot.Relations)
	}
}

func TestDjangoURLPatternsResolveHandlersAndBridgeHTTPClients(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "urls.py", `import requests
from django.urls import path, re_path

def health(request):
    return "ok"

def ready(request):
    return "ok"

urlpatterns = [
    path("health/", health),
    re_path("^ready/$", ready),
]

def ping():
    requests.get("http://localhost/health")
    requests.get("http://localhost/ready")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("missing Django path route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "ready", "/ready") {
		t.Fatalf("missing Django re_path route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "ready") {
		t.Fatalf("missing route bridge CALLS ping->ready: %#v", snapshot.Relations)
	}
}

func TestDjangoClassBasedViewRoutesResolveAndBridgeHTTPClients(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "urls.py", `from django.urls import path
from . import views

urlpatterns = [
    path("users/<int:id>/", views.UserView.as_view(), name="user"),
]
`)
	writeFile(t, repo, "views.py", `import requests
from django.views import View

class UserView(View):
    def get(self, request, id):
        return {"id": id}

def ping_user():
    return requests.get("/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UserView", "/users/{id}") {
		t.Fatalf("missing Django class-based view route: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "ping_user", "/users/{id}") {
		t.Fatalf("Django client function was misclassified as route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping_user", "UserView") {
		t.Fatalf("missing route bridge CALLS ping_user->UserView: %#v", snapshot.Relations)
	}
}

func TestDjangoIncludeURLPatternsComposeHandlersAndBridgeHTTPClients(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "project/urls.py", `import requests
from django.urls import include, path

urlpatterns = [
    path("api/", include("users.urls")),
]

def ping():
    requests.get("http://localhost/api/health")
`)
	writeFile(t, repo, "users/urls.py", `from django.urls import path
from . import views

urlpatterns = [
    path("health/", views.health),
]
`)
	writeFile(t, repo, "users/views.py", `def health(request):
    return "ok"
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/api/health") {
		t.Fatalf("missing composed Django include route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("included Django URLConf emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
}

func TestDjangoImportedIncludeURLConfComposesHandlersAndBridgeHTTPClients(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "project/urls.py", `import requests
from django.urls import include, path
from users import urls as users_urls

urlpatterns = [
    path("api/", include(users_urls)),
]

def ping():
    requests.get("http://localhost/api/health")
`)
	writeFile(t, repo, "users/urls.py", `from django.urls import path
from . import views

urlpatterns = [
    path("health/", views.health),
]
`)
	writeFile(t, repo, "users/views.py", `def health(request):
    return "ok"
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasImportRelationToPath(snapshot.Relations, "project/urls.py", "users/urls.py") {
		t.Fatalf("missing local import project/urls.py -> users/urls.py: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/api/health") {
		t.Fatalf("missing imported Django include route handler: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "health", "/health") {
		t.Fatalf("imported Django include emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "health") {
		t.Fatalf("missing route bridge CALLS ping->health: %#v", snapshot.Relations)
	}
}

func TestPythonImportedRouterPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.py", `from fastapi import APIRouter

users_router = APIRouter()

@users_router.get("/{id}")
def show_user(id: str):
    return {"id": id}
`)
	writeFile(t, repo, "app.py", `from fastapi import FastAPI
import requests

from .routes import users_router

app = FastAPI()
app.include_router(users_router, prefix="/api/users")

def ping():
    return requests.get("/api/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "IMPORTS", "app.py", "routes.py") {
		t.Fatalf("missing local relative import app.py -> routes.py: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/api/users/{id}") {
		t.Fatalf("missing composed Python router route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/{id}") {
		t.Fatalf("missing matching Python HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "show_user") {
		t.Fatalf("missing route bridge CALLS ping->show_user: %#v", snapshot.Relations)
	}
}

func TestFlaskBlueprintPrefixComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes.py", `from flask import Blueprint

users_bp = Blueprint("users", __name__)

@users_bp.route("/<id>")
def show_user(id: str):
    return {"id": id}
`)
	writeFile(t, repo, "app.py", `from flask import Flask
import requests

from .routes import users_bp

app = Flask(__name__)
app.register_blueprint(users_bp, url_prefix="/api/users")

def ping():
    return requests.get("/api/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "IMPORTS", "app.py", "routes.py") {
		t.Fatalf("missing local relative import app.py -> routes.py: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/api/users/{id}") {
		t.Fatalf("missing composed Flask blueprint route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/{id}") {
		t.Fatalf("missing matching Python HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "show_user") {
		t.Fatalf("missing route bridge CALLS ping->show_user: %#v", snapshot.Relations)
	}
}

func TestFlaskImportedBlueprintAliasComposesAndBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes/users.py", `from flask import Blueprint

users_bp = Blueprint("users", __name__)

@users_bp.route("/<id>")
def show_user(id: str):
    return {"id": id}
`)
	writeFile(t, repo, "app.py", `from flask import Flask
import requests

from .routes.users import users_bp as mounted_users

app = Flask(__name__)
app.register_blueprint(mounted_users, url_prefix="/api/users")

def ping():
    return requests.get("/api/users/{id}")
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasImportRelationToPath(snapshot.Relations, "app.py", "routes/users.py") {
		t.Fatalf("missing local relative import app.py -> routes/users.py: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "show_user", "/api/users/{id}") {
		t.Fatalf("missing aliased imported Flask blueprint route: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/{id}") {
		t.Fatalf("missing matching Python HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "show_user") {
		t.Fatalf("missing route bridge CALLS ping->show_user: %#v", snapshot.Relations)
	}
}

func TestSpringRouteAnnotationsComposeClassPrefixAndBridgeHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/main/java/com/acme/UserController.java", `package com.acme;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.client.RestTemplate;

@RestController
@RequestMapping("/api")
class UserController {
  @GetMapping("/users/{id}")
  String show(String id) {
    return id;
  }

  @GetMapping({
    "/members/{id}",
    "/profiles/{id}"
  })
  String showMember(String id) {
    return id;
  }

  String ping(RestTemplate restTemplate) {
    restTemplate.getForObject("/api/users/{id}", String.class);
    restTemplate.getForObject("/api/members/{id}", String.class);
    return restTemplate.getForObject("/api/profiles/{id}", String.class);
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.show", "/api/users/{id}") {
		t.Fatalf("missing composed Spring route annotation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.showMember", "/api/members/{id}") {
		t.Fatalf("missing composed multi-line Spring member route annotation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.showMember", "/api/profiles/{id}") {
		t.Fatalf("missing composed multi-line Spring profile route annotation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "UserController.ping", "/api/users/{id}") {
		t.Fatalf("missing RestTemplate HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UserController.ping", "UserController.show") {
		t.Fatalf("missing route bridge CALLS ping->show: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UserController.ping", "UserController.showMember") {
		t.Fatalf("missing route bridge CALLS ping->showMember: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "UserController", "/api/users/{id}") {
		t.Fatalf("class body misclassified as HTTP_CALLS caller: %#v", snapshot.Relations)
	}
}

func TestCSharpAspNetRouteAttributesAndHttpClientBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Controllers/UsersController.cs", `using System.Net.Http;
using Microsoft.AspNetCore.Mvc;

[ApiController]
[Route("api/[controller]")]
public class UsersController : ControllerBase
{
    [HttpGet("{id}")]
    public string Show(string id)
    {
        return id;
    }

    public async Task<object> Ping(HttpClient client, string id)
    {
        return await client.GetFromJsonAsync<object>($"/api/users/{id}");
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UsersController.Show", "/api/users/{id}") {
		t.Fatalf("missing ASP.NET attribute route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "UsersController.Ping", "/api/users/{id}") {
		t.Fatalf("missing C# HttpClient HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UsersController.Ping", "UsersController.Show") {
		t.Fatalf("missing route bridge CALLS Ping->Show: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UsersController", "/api/users/{id}") {
		t.Fatalf("controller class was misclassified as route handler: %#v", snapshot.Relations)
	}
}

func TestCSharpMinimalAPIRouteRegistrationAndHttpClientBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Program.cs", `using System.Net.Http;
using Microsoft.AspNetCore.Builder;

const string UserRoute = "/api/users/{id}";

var app = WebApplication.Create();
app.MapGet(UserRoute, ApiHandlers.GetUser);
app.MapPost("/api/users", ApiHandlers.CreateUser);

public static class ApiHandlers
{
    public static string GetUser(string id)
    {
        return id;
    }

    public static string CreateUser()
    {
        return "ok";
    }

    public static async Task<object> Ping(HttpClient client, string id)
    {
        return await client.GetFromJsonAsync<object>($"/api/users/{id}");
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.GetUser", "/api/users/{id}") {
		t.Fatalf("missing C# minimal API GET route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.CreateUser", "/api/users") {
		t.Fatalf("missing C# minimal API POST route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ApiHandlers.Ping", "/api/users/{id}") {
		t.Fatalf("missing C# minimal API HttpClient HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ApiHandlers.Ping", "ApiHandlers.GetUser") {
		t.Fatalf("missing route bridge CALLS Ping->GetUser: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "Program", "/api/users/{id}") {
		t.Fatalf("program/setup symbol was misclassified as minimal API route handler: %#v", snapshot.Relations)
	}
}

func TestCSharpMinimalAPIMapGroupComposesHandlerAndBridge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Program.cs", `using System.Net.Http;
using Microsoft.AspNetCore.Builder;

const string ApiPrefix = "/api";

var app = WebApplication.Create();
var api = app.MapGroup(ApiPrefix);
var v1 = api.MapGroup("/v1");

api.MapGet("/users/{id}", ApiHandlers.GetUser);
v1.MapGet("/teams/{id}", ApiHandlers.GetTeam);
app.MapGroup("/admin").MapGet("/health", ApiHandlers.Health);

public static class ApiHandlers
{
    public static string GetUser(string id)
    {
        return id;
    }

    public static string GetTeam(string id)
    {
        return id;
    }

    public static string Health()
    {
        return "ok";
    }

    public static async Task<object> Ping(HttpClient client, string id)
    {
        await client.GetFromJsonAsync<object>($"/api/users/{id}");
        await client.GetFromJsonAsync<object>($"/api/v1/teams/{id}");
        return await client.GetFromJsonAsync<object>("/admin/health");
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.GetUser", "/api/users/{id}") {
		t.Fatalf("missing C# minimal API grouped GET route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.GetTeam", "/api/v1/teams/{id}") {
		t.Fatalf("missing C# minimal API nested grouped GET route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.Health", "/admin/health") {
		t.Fatalf("missing C# minimal API chained grouped route handler: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "ApiHandlers.GetUser", "/users/{id}") {
		t.Fatalf("grouped C# minimal API route emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ApiHandlers.Ping", "ApiHandlers.GetUser") {
		t.Fatalf("missing route bridge CALLS Ping->GetUser: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ApiHandlers.Ping", "ApiHandlers.GetTeam") {
		t.Fatalf("missing route bridge CALLS Ping->GetTeam: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ApiHandlers.Ping", "ApiHandlers.Health") {
		t.Fatalf("missing route bridge CALLS Ping->Health: %#v", snapshot.Relations)
	}
}

func TestLaravelRoutesResolveControllerMethodsAndBridgeHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes/web.php", `<?php

use App\Http\Controllers\UserController;

Route::get('/users/{id}', [UserController::class, 'show']);

function ping() {
    return Http::get('/users/{id}');
}
`)
	writeFile(t, repo, "app/Http/Controllers/UserController.php", `<?php

namespace App\Http\Controllers;

class UserController
{
    public function show(string $id): string
    {
        return $id;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.show", "/users/{id}") {
		t.Fatalf("missing Laravel controller route handler: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/users/{id}") {
		t.Fatalf("missing PHP HTTP facade call: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "UserController.show") {
		t.Fatalf("missing route bridge CALLS ping->UserController.show: %#v", snapshot.Relations)
	}
}

func TestLaravelPrefixGroupsComposeControllerRoutes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes/api.php", `<?php

use App\Http\Controllers\UserController;

Route::prefix('api')->group(function () {
    Route::get('users/{id}', [UserController::class, 'show']);
});

function ping() {
    return Http::get('/api/users/{id}');
}
`)
	writeFile(t, repo, "app/Http/Controllers/UserController.php", `<?php

namespace App\Http\Controllers;

class UserController
{
    public function show(string $id): string
    {
        return $id;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.show", "/api/users/{id}") {
		t.Fatalf("missing Laravel prefixed route handler: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.show", "/users/{id}") {
		t.Fatalf("Laravel prefix group emitted unprefixed child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "UserController.show") {
		t.Fatalf("missing route bridge CALLS ping->UserController.show: %#v", snapshot.Relations)
	}
}

func TestLaravelControllerGroupsComposeControllerRoutes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "routes/web.php", `<?php

use App\Http\Controllers\UserController;

Route::controller(UserController::class)->prefix('api/users')->group(function () {
    Route::get('/{id}', 'show');
    Route::post('/', 'store');
});

Route::prefix('api/admin')->controller(UserController::class)->group(function () {
    Route::get('/health', 'health');
});

function ping() {
    Http::get('/api/users/{id}');
    Http::post('/api/users');
    return Http::get('/api/admin/health');
}
`)
	writeFile(t, repo, "app/Http/Controllers/UserController.php", `<?php

namespace App\Http\Controllers;

class UserController
{
    public function show(string $id): string
    {
        return $id;
    }

    public function store(): string
    {
        return "ok";
    }

    public function health(): string
    {
        return "ok";
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		method string
		route  string
	}{
		{"UserController.show", "/api/users/{id}"},
		{"UserController.store", "/api/users"},
		{"UserController.health", "/api/admin/health"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", want.method, want.route) {
			t.Fatalf("missing Laravel controller-group route %s -> %s in %#v", want.method, want.route, snapshot.Relations)
		}
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", want.method) {
			t.Fatalf("missing route bridge CALLS ping->%s in %#v", want.method, snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "UserController.show", "/{id}") {
		t.Fatalf("Laravel controller group emitted unprefixed child route: %#v", snapshot.Relations)
	}
}

func TestRailsRoutesResolveControllerActionsAndBridgeHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config/routes.rb", `Rails.application.routes.draw do
  get "/users/:id", to: "users#show"
  post "/users", to: "users#create"
end
`)
	writeFile(t, repo, "app/controllers/users_controller.rb", `class UsersController
  def show
    "ok"
  end

  def create
    "created"
  end

  def ping
    HTTP.get("/users/:id")
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.show", "/users/:id") {
		t.Fatalf("missing Rails GET route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.create", "/users") {
		t.Fatalf("missing Rails POST route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "UsersController.ping", "/users/:id") {
		t.Fatalf("missing Rails HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UsersController.ping", "UsersController.show") {
		t.Fatalf("missing route bridge CALLS ping->UsersController.show: %#v", snapshot.Relations)
	}
}

func TestRailsResourcesResolveControllerActionsAndBridgeHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config/routes.rb", `Rails.application.routes.draw do
  resources :users, only: [:show, :create]
end
`)
	writeFile(t, repo, "app/controllers/users_controller.rb", `class UsersController
  def show
    "ok"
  end

  def create
    "created"
  end

  def ping
    HTTP.get("/users/:id")
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.show", "/users/:id") {
		t.Fatalf("missing Rails resources show route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.create", "/users") {
		t.Fatalf("missing Rails resources create route handler: %#v", snapshot.Relations)
	}
	if !hasRelationToExternalRoute(snapshot.Relations, "HTTP_CALLS", "UsersController.ping", "/users/:id") {
		t.Fatalf("missing Rails resources HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UsersController.ping", "UsersController.show") {
		t.Fatalf("missing route bridge CALLS ping->UsersController.show: %#v", snapshot.Relations)
	}
}

func TestRailsScopeAndNamespaceRoutesComposeControllerActions(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config/routes.rb", `Rails.application.routes.draw do
  scope "/api" do
    resources :users, only: [:show]
    get "/health", to: "health#show"
  end

  namespace :admin do
    resources :reports, only: [:show]
    get "/status", to: "status#show"
  end
end
`)
	writeFile(t, repo, "app/controllers/users_controller.rb", `class UsersController
  def show
    "ok"
  end
end
`)
	writeFile(t, repo, "app/controllers/health_controller.rb", `class HealthController
  def show
    "ok"
  end
end
`)
	writeFile(t, repo, "app/controllers/admin/reports_controller.rb", `module Admin
  class ReportsController
    def show
      "ok"
    end

    def ping
      HTTP.get("/admin/reports/:id")
    end
  end
end
`)
	writeFile(t, repo, "app/controllers/admin/status_controller.rb", `module Admin
  class StatusController
    def show
      "ok"
    end
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		handler string
		route   string
	}{
		{"UsersController.show", "/api/users/:id"},
		{"HealthController.show", "/api/health"},
		{"ReportsController.show", "/admin/reports/:id"},
		{"StatusController.show", "/admin/status"},
	} {
		if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", want.handler, want.route) {
			t.Fatalf("missing Rails scoped route %s -> %s in %#v", want.handler, want.route, snapshot.Relations)
		}
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "UsersController.show", "/users/:id") {
		t.Fatalf("Rails scope emitted unmounted child route: %#v", snapshot.Relations)
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "ReportsController.show", "/reports/:id") {
		t.Fatalf("Rails namespace emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ReportsController.ping", "ReportsController.show") {
		t.Fatalf("missing route bridge CALLS ping->ReportsController.show: %#v", snapshot.Relations)
	}
}

func TestRailsNestedResourcesComposeControllerActions(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config/routes.rb", `Rails.application.routes.draw do
  resources :users, only: [:show] do
    resources :posts, only: [:show, :create]
  end
end
`)
	writeFile(t, repo, "app/controllers/users_controller.rb", `class UsersController
  def show
    "ok"
  end
end
`)
	writeFile(t, repo, "app/controllers/posts_controller.rb", `class PostsController
  def show
    "ok"
  end

  def create
    "created"
  end

  def ping
    HTTP.get("/users/:user_id/posts/:id")
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		handler string
		route   string
	}{
		{"UsersController.show", "/users/:id"},
		{"PostsController.show", "/users/:user_id/posts/:id"},
		{"PostsController.create", "/users/:user_id/posts"},
	} {
		if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", want.handler, want.route) {
			t.Fatalf("missing Rails nested route %s -> %s in %#v", want.handler, want.route, snapshot.Relations)
		}
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "PostsController.show", "/posts/:id") {
		t.Fatalf("Rails nested resources emitted unmounted child route: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "PostsController.ping", "PostsController.show") {
		t.Fatalf("missing route bridge CALLS ping->PostsController.show: %#v", snapshot.Relations)
	}
}

func TestRailsDefaultResourcesAndExceptResolveControllerActions(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config/routes.rb", `Rails.application.routes.draw do
  resources :users
  resources :projects, except: %i[destroy]
end
`)
	writeFile(t, repo, "app/controllers/users_controller.rb", `class UsersController
  def index
    "index"
  end

  def new
    "new"
  end

  def show
    "show"
  end

  def edit
    "edit"
  end

  def create
    "create"
  end

  def update
    "update"
  end

  def destroy
    "destroy"
  end
end
`)
	writeFile(t, repo, "app/controllers/projects_controller.rb", `class ProjectsController
  def index
    "index"
  end

  def destroy
    "destroy"
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"UsersController.index", "/users"},
		{"UsersController.new", "/users/new"},
		{"UsersController.show", "/users/:id"},
		{"UsersController.edit", "/users/:id/edit"},
		{"UsersController.create", "/users"},
		{"UsersController.update", "/users/:id"},
		{"UsersController.destroy", "/users/:id"},
		{"ProjectsController.index", "/projects"},
	} {
		if !hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", edge[0], edge[1]) {
			t.Fatalf("missing Rails resource route %s -> %s in %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
	if hasRelationToExternalRoute(snapshot.Relations, "HANDLES_ROUTE", "ProjectsController.destroy", "/projects/:id") {
		t.Fatalf("Rails resources except emitted excluded destroy route: %#v", snapshot.Relations)
	}
}

func TestSymfonyRouteAttributesResolveHandler(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Controller/HealthController.php", `<?php

namespace App\Controller;

use Symfony\Component\Routing\Annotation\Route;

#[Route('/api')]
class HealthController
{
    #[Route('/health')]
    public function health(): string
    {
        return 'ok';
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "HealthController.health", "/api/health") {
		t.Fatalf("missing Symfony route attribute handler: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "HealthController", "/api") {
		t.Fatalf("controller class was misclassified as route handler: %#v", snapshot.Relations)
	}
}

func TestRouteDetectionRequiresRoutingContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.js", `function register(app) {
  app.get("/users/:id", show)
}

function loadFile() {
  const path = "/var/log/app.log"
  return readFileSync(path)
}

function buildUrl() {
  return "/api/v1/widgets"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var routes []string
	for _, r := range snapshot.Relations {
		if r.Type == "HANDLES_ROUTE" {
			routes = append(routes, r.ToID)
		}
	}
	if len(routes) != 1 {
		t.Fatalf("want exactly 1 route (the app.get registration), got %v", routes)
	}
	if !strings.HasSuffix(routes[0], "route:/users/:id") {
		t.Fatalf("unexpected route %q", routes[0])
	}
	// The /var/log path and the returned /api path must NOT become routes.
}

func TestBuildProviderSnapshotResolvesReceiverCalls(t *testing.T) {
	// Python uses '.' receivers, which the name-based path drops, so these edges
	// come only from receiver-type inference.
	repo := t.TempDir()
	writeFile(t, repo, "svc.py", `class Service:
    def helper(self):
        return 1

    def run(self):
        return self.helper()


def use(other):
    s = Service()
    s.helper()
    other.mystery()
`)
	writeFile(t, repo, "widget.ts", `class Widget {
  label(): string {
    return "ok"
  }
}

class Container {
  section(): Section {
    return new Section()
  }

  widget(): Widget {
    return new Widget()
  }
}

class Section {
  widget(): Widget {
    return new Widget()
  }
}

export function makeLabel(): string {
  return new Widget().label()
}

export function makeWidget(): Widget {
  return new Widget()
}

export function makeContainer(): Container {
  return new Container()
}

export function labelFromFactory(): string {
  return makeWidget().label()
}

export function labelFromConstructorChain(): string {
  return new Container().widget().label()
}

export function labelFromFactoryChain(): string {
  return makeContainer().widget().label()
}

export function labelFromDeepConstructorChain(): string {
  return new Container().section().widget().label()
}

export function labelFromDeepFactoryChain(): string {
  return makeContainer().section().widget().label()
}

export function labelFromAssignedFactory(): string {
  const widget = makeWidget()
  return widget.label()
}

export function labelFor(widget: Widget): string {
  return widget.label()
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	inferred := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && r.Resolution == "type_inferred" {
			inferred[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// self.helper() inside run -> resolves to the enclosing type's method.
	if r, ok := inferred["Service.run->Service.helper"]; !ok || r.Confidence != 0.9 {
		t.Fatalf("self-call not resolved (0.9): %#v", inferred)
	}
	// s = Service(); s.helper() -> resolves via the local variable's type.
	if r, ok := inferred["use->Service.helper"]; !ok || r.Confidence != 0.85 {
		t.Fatalf("local-var call not resolved (0.85): %#v", inferred)
	}
	// new Widget().label() -> resolves through the direct constructor chain.
	if r, ok := inferred["makeLabel->Widget.label"]; !ok || r.Confidence != 0.8 {
		t.Fatalf("constructor-chain call not resolved (0.8): %#v", inferred)
	}
	// makeWidget(): Widget; makeWidget().label() -> resolves through the factory return type.
	if r, ok := inferred["labelFromFactory->Widget.label"]; !ok || r.Confidence != 0.78 {
		t.Fatalf("returned-receiver call not resolved (0.78): %#v", inferred)
	}
	// new Container().widget().label() -> resolves through the explicitly returned Widget.
	if r, ok := inferred["labelFromConstructorChain->Widget.label"]; !ok || r.Confidence != 0.74 {
		t.Fatalf("constructor-return-chain call not resolved (0.74): %#v", inferred)
	}
	// makeContainer(): Container; Container.widget(): Widget; ...label() -> resolves two explicit return hops.
	if r, ok := inferred["labelFromFactoryChain->Widget.label"]; !ok || r.Confidence != 0.73 {
		t.Fatalf("returned-receiver-chain call not resolved (0.73): %#v", inferred)
	}
	// new Container().section().widget().label() resolves three explicit return hops.
	if r, ok := inferred["labelFromDeepConstructorChain->Widget.label"]; !ok || r.Confidence != 0.71 {
		t.Fatalf("deep constructor-return-chain call not resolved (0.71): %#v", inferred)
	}
	// makeContainer(): Container; Container.section(): Section; Section.widget(): Widget; ...label().
	if r, ok := inferred["labelFromDeepFactoryChain->Widget.label"]; !ok || r.Confidence != 0.7 {
		t.Fatalf("deep returned-receiver-chain call not resolved (0.7): %#v", inferred)
	}
	// const widget = makeWidget(); widget.label() -> resolves through the assigned factory return type.
	if r, ok := inferred["labelFromAssignedFactory->Widget.label"]; !ok || r.Confidence != 0.77 {
		t.Fatalf("assigned factory receiver call not resolved (0.77): %#v", inferred)
	}
	// widget: Widget -> resolves through the typed parameter.
	if r, ok := inferred["labelFor->Widget.label"]; !ok || r.Confidence != 0.83 {
		t.Fatalf("typed-parameter call not resolved (0.83): %#v", inferred)
	}
	// other.mystery(): receiver type unknown -> no fabricated edge.
	for key := range inferred {
		if strings.Contains(key, "mystery") {
			t.Fatalf("fabricated edge for unknown receiver: %s", key)
		}
	}
}

func TestBuildProviderSnapshotResolvesJavaSamePackageStaticOverload(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/main/java/org/acme/LauncherDiscoveryRequest.java", `package org.acme;

public class LauncherDiscoveryRequest {}
`)
	writeFile(t, repo, "src/main/java/org/acme/TestPlan.java", `package org.acme;

public class TestPlan {}
`)
	writeFile(t, repo, "src/main/java/org/acme/LauncherExecutionRequestBuilder.java", `package org.acme;

public class LauncherExecutionRequestBuilder {
  public static LauncherExecutionRequestBuilder request(LauncherDiscoveryRequest discoveryRequest) {
    return new LauncherExecutionRequestBuilder();
  }

  public static LauncherExecutionRequestBuilder request(TestPlan testPlan) {
    return new LauncherExecutionRequestBuilder();
  }
}
`)
	writeFile(t, repo, "src/main/java/org/acme/LauncherDiscoveryRequestBuilder.java", `package org.acme;

public class LauncherDiscoveryRequestBuilder {
  public LauncherExecutionRequestBuilder forExecution() {
    return LauncherExecutionRequestBuilder.request(build());
  }

  public LauncherDiscoveryRequest build() {
    return new LauncherDiscoveryRequest();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}
	var found RelationRecord
	for _, r := range snapshot.Relations {
		target := symbolsByID[r.ToID]
		if r.Type == "CALLS" &&
			strings.Contains(r.FromID, "LauncherDiscoveryRequestBuilder.forExecution") &&
			strings.Contains(r.ToID, "LauncherExecutionRequestBuilder.request") &&
			strings.Contains(target.Signature, "LauncherDiscoveryRequest") {
			found = r
			break
		}
	}
	if found.ToID == "" {
		t.Fatalf("missing same-package static overload CALLS relation: %#v", snapshot.Relations)
	}
	if found.Resolution != "type_inferred" || found.RelationScope != "module" || found.Confidence < 0.78 {
		t.Fatalf("unexpected relation metadata: %#v", found)
	}
}

func TestUniqueMethodFallbackSkipsImportedPackageCall(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "use.go", `package pkg

import "encoding/json"

func Encode(v any) ([]byte, error) {
	return json.Marshal(v)
}
`)
	writeFile(t, repo, "codec.go", `package pkg

type Codec struct{}

func (c Codec) Marshal() []byte { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// json.Marshal() is an imported-package call; the globally-unique
	// method-name fallback must NOT resolve it to the local Codec.Marshal method.
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, ":Encode") &&
			strings.Contains(r.ToID, "method:Codec.Marshal") {
			t.Fatalf("spurious local method edge for imported json.Marshal call: %s -> %s (%s)",
				r.FromID, r.ToID, r.Reason)
		}
	}
}

func TestUniqueMethodFallbackSkipsExternalTypedReceiver(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "use.go", `package pkg

import "bytes"

func Encode(buf *bytes.Buffer) {
	buf.Marshal()
}
`)
	writeFile(t, repo, "codec.go", `package pkg

type Codec struct{}

func (c Codec) Marshal() string { return "" }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// buf is a value of an external type (*bytes.Buffer); the globally-unique
	// method-name fallback must NOT resolve buf.Marshal() to the local
	// Codec.Marshal method just because the name is locally unique.
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, ":Encode") &&
			strings.Contains(r.ToID, "method:Codec.Marshal") {
			t.Fatalf("spurious local method edge for external-typed receiver buf.Marshal(): %s -> %s (%s)",
				r.FromID, r.ToID, r.Reason)
		}
	}
}

func TestUniqueMethodFallbackResolvesInModuleQualifiedReceiver(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/app\n\ngo 1.21\n")
	writeFile(t, repo, "conn.go", `package app

type Conn struct{}

func (c *Conn) WriteMessage() error { return nil }
`)
	writeFile(t, repo, "examples/client/main.go", `package main

import "example.com/app"

func run(ws *app.Conn) {
	ws.WriteMessage()
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// ws is qualified by the repo's OWN package (example.com/app), so Conn is an
	// in-module type and ws.WriteMessage() must resolve to the local
	// Conn.WriteMessage — the external-receiver guard must not suppress it.
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, ":run") &&
			strings.Contains(r.ToID, "method:Conn.WriteMessage") {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-module qualified receiver ws *app.Conn: expected ws.WriteMessage() -> Conn.WriteMessage edge, got none")
	}
}

func TestUniqueMethodFallbackResolvesFunctionReturnedReceiver(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/parse\n\ngo 1.21\n")
	writeFile(t, repo, "parse.go", `package parse

type Result struct{}

func (r Result) ForEach() {}

func Parse(s string) Result { return Result{} }
`)
	writeFile(t, repo, "use.go", `package parse

func walk(s string) {
	res := Parse(s)
	res.ForEach()
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// res := Parse(s) makes naive type inference record res's "type" as the
	// function name Parse (not its return type Result); the fallback must still
	// resolve res.ForEach() to the unique local Result.ForEach.
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, ":walk") &&
			strings.Contains(r.ToID, "method:Result.ForEach") {
			found = true
		}
	}
	if !found {
		t.Fatalf("function-returned receiver res := Parse(): expected res.ForEach() -> Result.ForEach edge, got none")
	}
}

func TestBuildProviderSnapshotEmitsImportedExternalCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "trim.go", `package api

import "strings"

func Clean(value string) string {
	return strings.TrimSpace(value)
}
`)
	writeFile(t, repo, "encode.py", `import json

def encode(value):
    return json.dumps(value)
`)
	writeFile(t, repo, "read.ts", `import { readFileSync } from "fs"
import * as path from "path"
import axios from "axios"

export function readConfig(name: string): string {
  axios.get("/config")
  return path.join("config", readFileSync(name, "utf8"))
}
`)
	writeFile(t, repo, "cjs.js", "const fs = require(\"fs\")\n"+
		"const { join } = require(\"path\")\n"+
		"const nodePrefix = \"node:\"\n"+
		"const fsModule = nodePrefix + \"fs\"\n"+
		"const pathModule = `${nodePrefix}path`\n"+
		"const streamModule = [nodePrefix, \"stream\"].join(\"\")\n"+
		"const runtimeFs = require(fsModule)\n"+
		"const { resolve } = require(pathModule)\n\n"+
		"const { Readable } = require(streamModule)\n\n"+
		"export async function readCommonJS(name) {\n"+
		"  await import(\"crypto\")\n"+
		"  return join(\"config\", fs.readFileSync(name, \"utf8\"))\n"+
		"}\n\n"+
		"export async function readComputedCommonJS(name) {\n"+
		"  await import(`${nodePrefix}crypto`)\n"+
		"  return resolve(\"config\", runtimeFs.readFileSync(name, \"utf8\"))\n"+
		"}\n\n"+
		"export function fromStream(value) {\n"+
		"  return Readable.from(value)\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		from   string
		target string
		detail string
	}{
		{from: "Clean", target: "strings.TrimSpace", detail: "strings.TrimSpace"},
		{from: "encode", target: "json.dumps", detail: "json.dumps"},
		{from: "readConfig", target: "fs.readFileSync", detail: "readFileSync"},
		{from: "readConfig", target: "path.join", detail: "path.join"},
		{from: "readConfig", target: "axios.get", detail: "axios.get"},
		{from: "readCommonJS", target: "fs.readFileSync", detail: "fs.readFileSync"},
		{from: "readCommonJS", target: "path.join", detail: "join"},
		{from: "readComputedCommonJS", target: "node:fs.readFileSync", detail: "runtimeFs.readFileSync"},
		{from: "readComputedCommonJS", target: "node:path.resolve", detail: "resolve"},
		{from: "fromStream", target: "node:stream.from", detail: "Readable.from"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "CALLS" && lastSegment(relation.FromID) == want.from && relation.ToID == externalID("symbol", want.target) {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing imported external call %s -> %s in %#v", want.from, want.target, snapshot.Relations)
		}
		if found.Resolution != "import_external" || found.RelationScope != "external" || found.TargetKind != "external" || found.Confidence < 0.78 {
			t.Fatalf("unexpected imported external call metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "imported_call_site" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected imported external call evidence: %#v", found.Evidence)
		}
	}
	for _, target := range []string{"external:import:fs", "external:import:path", "external:import:crypto", "external:import:node:fs", "external:import:node:path", "external:import:node:crypto", "external:import:node:stream"} {
		if !hasRelationTo(snapshot.Relations, "IMPORTS", target) {
			t.Fatalf("missing JS dynamic/CommonJS import to %s in %#v", target, snapshot.Relations)
		}
	}
}

func TestBuildProviderSnapshotEmitsGraphQLResolverBoundaries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/resolvers.ts", `export const resolvers = {
  Query: {
    user: (_parent, args) => ({ id: args.id }),
  },
  Mutation: {
    createUser: async (_parent, args) => ({ id: args.input.id }),
  },
  Subscription: {
    userCreated: {
      subscribe: (_parent, _args, ctx) => ctx.pubsub.asyncIterator("USER_CREATED"),
    },
  },
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		from string
		to   string
	}{
		{"Query.user", "query user"},
		{"Mutation.createUser", "mutation createUser"},
		{"Subscription.userCreated", "subscription userCreated"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", want.from, want.to) {
			t.Fatalf("missing HANDLES_GRAPHQL %s -> %s in %#v", want.from, want.to, snapshot.Relations)
		}
	}
}

func TestBuildProviderSnapshotEmitsGraphQLSchemaBoundaries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "schema.graphql", `type Query {
  user(id: ID!): User!
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

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		from string
		to   string
	}{
		{"Query.user", "query user"},
		{"Query.search", "query search"},
		{"Mutation.createUser", "mutation createUser"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", want.from, want.to) {
			t.Fatalf("missing schema HANDLES_GRAPHQL %s -> %s in %#v", want.from, want.to, snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", "User.id", "query id") {
		t.Fatalf("non-root object field was misreported as GraphQL boundary: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", "User.id", "user id") {
		t.Fatalf("non-root object field was misreported as GraphQL boundary: %#v", snapshot.Relations)
	}
}

func TestGraphQLSchemaAliasedRootsLinkToResolverFields(t *testing.T) {
	aliases := map[string]string{"rootquery": "query"}
	if got := graphqlBoundaryEndpoint(SymbolRecord{Kind: "graphql_resolver", Signature: "GraphQL resolver rootquery user"}, aliases); got != "query user" {
		t.Fatalf("aliased resolver endpoint = %q", got)
	}

	repo := t.TempDir()
	writeFile(t, repo, "schema.graphql", `schema {
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
	writeFile(t, repo, "src/resolvers.ts", `export const resolvers = {
  RootQuery: {
    user: (_parent, args) => ({ id: args.id }),
  },
  RootMutation: {
    createUser: async (_parent, args) => ({ id: args.input.id }),
  },
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"RootQuery.user", "query user"},
		{"RootMutation.createUser", "mutation createUser"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", want[0], want[1]) {
			t.Fatalf("missing aliased schema HANDLES_GRAPHQL %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
	}
	for _, want := range [][2]string{
		{"RootQuery.user", "RootQuery.user"},
		{"RootMutation.createUser", "RootMutation.createUser"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing aliased GraphQL schema-to-resolver CALLS %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
	}
}

func TestGraphQLSchemaFieldsLinkToResolverFields(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "schema.graphql", `type Query {
  user(id: ID!): User!
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
	writeFile(t, repo, "src/resolvers.ts", `export const resolvers = {
  Query: {
    user: getUser,
    search: searchUsers,
  },
  Mutation: {
    createUser: mutationResolvers.createUser,
  },
  User: {
    id: userFieldResolvers.id,
  },
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"Query.user", "Query.user"},
		{"Query.search", "Query.search"},
		{"Mutation.createUser", "Mutation.createUser"},
		{"User.id", "User.id"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing GraphQL schema-to-resolver CALLS %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", "User.id", "user id") {
		t.Fatalf("non-root GraphQL object field was misreported as GraphQL boundary: %#v", snapshot.Relations)
	}
}

func TestGraphQLSchemaFieldsLinkToModularResolverObjects(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "schema.graphql", `type Query {
  user(id: ID!): User!
}

type Mutation {
  createUser(input: CreateUserInput!): User!
}

type User {
  id: ID!
}
`)
	writeFile(t, repo, "src/user.resolvers.ts", `export const Query = {
  user: getUser,
}

export const Mutation = {
  createUser: mutationResolvers.createUser,
}

export const User = {
  id: userFieldResolvers.id,
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"Query.user", "Query.user"},
		{"Mutation.createUser", "Mutation.createUser"},
		{"User.id", "User.id"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing modular GraphQL schema-to-resolver CALLS %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
	}
	for _, want := range [][2]string{
		{"Query.user", "query user"},
		{"Mutation.createUser", "mutation createUser"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", want[0], want[1]) {
			t.Fatalf("missing modular GraphQL HANDLES_GRAPHQL %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
	}
	if hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", "User.id", "user id") {
		t.Fatalf("non-root modular GraphQL object field was misreported as GraphQL boundary: %#v", snapshot.Relations)
	}
}

func TestGraphQLOperationLiteralsEmitRootFieldBoundaries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "schema.graphql", `type Query {
  viewer: User!
  user(id: ID!): User!
}

type Mutation {
  updateUser(id: ID!): User!
}

type User {
  id: ID!
}
`)
	writeFile(t, repo, "src/client.ts", "export function loadViewer(id: string): unknown {\n"+
		"  return gql`query GetViewer($id: ID!) { viewer { id } me: user(id: $id) { id } }`\n"+
		"}\n\n"+
		"export function loadAnonymous(): unknown {\n"+
		"  return gql`query { viewer { id } }`\n"+
		"}\n\n"+
		"export function loadFragment(): unknown {\n"+
		"  return gql`query GetFragmentViewer { ...ViewerFields } fragment ViewerFields on Query { viewer { id } }`\n"+
		"}\n\n"+
		"export function mutate(id: string): unknown {\n"+
		"  return gql`mutation Update($id: ID!) { updateUser(id: $id) { id } }`\n"+
		"}\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		from string
		to   string
	}{
		{"loadViewer", "query viewer"},
		{"loadViewer", "query user"},
		{"loadAnonymous", "query viewer"},
		{"loadFragment", "query viewer"},
		{"mutate", "mutation updateUser"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", want.from, want.to) {
			t.Fatalf("missing GraphQL operation root-field HANDLES_GRAPHQL %s -> %s in %#v", want.from, want.to, snapshot.Relations)
		}
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_GRAPHQL", "loadViewer", "query GetViewer") {
		t.Fatalf("missing compatibility named-operation GraphQL boundary in %#v", snapshot.Relations)
	}
}

func TestBuildProviderSnapshotEmitsAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function helper(): string {
  return "ok"
}

function side(): string {
  return "side"
}

export function run(): string {
  const value = helper()
  const ignored = side()
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "helper" && lastSegment(relation.ToID) == "run" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing assigned return DATA_FLOWS helper->run: %#v", snapshot.Relations)
	}
	if found.Reason != "callee return value assigned to local and returned by caller" || found.Confidence > 0.75 {
		t.Fatalf("unexpected assigned return flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "assigned_return_flow" || found.Evidence[0].Detail != "helper -> value" {
		t.Fatalf("unexpected assigned return flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "side" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("non-returned assignment produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsDestructuredAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function helper(): [string, string] {
  return ["ok", "ignored"]
}

function side(): [string, string] {
  return ["side", "ignored"]
}

export function run(): string {
  const [value, _ignored] = helper()
  const [other] = side()
  return value
}
`)
	writeFile(t, repo, "flow.py", `def load_pair():
    return "ok", "ignored"

def ignored_pair():
    return "ignored", "ignored"

def run_py():
    value, _ = load_pair()
    other, _ = ignored_pair()
    return value
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"helper", "run"},
		{"load_pair", "run_py"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want[0] && lastSegment(relation.ToID) == want[1] && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "destructured_assigned_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing destructured assigned return DATA_FLOWS %s->%s: %#v", want[0], want[1], snapshot.Relations)
		}
		if found.Reason != "callee return value destructured into local and returned by caller" || found.Confidence > 0.75 {
			t.Fatalf("unexpected destructured flow metadata: %#v", found)
		}
	}
	for _, stale := range []string{"side", "ignored_pair"} {
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == stale {
				t.Fatalf("non-returned destructured assignment produced DATA_FLOWS: %#v", relation)
			}
		}
	}
}

func TestBuildProviderSnapshotEmitsBranchAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

function fallback(): string {
  return "fallback"
}

function ignored(): string {
  return "ignored"
}

export function run(flag: boolean): string {
  let value = ignored()
  if (flag) {
    value = primary()
  } else {
    value = fallback()
  }
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing branch DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value assigned in branch and returned by caller" || found.Confidence > 0.75 {
			t.Fatalf("unexpected branch flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "branch_assigned_return_flow" || found.Evidence[0].Detail != want+" -> value" {
			t.Fatalf("unexpected branch flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "ignored" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("pre-branch overwritten assignment produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsConditionalReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

async function fallback(): Promise<string> {
  return "fallback"
}

function side(): string {
  return "side"
}

export async function run(flag: boolean): Promise<string> {
  side()
  return flag ? primary() : await fallback()
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing conditional return DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value returned through conditional expression" || found.Confidence > 0.75 {
			t.Fatalf("unexpected conditional return flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "conditional_return_flow" || found.Evidence[0].Detail != want {
			t.Fatalf("unexpected conditional return flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "side" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("non-returned side call produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonConditionalReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def primary():
    return "primary"

def fallback():
    return "fallback"

def side():
    return "side"

def run(flag):
    side()
    return primary() if flag else fallback()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "conditional_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python conditional DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value returned through conditional expression" || found.Confidence > 0.75 {
			t.Fatalf("unexpected Python conditional return flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want {
			t.Fatalf("unexpected Python conditional return flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" || lastSegment(relation.ToID) != "run" {
			continue
		}
		if lastSegment(relation.FromID) == "side" {
			t.Fatalf("non-returned Python side call produced DATA_FLOWS: %#v", relation)
		}
		if lastSegment(relation.FromID) == "primary" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "return_flow" {
			t.Fatalf("Python conditional branch was mislabeled as unconditional return flow: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsFallbackReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

async function fallback(): Promise<string> {
  return "fallback"
}

function side(): string {
  return "side"
}

export async function run(): Promise<string> {
  side()
  return primary() || await fallback()
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing fallback return DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value returned through fallback expression" || found.Confidence > 0.75 {
			t.Fatalf("unexpected fallback return flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "fallback_return_flow" || found.Evidence[0].Detail != want {
			t.Fatalf("unexpected fallback return flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" || lastSegment(relation.ToID) != "run" {
			continue
		}
		if lastSegment(relation.FromID) == "side" {
			t.Fatalf("non-returned side call produced DATA_FLOWS: %#v", relation)
		}
		if lastSegment(relation.FromID) == "primary" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "return_flow" {
			t.Fatalf("fallback branch was mislabeled as unconditional return flow: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonFallbackReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def primary():
    return "primary"

def fallback():
    return "fallback"

def side():
    return "side"

def run():
    side()
    return primary() or fallback()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "fallback_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python fallback DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value returned through fallback expression" || found.Confidence > 0.75 {
			t.Fatalf("unexpected Python fallback return flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want {
			t.Fatalf("unexpected Python fallback return flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" || lastSegment(relation.ToID) != "run" {
			continue
		}
		if lastSegment(relation.FromID) == "side" {
			t.Fatalf("non-returned Python side call produced DATA_FLOWS: %#v", relation)
		}
		if lastSegment(relation.FromID) == "primary" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "return_flow" {
			t.Fatalf("Python fallback branch was mislabeled as unconditional return flow: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsFallbackAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

async function fallback(): Promise<string> {
  return "fallback"
}

function side(): string {
  return "side"
}

export async function run(): Promise<string> {
  const value = primary() ?? await fallback()
  const ignored = side()
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "fallback_assigned_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing fallback assigned DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value assigned through fallback expression and returned by caller" || found.Confidence > 0.75 {
			t.Fatalf("unexpected fallback assigned flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want+" -> value" {
			t.Fatalf("unexpected fallback assigned flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "side" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("non-returned side assignment produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsConditionalAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

async function fallback(): Promise<string> {
  return "fallback"
}

export async function run(flag: boolean): Promise<string> {
  const value = flag ? primary() : await fallback()
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "conditional_assigned_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing conditional assigned DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value assigned through conditional expression and returned by caller" || found.Confidence > 0.75 {
			t.Fatalf("unexpected conditional assigned flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want+" -> value" {
			t.Fatalf("unexpected conditional assigned flow evidence: %#v", found.Evidence)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonFallbackAssignedReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def primary():
    return "primary"

def fallback():
    return "fallback"

def side():
    return "side"

def run():
    value = primary() or fallback()
    ignored = side()
    return value
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary", "fallback"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == want && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "fallback_assigned_return_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python fallback assigned DATA_FLOWS %s->run: %#v", want, snapshot.Relations)
		}
		if found.Reason != "callee return value assigned through fallback expression and returned by caller" || found.Confidence > 0.75 {
			t.Fatalf("unexpected Python fallback assigned flow metadata: %#v", found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want+" -> value" {
			t.Fatalf("unexpected Python fallback assigned flow evidence: %#v", found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "side" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("non-returned Python side assignment produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotFallbackAssignedReturnHonorsOverwrite(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function primary(): string {
  return "primary"
}

function fallback(): string {
  return "fallback"
}

function finalValue(): string {
  return "final"
}

export function run(): string {
  let value = primary() || fallback()
  value = finalValue()
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "DATA_FLOWS", "finalValue", "run") {
		t.Fatalf("missing overwritten final assignment flow finalValue->run: %#v", snapshot.Relations)
	}
	for _, stale := range []string{"primary", "fallback"} {
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == stale && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "fallback_assigned_return_flow" {
				t.Fatalf("overwritten fallback assignment produced DATA_FLOWS %s->run: %#v", stale, relation)
			}
		}
	}
}

func TestBuildProviderSnapshotSequentialAssignmentKeepsLastReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function first(): string {
  return "first"
}

function second(): string {
  return "second"
}

export function run(): string {
  let value = first()
  value = second()
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "DATA_FLOWS", "second", "run") {
		t.Fatalf("missing last assignment DATA_FLOWS second->run: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "DATA_FLOWS", "first", "run") {
		t.Fatalf("overwritten sequential assignment produced DATA_FLOWS first->run: %#v", snapshot.Relations)
	}
}

func TestBuildProviderSnapshotEmitsAssignedPropertyReturnDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Result = { data: string }

function helper(): Result {
  return { data: "ok" }
}

function side(): Result {
  return { data: "side" }
}

export function run(): string {
  const result = helper()
  const ignored = side()
  return result.data
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "helper" && lastSegment(relation.ToID) == "run" && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "assigned_property_return_flow" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing assigned property return DATA_FLOWS helper->run: %#v", snapshot.Relations)
	}
	if found.Reason != "callee return value assigned to local and returned through property by caller" || found.Confidence > 0.75 {
		t.Fatalf("unexpected assigned property flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Detail != "helper -> result.data" {
		t.Fatalf("unexpected assigned property flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "side" && lastSegment(relation.ToID) == "run" {
			t.Fatalf("non-returned property assignment produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsArgumentForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function normalize(value: string): string {
  return value.trim()
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: string): string {
  const other = "static"
  normalize(input)
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing argument forward DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter forwarded into callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected argument forward flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "argument_forward_flow" || found.Evidence[0].Detail != "input -> normalize()" {
		t.Fatalf("unexpected argument forward flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter argument produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsParameterPropertyForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Input = { value: string, other: string }

function normalize(value: string): string {
  return value.trim()
}

function collect(value: string): string {
  return value.trim()
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: Input): string {
  normalize(input.value)
  collect(input["other"])
  const local = { value: "static" }
  ignore(local.value)
  return input.value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "input.value -> normalize()"},
		{callee: "collect", detail: "input[] -> collect()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing parameter property DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller parameter property forwarded into callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected parameter property flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "parameter_property_forward_flow" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected parameter property flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("local object property produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonParameterPropertyForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(value):
    return value.strip()

def collect(value):
    return value.strip()

def ignore(value):
    return "ignored"

def run(input):
    normalize(input.value)
    collect(input["other"])
    local = {"value": "static"}
    ignore(local["value"])
    return input.value
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "input.value -> normalize()"},
		{callee: "collect", detail: "input[] -> collect()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python parameter property DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller parameter property forwarded into callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected Python parameter property flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "parameter_property_forward_flow" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected Python parameter property flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("local Python object property produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsParameterPropertyAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Input = { value: string, other: string }

function normalize(value: string): string {
  return value.trim()
}

function collect(value: string): string {
  return value.trim()
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: Input): string {
  const value = input.value
  const other = input["other"]
  normalize(value)
  collect(other)
  const local = { value: "static" }
  const ignored = local.value
  ignore(ignored)
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "input.value -> value -> normalize()"},
		{callee: "collect", detail: "input[] -> other -> collect()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && relation.Evidence[0].Kind == "parameter_property_alias_forward_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing parameter property alias DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller parameter property alias forwarded into callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected parameter property alias flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "parameter_property_alias_forward_flow" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected parameter property alias flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("local object property alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonParameterPropertyAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(value):
    return value.strip()

def collect(value):
    return value.strip()

def ignore(value):
    return "ignored"

def run(input):
    value = input.value
    other = input["other"]
    normalize(value)
    collect(other)
    local = {"value": "static"}
    ignored = local["value"]
    ignore(ignored)
    return value
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "input.value -> value -> normalize()"},
		{callee: "collect", detail: "input[] -> other -> collect()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && relation.Evidence[0].Kind == "parameter_property_alias_forward_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python parameter property alias DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller parameter property alias forwarded into callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected Python parameter property alias flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "parameter_property_alias_forward_flow" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected Python parameter property alias flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("local Python object property alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function normalize(value: string): string {
  return value.trim()
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: string): string {
  const alias = input
  normalize(alias)
  const other = "static"
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing alias forward DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter alias forwarded into callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected alias forward flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "alias_forward_flow" || found.Evidence[0].Detail != "input -> alias -> normalize()" {
		t.Fatalf("unexpected alias forward flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsAliasContainerForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { value?: string }

function normalize(payload: Payload): string {
  return payload.value ?? ""
}

function fieldNormalize(payload: Payload): string {
  return payload.value ?? ""
}

function collect(values: string[]): string {
  return values.join(",")
}

function directObject(payload: Payload): string {
  return payload.value ?? ""
}

function directList(values: string[]): string {
  return values.join(",")
}

function ignore(payload: Payload): string {
  return "ignored"
}

export function run(input: string): string {
  const alias = input
  const payload = { value: alias }
  const fieldPayload = {}
  fieldPayload.value = alias
  const values = [alias]
  normalize(payload)
  fieldNormalize(fieldPayload)
  collect(values)
  directObject({ value: alias })
  directList([alias])
  const otherAlias = "input"
  const other = { value: otherAlias }
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		kind   string
		detail string
	}{
		{callee: "normalize", kind: "object_field_forward_flow", detail: "input -> payload -> normalize()"},
		{callee: "fieldNormalize", kind: "object_field_forward_flow", detail: "input -> fieldPayload -> fieldNormalize()"},
		{callee: "collect", kind: "collection_element_forward_flow", detail: "input -> values[] -> collect()"},
		{callee: "directObject", kind: "literal_argument_forward_flow", detail: "input -> literal -> directObject()"},
		{callee: "directList", kind: "literal_argument_forward_flow", detail: "input -> literal -> directList()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == want.kind {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing alias container DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected alias container flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("string alias container produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonAliasContainerForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(payload):
    return payload.get("value", "")

def field_normalize(payload):
    return payload.get("value", "")

def collect(values):
    return ",".join(values)

def direct_object(payload):
    return payload.get("value", "")

def direct_list(values):
    return ",".join(values)

def ignore(payload):
    return "ignored"

def run(input):
    alias = input
    payload = {"value": alias}
    field_payload = {}
    field_payload.value = alias
    values = [alias]
    normalize(payload)
    field_normalize(field_payload)
    collect(values)
    direct_object({"value": alias})
    direct_list([alias])
    other_alias = "input"
    other = {"value": other_alias}
    ignore(other)
    return input
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		kind   string
		detail string
	}{
		{callee: "normalize", kind: "object_field_forward_flow", detail: "input -> payload -> normalize()"},
		{callee: "field_normalize", kind: "object_field_forward_flow", detail: "input -> field_payload -> field_normalize()"},
		{callee: "collect", kind: "collection_element_forward_flow", detail: "input -> values[] -> collect()"},
		{callee: "direct_object", kind: "literal_argument_forward_flow", detail: "input -> literal -> direct_object()"},
		{callee: "direct_list", kind: "literal_argument_forward_flow", detail: "input -> literal -> direct_list()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == want.kind {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python alias container DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected Python alias container flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("Python string alias container produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsCallbackElementForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Item = { id: string }

function normalize(item: Item): string {
  return item.id
}

function persist(item: Item): void {
  console.log(item.id)
}

function ignore(value: string): void {
  console.log(value)
}

export function run(items: Item[]): Item[] {
  const alias = items
  items.map(item => normalize(item))
  alias.forEach(function(entry) {
    persist(entry)
  })
  const local = ["static"]
  local.map(value => ignore(value))
  return items
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "items[] -> item -> normalize()"},
		{callee: "persist", detail: "items[] -> entry -> persist()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == "callback_element_forward_flow" {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing callback element DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller collection element forwarded into callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected callback element flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected callback element flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("local collection callback produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsMultiHopAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { value?: string }

function normalize(value: string): string {
  return value.trim()
}

function wrap(payload: Payload): string {
  return payload.value ?? ""
}

function collect(values: string[]): string {
  return values.join(",")
}

function direct(payload: Payload): string {
  return payload.value ?? ""
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: string): string {
  const first = input
  const second = first
  normalize(second)
  const payload = { value: second }
  wrap(payload)
  const values = [second]
  collect(values)
  direct({ value: second })
  const local = "input"
  const localAlias = local
  ignore(localAlias)
  return second
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		kind   string
		detail string
	}{
		{callee: "normalize", kind: "alias_forward_flow", detail: "input -> second -> normalize()"},
		{callee: "wrap", kind: "object_field_forward_flow", detail: "input -> payload -> wrap()"},
		{callee: "collect", kind: "collection_element_forward_flow", detail: "input -> values[] -> collect()"},
		{callee: "direct", kind: "literal_argument_forward_flow", detail: "input -> literal -> direct()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == want.kind {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing multi-hop alias DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected multi-hop alias flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter multi-hop alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonMultiHopAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(value):
    return value.strip()

def wrap(payload):
    return payload.get("value", "")

def collect(values):
    return ",".join(values)

def direct(payload):
    return payload.get("value", "")

def ignore(value):
    return "ignored"

def run(input):
    first = input
    second = first
    normalize(second)
    payload = {"value": second}
    wrap(payload)
    values = [second]
    collect(values)
    direct({"value": second})
    local = "input"
    local_alias = local
    ignore(local_alias)
    return second
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		kind   string
		detail string
	}{
		{callee: "normalize", kind: "alias_forward_flow", detail: "input -> second -> normalize()"},
		{callee: "wrap", kind: "object_field_forward_flow", detail: "input -> payload -> wrap()"},
		{callee: "collect", kind: "collection_element_forward_flow", detail: "input -> values[] -> collect()"},
		{callee: "direct", kind: "literal_argument_forward_flow", detail: "input -> literal -> direct()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee && len(relation.Evidence) == 1 && relation.Evidence[0].Kind == want.kind {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python multi-hop alias DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected Python multi-hop alias flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter Python multi-hop alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsDestructuredAliasForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Input = { value: string }

function normalize(value: string): string {
  return value.trim()
}

function ignore(value: string): string {
  return "ignored"
}

export function run(input: Input): string {
  const { value } = input
  normalize(value)
  const { other } = { other: "static" }
  ignore(other)
  return value
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing destructured alias forward DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter destructured alias forwarded into callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected destructured alias flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "destructured_alias_forward_flow" || found.Evidence[0].Detail != "input -> value -> normalize()" {
		t.Fatalf("unexpected destructured alias flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter destructured alias produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsObjectFieldForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { value?: string }

function normalize(payload: Payload): string {
  return payload.value ?? ""
}

function ignore(payload: Payload): string {
  return "ignored"
}

export function run(input: string): string {
  const payload = {}
  payload.value = input
  normalize(payload)
  const other = {}
  other.value = "static"
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing object field DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter assigned into object field forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected object field flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "object_field_forward_flow" || found.Evidence[0].Detail != "input -> payload -> normalize()" {
		t.Fatalf("unexpected object field flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter object field produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsObjectLiteralForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { value?: string }

function normalize(payload: Payload): string {
  return payload.value ?? ""
}

function ignore(payload: Payload): string {
  return "ignored"
}

export function run(input: string): string {
  const payload = { value: input }
  normalize(payload)
  const other = { value: "static" }
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing object literal DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter assigned into object field forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected object literal flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "object_field_forward_flow" || found.Evidence[0].Detail != "input -> payload -> normalize()" {
		t.Fatalf("unexpected object literal flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter object literal produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsObjectShorthandForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { input?: string }

function normalize(payload: Payload): string {
  return payload.input ?? ""
}

function ignore(payload: Payload): string {
  return "ignored"
}

export function run(input: string): string {
  const payload = { input }
  normalize(payload)
  const other = { value: "input" }
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing object shorthand DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter assigned into object field forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected object shorthand flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "object_field_forward_flow" || found.Evidence[0].Detail != "input -> payload -> normalize()" {
		t.Fatalf("unexpected object shorthand flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter object shorthand produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonDictLiteralForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(payload):
    return payload.get("value", "")

def ignore(payload):
    return "ignored"

def run(input):
    payload = {"value": input}
    normalize(payload)
    other = {"value": "input"}
    ignore(other)
    return input
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing Python dict literal DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter assigned into object field forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected Python dict literal flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "object_field_forward_flow" || found.Evidence[0].Detail != "input -> payload -> normalize()" {
		t.Fatalf("unexpected Python dict literal flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter Python dict literal produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsDirectLiteralArgumentForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `type Payload = { value?: string }

function normalize(payload: Payload): string {
  return payload.value ?? ""
}

function collect(values: string[]): string {
  return values.join(",")
}

function ignore(payload: Payload): string {
  return "ignored"
}

export function run(input: string): string {
  normalize({ value: input })
  collect([input])
  ignore({ value: "input" })
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"normalize", "collect"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing direct literal DATA_FLOWS run->%s: %#v", want, snapshot.Relations)
		}
		if found.Reason != "caller parameter forwarded through literal callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected direct literal flow metadata for %s: %#v", want, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "literal_argument_forward_flow" || found.Evidence[0].Detail != "input -> literal -> "+want+"()" {
			t.Fatalf("unexpected direct literal flow evidence for %s: %#v", want, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("string literal direct object produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonDirectLiteralArgumentForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(payload):
    return payload.get("value", "")

def collect(values):
    return ",".join(values)

def ignore(payload):
    return "ignored"

def run(input):
    normalize({"value": input})
    collect([input])
    ignore({"value": "input"})
    return input
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"normalize", "collect"} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing Python direct literal DATA_FLOWS run->%s: %#v", want, snapshot.Relations)
		}
		if found.Reason != "caller parameter forwarded through literal callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected Python direct literal flow metadata for %s: %#v", want, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "literal_argument_forward_flow" || found.Evidence[0].Detail != "input -> literal -> "+want+"()" {
			t.Fatalf("unexpected Python direct literal flow evidence for %s: %#v", want, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("string literal Python direct object produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsCollectionElementForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function normalize(values: string[]): string {
  return values.join(",")
}

function normalizeMap(values: Map<string, string>): string {
  return values.get("value") ?? ""
}

function ignore(values: string[]): string {
  return "ignored"
}

function ignoreMap(values: Map<string, string>): string {
  return "ignored"
}

export function run(input: string): string {
  const values: string[] = []
  values.push(input)
  normalize(values)
  const alias = input
  const mapped = new Map()
  mapped.set("value", alias)
  normalizeMap(mapped)
  const other: string[] = []
  other.push("static")
  ignore(other)
  const otherMap = new Map()
  otherMap.set("value", "static")
  ignoreMap(otherMap)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		callee string
		detail string
	}{
		{callee: "normalize", detail: "input -> values[] -> normalize()"},
		{callee: "normalizeMap", detail: "input -> mapped[] -> normalizeMap()"},
	} {
		var found RelationRecord
		for _, relation := range snapshot.Relations {
			if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == want.callee {
				found = relation
				break
			}
		}
		if found.FromID == "" {
			t.Fatalf("missing collection element DATA_FLOWS run->%s: %#v", want.callee, snapshot.Relations)
		}
		if found.Reason != "caller parameter inserted into collection forwarded to callee argument" || found.Confidence > 0.7 {
			t.Fatalf("unexpected collection element flow metadata for %s: %#v", want.callee, found)
		}
		if len(found.Evidence) != 1 || found.Evidence[0].Kind != "collection_element_forward_flow" || found.Evidence[0].Detail != want.detail {
			t.Fatalf("unexpected collection element flow evidence for %s: %#v", want.callee, found.Evidence)
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && (lastSegment(relation.ToID) == "ignore" || lastSegment(relation.ToID) == "ignoreMap") {
			t.Fatalf("non-parameter collection element produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsCollectionLiteralElementForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function normalize(values: string[]): string {
  return values.join(",")
}

function ignore(values: string[]): string {
  return "ignored"
}

export function run(input: string): string {
  const values = [input]
  normalize(values)
  const other = ["static"]
  ignore(other)
  return input
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing collection literal DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter inserted into collection forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected collection literal flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "collection_element_forward_flow" || found.Evidence[0].Detail != "input -> values[] -> normalize()" {
		t.Fatalf("unexpected collection literal flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter collection literal produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsPythonCollectionLiteralElementForwardDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.py", `def normalize(values):
    return ",".join(values)

def ignore(values):
    return "ignored"

def run(input):
    values = [input]
    normalize(values)
    other = ["static"]
    ignore(other)
    return input
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var found RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "normalize" {
			found = relation
			break
		}
	}
	if found.FromID == "" {
		t.Fatalf("missing Python collection literal DATA_FLOWS run->normalize: %#v", snapshot.Relations)
	}
	if found.Reason != "caller parameter inserted into collection forwarded to callee argument" || found.Confidence > 0.7 {
		t.Fatalf("unexpected Python collection literal flow metadata: %#v", found)
	}
	if len(found.Evidence) != 1 || found.Evidence[0].Kind != "collection_element_forward_flow" || found.Evidence[0].Detail != "input -> values[] -> normalize()" {
		t.Fatalf("unexpected Python collection literal flow evidence: %#v", found.Evidence)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "ignore" {
			t.Fatalf("non-parameter Python collection literal produced DATA_FLOWS: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotEmitsTypeRelations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Animals.java", `package zoo;

interface Named {}

class Animal implements Named {}

public class Dog extends Animal implements Named {}
`)
	// C# uses ':' for both base class and interfaces; the I-prefix heuristic
	// distinguishes them, and an unknown supertype falls back to external.
	writeFile(t, repo, "Shapes.cs", `namespace S {
    interface IShape {}
    class Circle : Base, IShape {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type != "EXTENDS" && r.Type != "IMPLEMENTS" {
			continue
		}
		key := r.Type + " " + lastSegment(r.FromID) + "->" + lastSegment(r.ToID) + " " + r.Resolution
		seen[key] = r
	}
	keys := func() []string {
		out := make([]string, 0, len(seen))
		for k := range seen {
			out = append(out, k)
		}
		return out
	}

	// Java: Dog extends Animal (local), Dog implements Named (local), Animal
	// implements Named (local).
	for _, want := range []string{
		"EXTENDS Dog->Animal exact",
		"IMPLEMENTS Dog->Named exact",
		"IMPLEMENTS Animal->Named exact",
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing %q in %v", want, keys())
		}
	}

	// C#: Circle implements IShape (I-prefix heuristic), Circle extends Base
	// (unknown -> external endpoint).
	if _, ok := seen["IMPLEMENTS Circle->IShape exact"]; !ok {
		t.Fatalf("C# IShape not classified as IMPLEMENTS: %v", keys())
	}
	ext, ok := seen["EXTENDS Circle->Base name_only"]
	if !ok || ext.TargetKind != "external" {
		t.Fatalf("C# unknown base should be external EXTENDS: %v", keys())
	}
}

func lastSegment(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func hasRelationByLastSegment(relations []RelationRecord, relationType, from, to string) bool {
	for _, relation := range relations {
		if relation.Type == relationType && lastSegment(relation.FromID) == from && lastSegment(relation.ToID) == to {
			return true
		}
	}
	return false
}

func hasRelationByLastSegmentWithResolution(relations []RelationRecord, relationType, from, to, resolution string) bool {
	for _, relation := range relations {
		if relation.Type == relationType && relation.Resolution == resolution && lastSegment(relation.FromID) == from && lastSegment(relation.ToID) == to {
			return true
		}
	}
	return false
}

func hasRelationToExternalRoute(relations []RelationRecord, relationType, from, route string) bool {
	for _, relation := range relations {
		if relation.Type == relationType && lastSegment(relation.FromID) == from && relation.ToID == externalID("route", route) {
			return true
		}
	}
	return false
}

func hasRelationTo(relations []RelationRecord, relationType, to string) bool {
	for _, relation := range relations {
		if relation.Type == relationType && relation.ToID == to {
			return true
		}
	}
	return false
}

func hasImportRelation(relations []RelationRecord, fromPath, toID string) bool {
	for _, relation := range relations {
		if relation.Type == "IMPORTS" && strings.HasSuffix(relation.FromID, "file:"+fromPath) && relation.ToID == toID {
			return true
		}
	}
	return false
}

func hasImportRelationToPath(relations []RelationRecord, fromPath, toPath string) bool {
	for _, relation := range relations {
		if relation.Type != "IMPORTS" || relation.TargetKind != "file" {
			continue
		}
		if strings.HasSuffix(relation.FromID, "file:"+fromPath) && strings.HasSuffix(relation.ToID, "file:"+toPath) {
			return true
		}
	}
	return false
}

func hasImportRelationWithEvidence(relations []RelationRecord, fromPath, toID, detail, evidenceKind string) bool {
	for _, relation := range relations {
		if relation.Type != "IMPORTS" || !strings.HasSuffix(relation.FromID, "file:"+fromPath) || relation.ToID != toID {
			continue
		}
		if len(relation.Evidence) == 1 && relation.Evidence[0].Detail == detail && relation.Evidence[0].Kind == evidenceKind {
			return true
		}
	}
	return false
}

func TestBuildProviderSnapshotEmitsOverrides(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Shapes.java", `package s;

class Base {
    public String describe() { return "base"; }
    public int unique() { return 1; }
}

public class Circle extends Base {
    public String describe() { return "circle"; }
}
`)
	// External/unknown supertype: no local methods are known, so no override.
	writeFile(t, repo, "Ext.java", `package s;

public class Widget extends javax.swing.JComponent {
    public void paint() {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var overrides []RelationRecord
	for _, r := range snapshot.Relations {
		if r.Type == "OVERRIDES" {
			overrides = append(overrides, r)
		}
	}
	if len(overrides) != 1 {
		t.Fatalf("want exactly 1 override (Circle.describe -> Base.describe), got %d: %#v", len(overrides), overrides)
	}
	o := overrides[0]
	if !strings.HasSuffix(o.FromID, "method:Circle.describe") || !strings.HasSuffix(o.ToID, "method:Base.describe") {
		t.Fatalf("override edge = %s -> %s", o.FromID, o.ToID)
	}
	if o.TargetKind != "symbol" || o.Resolution != "exact" {
		t.Fatalf("override classification = %#v", o)
	}
}

func TestBuildRelationsDoesNotCreditContainerAsCaller(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", `class AuthService:
    def validate(self, token):
        return bool(token)


def check_token(token):
    service = AuthService()
    return service.validate(token)
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		// The class must never be reported as calling its own members; those
		// matches come from member definition lines, not real call sites.
		if strings.Contains(relation.FromID, ":class:AuthService") {
			t.Fatalf("class credited as caller: %#v", relation)
		}
	}
}

func TestBuildProviderSnapshotResolvesRelativeImports(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/util.ts", "export function helper(v: string): string {\n  return v\n}\n")
	writeFile(t, repo, "src/app.ts", `import { helper } from "./util"
import { readFileSync } from "fs"

export function run(): string {
  return helper(readFileSync("c", "utf8"))
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var resolved, external RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type != "IMPORTS" {
			continue
		}
		switch {
		case relation.Resolution == "import_resolved":
			resolved = relation
		case strings.Contains(relation.ToID, "external:import:fs"):
			external = relation
		}
	}

	if resolved.ToID == "" {
		t.Fatalf("relative import ./util was not resolved: %#v", snapshot.Relations)
	}
	if !strings.HasSuffix(resolved.ToID, ":file:src/util.ts") || resolved.TargetKind != "file" || resolved.RelationScope != "module" {
		t.Fatalf("resolved import = %#v", resolved)
	}
	if external.ToID == "" || external.Resolution != "name_only" || external.TargetKind != "external" {
		t.Fatalf("external import fs misclassified: %#v", external)
	}
}

func TestBuildProviderSnapshotEmitsSchema11Fields(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.go", `package auth

import "strings"

func Validate(token string) bool {
	return strings.TrimSpace(token) != ""
}

func Check(token string) bool {
	return Validate(token)
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	// Header advertises the optional features and a completeness breakdown.
	if !contains(snapshot.Header.SchemaFeatures, "relation_resolution") {
		t.Fatalf("schema_features missing relation_resolution: %#v", snapshot.Header.SchemaFeatures)
	}
	if snapshot.Header.LanguageVersions["go-tree-sitter"] == "" {
		t.Fatalf("language_versions missing go-tree-sitter: %#v", snapshot.Header.LanguageVersions)
	}
	if snapshot.Header.Completeness.Languages["Go"].Symbols == 0 {
		t.Fatalf("completeness has no Go symbols: %#v", snapshot.Header.Completeness)
	}
	if snapshot.Header.Completeness.Relations["DEFINES"] == 0 {
		t.Fatalf("completeness has no DEFINES relations: %#v", snapshot.Header.Completeness)
	}

	var calls, defines RelationRecord
	for _, relation := range snapshot.Relations {
		switch relation.Type {
		case "CALLS":
			if relation.TargetKind == "symbol" {
				calls = relation
			}
		case "DEFINES":
			defines = relation
		}
	}
	if defines.TargetKind != "symbol" || defines.Resolution != "exact" || defines.RelationScope != "file" {
		t.Fatalf("DEFINES classification = %#v", defines)
	}
	if calls.FromID == "" {
		t.Fatalf("missing CALLS relation in %#v", snapshot.Relations)
	}
	if calls.TargetKind != "symbol" || calls.Resolution == "" || calls.RelationScope == "" {
		t.Fatalf("CALLS classification = %#v", calls)
	}
	if len(calls.Evidence) == 0 || calls.Evidence[0].Kind != "call_site" || calls.Evidence[0].FilePath == "" {
		t.Fatalf("CALLS evidence = %#v", calls.Evidence)
	}
}

func TestBuildProviderSnapshotAddsBoundarySourceLocations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "apps/web/src/app/oauth/device/code/route.ts", `export async function POST(request: Request) {
  return Response.json({ ok: true })
}
`)
	writeFile(t, repo, "apps/web/src/app/api/internal/feed-crawler/tick/route.ts", `async function handleFeedCrawlerTick(request: Request) {
  return Response.json({ ok: true })
}

export async function GET(request: Request) {
  return handleFeedCrawlerTick(request)
}
`)
	writeFile(t, repo, "src/app/api/internal/post-transcription/tick/route.ts", `export async function GET(request: Request) {
  return Response.json({ ok: true })
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	route := symbolByKindAndName(snapshot.Symbols, "route", "/oauth/device/code")
	if route.ID == "" {
		t.Fatalf("missing route boundary in %#v", snapshot.Symbols)
	}
	if route.FilePath != "apps/web/src/app/oauth/device/code/route.ts" || route.StartLine != 1 {
		t.Fatalf("route source = %#v", route)
	}

	workflow := symbolByKindAndName(snapshot.Symbols, "workflow", "feed-crawler")
	if workflow.ID == "" {
		t.Fatalf("missing workflow boundary in %#v", snapshot.Symbols)
	}
	if workflow.FilePath != "apps/web/src/app/api/internal/feed-crawler/tick/route.ts" {
		t.Fatalf("workflow source = %#v", workflow)
	}

	rootRoute := symbolByKindAndName(snapshot.Symbols, "route", "/api/internal/post-transcription/tick")
	if rootRoute.ID == "" {
		t.Fatalf("missing route boundary for repo-root src/app path in %#v", snapshot.Symbols)
	}
	rootWorkflow := symbolByKindAndName(snapshot.Symbols, "workflow", "post-transcription")
	if rootWorkflow.ID == "" {
		t.Fatalf("missing workflow boundary for repo-root src/app path in %#v", snapshot.Symbols)
	}
}

func TestNextJSRouteBoundaryBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/app/api/users/[id]/route.ts", `export async function GET(request: Request) {
  return Response.json({ ok: true })
}
`)
	writeFile(t, repo, "src/client.ts", `export async function ping(): Promise<unknown> {
  return fetch("/api/users/[id]")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/api/users/{id}") {
		t.Fatalf("missing HTTP_CALLS to Next.js route boundary: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "/api/users/{id}") {
		t.Fatalf("missing route bridge CALLS ping->Next.js route boundary: %#v", snapshot.Relations)
	}
	route := symbolByKindAndName(snapshot.Symbols, "route", "/api/users/{id}")
	if route.ID == "" || route.ContainerID == "" {
		t.Fatalf("missing Next.js route boundary sourced to handler: %#v", route)
	}
}

func TestSvelteKitRouteBoundaryBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/routes/users/[id]/+server.ts", `export async function GET({ params }): Promise<Response> {
  return Response.json({ id: params.id })
}
`)
	writeFile(t, repo, "src/lib/client.ts", `export async function ping(): Promise<unknown> {
  return fetch("/users/[id]")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/users/{id}") {
		t.Fatalf("missing HTTP_CALLS to SvelteKit route boundary: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "/users/{id}") {
		t.Fatalf("missing route bridge CALLS ping->SvelteKit route boundary: %#v", snapshot.Relations)
	}
	route := symbolByKindAndName(snapshot.Symbols, "route", "/users/{id}")
	if route.ID == "" || route.ContainerID == "" || route.FilePath != "src/routes/users/[id]/+server.ts" {
		t.Fatalf("missing SvelteKit route boundary sourced to handler: %#v", route)
	}
}

func TestRemixRouteBoundaryBridgesHTTPClient(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/routes/users.$id.tsx", `export async function loader({ params }): Promise<Response> {
  return Response.json({ id: params.id })
}
`)
	writeFile(t, repo, "app/client.ts", `export async function ping(): Promise<unknown> {
  return fetch("/users/$id")
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "ping", "/users/{id}") {
		t.Fatalf("missing HTTP_CALLS to Remix route boundary: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "ping", "/users/{id}") {
		t.Fatalf("missing route bridge CALLS ping->Remix route boundary: %#v", snapshot.Relations)
	}
	route := symbolByKindAndName(snapshot.Symbols, "route", "/users/{id}")
	if route.ID == "" || route.ContainerID == "" || route.FilePath != "app/routes/users.$id.tsx" {
		t.Fatalf("missing Remix route boundary sourced to handler: %#v", route)
	}
}

func TestCapabilitiesAdvertiseExpandedLanguageSet(t *testing.T) {
	caps := Capabilities()
	if caps.SchemaVersion != SchemaVersion || caps.Provider != ProviderName {
		t.Fatalf("capabilities identity = %#v", caps)
	}
	if len(caps.SupportedLanguages) < 158 {
		t.Fatalf("supported language/filetype count = %d, want at least 158: %#v", len(caps.SupportedLanguages), caps.SupportedLanguages)
	}
	if len(caps.SemanticLanguages) == 0 {
		t.Fatalf("semantic languages missing: %#v", caps)
	}
	if len(caps.InventoryOnlyLanguages) == 0 {
		t.Fatalf("inventory-only languages missing: %#v", caps)
	}
	seen := map[string]bool{}
	for _, language := range caps.SupportedLanguages {
		seen[language] = true
	}
	semanticSeen := map[string]bool{}
	for _, language := range caps.SemanticLanguages {
		semanticSeen[language] = true
		if !seen[language] {
			t.Fatalf("semantic language %q missing from supported languages", language)
		}
	}
	inventorySeen := map[string]bool{}
	for _, language := range caps.InventoryOnlyLanguages {
		inventorySeen[language] = true
		if !seen[language] {
			t.Fatalf("inventory-only language %q missing from supported languages", language)
		}
		if semanticSeen[language] {
			t.Fatalf("language %q appears in both semantic and inventory-only tiers", language)
		}
	}
	for _, want := range []string{
		"Bash",
		"C",
		"C#",
		"C++",
		"CUE",
		"Elixir",
		"Go",
		"Groovy",
		"HCL",
		"Java",
		"JavaScript",
		"Kotlin",
		"Lua",
		"OCaml",
		"PHP",
		"Protocol Buffers",
		"Python",
		"Ruby",
		"Rust",
		"SQL",
		"Scala",
		"Swift",
		"TypeScript",
		"Zsh",
		"Dart",
		"Zig",
		"Bicep",
		"GraphQL",
		"Solidity",
		"Nix",
		"Kustomize",
	} {
		if !seen[want] {
			t.Fatalf("capabilities missing language %q in %#v", want, caps.SupportedLanguages)
		}
	}
	for _, want := range []string{"TypeScript", "Python", "JavaScript", "Java", "C++", "C", "C#", "Go", "PHP", "Rust", "Kotlin", "Ruby", "Swift", "SQL", "Bash", "Zsh", "Dart"} {
		if !semanticSeen[want] {
			t.Fatalf("capabilities should classify %q as semantic, got semantic=%#v inventory=%#v", want, caps.SemanticLanguages, caps.InventoryOnlyLanguages)
		}
	}
	for _, want := range []string{"Zig", "Bicep", "Solidity", "Nix", "Blade"} {
		if !inventorySeen[want] {
			t.Fatalf("capabilities should classify %q as inventory-only, got semantic=%#v inventory=%#v", want, caps.SemanticLanguages, caps.InventoryOnlyLanguages)
		}
	}
	for _, want := range []string{".go", ".py", ".ts", ".rs", ".swift", ".proto", ".dart", ".zig", ".bicep", ".graphql"} {
		if !contains(caps.SupportedFileExtensions, want) {
			t.Fatalf("capabilities missing extension %q in %#v", want, caps.SupportedFileExtensions)
		}
	}
	for _, want := range relationTypes {
		if !contains(caps.SupportedRelationTypes, want) {
			t.Fatalf("capabilities missing relation type %q in %#v", want, caps.SupportedRelationTypes)
		}
	}
	if caps.ParserVersions["go-tree-sitter"] == "" {
		t.Fatalf("capabilities missing parser metadata: %#v", caps.ParserVersions)
	}
	for feature, requiresNetwork := range caps.FeaturesRequiringNetworkAccess {
		if requiresNetwork {
			t.Fatalf("feature %s should not require network access", feature)
		}
	}
	for _, feature := range []string{"stable_symbol_ids", "semantic_diff", "ndjson_snapshot"} {
		if !caps.OptionalLocalOnlyFeatures[feature] {
			t.Fatalf("optional feature %s not advertised: %#v", feature, caps.OptionalLocalOnlyFeatures)
		}
	}
}

func TestInventoryOnlyLanguagesEmitDocumentSymbols(t *testing.T) {
	repo := t.TempDir()
	// Dart was promoted to the semantic tier; it now emits function/class symbols
	// rather than an inventory document symbol, so it is covered by the semantic
	// tests instead of this inventory-only one.
	writeFile(t, repo, "infra/main.bicep", "resource storage 'Microsoft.Storage/storageAccounts@2023-01-01' = {\n  name: 'stapp'\n}\n")
	writeFile(t, repo, "schema/user.graphql", "type User {\n  id: ID!\n}\n")
	writeFile(t, repo, "views/home.blade.php", "<h1>{{ $title }}</h1>\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		path     string
		language string
		name     string
	}{
		{"infra/main.bicep", "Bicep", "main"},
		{"schema/user.graphql", "GraphQL", "user"},
		{"views/home.blade.php", "Blade", "blade"},
	} {
		var found bool
		for _, symbol := range snapshot.Symbols {
			if symbol.FilePath == want.path && symbol.Language == want.language && symbol.Kind == "document" && symbol.Name == want.name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing inventory symbol for %#v in %#v", want, snapshot.Symbols)
		}
	}
}

func TestKotlinPrimaryConstructorFieldsEmitSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/User.kt", `package com.acme

data class User(
  val id: String,
  var displayName: String = "anonymous",
)
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name      string
		signature string
	}{
		{name: "User.id", signature: "id String"},
		{name: "User.displayName", signature: "displayName String"},
	} {
		var found SymbolRecord
		for _, symbol := range snapshot.Symbols {
			if symbol.FilePath == "src/User.kt" && symbol.Kind == "field" && symbol.QualifiedName == want.name {
				found = symbol
				break
			}
		}
		if found.ID == "" {
			t.Fatalf("missing Kotlin primary constructor field %s in %#v", want.name, snapshot.Symbols)
		}
		if found.Language != "Kotlin" || found.Signature != want.signature {
			t.Fatalf("unexpected Kotlin field symbol %s: %#v", want.name, found)
		}
	}
}

func TestKotlinTypeAndFieldRelations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/User.kt", `package com.acme

data class User(
  val id: String,
  var displayName: String,
)

class Greeter {
  fun rename(user: User): User {
    user.displayName = user.id
    val current = user.displayName
    return user
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		relationType string
		from         string
		to           string
	}{
		{relationType: "PARAM_TYPE", from: "Greeter.rename", to: "User"},
		{relationType: "RETURNS_TYPE", from: "Greeter.rename", to: "User"},
		{relationType: "READS_FIELD", from: "Greeter.rename", to: "User.id"},
		{relationType: "READS_FIELD", from: "Greeter.rename", to: "User.displayName"},
		{relationType: "WRITES_FIELD", from: "Greeter.rename", to: "User.displayName"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, want.relationType, want.from, want.to) {
			t.Fatalf("missing Kotlin %s %s -> %s in %#v", want.relationType, want.from, want.to, snapshot.Relations)
		}
	}
}

func TestCapabilitiesReportRelationSupportPerLanguage(t *testing.T) {
	caps := Capabilities()

	// Every supported language reports the structural relations and nothing
	// outside the documented vocabulary.
	if len(caps.RelationSupportByLanguage) != len(caps.SupportedLanguages) {
		t.Fatalf("relation matrix covers %d languages, want %d", len(caps.RelationSupportByLanguage), len(caps.SupportedLanguages))
	}
	for language, types := range caps.RelationSupportByLanguage {
		for _, base := range []string{"DEFINES", "CONTAINS"} {
			if !contains(types, base) {
				t.Fatalf("language %q missing structural relation %q: %#v", language, base, types)
			}
		}
		for _, relation := range types {
			if !contains(relationTypes, relation) {
				t.Fatalf("language %q reports unknown relation %q", language, relation)
			}
		}
	}
	for _, language := range []string{"Go", "Python", "TypeScript", "Java", "Rust", "C#", "PHP"} {
		if !contains(caps.RelationSupportByLanguage[language], "CALLS") {
			t.Fatalf("language %q should support CALLS: %#v", language, caps.RelationSupportByLanguage[language])
		}
	}
	for _, language := range []string{"Zig", "Bicep", "Dockerfile", "YAML", "Kustomize"} {
		if contains(caps.RelationSupportByLanguage[language], "CALLS") {
			t.Fatalf("inventory/config language %q should not advertise CALLS: %#v", language, caps.RelationSupportByLanguage[language])
		}
	}

	// IMPORTS is reported exactly where importsFor has a scanner.
	importsFound := func(language string) bool {
		return contains(caps.RelationSupportByLanguage[language], "IMPORTS")
	}
	for _, language := range []string{"Go", "Python", "TypeScript", "Java", "Rust", "C#", "PHP"} {
		if !importsFound(language) {
			t.Fatalf("language %q should support IMPORTS: %#v", language, caps.RelationSupportByLanguage[language])
		}
	}
	for _, language := range []string{"HCL", "SQL", "YAML"} {
		if importsFound(language) {
			t.Fatalf("language %q should not support IMPORTS: %#v", language, caps.RelationSupportByLanguage[language])
		}
	}

	// Heuristic, path/pattern-driven relations are reported separately and not
	// attributed to individual languages.
	for _, relation := range []string{"HANDLES_ROUTE", "HANDLES_TOOL"} {
		if !contains(caps.HeuristicRelationTypes, relation) {
			t.Fatalf("heuristic relation %q not reported: %#v", relation, caps.HeuristicRelationTypes)
		}
		for language, types := range caps.RelationSupportByLanguage {
			if contains(types, relation) {
				t.Fatalf("heuristic relation %q should not be attributed to %q", relation, language)
			}
		}
	}
}

func TestWriteSnapshotNDJSON(t *testing.T) {
	snapshot := ProviderSnapshot{
		Header: SnapshotHeader{
			SchemaVersion:   SchemaVersion,
			Provider:        ProviderName,
			ProviderVersion: "test",
		},
		Files: []FileRecord{{RecordType: "file", ID: "repo:file:main.go", Path: "main.go", Blob: "abc"}},
		Symbols: []SymbolRecord{{
			RecordType:      "symbol",
			ID:              "id",
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "function",
			Name:            "main",
			QualifiedName:   "main",
			FilePath:        "main.go",
			Language:        "Go",
		}},
		Relations: []RelationRecord{{RecordType: "relation", FromID: "file", ToID: "id", Type: "DEFINES", WarningCodes: []string{}}},
	}

	var out bytes.Buffer
	if err := WriteSnapshotNDJSON(&out, snapshot); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d:\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("invalid json line %q: %v", line, err)
		}
	}
}

func TestBuildProviderSnapshotReportsParseErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "good.py", "def valid():\n    return True\n")
	writeFile(t, repo, "bad.py", "def broken(:\n    return False\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	seenSymbol := false
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == "valid" {
			seenSymbol = true
		}
	}
	if !seenSymbol {
		t.Fatalf("valid file symbols were not emitted: %#v", snapshot.Symbols)
	}
	var found bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.Code == "E_PARSE_ERROR" && failure.FilePath == "bad.py" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing parse error partial failure: %#v", snapshot.Header.PartialFailures)
	}
	if snapshot.Header.Stats.CompletenessLevel == "ok" {
		t.Fatalf("parse failures should affect completeness: %#v", snapshot.Header.Stats)
	}
}

func TestBuildRelationsUsesSymbolBlockIdentifierLookup(t *testing.T) {
	const symbolCount = 5000

	files := make([]FileRecord, 0, symbolCount+1)
	recordsByFile := make(map[string][]SymbolRecord, symbolCount+1)
	contentByFile := make(map[string]string, symbolCount+1)

	for i := 0; i < symbolCount; i++ {
		path := "pkg/symbol_" + strconv.Itoa(i) + ".go"
		name := "UnrelatedSymbol" + strconv.Itoa(i)
		if i == symbolCount-1 {
			name = "TargetSymbol"
		}
		symbol := SymbolRecord{
			RecordType:    "symbol",
			ID:            "sym-" + strconv.Itoa(i),
			Kind:          "function",
			Name:          name,
			QualifiedName: name,
			FilePath:      path,
			StartLine:     1,
			EndLine:       3,
			Language:      "Go",
		}
		files = append(files, FileRecord{RecordType: "file", ID: fileID("repo", path), Path: path, Language: "Go"})
		recordsByFile[path] = []SymbolRecord{symbol}
		contentByFile[path] = "package pkg\nfunc " + name + "() {}\n"
	}

	caller := SymbolRecord{
		RecordType:    "symbol",
		ID:            "caller",
		Kind:          "function",
		Name:          "Caller",
		QualifiedName: "Caller",
		FilePath:      "pkg/caller.go",
		StartLine:     2,
		EndLine:       4,
		Language:      "Go",
	}
	files = append(files, FileRecord{RecordType: "file", ID: fileID("repo", caller.FilePath), Path: caller.FilePath, Language: "Go"})
	recordsByFile[caller.FilePath] = []SymbolRecord{caller}
	contentByFile[caller.FilePath] = "package pkg\nfunc Caller() {\n\tTargetSymbol()\n}\n"

	relations := buildRelations("repo", files, recordsByFile, mapReader(contentByFile))

	var sawTargetCall bool
	for _, relation := range relations {
		if relation.Type != "CALLS" {
			continue
		}
		if relation.FromID != caller.ID {
			t.Fatalf("unexpected CALLS relation from non-caller symbol: %#v", relation)
		}
		switch relation.ToID {
		case "sym-" + strconv.Itoa(symbolCount-1):
			sawTargetCall = true
		case "sym-0":
			t.Fatalf("unrelated symbol was emitted as a CALLS relation: %#v", relation)
		default:
			t.Fatalf("unexpected CALLS relation: %#v", relation)
		}
	}
	if !sawTargetCall {
		t.Fatalf("missing CALLS relation from caller to TargetSymbol in %#v", relations)
	}
}

func TestBuildRelationsDropsAmbiguousCrossFileCallNameCollisions(t *testing.T) {
	files := []FileRecord{
		{RecordType: "file", ID: fileID("repo", "caller.go"), Path: "caller.go", Language: "Go"},
		{RecordType: "file", ID: fileID("repo", "embeddings.ts"), Path: "embeddings.ts", Language: "TypeScript"},
		{RecordType: "file", ID: fileID("repo", "runtime.ts"), Path: "runtime.ts", Language: "TypeScript"},
	}
	recordsByFile := map[string][]SymbolRecord{
		"caller.go": {{
			RecordType:    "symbol",
			ID:            "caller",
			Kind:          "function",
			Name:          "Login",
			QualifiedName: "Login",
			FilePath:      "caller.go",
			StartLine:     1,
			EndLine:       4,
			Language:      "Go",
		}},
		"embeddings.ts": {{
			RecordType:    "symbol",
			ID:            "embeddings-sleep",
			Kind:          "function",
			Name:          "sleep",
			QualifiedName: "sleep",
			FilePath:      "embeddings.ts",
			StartLine:     1,
			EndLine:       3,
			Language:      "TypeScript",
		}},
		"runtime.ts": {{
			RecordType:    "symbol",
			ID:            "runtime-sleep",
			Kind:          "function",
			Name:          "sleep",
			QualifiedName: "sleep",
			FilePath:      "runtime.ts",
			StartLine:     1,
			EndLine:       3,
			Language:      "TypeScript",
		}},
	}
	contentByFile := map[string]string{
		"caller.go":     "func Login() {\n\tsleep := options.Sleep\n\tsleep(interval)\n}\n",
		"embeddings.ts": "function sleep(ms: number) {}\n",
		"runtime.ts":    "function sleep(ms: number) {}\n",
	}

	for _, relation := range buildRelations("repo", files, recordsByFile, mapReader(contentByFile)) {
		if relation.Type == "CALLS" && relation.FromID == "caller" {
			t.Fatalf("ambiguous sleep call should not resolve globally: %#v", relation)
		}
	}
}

func TestBuildRelationsResolvesCPlusPlusSameFileOverloadSet(t *testing.T) {
	files := []FileRecord{
		{RecordType: "file", ID: fileID("repo", "src/format-inl.h"), Path: "src/format-inl.h", Language: "C++"},
		{RecordType: "file", ID: fileID("repo", "include/format.h"), Path: "include/format.h", Language: "C++"},
	}
	recordsByFile := map[string][]SymbolRecord{
		"src/format-inl.h": {{
			RecordType:    "symbol",
			ID:            "caller",
			Kind:          "function",
			Name:          "vformat",
			QualifiedName: "vformat",
			FilePath:      "src/format-inl.h",
			StartLine:     1,
			EndLine:       4,
			Language:      "C++",
		}},
		"include/format.h": {{
			RecordType:    "symbol",
			ID:            "to-string-buffer",
			Kind:          "function",
			Name:          "to_string",
			QualifiedName: "to_string",
			FilePath:      "include/format.h",
			StartLine:     1,
			EndLine:       3,
			Language:      "C++",
		}, {
			RecordType:    "symbol",
			ID:            "to-string-value",
			Kind:          "function",
			Name:          "to_string",
			QualifiedName: "to_string",
			FilePath:      "include/format.h",
			StartLine:     5,
			EndLine:       7,
			Language:      "C++",
		}},
	}
	contentByFile := map[string]string{
		"src/format-inl.h": "auto vformat() -> std::string {\n  return to_string(buffer);\n}\n",
		"include/format.h": "auto to_string(Buffer) -> std::string {}\nauto to_string(int) -> std::string {}\n",
	}

	var targets []string
	for _, relation := range buildRelations("repo", files, recordsByFile, mapReader(contentByFile)) {
		if relation.Type == "CALLS" && relation.FromID == "caller" {
			targets = append(targets, relation.ToID)
			if relation.Resolution != "name_only" || relation.RelationScope != "workspace" {
				t.Fatalf("unexpected overload relation metadata: %#v", relation)
			}
		}
	}
	sort.Strings(targets)
	if !reflect.DeepEqual(targets, []string{"to-string-buffer", "to-string-value"}) {
		t.Fatalf("expected C++ overload set call targets, got %#v", targets)
	}
}

func TestRecordsByRelationSupportFiltersUnsupportedLanguages(t *testing.T) {
	recordsByFile := map[string][]SymbolRecord{
		"kernel/sched/core.c": {{
			ID:       "symbol:core.c:schedule",
			Language: "C",
			Kind:     "function",
			Name:     "schedule",
		}},
		"deploy/app.yaml": {{
			ID:       "symbol:app.yaml:Deployment.app",
			Language: "YAML",
			Kind:     "resource",
			Name:     "app",
		}},
		"infra/main.tf": {{
			ID:       "symbol:main.tf:aws_vpc.main",
			Language: "HCL",
			Kind:     "block",
			Name:     "main",
		}},
	}

	resourceRecords := recordsByRelationSupport(recordsByFile, "RESOURCE_DEPENDS_ON")
	if _, ok := resourceRecords["kernel/sched/core.c"]; ok {
		t.Fatalf("C records should not be included in resource dependency scans")
	}
	if _, ok := resourceRecords["deploy/app.yaml"]; !ok {
		t.Fatalf("YAML records should be included in resource dependency scans")
	}
	if _, ok := resourceRecords["infra/main.tf"]; !ok {
		t.Fatalf("HCL records should be included in resource dependency scans")
	}

	configRecords := recordsByRelationSupport(recordsByFile, "CONFIGURES")
	if _, ok := configRecords["kernel/sched/core.c"]; ok {
		t.Fatalf("C records should not be included in config scans")
	}
	if _, ok := configRecords["deploy/app.yaml"]; !ok {
		t.Fatalf("YAML records should be included in config scans")
	}
}

func TestEntitySymbolsDisambiguatesDuplicateNames(t *testing.T) {
	symbols := entitySymbols("gh/example/repo", "src/session.ts", "TypeScript", []Entity{
		{Kind: "method", Name: "Session.toTime", StartLine: 10, EndLine: 12},
		{Kind: "method", Name: "Session.toTime", StartLine: 20, EndLine: 22},
		{Kind: "method", Name: "Session.toPosition", StartLine: 30, EndLine: 32},
	})

	ids := map[string]bool{}
	for _, symbol := range symbols {
		if ids[symbol.ID] {
			t.Fatalf("duplicate symbol id %q in %#v", symbol.ID, symbols)
		}
		ids[symbol.ID] = true
	}
	if symbols[0].ID == "gh/example/repo:TypeScript:src/session.ts:method:Session.toTime" {
		t.Fatalf("first duplicate was not disambiguated: %#v", symbols)
	}
	if symbols[1].ID == "gh/example/repo:TypeScript:src/session.ts:method:Session.toTime" {
		t.Fatalf("second duplicate was not disambiguated: %#v", symbols)
	}
	if symbols[2].ID != "gh/example/repo:TypeScript:src/session.ts:method:Session.toPosition" {
		t.Fatalf("unique symbol id changed: %q", symbols[2].ID)
	}
}

func TestEntitySymbolsDisambiguatesOverloadsStablyAcrossLineShifts(t *testing.T) {
	// Two same-name overloads with distinct signatures must get distinct,
	// signature-derived IDs that do not depend on line numbers.
	build := func(firstStart, secondStart int) []SymbolRecord {
		return entitySymbols("gh/example/repo", "src/calc.cs", "C#", []Entity{
			{Kind: "method", Name: "Calc.Add", StartLine: firstStart, EndLine: firstStart + 2, Signature: "int Add(int a, int b)"},
			{Kind: "method", Name: "Calc.Add", StartLine: secondStart, EndLine: secondStart + 2, Signature: "double Add(double a, double b)"},
		})
	}
	before := build(10, 20)
	after := build(40, 55)

	if before[0].ID == before[1].ID {
		t.Fatalf("overloads share id: %#v", before)
	}
	if before[0].ID != after[0].ID || before[1].ID != after[1].ID {
		t.Fatalf("overload ids shifted with line numbers: before=%v after=%v",
			[]string{before[0].ID, before[1].ID}, []string{after[0].ID, after[1].ID})
	}
	for _, symbol := range before {
		if strings.Contains(symbol.ID, "#L") {
			t.Fatalf("id still uses line-range disambiguation: %q", symbol.ID)
		}
		if !strings.Contains(symbol.ID, "#sig:") {
			t.Fatalf("id missing signature disambiguation: %q", symbol.ID)
		}
	}
}

func TestEntitySymbolsKeepCompoundIDStableAcrossBodyEdits(t *testing.T) {
	before := entitySymbols("gh/example/repo", "src/auth.py", "Python", []Entity{
		{Kind: "function", Name: "validate_token", StartLine: 1, EndLine: 2, Signature: "def validate_token(token):", BodyHash: "old"},
	})
	after := entitySymbols("gh/example/repo", "src/auth.py", "Python", []Entity{
		{Kind: "function", Name: "validate_token", StartLine: 1, EndLine: 4, Signature: "def validate_token(token):", BodyHash: "new"},
	})
	if before[0].ID != after[0].ID {
		t.Fatalf("compound id changed across body edit: before=%q after=%q", before[0].ID, after[0].ID)
	}
}

func TestBuildProviderSnapshotReadsAdvertisedHeadTree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "tracked.py", "def dirty():\n    return False\n")
	writeFile(t, repo, "untracked.py", "def should_not_emit():\n    return True\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.Commit == "" || snapshot.Header.Tree == "" {
		t.Fatalf("missing git metadata: %#v", snapshot.Header)
	}

	seenSymbols := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		seenSymbols[symbol.QualifiedName] = true
		if symbol.FilePath == "untracked.py" {
			t.Fatalf("snapshot included untracked file symbol: %#v", symbol)
		}
	}
	if !seenSymbols["committed"] {
		t.Fatalf("snapshot did not include committed symbol: %#v", snapshot.Symbols)
	}
	if seenSymbols["dirty"] || seenSymbols["should_not_emit"] {
		t.Fatalf("snapshot included working-tree-only symbols: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotEmitsFileChangesWithFromGitHistory(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	writeFile(t, repo, "a.go", "package p\n\nfunc A() {}\n")
	writeFile(t, repo, "b.go", "package p\n\nfunc B() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "a.go", "package p\n\nfunc A() string { return \"one\" }\n")
	writeFile(t, repo, "b.go", "package p\n\nfunc B() string { return \"one\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "cochange one")

	writeFile(t, repo, "a.go", "package p\n\nfunc A() string { return \"two\" }\n")
	writeFile(t, repo, "b.go", "package p\n\nfunc B() string { return \"two\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "cochange two")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "FILE_CHANGES_WITH" &&
			relation.FromID == fileID(snapshot.Header.RepoKey, "a.go") &&
			relation.ToID == fileID(snapshot.Header.RepoKey, "b.go") {
			return
		}
	}
	t.Fatalf("FILE_CHANGES_WITH relation missing: %#v", snapshot.Relations)
}

func TestBuildProviderSnapshotWorktreeIncludesDirtyFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "tracked.py", "def dirty():\n    return False\n")
	writeFile(t, repo, "untracked.py", "def worktree_only():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		NoNetwork: true,
		Worktree:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	seenSymbols := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		seenSymbols[symbol.QualifiedName] = true
	}
	if !seenSymbols["dirty"] || !seenSymbols["worktree_only"] {
		t.Fatalf("snapshot did not include worktree symbols: %#v", snapshot.Symbols)
	}
	if seenSymbols["committed"] {
		t.Fatalf("snapshot included HEAD-only symbol: %#v", snapshot.Symbols)
	}
	if len(snapshot.Header.Warnings) != 1 || snapshot.Header.Warnings[0].Code != "W_WORKTREE_SNAPSHOT" {
		t.Fatalf("warnings = %#v", snapshot.Header.Warnings)
	}
	if snapshot.Header.Commit == "" || snapshot.Header.Tree == "" {
		t.Fatalf("worktree snapshot should include HEAD commit/tree metadata: %#v", snapshot.Header)
	}
}

func TestBuildProviderSnapshotWorktreeHonorsRootGitignore(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\n")
	writeFile(t, repo, "cache/ignored.py", "def ignored():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "cache/")
	if snapshotHasSymbol(snapshot, "ignored") {
		t.Fatalf("snapshot included ignored symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeHonorsAdditionalIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "generated/\n")
	writeFile(t, repo, "generated/ignored.py", "def ignored():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{".brainignore"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "generated/")
}

func TestBuildProviderSnapshotWorktreeCombinesMultipleIgnoreFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "cache/\n")
	writeFile(t, repo, ".semignore", "# comments and blanks are ignored\n\n**/generated.py\nbenchmarks/agent-brain/results/\n")
	writeFile(t, repo, "cache/cache.py", "def cache():\n    return True\n")
	writeFile(t, repo, "src/generated.py", "def generated():\n    return True\n")
	writeFile(t, repo, "benchmarks/agent-brain/results/result.py", "def result():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{".brainignore", ".semignore"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	for _, prefix := range []string{"cache/", "benchmarks/agent-brain/results/"} {
		assertSnapshotOmitsPathPrefix(t, snapshot, prefix)
	}
	if snapshotHasPath(snapshot, "src/generated.py") {
		t.Fatalf("snapshot included ignored recursive glob path: %#v", snapshot.Files)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeFileReopensIgnoredDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\n")
	writeFile(t, repo, ".seminclude", "cache/\n")
	writeFile(t, repo, "cache/included.py", "def included():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "included") {
		t.Fatalf("snapshot did not include file from reopened directory: %#v", snapshot.Symbols)
	}
	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeFileWinsAfterIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".brainignore", "generated/\n")
	writeFile(t, repo, ".seminclude", "generated/\n")
	writeFile(t, repo, "generated/included.py", "def included():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IgnoreFiles:  []string{".brainignore"},
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "included") {
		t.Fatalf("snapshot did not include file from include-file override: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWorktreeIncludeDirectoryKeepsSpecificFileIgnore(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "cache/\ncache/skip.py\n")
	writeFile(t, repo, ".seminclude", "cache/\n")
	writeFile(t, repo, "cache/include.py", "def include_me():\n    return True\n")
	writeFile(t, repo, "cache/skip.py", "def skip_me():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "include_me") {
		t.Fatalf("snapshot did not include file from reopened directory: %#v", snapshot.Symbols)
	}
	if snapshotHasPath(snapshot, "cache/skip.py") || snapshotHasSymbol(snapshot, "skip_me") {
		t.Fatalf("snapshot included specifically ignored file: files=%#v symbols=%#v", snapshot.Files, snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotMissingIncludeFileFailsClosed(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{"does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected missing include file error")
	}
	if !strings.Contains(err.Error(), "include file") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("missing include file error was not clear: %v", err)
	}
}

func TestBuildProviderSnapshotIgnoredUnsupportedFilesDoNotProduceFailures(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "ignored/\n")
	writeFile(t, repo, "ignored/Unsupported.f90", "subroutine ignored\nend subroutine ignored\n")
	writeFile(t, repo, "Visible.f90", "subroutine visible\nend subroutine visible\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawVisibleFailure bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == "ignored/Unsupported.f90" {
			t.Fatalf("ignored unsupported file produced a partial failure: %#v", snapshot.Header.PartialFailures)
		}
		if failure.FilePath == "Visible.f90" && failure.Code == "E_UNSUPPORTED_LANGUAGE" {
			sawVisibleFailure = true
		}
	}
	if !sawVisibleFailure {
		t.Fatalf("visible unsupported file did not produce a partial failure: %#v", snapshot.Header.PartialFailures)
	}
}

func TestBuildProviderSnapshotMissingIgnoreFileFailsClosed(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:    true,
		IgnoreFiles: []string{"does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected missing ignore file error")
	}
	if !strings.Contains(err.Error(), "ignore file") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("missing ignore file error was not clear: %v", err)
	}
}

func TestBuildProviderSnapshotHeadDoesNotReadLiveIgnoreFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "tracked.py", "def committed():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, ".gitignore", "tracked.py\nignored/\n")
	writeFile(t, repo, "ignored/worktree.py", "def worktree_only():\n    return True\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "committed") {
		t.Fatalf("HEAD snapshot did not include committed tracked symbol: %#v", snapshot.Symbols)
	}
	if snapshotHasSymbol(snapshot, "worktree_only") {
		t.Fatalf("HEAD snapshot included ignored untracked symbol: %#v", snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotWarnsWithoutGitHead(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Header.Warnings) != 1 {
		t.Fatalf("warnings = %#v", snapshot.Header.Warnings)
	}
	if snapshot.Header.Warnings[0].Code != "E_NO_GIT_HEAD" {
		t.Fatalf("warning code = %q", snapshot.Header.Warnings[0].Code)
	}
	if snapshot.Header.Commit != "" || snapshot.Header.Tree != "" {
		t.Fatalf("unexpected git metadata: %#v", snapshot.Header)
	}
}

func TestBuildProviderSnapshotUsesGitHubSSHRepoKey(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "remote", "add", "origin", "git@github.com:jayparikh/agentviz.git")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "gh/jayparikh/agentviz" {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotUsesGitHubHTTPSRepoKey(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "remote", "add", "origin", "https://github.com/jayparikh/agentviz.git/")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "gh/jayparikh/agentviz" {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotFallsBackWithoutSupportedRemote(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	writeFile(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.RepoKey != "local/"+filepath.Base(repo) {
		t.Fatalf("repo_key = %q", snapshot.Header.RepoKey)
	}
}

func TestBuildProviderSnapshotReportsUnsupportedSourceFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Supported.py", "def validate_token(token):\n    return bool(token)\n")
	writeFile(t, repo, "Unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.Code == "E_UNSUPPORTED_LANGUAGE" && failure.FilePath == "Unsupported.f90" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing unsupported language partial failure: %#v", snapshot.Header.PartialFailures)
	}
}

func TestGoImportScannerOnlyReadsImportDeclarations(t *testing.T) {
	imports := importsFor("main.go", `package main

import (
	"fmt"
	alias "net/http"
)

var notImport = "not/a/package"

func main() {
	_ = "also/not/imported"
	fmt.Println(http.MethodGet)
}
`)
	got := strings.Join(imports, ",")
	if got != "fmt,net/http" {
		t.Fatalf("imports = %q", got)
	}
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotHasSymbol(snapshot ProviderSnapshot, qualifiedName string) bool {
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == qualifiedName {
			return true
		}
	}
	return false
}

func snapshotHasPath(snapshot ProviderSnapshot, path string) bool {
	for _, file := range snapshot.Files {
		if file.Path == path {
			return true
		}
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == path {
			return true
		}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == path {
			return true
		}
	}
	for _, warning := range snapshot.Header.Warnings {
		if warning.FilePath == path {
			return true
		}
	}
	return false
}

func assertSnapshotOmitsPathPrefix(t *testing.T, snapshot ProviderSnapshot, prefix string) {
	t.Helper()
	for _, file := range snapshot.Files {
		if strings.HasPrefix(file.Path, prefix) {
			t.Fatalf("snapshot included ignored file record: %#v", file)
		}
	}
	for _, symbol := range snapshot.Symbols {
		if strings.HasPrefix(symbol.FilePath, prefix) {
			t.Fatalf("snapshot included ignored symbol record: %#v", symbol)
		}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if strings.HasPrefix(failure.FilePath, prefix) {
			t.Fatalf("snapshot included ignored partial failure: %#v", failure)
		}
	}
	for _, warning := range snapshot.Header.Warnings {
		if strings.HasPrefix(warning.FilePath, prefix) {
			t.Fatalf("snapshot included ignored warning: %#v", warning)
		}
	}
	for _, relation := range snapshot.Relations {
		if strings.Contains(relation.FromID, prefix) || strings.Contains(relation.ToID, prefix) {
			t.Fatalf("snapshot included ignored relation: %#v", relation)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func externalByID(records []ExternalRecord, id string) ExternalRecord {
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	return ExternalRecord{}
}

func symbolByKindAndName(records []SymbolRecord, kind, qualifiedName string) SymbolRecord {
	for _, record := range records {
		if record.Kind == kind && record.QualifiedName == qualifiedName {
			return record
		}
	}
	return SymbolRecord{}
}

func TestRustCodegenMacroBodiesDoNotProduceCallEdges(t *testing.T) {
	// Identifiers inside Rust code-generation macros (quote!/quote_block!) are
	// token templates for generated code, not calls executed by the function.
	// serde_derive's `_serde::Serializer::serialize_struct(...)` inside quote!
	// must not resolve to a local `fn serialize_struct`.
	repo := t.TempDir()
	writeFile(t, repo, "src/ser.rs", `fn serialize_struct() -> i32 { 1 }

fn real_helper() -> i32 { 2 }

fn caller() -> TokenStream {
    let value = real_helper();
    quote_block! {
        let mut __serde_state = _serde::Serializer::serialize_struct(
            __serializer, name, len,
        )?;
        _serde::ser::SerializeStruct::serialize_field(&mut state, key, value);
    }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "caller", "real_helper") {
		t.Fatalf("missing real call outside the macro: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "caller", "serialize_struct") {
		t.Fatalf("macro-template token treated as a call to local serialize_struct: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "caller", "serialize_field") {
		t.Fatalf("macro-template token treated as a call to local serialize_field: %#v", snapshot.Relations)
	}
}

func TestIsVendoredScanDir(t *testing.T) {
	cases := []struct {
		rel, name string
		want      bool
	}{
		// Nested bundled C/C++ runtime trees (Zig) are excluded.
		{"lib/libc", "libc", true},
		{"lib/libcxx", "libcxx", true},
		{"lib/libcxx/src", "src", false},
		{"lib/libcxxabi", "libcxxabi", true},
		{"lib/libunwind", "libunwind", true},
		// A project's own top-level dir of the same name is preserved.
		{"libcxx", "libcxx", false},
		{"libc", "libc", false},
		// Zig's own Zig-language compiler_rt (underscore) is kept; only the
		// hyphenated LLVM compiler-rt is treated as vendored.
		{"lib/compiler_rt", "compiler_rt", false},
		// Universally vendored/generated directories at any depth.
		{"test/thirdparty", "thirdparty", true},
		{"a/b/third_party", "third_party", true},
		{"node_modules", "node_modules", true},
		{"vendor", "vendor", true},
		// Normal source directories are kept.
		{"src", "src", false},
		{"lib/std", "std", false},
		{"include", "include", false},
	}
	for _, c := range cases {
		if got := isVendoredScanDir(c.rel, c.name); got != c.want {
			t.Errorf("isVendoredScanDir(%q, %q) = %v, want %v", c.rel, c.name, got, c.want)
		}
	}
}

func TestDartSemanticExtraction(t *testing.T) {
	// Dart was promoted from inventory to the semantic tier (vendored grammar);
	// it must now extract classes, methods, and top-level functions.
	repo := t.TempDir()
	writeFile(t, repo, "lib/app.dart", `int helper(int n) {
  return n + 1;
}

class Widget {
  int build(int x) {
    return helper(x);
  }
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Dart" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["helper"] != "function" {
		t.Fatalf("Dart top-level function not extracted: %#v", kinds)
	}
	if kinds["Widget"] != "class" {
		t.Fatalf("Dart class not extracted: %#v", kinds)
	}
	if kinds["Widget.build"] == "" && kinds["build"] == "" {
		t.Fatalf("Dart method not extracted: %#v", kinds)
	}
}
