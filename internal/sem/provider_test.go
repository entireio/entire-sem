package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestLanguageTiersClassifiesSemanticAndInventory(t *testing.T) {
	tiers := languageTiers(map[string]struct{}{"Go": {}, "Zig": {}, "Bicep": {}})
	if tiers["Go"] != "semantic" {
		t.Fatalf("Go tier = %q, want semantic", tiers["Go"])
	}
	if tiers["Zig"] != "semantic" {
		t.Fatalf("Zig tier = %q, want inventory-only", tiers["Zig"])
	}
	if tiers["Bicep"] != "inventory-only" {
		t.Fatalf("Bicep tier = %q, want inventory-only", tiers["Bicep"])
	}
	if languageTiers(nil) != nil {
		t.Fatalf("nil languageSet should yield nil tiers, got %#v", languageTiers(nil))
	}
}

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
	if snapshot.Header.LanguageTiers["Python"] != "semantic" || snapshot.Header.LanguageTiers["TypeScript"] != "semantic" {
		t.Fatalf("language_tiers = %#v", snapshot.Header.LanguageTiers)
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

func TestTypeScriptReceiverCallsWithTypeArgumentsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/injector.ts", `class Controller {}

class Injector {
  public async loadInstance<T>() {
    const callback = async () => {
      await this.resolveProperties<T>();
      await this.instantiateClass<T>();
    };
    await this.resolveConstructorParams<T>(callback);
  }

  public async loadController() {
    await this.loadInstance<Controller>();
  }

  public async loadProvider<T>() {
    await this.loadInstance<T>();
  }

  private async resolveConstructorParams<T>(callback: () => Promise<void>) {
    await callback();
  }

  private async resolveProperties<T>() {}

  private async instantiateClass<T>() {}
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"Injector.loadController", "Injector.loadInstance"},
		{"Injector.loadProvider", "Injector.loadInstance"},
		{"Injector.loadInstance", "Injector.resolveConstructorParams"},
		{"Injector.loadInstance", "Injector.resolveProperties"},
		{"Injector.loadInstance", "Injector.instantiateClass"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing TypeScript generic receiver call %s -> %s in %#v", want[0], want[1], snapshot.Relations)
		}
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

func TestPythonDottedCallModulesDoesNotDuplicateResolvedTail(t *testing.T) {
	got, _ := pythonDottedCallModules("acme_pkg", []string{"service"}, []string{"acme_pkg.service"}, nil, nil)
	if want := []string{"acme_pkg.service"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pythonDottedCallModules resolved tail = %#v, want %#v", got, want)
	}
}

// When the imported module is a proper prefix shorter than the call's dotted
// tail (`import pkg.a` binds pkg -> pkg.a, then `pkg.a.b.fn()`), the composed
// candidate must be the full literal call path (pkg.a.b) with the shared `a`
// segment stripped — never the doubled pkg.a.a.b, and never the too-short pkg.a.
func TestPythonDottedCallModulesStripsSharedPrefixOverlap(t *testing.T) {
	cases := []struct {
		name  string
		alias string
		tail  []string
		imp   []string
		want  []string
	}{
		// The finding's repro: import pkg.a + pkg.a.b.fn().
		{"prefix-overlap", "pkg", []string{"a", "b"}, []string{"pkg.a"}, []string{"pkg.a.b"}},
		// module == alias (plain `import a`): full literal path.
		{"module-eq-alias", "a", []string{"b", "c"}, []string{"a"}, []string{"a.b.c"}},
		// module == "a.b", tail = [b, c]: strip the shared `b`.
		{"one-segment-overlap", "a", []string{"b", "c"}, []string{"a.b"}, []string{"a.b.c"}},
		// module == "a.b.c" but the call only reaches a.b: the literal call path
		// (a.b) is authoritative, no `a.b.c.b` doubling.
		{"module-deeper-than-call", "a", []string{"b"}, []string{"a.b.c"}, []string{"a.b"}},
	}
	for _, c := range cases {
		all, external := pythonDottedCallModules(c.alias, c.tail, c.imp, nil, nil)
		if !reflect.DeepEqual(all, c.want) {
			t.Errorf("%s: pythonDottedCallModules(%q, %v, %v) all = %#v, want %#v", c.name, c.alias, c.tail, c.imp, all, c.want)
		}
		if !reflect.DeepEqual(external, c.want) {
			t.Errorf("%s: pythonDottedCallModules(%q, %v, %v) external = %#v, want %#v", c.name, c.alias, c.tail, c.imp, external, c.want)
		}
	}
}

func TestPythonDottedCallModulesUseAddressedParentForDeepImports(t *testing.T) {
	got, _ := pythonDottedCallImportedNames(
		"pkg.sub.fn()\npkg.sub.mod.call()\n",
		map[string][]string{"pkg": {"pkg.sub.mod"}},
		nil, nil,
	)
	if want := []string{"pkg.sub"}; !reflect.DeepEqual(got["fn"], want) {
		t.Fatalf("pkg.sub.fn() modules = %#v, want %#v", got["fn"], want)
	}
	if want := []string{"pkg.sub.mod"}; !reflect.DeepEqual(got["call"], want) {
		t.Fatalf("pkg.sub.mod.call() modules = %#v, want %#v", got["call"], want)
	}
}

func TestPythonDottedImportedCallsAllowSingleSelectorAlias(t *testing.T) {
	got, _ := pythonDottedCallImportedNames(
		"json.dumps({})\nok = path.isdir(dst)\nsvc.run()\n# leak.call()\n\"\"\"leak.call()\"\"\"\n",
		map[string][]string{
			"json": {"json"},
			"leak": {"bad.module"},
			"path": {"os"},
			"svc":  {"acme_pkg.service"},
		},
		nil, nil,
	)
	if _, ok := got["dumps"]; ok {
		t.Fatalf("plain import json; json.dumps() should not synthesize a bare dumps hint: %#v", got)
	}
	if !slices.Contains(got["isdir"], "genericpath") {
		t.Fatalf("path.isdir() did not include genericpath hint: %#v", got)
	}
	if slices.Contains(got["isdir"], "os") {
		t.Fatalf("path.isdir() emitted bare os module hint: %#v", got)
	}
	if !slices.Contains(got["run"], "acme_pkg.service") {
		t.Fatalf("svc.run() did not include imported module hint: %#v", got)
	}
	if _, ok := got["call"]; ok {
		t.Fatalf("comment/docstring dotted call synthesized import hints: %#v", got)
	}
}

func TestPythonDottedImportedModuleCallsResolveToLocalSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/acme_pkg/__init__.py", "")
	writeFile(t, repo, "src/acme_pkg/service.py", `def run():
    return "ok"
`)
	writeFile(t, repo, "src/acme_pkg/consumer.py", `import acme_pkg.service

def call_service():
    return acme_pkg.service.run()
`)
	writeFile(t, repo, "Lib/genericpath.py", `def isdir(path):
    return False
`)
	writeFile(t, repo, "Lib/shutil.py", `import os

def copy2(src, dst):
    if os.path.isdir(dst):
        return dst
    return src
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "call_service", "src/acme_pkg/consumer.py", "run", "src/acme_pkg/service.py") {
		t.Fatalf("missing package.module.function dotted Python call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "copy2", "Lib/shutil.py", "isdir", "Lib/genericpath.py") {
		t.Fatalf("missing os.path.isdir -> genericpath.isdir call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestPythonDottedImportedCallsDoNotResolveToSameFileBareName(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/pkg/__init__.py", "")
	writeFile(t, repo, "src/pkg/sub.py", `def fn():
    return "remote"
`)
	writeFile(t, repo, "src/consumer.py", `import pkg.sub

def fn():
    return "local"

def call_remote():
    return pkg.sub.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "call_remote", "src/consumer.py", "fn", "src/pkg/sub.py") {
		t.Fatalf("missing dotted imported call to remote fn: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "call_remote", "src/consumer.py", "fn", "src/consumer.py") {
		t.Fatalf("dotted imported call resolved to same-file bare fn: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestDartSetterAssignmentCallsAllowDollarSetterNames(t *testing.T) {
	got := dartSetterAssignmentCalls("obj._$field = value;\n")
	if len(got) != 1 || got[0].Receiver != "obj" || got[0].Method != "_$field" {
		t.Fatalf("dartSetterAssignmentCalls() = %#v, want obj._$field", got)
	}
}

func TestDartSetterAssignmentCallsIgnoreMultilineStrings(t *testing.T) {
	got := dartSetterAssignmentCalls(`final text = '''
obj.fake = 1;
''';
real.value = 2;
final raw = r"""
raw.fake = 3;
""";
`)
	if len(got) != 1 || got[0].Receiver != "real" || got[0].Method != "value" {
		t.Fatalf("dartSetterAssignmentCalls() = %#v, want real.value only", got)
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

// streamMinifiedProbe runs a snapshot over repo and returns the symbol names and
// the set of partial-failure codes, for the minified-detection tests below.
func streamMinifiedProbe(t *testing.T, repo string) (symbolNames []string, failureCodes []string) {
	t.Helper()
	err := StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{}, func(rec any) error {
		switch r := rec.(type) {
		case SymbolRecord:
			symbolNames = append(symbolNames, r.Name)
		case SnapshotSummary:
			for _, failure := range r.PartialFailures {
				failureCodes = append(failureCodes, failure.Code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return symbolNames, failureCodes
}

// Real source with a few enormous single-line data literals (the shape of
// microsoft/TypeScript's src/compiler/scanner.ts) must not be treated as
// minified: long lines exist but do not dominate the file's bytes.
func TestFewGiantDataLinesInRealSourceStillParsed(t *testing.T) {
	repo := t.TempDir()
	var sb strings.Builder
	sb.WriteString("export function createScanner(): number {\n\treturn unicodeTable0.length;\n}\n")
	// ~172KB of ordinary lines.
	sb.WriteString(strings.Repeat("// ordinary source line describing the scanner state machine\n", 2800))
	// Three ~50KB single-line array literals (~150KB, well under 70% of total).
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&sb, "const unicodeTable%d = [%s0];\n", i, strings.Repeat("170,171,", 6250))
	}
	writeFile(t, repo, "scanner.ts", sb.String())

	symbols, failures := streamMinifiedProbe(t, repo)
	for _, code := range failures {
		if code == "E_MINIFIED" {
			t.Fatalf("real source with a few giant data lines was flagged E_MINIFIED (failures = %v)", failures)
		}
	}
	if !slices.Contains(symbols, "createScanner") {
		t.Fatalf("symbols = %v, want createScanner extracted", symbols)
	}
}

// A genuine bundle packing the whole program onto one enormous line must still
// be flagged and skipped.
func TestSingleLineBundleFlaggedMinified(t *testing.T) {
	repo := t.TempDir()
	// ~500KB of minified-style JS on a single line.
	writeFile(t, repo, "main.min.js", "!function(){"+strings.Repeat("var a=fn(1);a&&a();", 26000)+"}();")

	symbols, failures := streamMinifiedProbe(t, repo)
	if !slices.Contains(failures, "E_MINIFIED") {
		t.Fatalf("single-line bundle not flagged; failures = %v", failures)
	}
	if len(symbols) != 0 {
		t.Fatalf("symbols = %v, want none for minified bundle", symbols)
	}
}

// A bundle split across a handful of lines that are all overlong (a common
// webpack/rollup output shape) must also be flagged.
func TestFewLinesAllLongBundleFlaggedMinified(t *testing.T) {
	repo := t.TempDir()
	line := "!function(){" + strings.Repeat("var b=go(2);b&&b();", 1200) + "}();\n"
	writeFile(t, repo, "bundle.js", strings.Repeat(line, 6))

	symbols, failures := streamMinifiedProbe(t, repo)
	if !slices.Contains(failures, "E_MINIFIED") {
		t.Fatalf("few-lines-all-long bundle not flagged; failures = %v", failures)
	}
	if len(symbols) != 0 {
		t.Fatalf("symbols = %v, want none for minified bundle", symbols)
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

// Shell functions call each other as bare commands (no parentheses), which the
// generic paren-based scanner cannot see; the shell command-position scanner
// must recover those CALLS while ignoring builtins like cd/return. Shaped like
// ohmyzsh's dirhistory.plugin.zsh.
func TestBuildProviderSnapshotResolvesZshBareCommandCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "plugins/dirhistory/dirhistory.plugin.zsh", `dirhistory_past=($PWD)
export DIRHISTORY_SIZE=30

alias cde='dirhistory_cd'

function pop_past() {
  setopt localoptions no_ksh_arrays
  if [[ $#dirhistory_past -gt 0 ]]; then
    typeset -g $1="${dirhistory_past[$#dirhistory_past]}"
  fi
}

function push_past() {
  if [[ $#dirhistory_past -ge $DIRHISTORY_SIZE ]]; then
    shift dirhistory_past
  fi
}

function push_future() {
  dirhistory_future+=($1)
}

function dirhistory_cd(){
  DIRHISTORY_CD="1"
  cd $1
  unset DIRHISTORY_CD
}

function dirhistory_back() {
  local cw=""
  local d=""

  pop_past cw
  if [[ "" == "$cw" ]]; then
    dirhistory_past=($PWD)
    return
  fi

  pop_past d
  if [[ "" != "$d" ]]; then
    dirhistory_cd $d
    push_future $cw
  else
    push_past $cw
  fi
}

function dirhistory_zle_dirhistory_back() {
  zle .kill-buffer
  dirhistory_back
  zle .accept-line
}

zle -N dirhistory_zle_dirhistory_back
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, edge := range [][2]string{
		{"dirhistory_zle_dirhistory_back", "dirhistory_back"},
		{"dirhistory_back", "pop_past"},
		{"dirhistory_back", "dirhistory_cd"},
		{"dirhistory_back", "push_future"},
		{"dirhistory_back", "push_past"},
	} {
		if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", edge[0], edge[1], "exact") {
			t.Fatalf("shell call %s -> %s not resolved: %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
	// Builtins and keywords must not fabricate edges.
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		if lastSegment(relation.FromID) == "dirhistory_back" {
			switch lastSegment(relation.ToID) {
			case "pop_past", "dirhistory_cd", "push_future", "push_past":
			default:
				t.Fatalf("unexpected outbound call from dirhistory_back: %#v", relation)
			}
		}
	}
}

// Swift methods call same-type siblings without a receiver (implicit self),
// including across `extension` blocks, and construct types whose extensions
// must not break the unique-name gate. Shaped like swift-argument-parser's
// CommandParser.swift / ArgumentSet.swift.
func TestBuildProviderSnapshotResolvesSwiftExtensionAndImplicitSelfCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Sources/Parsing/CommandParser.swift", `struct CommandParser {
  var commandTree: Tree
}

extension CommandParser {
  func checkForBuiltInFlags(_ split: SplitArguments) throws {
  }

  fileprivate mutating func parseCurrent(
    _ split: inout SplitArguments
  ) throws -> ParsableCommand {
    var parser = LenientParser(commandTree, split)
    let values = try parser.parse()
    try checkForBuiltInFlags(values)
    return values
  }

  internal mutating func descendingParse(_ split: inout SplitArguments) throws {
    var parsedCommand = try parseCurrent(&split)
  }
}

extension CommandParser {
  func checkForCompletionScriptRequest(_ split: inout SplitArguments) throws {
    var completionsParser = CommandParser(GenerateCompletions.self)
    if let result = try? completionsParser.parseCurrent(&split) {
      return
    }
  }
}
`)
	writeFile(t, repo, "Sources/Parsing/ArgumentSet.swift", `struct LenientParser {
  var content: Int

  mutating func parse() throws -> ParsedValues {
    return ParsedValues()
  }
}

extension LenientParser {
  var describing: String { "parser" }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	// Bare same-file sibling-method calls (implicit self).
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "CommandParser.descendingParse", "CommandParser.parseCurrent", "exact") {
		t.Fatalf("implicit-self call descendingParse -> parseCurrent not resolved: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "CommandParser.parseCurrent", "CommandParser.checkForBuiltInFlags", "exact") {
		t.Fatalf("implicit-self call parseCurrent -> checkForBuiltInFlags not resolved: %#v", snapshot.Relations)
	}
	// Receiver-typed call on a constructor-assigned local; only works when the
	// extension did not fork the CommandParser container.
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "CommandParser.checkForCompletionScriptRequest", "CommandParser.parseCurrent", "type_inferred") {
		t.Fatalf("receiver call checkForCompletionScriptRequest -> parseCurrent not resolved: %#v", snapshot.Relations)
	}
	// Cross-file receiver call through the constructor-assigned type.
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "CommandParser.parseCurrent", "LenientParser.parse", "type_inferred") {
		t.Fatalf("receiver call parseCurrent -> LenientParser.parse not resolved: %#v", snapshot.Relations)
	}
	// LenientParser has an extension in its defining file; construction must
	// still resolve as a globally unique name (extensions emit no duplicate).
	if !hasRelationByLastSegment(snapshot.Relations, "CONSTRUCTS", "CommandParser.parseCurrent", "LenientParser") {
		t.Fatalf("construction parseCurrent -> LenientParser not resolved: %#v", snapshot.Relations)
	}
}

func TestBuildProviderSnapshotResolvesSwiftExistentialDelegateAndLabelCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Source/Core/Request.swift", `class Request {
  public private(set) weak var delegate: (any RequestDelegate)?
  var isCancelled: Bool { false }

  func retryOrFinish(error: AFError?) {
    guard !isCancelled, let error, let delegate else { finish(); return }
    delegate.retryResult(for: self, dueTo: error) { retryResult in
      switch retryResult {
      case .doNotRetry:
        self.finish()
      case let .doNotRetryWithError(retryError):
        self.finish(error: retryError.asAFError(orFailWith: "Received retryError was not already AFError"))
      case .retry, .retryWithDelay:
        delegate.retryRequest(self, withDelay: retryResult.delay)
      }
    }
  }

  func finish(error: AFError? = nil) {}
}

protocol RequestDelegate: AnyObject {
  func retryResult(for request: Request, dueTo error: AFError, completion: @escaping (RetryResult) -> Void)
  func retryRequest(_ request: Request, withDelay timeDelay: Double?)
}
`)
	writeFile(t, repo, "Source/Core/Session.swift", `class Session: RequestDelegate {
  func retryResult(for request: Request, dueTo error: AFError, completion: @escaping (RetryResult) -> Void) {}
  func retryRequest(_ request: Request, withDelay timeDelay: Double?) {}
}
`)
	writeFile(t, repo, "Source/Core/AFError.swift", `struct AFError {}

extension Error {
  func asAFError(orFailWith message: @autoclosure () -> String) -> AFError {
    return AFError()
  }

  func asAFError(or defaultAFError: @autoclosure () -> AFError) -> AFError {
    return defaultAFError()
  }
}
`)
	writeFile(t, repo, "Source/Features/RequestInterceptor.swift", `enum RetryResult {
  case doNotRetry
  case doNotRetryWithError(any Error)
  case retry
  case retryWithDelay

  var delay: Double? { nil }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, edge := range [][2]string{
		{"Request.retryOrFinish", "Request.finish"},
		{"Request.retryOrFinish", "Session.retryResult"},
		{"Request.retryOrFinish", "Session.retryRequest"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", edge[0], edge[1]) {
			t.Fatalf("Swift call %s -> %s not resolved: %#v", edge[0], edge[1], snapshot.Relations)
		}
	}
	foundAsAFError := false
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" &&
			lastSegment(relation.FromID) == "Request.retryOrFinish" &&
			strings.Contains(relation.ToID, ":method:Error.asAFError") {
			foundAsAFError = true
			break
		}
	}
	if !foundAsAFError {
		t.Fatalf("Swift call Request.retryOrFinish -> Error.asAFError not resolved: %#v", snapshot.Relations)
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

func TestGoReceiverMethodResolvesAcrossPackageFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/etcdlike\n\ngo 1.21\n")
	// The receiver type and the caller live in server.go; the callee method
	// lives in a sibling file of the same package. A same-named method on
	// another package's type defeats the globally-unique-name fallback, so the
	// edge only resolves when the sibling file's method is linked to its
	// cross-file container type.
	writeFile(t, repo, "server/server.go", `package server

import "context"

type Server struct{}

func (s *Server) checkPermission(ctx context.Context) error {
	info, err := s.AuthInfoFromCtx(ctx)
	_ = info
	return err
}
`)
	writeFile(t, repo, "server/v3.go", `package server

import "context"

type Info struct{ User string }

func (s *Server) AuthInfoFromCtx(ctx context.Context) (*Info, error) {
	return &Info{}, nil
}
`)
	writeFile(t, repo, "auth/store.go", `package auth

import "context"

type Info struct{ User string }

type authStore struct{}

func (as *authStore) AuthInfoFromCtx(ctx context.Context) (*Info, error) {
	return &Info{}, nil
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "Server.checkPermission", "Server.AuthInfoFromCtx", "type_inferred") {
		t.Fatalf("cross-file receiver call s.AuthInfoFromCtx() not resolved to the sibling-file method: %#v", snapshot.Relations)
	}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "Server.checkPermission" &&
			strings.Contains(r.ToID, "auth/store.go") {
			t.Fatalf("cross-file receiver call bound to the wrong package's method: %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
	}
	// The reconciled container also restores type CONTAINS method across files.
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CONTAINS" && strings.Contains(r.FromID, "server/server.go:type:Server") &&
			strings.Contains(r.ToID, "server/v3.go:method:Server.AuthInfoFromCtx") {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-file method missing CONTAINS edge from its receiver type: %#v", snapshot.Relations)
	}
}

func TestGoMultiAssignedReceiverCallResolvesViaCalleeFirstResult(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/cobralike\n\ngo 1.21\n")
	// `cmd` is typed only by the multi-value assignment from Find's first
	// result. A same-named method on another package's type defeats the
	// globally-unique-name fallback, so the edge only resolves when the
	// multi-assign receiver is actually typed.
	writeFile(t, repo, "cli/command.go", `package cli

type Command struct{}

func (c *Command) Find(args []string) (*Command, []string, error) {
	return c, args, nil
}

func (c *Command) execute(a []string) error { return nil }

func (c *Command) Run(args []string) error {
	cmd, flags, err := c.Find(args)
	if err != nil {
		return err
	}
	return cmd.execute(flags)
}
`)
	writeFile(t, repo, "worker/worker.go", `package worker

type Job struct{}

func (j *Job) execute(a []string) error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "Command.Run", "Command.execute", "type_inferred") {
		t.Fatalf("multi-assigned receiver call cmd.execute() not resolved through Find's first result type: %#v", snapshot.Relations)
	}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "Command.Run" && strings.Contains(r.ToID, "worker/worker.go") {
			t.Fatalf("multi-assigned receiver call bound to the wrong package's method: %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
	}
}

func TestGoNamedResultReceiverCallResolvesViaSignature(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/cobralike\n\ngo 1.21\n")
	// `st` is typed only by the named result declaration `(st *Store, err
	// error)`: the assignment `st = s.lookup()` is receiver-qualified, which
	// neither the constructor scan nor the factory-assignment scan types. A
	// same-named method elsewhere defeats the unique-name fallback.
	writeFile(t, repo, "registry/registry.go", `package registry

type Store struct{}

func (s *Store) lookup() *Store { return s }

func (s *Store) refresh() error { return nil }

func (s *Store) Sync() (st *Store, err error) {
	st = s.lookup()
	return st, st.refresh()
}
`)
	writeFile(t, repo, "cache/cache.go", `package cache

type Cache struct{}

func (c *Cache) refresh() error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "Store.Sync", "Store.refresh", "type_inferred") {
		t.Fatalf("named-result receiver call st.refresh() not resolved through the signature's result type: %#v", snapshot.Relations)
	}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "Store.Sync" && strings.Contains(r.ToID, "cache/cache.go") {
			t.Fatalf("named-result receiver call bound to the wrong package's method: %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
	}
}

func TestGoPackageQualifiedSubpackageCallsResolveAcrossPackageFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/hugolike\n\ngo 1.21\n")
	// Both callees live in a NON-representative file of their package (the
	// import resolver records the alphabetically first non-test file —
	// handlerdefault.go / finder.go), so `pkg.Fn(...)` only resolves when
	// import matching is package-directory granular. WalkDeep is called from
	// inside a func literal.
	writeFile(t, repo, "hugolib/map.go", `package hugolib

import (
	"example.com/hugolike/common/loggers"
	"example.com/hugolike/identity"
)

type Sites struct{}

func (h *Sites) resolveState(changes []string) error {
	for _, change := range changes {
		fn := func() bool {
			identity.WalkDeep(change)
			return false
		}
		_ = fn()
	}
	return loggers.TimeTrack(changes)
}
`)
	writeFile(t, repo, "common/loggers/handlerdefault.go", `package loggers

func Handler() {}
`)
	writeFile(t, repo, "common/loggers/logger.go", `package loggers

func TimeTrack(v []string) error { return nil }
`)
	writeFile(t, repo, "identity/finder.go", `package identity

func Find() {}
`)
	writeFile(t, repo, "identity/identity.go", `package identity

func WalkDeep(v string) {}
`)
	writeFile(t, repo, "util/track.go", `package util

func TimeTrack(v []string) error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	assertQualifiedCall := func(calleeFile, callee string) {
		t.Helper()
		for _, r := range snapshot.Relations {
			if r.Type == "CALLS" && lastSegment(r.FromID) == "Sites.resolveState" &&
				strings.Contains(r.ToID, calleeFile+":function:"+callee) && r.Resolution == "import_resolved" {
				return
			}
		}
		t.Fatalf("package-qualified call to %s (%s) not resolved through the imported package directory: %#v", callee, calleeFile, snapshot.Relations)
	}
	assertQualifiedCall("common/loggers/logger.go", "TimeTrack")
	assertQualifiedCall("identity/identity.go", "WalkDeep")
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "Sites.resolveState" {
			continue
		}
		if strings.Contains(r.ToID, "util/track.go") {
			t.Fatalf("package-qualified call bound to a same-named function outside the imported package: %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
		if strings.Contains(r.ToID, "external:symbol:example.com/hugolike/") {
			t.Fatalf("in-module package-qualified call left as an external edge: %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
	}
}

func TestGoInterfaceReturnedCallResolvesUniqueImplementingMethod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/etcdlike\n\ngo 1.21\n")
	// AuthStore() returns an interface declared in another package; the
	// interface's methods are part of its type declaration, not method
	// symbols, so resolution must fall back to the unique implementation.
	writeFile(t, repo, "server/server.go", `package server

import "example.com/etcdlike/auth"

type Server struct {
	authStore auth.AuthStore
}

func (s *Server) AuthStore() auth.AuthStore { return s.authStore }

func (s *Server) store() auth.AuthStore { return s.authStore }

func (s *Server) checkPermission(info *auth.Info) error {
	return s.AuthStore().IsAdminPermitted(info)
}
`)
	writeFile(t, repo, "server/v3.go", `package server

import "example.com/etcdlike/auth"

func (s *Server) permitted(info *auth.Info) bool {
	return s.store().IsAdminPermitted(info) == nil
}
`)
	writeFile(t, repo, "auth/store.go", `package auth

type Info struct{ User string }

type AuthStore interface {
	Authenticate(name string) (*Info, error)
	IsAdminPermitted(info *Info) error
}

type authStore struct{}

func (as *authStore) Authenticate(name string) (*Info, error) { return &Info{}, nil }

func (as *authStore) IsAdminPermitted(info *Info) error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// One-hop chain through the capitalized getter (same file as the caller).
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Server.checkPermission", "authStore.IsAdminPermitted") {
		t.Fatalf("interface-typed chain s.AuthStore().IsAdminPermitted() not resolved to the unique implementation: %#v", snapshot.Relations)
	}
	// One-hop chain through a lower-case getter declared in a sibling file of
	// the same package (exercises the package-level return-type lookup).
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Server.permitted", "authStore.IsAdminPermitted") {
		t.Fatalf("interface-typed chain s.store().IsAdminPermitted() with sibling-file getter not resolved: %#v", snapshot.Relations)
	}
}

func TestGoInterfaceReturnedCallStaysUnresolvedWhenMethodNameAmbiguous(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/etcdlike\n\ngo 1.21\n")
	writeFile(t, repo, "server/server.go", `package server

import "example.com/etcdlike/auth"

type Server struct {
	authStore auth.AuthStore
}

func (s *Server) AuthStore() auth.AuthStore { return s.authStore }

func (s *Server) Close() error { return nil }

func (s *Server) shutdown() error {
	return s.AuthStore().Close()
}
`)
	writeFile(t, repo, "auth/store.go", `package auth

type AuthStore interface {
	Close() error
}

type authStore struct{}

func (as *authStore) Close() error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// Two methods named Close exist, so the conservative unique-name subset
	// must not guess between them.
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "Server.shutdown" &&
			(lastSegment(r.ToID) == "authStore.Close" || lastSegment(r.ToID) == "Server.Close") {
			t.Fatalf("ambiguous interface method call must stay unresolved, got %s -> %s (%s)", r.FromID, r.ToID, r.Reason)
		}
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
import requests.sessions

def encode(value):
    return json.dumps(value)

def open_session():
    return requests.sessions.session()
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
		{from: "open_session", target: "requests.sessions.session", detail: "session"},
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

func hasRelationBySymbolName(snapshot ProviderSnapshot, relationType, from, to string) bool {
	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != relationType {
			continue
		}
		fromSymbol, fromOK := symbolsByID[relation.FromID]
		toSymbol, toOK := symbolsByID[relation.ToID]
		if fromOK && toOK && fromSymbol.Name == from && toSymbol.Name == to {
			return true
		}
	}
	return false
}

func hasRelationBySymbolNameAndFile(snapshot ProviderSnapshot, relationType, from, fromFile, to, toFile string) bool {
	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}
	for _, relation := range snapshot.Relations {
		if relation.Type != relationType {
			continue
		}
		fromSymbol, fromOK := symbolsByID[relation.FromID]
		toSymbol, toOK := symbolsByID[relation.ToID]
		if fromOK && toOK && fromSymbol.Name == from && fromSymbol.FilePath == fromFile && toSymbol.Name == to && toSymbol.FilePath == toFile {
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
		"R",
		"Julia",
		"F#",
		"Objective-C",
		"Erlang",
		"Haskell",
		"Perl",
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
	for _, want := range []string{"TypeScript", "Python", "JavaScript", "Java", "C++", "C", "C#", "Go", "PHP", "Rust", "Kotlin", "Ruby", "Swift", "SQL", "Bash", "Zsh", "Dart", "R", "Julia", "Clojure", "ClojureScript", "Zig", "Perl", "Haskell", "Erlang", "Objective-C", "F#"} {
		if !semanticSeen[want] {
			t.Fatalf("capabilities should classify %q as semantic, got semantic=%#v inventory=%#v", want, caps.SemanticLanguages, caps.InventoryOnlyLanguages)
		}
	}
	for _, want := range []string{"Bicep", "Solidity", "Nix", "Blade"} {
		if !inventorySeen[want] {
			t.Fatalf("capabilities should classify %q as inventory-only, got semantic=%#v inventory=%#v", want, caps.SemanticLanguages, caps.InventoryOnlyLanguages)
		}
	}
	for _, want := range []string{".go", ".py", ".ts", ".rs", ".swift", ".proto", ".dart", ".r", ".jl", ".zig", ".bicep", ".graphql", ".pl", ".pm", ".hs", ".erl", ".hrl", ".m", ".fs", ".fsx"} {
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

func TestKotlinClassPropertyInitializersAndImportedTopLevelCallsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/org/koin/core/module/Module.kt", `package org.koin.core.module

class Module

fun flatten(modules: List<Module>): Set<Module> = modules.toSet()
`)
	writeFile(t, repo, "src/org/koin/core/registry/InstanceRegistry.kt", `package org.koin.core.registry

import org.koin.core.module.Module

class InstanceRegistry {
  fun loadModules(modules: Set<Module>, allowOverride: Boolean) {}
}
`)
	writeFile(t, repo, "src/org/koin/core/registry/ScopeRegistry.kt", `package org.koin.core.registry

import org.koin.core.module.Module

class ScopeRegistry {
  fun loadScopes(modules: Set<Module>) {}
}
`)
	writeFile(t, repo, "src/org/koin/core/Koin.kt", `package org.koin.core

import org.koin.core.module.Module
import org.koin.core.module.flatten
import org.koin.core.registry.InstanceRegistry
import org.koin.core.registry.ScopeRegistry

class Koin {
  val instanceRegistry = InstanceRegistry()
  val scopeRegistry = ScopeRegistry()

  fun loadModules(modules: List<Module>, allowOverride: Boolean = true, createEagerInstances: Boolean = false) {
    val flattedModules = flatten(modules)
    instanceRegistry.loadModules(flattedModules, allowOverride)
    scopeRegistry.loadScopes(flattedModules)
    if (createEagerInstances) {
      createEagerInstances()
    }
  }

  fun createEagerInstances() {}
}
`)
	writeFile(t, repo, "src/org/koin/core/KoinApplication.kt", `package org.koin.core

import org.koin.core.module.Module

class KoinApplication {
  val koin = Koin()

  private fun loadModules(modules: List<Module>) {
    koin.loadModules(modules, allowOverride = true, createEagerInstances = false)
  }
}
`)
	writeFile(t, repo, "src/other/Other.kt", `package other

import org.koin.core.module.Module

fun flatten(modules: List<Module>): Set<Module> = emptySet()
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		from     string
		fromFile string
		to       string
		toFile   string
	}{
		{"loadModules", "src/org/koin/core/KoinApplication.kt", "loadModules", "src/org/koin/core/Koin.kt"},
		{"loadModules", "src/org/koin/core/Koin.kt", "flatten", "src/org/koin/core/module/Module.kt"},
		{"loadModules", "src/org/koin/core/Koin.kt", "loadModules", "src/org/koin/core/registry/InstanceRegistry.kt"},
		{"loadModules", "src/org/koin/core/Koin.kt", "loadScopes", "src/org/koin/core/registry/ScopeRegistry.kt"},
		{"loadModules", "src/org/koin/core/Koin.kt", "createEagerInstances", "src/org/koin/core/Koin.kt"},
	} {
		if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", want.from, want.fromFile, want.to, want.toFile) {
			t.Fatalf("missing Kotlin CALLS %s:%s -> %s:%s in %#v", want.fromFile, want.from, want.toFile, want.to, relationsOfType(snapshot.Relations, "CALLS"))
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
	for _, language := range []string{"Go", "Python", "TypeScript", "Java", "Rust", "C#", "PHP", "Dart", "Erlang", "OCaml", "Haskell"} {
		if !contains(caps.RelationSupportByLanguage[language], "CALLS") {
			t.Fatalf("language %q should support CALLS: %#v", language, caps.RelationSupportByLanguage[language])
		}
	}
	for _, language := range []string{"Bicep", "Dockerfile", "YAML", "Kustomize"} {
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

func TestBuildProviderSnapshotHeadHonorsIgnoreAndIncludeFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, ".semignore", "*\n")
	writeFile(t, repo, ".seminclude", "src/keep.py\n")
	writeFile(t, repo, "src/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, "src/drop.py", "def drop_me():\n    return False\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		IgnoreFiles:  []string{".semignore"},
		IncludeFiles: []string{".seminclude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !snapshotHasSymbol(snapshot, "keep_me") {
		t.Fatalf("snapshot did not include explicitly included HEAD file: %#v", snapshot.Symbols)
	}
	if snapshotHasPath(snapshot, "src/drop.py") || snapshotHasSymbol(snapshot, "drop_me") {
		t.Fatalf("snapshot included ignored HEAD file: files=%#v symbols=%#v", snapshot.Files, snapshot.Symbols)
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
		// Ambiguous generated-output names are not unconditionally vendored;
		// skipVendoredDir decides them from git tracked-ness.
		{"build", "build", false},
		{"packages/next/src/build", "build", false},
		{"dist", "dist", false},
		{"deps", "deps", false},
		{"external", "external", false},
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

func TestSkipVendoredDirTrackedAmbiguousNames(t *testing.T) {
	tracked := func(rel string) bool { return rel == "src/build" || rel == "deps" }
	cases := []struct {
		rel, name string
		want      bool
	}{
		// A tracked build/deps directory is first-party source and is walked.
		{"src/build", "build", false},
		{"deps", "deps", false},
		// The same names untracked are generated/fetched output and skipped.
		{"build", "build", true},
		{"a/dist", "dist", true},
		{"external", "external", true},
		// Unambiguous vendored names are skipped even when tracked.
		{"node_modules", "node_modules", true},
		{"vendor", "vendor", true},
	}
	for _, c := range cases {
		if got := skipVendoredDir(c.rel, c.name, ignoreMatcher{}, tracked); got != c.want {
			t.Errorf("skipVendoredDir(%q, %q) = %v, want %v", c.rel, c.name, got, c.want)
		}
	}
}

func TestBuildProviderSnapshotWorktreeKeepsTrackedBuildDir(t *testing.T) {
	// packages/next/src/build in vercel/next.js is first-party, git-tracked
	// source; the vendored-directory heuristic must not skip a tracked
	// directory just because it is named "build". The untracked build/ output
	// directory stays excluded.
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "src/build/lib.ts", "export function collectTraces(): number {\n  return 1\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "build/out.js", "function generatedOutput() {\n  return 1\n}\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasPath(snapshot, "src/build/lib.ts") || !snapshotHasSymbol(snapshot, "collectTraces") {
		t.Fatalf("tracked src/build was skipped: files=%#v", snapshot.Files)
	}
	if snapshotHasPath(snapshot, "build/out.js") || snapshotHasSymbol(snapshot, "generatedOutput") {
		t.Fatalf("untracked build output was included: files=%#v symbols=%#v", snapshot.Files, snapshot.Symbols)
	}
}

func TestBuildProviderSnapshotHeadKeepsTrackedBuildDir(t *testing.T) {
	// The HEAD-tree listing only contains tracked paths, so ambiguous
	// generated-output names (build, dist, deps, external) must never be
	// filtered from it.
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, "src/build/lib.ts", "export function collectTraces(): number {\n  return 1\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasPath(snapshot, "src/build/lib.ts") || !snapshotHasSymbol(snapshot, "collectTraces") {
		t.Fatalf("HEAD snapshot skipped tracked src/build: files=%#v", snapshot.Files)
	}
}

func TestBuildProviderSnapshotWorktreeUntrackedBuildDirStaysExcluded(t *testing.T) {
	// Without git metadata (plain directory), an ambiguous name cannot be
	// proven first-party, so it keeps the conservative vendored treatment.
	repo := t.TempDir()
	writeFile(t, repo, "build/out.py", "def generated_output():\n    return True\n")
	writeFile(t, repo, "src/keep.py", "def keep():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "keep") {
		t.Fatalf("snapshot missing kept symbol: %#v", snapshot.Symbols)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "build/")
}

func TestBuildProviderSnapshotWorktreeDepsGitignoreNegation(t *testing.T) {
	// rabbitmq-server layout: fetched dependencies live in deps/ and are
	// gitignored (`/deps/*`), while the project's own applications are negated
	// back in (`!/deps/rabbit/`) and committed. The tracked deps/ tree must be
	// walked, with the ignore rules keeping the fetched dependencies out.
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")
	writeFile(t, repo, ".gitignore", "/deps/*\n!/deps/rabbit/\n")
	writeFile(t, repo, "deps/rabbit/src/app.py", "def first_party_app():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "deps/fetched/dep.py", "def fetched_dependency():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasPath(snapshot, "deps/rabbit/src/app.py") || !snapshotHasSymbol(snapshot, "first_party_app") {
		t.Fatalf("first-party deps application was skipped: files=%#v", snapshot.Files)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "deps/fetched/")
	if snapshotHasSymbol(snapshot, "fetched_dependency") {
		t.Fatalf("fetched dependency was included: %#v", snapshot.Symbols)
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

// Dart CALLS extraction (evidence: on dart-lang/http the focus method
// BaseClient._sendUnstreamed had zero inbound/outbound CALLS). Dart keeps a
// declaration's body as a *sibling* function_body node, so symbols spanned only
// the head and the call scanner saw no call sites; bare sibling-method calls
// were also dropped because Dart wasn't an implicit-receiver language; and
// constructor calls to names like Request were eaten by the JS-builtin ignore
// list. Private `_names` must survive the identifier scan.
func TestDartCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/src/base_client.dart", `import 'request.dart';
import 'response.dart';

abstract mixin class BaseClient implements Client {
  Future<Response> head(Uri url, {Map<String, String>? headers}) =>
      _sendUnstreamed('HEAD', url, headers);

  Future<Response> get(Uri url, {Map<String, String>? headers}) =>
      _sendUnstreamed('GET', url, headers);

  Future<Response> _sendUnstreamed(
      String method, Uri url, Map<String, String>? headers,
      {Object? body, Encoding? encoding}) async {
    var request = Request(method, url);
    if (headers != null) request.headers.addAll(headers);
    if (body is Map<String, String>) request.bodyFields = body;
    return Response.fromStream(await send(request));
  }

  Future<StreamedResponse> send(BaseRequest request);
}
`)
	writeFile(t, repo, "lib/src/request.dart", `class Request extends BaseRequest {
  Request(super.method, super.url);

  set bodyFields(Map<String, String> fields) {}
}
`)
	writeFile(t, repo, "lib/src/response.dart", `class Response extends BaseResponse {
  static Future<Response> fromStream(StreamedResponse response) async {
    final body = await response.stream.toBytes();
    return Response(body, response.statusCode);
  }
}
`)
	// A same-named class in another package: the constructor call must still
	// resolve — to the same-directory Request — instead of being dropped as
	// globally ambiguous (dart-lang/http has a second Request in ok_http).
	writeFile(t, repo, "other_pkg/lib/src/bindings.dart", `class Request {
  Request();
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	edges := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" || r.Type == "CONSTRUCTS" {
			edges[r.Type+" "+lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}
	// Public methods calling a private (`_`-prefixed) sibling method: the
	// symbol block must include the sibling function_body, the identifier scan
	// must keep the leading underscore, and the bare call must resolve to a
	// same-class method (implicit receiver).
	for _, want := range []string{
		"CALLS BaseClient.head->BaseClient._sendUnstreamed",
		"CALLS BaseClient.get->BaseClient._sendUnstreamed",
	} {
		if r, ok := edges[want]; !ok || r.Resolution != "exact" {
			t.Fatalf("missing/weak %q: %#v", want, edges)
		}
	}
	// Bare sibling-method call inside the method body.
	if _, ok := edges["CALLS BaseClient._sendUnstreamed->BaseClient.send"]; !ok {
		t.Fatalf("missing _sendUnstreamed->send call: %#v", edges)
	}
	// Static class-name receiver call resolved cross-file.
	if _, ok := edges["CALLS BaseClient._sendUnstreamed->Response.fromStream"]; !ok {
		t.Fatalf("missing _sendUnstreamed->Response.fromStream call: %#v", edges)
	}
	if _, ok := edges["CALLS BaseClient._sendUnstreamed->Request.bodyFields"]; !ok {
		t.Fatalf("missing _sendUnstreamed->Request.bodyFields setter call: %#v", edges)
	}
	// Constructor call to a class in another file: a call to a type-like symbol
	// is recorded as CONSTRUCTS (the convention shared with Go/TS), the name
	// Request must not be dropped by the JS-builtin ignore list in Dart, and the
	// decoy Request in other_pkg/ must lose to the same-directory class.
	r, ok := edges["CONSTRUCTS BaseClient._sendUnstreamed->Request"]
	if !ok || r.Resolution != "package" {
		t.Fatalf("missing/weak _sendUnstreamed->Request construction: %#v", edges)
	}
	if !strings.Contains(r.ToID, "lib/src/request.dart") {
		t.Fatalf("Request construction resolved to the wrong class: %#v", r)
	}
	// The method_signature wrapper must not surface as a bogus method named
	// after the return type.
	for _, s := range snapshot.Symbols {
		if s.Language == "Dart" && strings.HasSuffix(s.Name, ".Future") {
			t.Fatalf("return type extracted as method symbol: %#v", s)
		}
	}
}

func TestRSemanticExtraction(t *testing.T) {
	// R was promoted from inventory to the semantic tier (vendored grammar).
	// R defines functions by assignment, so the extractor must name symbols
	// from the assignment target across the assignment operator spellings.
	repo := t.TempDir()
	writeFile(t, repo, "R/plot.R", `helper <- function(n) {
  n + 1
}

scale = function(x) x * 2

"at<-" <- function(x, value) {
  x
}

Person <- R6::R6Class("Person", public = list(
  greet = function() cat("hi")
))

build_ggplot <- S7::method(plot_theme, class_theme) <- function(x, y) {
  helper(x)
}

render <- function(x) {
  helper(x)
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "R" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["helper"] != "function" {
		t.Fatalf("R `<-` function not extracted: %#v", kinds)
	}
	if kinds["scale"] != "function" {
		t.Fatalf("R `=` function not extracted: %#v", kinds)
	}
	if kinds["at<-"] != "function" {
		t.Fatalf("R string-named replacement function not extracted: %#v", kinds)
	}
	if kinds["Person"] != "class" {
		t.Fatalf("R R6 class not extracted: %#v", kinds)
	}
	if kinds["build_ggplot"] != "function" {
		t.Fatalf("R chained assignment function not extracted: %#v", kinds)
	}
	// Anonymous function_definition nodes must not invent symbols named after
	// their first parameter.
	if _, ok := kinds["n"]; ok {
		t.Fatalf("R anonymous function invented a parameter-named symbol: %#v", kinds)
	}
	var calls int
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls++
		}
	}
	if calls == 0 {
		t.Fatalf("expected CALLS relations for R (render -> helper), got none")
	}
}

func TestJuliaSemanticExtraction(t *testing.T) {
	// Julia was promoted from inventory to the semantic tier (vendored grammar);
	// it must now extract modules, structs, macros, and both long- and
	// short-form function definitions.
	repo := t.TempDir()
	writeFile(t, repo, "src/app.jl", `function long_form(x, y)
    return x + y
end

short_form(a, b) = a * b

Base.show(io::IO, p::Point) = print(io, p.x)

struct Point{T}
    x::T
    y::T
end

mutable struct Counter
    n::Int
end

macro trace(ex)
    return ex
end

module Inner

inner_helper(v) = v + 1

end
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Julia" {
			kinds[s.QualifiedName] = s.Kind
		}
	}
	if kinds["long_form"] != "function" {
		t.Fatalf("Julia long-form function not extracted: %#v", kinds)
	}
	if kinds["short_form"] != "function" {
		t.Fatalf("Julia short-form function not extracted: %#v", kinds)
	}
	if kinds["Base.show"] != "function" {
		t.Fatalf("Julia qualified extension method not extracted: %#v", kinds)
	}
	if kinds["Point"] != "struct" {
		t.Fatalf("Julia parametric struct not extracted: %#v", kinds)
	}
	if kinds["Counter"] != "struct" {
		t.Fatalf("Julia mutable struct not extracted: %#v", kinds)
	}
	if kinds["trace"] != "function" {
		t.Fatalf("Julia macro not extracted: %#v", kinds)
	}
	if kinds["Inner"] != "module" {
		t.Fatalf("Julia module not extracted: %#v", kinds)
	}
	if kinds["Inner.inner_helper"] == "" && kinds["inner_helper"] == "" {
		t.Fatalf("Julia module-scoped function not extracted: %#v", kinds)
	}
}

func TestJuliaBangCallsAndHashCommentsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/hpack.jl", `function _decode_integer(block, index, bits)
    return index, bits
end

function _decode_literal!(decoder, block, index)
    return decoder, block, index
end

function set_max_dynamic_table_size!(decoder, size)
    return size
end

function decode_header_block(decoder, block)
    index, bits = _decode_integer(block, 1, 7)
    header = _decode_literal!(decoder, block, index)
    # Keep this comment ending with a selector-looking dot.
    set_max_dynamic_table_size!(decoder, bits)
    return header
end
`)
	writeFile(t, repo, "src/http2_server.jl", `function _h2_server_refuse_stream!(decoder, block)
    # Keep the shared decoder state consistent with the peer's encoder.
    decode_header_block(decoder, block)
    return nothing
end
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"decode_header_block", "_decode_integer"},
		{"decode_header_block", "_decode_literal!"},
		{"decode_header_block", "set_max_dynamic_table_size!"},
		{"_h2_server_refuse_stream!", "decode_header_block"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing Julia CALLS %s->%s: %#v", want[0], want[1], relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

func TestClojureSemanticExtraction(t *testing.T) {
	// Clojure was promoted from inventory to the semantic tier (vendored
	// grammar). tree-sitter-clojure only produces generic list_lit nodes, so
	// def-forms are recognized by list-head inspection, including namespaced
	// heads such as `mu/defn` (a common def-macro idiom).
	repo := t.TempDir()
	writeFile(t, repo, "src/app/core.clj", `(ns app.core)

(def config {:port 8080})

(defonce state (atom nil))

(defn- helper [n]
  (inc n))

(defn ^:private process-item
  "Docstring is skipped."
  [item]
  (helper item))

(mu/defn instrumented :- :int
  [x :- :int]
  (inc x))

(defmacro with-thing [& body]
  body)

(defmulti area :shape)

(defmethod area :circle [c]
  (* 3.14 (:radius c) (:radius c)))

(defprotocol Renderer
  (render [this]))

(defrecord Card [title]
  Renderer
  (render [this] title))

(deftype Box [w h])

(definterface IThing
  (doThing []))
`)
	writeFile(t, repo, "src/app/shared.cljc", "(defn shared-fn [x] x)\n")
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Clojure" {
			kinds[s.Name] = s.Kind
		}
	}
	for name, want := range map[string]string{
		"config":       "variable",
		"state":        "variable",
		"helper":       "function",
		"process-item": "function",
		"instrumented": "function",
		"with-thing":   "function",
		"area":         "function",
		"Renderer":     "interface",
		"Card":         "class",
		"Box":          "class",
		"IThing":       "interface",
		"shared-fn":    "function",
	} {
		if kinds[name] != want {
			t.Fatalf("Clojure symbol %q: want kind %q, got %q (all: %#v)", name, want, kinds[name], kinds)
		}
	}
}
func TestZigSemanticExtraction(t *testing.T) {
	// Zig was promoted from inventory to the semantic tier (vendored grammar);
	// it must now extract functions and const-bound struct/enum/union types.
	repo := t.TempDir()
	writeFile(t, repo, "src/app.zig", `pub fn add(a: i32, b: i32) i32 {
    return a + b;
}

const Point = struct {
    x: i32,
    y: i32,

    pub fn norm(self: Point) i32 {
        return add(self.x, self.y);
    }
};

pub const Mode = enum { fast, slow };

const Value = union(enum) {
    int: i64,
    float: f64,
};

test "add works" {
    _ = add(1, 2);
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Zig" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["add"] != "function" {
		t.Fatalf("Zig top-level fn not extracted: %#v", kinds)
	}
	if kinds["Point"] != "struct" {
		t.Fatalf("Zig struct const not extracted: %#v", kinds)
	}
	if kinds["Mode"] != "enum" {
		t.Fatalf("Zig enum const not extracted: %#v", kinds)
	}
	if kinds["Value"] != "type" {
		t.Fatalf("Zig union const not extracted: %#v", kinds)
	}
	if kinds["Point.norm"] == "" && kinds["norm"] == "" {
		t.Fatalf("Zig container fn not extracted: %#v", kinds)
	}
	for name, kind := range kinds {
		if strings.Contains(name, "add works") || kind == "" {
			t.Fatalf("Zig test declaration should be skipped: %#v", kinds)
		}
	}
}
func TestPerlSemanticExtraction(t *testing.T) {
	// Perl was promoted from inventory to the semantic tier (vendored grammar);
	// it must now extract packages (as modules) and subroutines (as functions).
	repo := t.TempDir()
	writeFile(t, repo, "lib/My/Util.pm", `package My::Util;

sub slugify {
  my ($value) = @_;
  return lc $value;
}

package My::Inner {
  sub scoped { return 1 }
}

1;
`)
	writeFile(t, repo, "bin/tool.pl", `#!/usr/bin/perl
sub run {
  return 42;
}
run();
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Perl" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["My::Util"] != "module" {
		t.Fatalf("Perl package statement not extracted as module: %#v", kinds)
	}
	if kinds["My::Inner"] != "module" {
		t.Fatalf("Perl package block not extracted as module: %#v", kinds)
	}
	if kinds["slugify"] != "function" {
		t.Fatalf("Perl sub in .pm not extracted as function: %#v", kinds)
	}
	if kinds["run"] != "function" {
		t.Fatalf("Perl sub in .pl not extracted as function: %#v", kinds)
	}
	if kinds["My::Inner.scoped"] != "function" && kinds["scoped"] != "function" {
		t.Fatalf("Perl sub inside package block not extracted: %#v", kinds)
	}
}

func TestPerlReceiverCallsWithoutParentheses(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/Mojolicious/Controller.pm", `package Mojolicious::Controller;

sub stash { return {} }

sub url_for {
  my ($self, $target) = (shift, shift // '');
  my $url  = Mojo::URL->new;
  my $req  = $self->req;
  my $base = $url->base($req->url->base->clone)->base->userinfo(undef);

  if (defined(my $prefix = $self->stash->{path})) {
    my $real = $req->url->path->to_route;
  }

  return $base->protocol eq 'https';
}
`)
	writeFile(t, repo, "lib/Mojo/Path.pm", `package Mojo::Path;

sub to_route {
  return '/';
}
`)
	writeFile(t, repo, "lib/Mojo/URL.pm", `package Mojo::URL;

sub new {
  return bless {}, shift;
}

sub base {
  return shift;
}

sub userinfo {
  return shift;
}

sub protocol {
  return 'http';
}
`)
	writeFile(t, repo, "lib/Mojo/Transaction/WebSocket.pm", `package Mojo::Transaction::WebSocket;

sub protocol {
  return 'websocket';
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}
	for _, want := range []string{"url_for->stash", "url_for->to_route", "url_for->protocol"} {
		r, ok := calls[want]
		if !ok {
			t.Fatalf("missing Perl receiver call %s in %#v", want, calls)
		}
		wantReason := "Perl receiver call matched globally unique subroutine name"
		if want == "url_for->protocol" {
			wantReason = "Perl receiver call resolved via inferred package receiver type"
		}
		if got := r.Reason; got != wantReason {
			t.Fatalf("%s reason = %q, want %q", want, got, wantReason)
		}
		if symbolsByID[r.ToID].Language != "Perl" {
			t.Fatalf("%s resolved to non-Perl target: %#v", want, symbolsByID[r.ToID])
		}
		if want == "url_for->protocol" && symbolsByID[r.ToID].FilePath != "lib/Mojo/URL.pm" {
			t.Fatalf("%s resolved to wrong Perl protocol target: %#v", want, symbolsByID[r.ToID])
		}
	}
}

func TestPerlCommentArrowsDoNotAffectReceiverTypeInference(t *testing.T) {
	block := `my $url = Mojo::URL->new;
my $path = $url->path # ->base->userinfo
;
# $path->protocol
`
	types := perlLocalVarTypes(block, func(hop, pkgType string) bool { return true })
	if got := types["path"]; got != "" {
		t.Fatalf("commented receiver chain inferred path type %q from %#v", got, types)
	}
	calls := perlReceiverCalls(block)
	if len(calls) != 1 || calls[0].Receiver != "url" || calls[0].Method != "path" {
		t.Fatalf("commented receiver call should be ignored while real call remains, got %#v", calls)
	}
}

func TestPerlLocalVarTypesAllowKeywordlessAssignments(t *testing.T) {
	types := perlLocalVarTypes(`$url = Mojo::URL->new;
$base = $url->base->userinfo;
`, func(hop, pkgType string) bool { return true })
	if got := types["url"]; got != "Mojo::URL" {
		t.Fatalf("keywordless constructor inferred url type %q from %#v", got, types)
	}
	if got := types["base"]; got != "Mojo::URL" {
		t.Fatalf("keywordless fluent assignment inferred base type %q from %#v", got, types)
	}
}

func TestPerlCommentStrippingPreservesHashSyntax(t *testing.T) {
	stripped := stripPerlCodeLiteralsAndComments(`my $last = $#items;
$value =~ s/#/x/;
$value =~ s{#}{x}; $obj->method;
$value =~ s#foo#bar#; $obj->hashSub;
$value =~ m{#};
$value =~ m#foo#; $obj->hashMatch;
$value =~ /#/;
/#/; $obj->implicit;
if (/#/) { $obj->grouped; }
$obj->real; foo()# $obj->fake
$value = $maybe // $fallback;
$base = $url->base # ->userinfo
`)
	if !strings.Contains(stripped, "$#items") {
		t.Fatalf("Perl array-last-index token was masked: %q", stripped)
	}
	if !strings.Contains(stripped, "s/#/x/") {
		t.Fatalf("Perl regex hash was masked: %q", stripped)
	}
	if !strings.Contains(stripped, "s{#}{x}; $obj->method") {
		t.Fatalf("Perl brace-delimited regex hash masked following code: %q", stripped)
	}
	if !strings.Contains(stripped, "$obj->hashSub") {
		t.Fatalf("Perl hash-delimited substitution masked following code: %q", stripped)
	}
	if !strings.Contains(stripped, "m{#}") {
		t.Fatalf("Perl match regex hash was masked: %q", stripped)
	}
	if !strings.Contains(stripped, "$obj->hashMatch") {
		t.Fatalf("Perl hash-delimited match masked following code: %q", stripped)
	}
	if !strings.Contains(stripped, "/#/") {
		t.Fatalf("Perl binding regex hash was masked: %q", stripped)
	}
	if !strings.Contains(stripped, "$obj->implicit") {
		t.Fatalf("Perl expression-start regex hash masked following code: %q", stripped)
	}
	if !strings.Contains(stripped, "$obj->grouped") {
		t.Fatalf("Perl grouped regex hash masked following code: %q", stripped)
	}
	if !strings.Contains(stripped, "// $fallback") {
		t.Fatalf("Perl defined-or operator was masked: %q", stripped)
	}
	if strings.Contains(stripped, "userinfo") {
		t.Fatalf("Perl line comment was not masked: %q", stripped)
	}
	if strings.Contains(stripped, "fake") {
		t.Fatalf("Perl trailing line comment was not masked: %q", stripped)
	}
	calls := perlReceiverCalls(`$value =~ s{#}{x}; $obj->method;`)
	if len(calls) != 1 || calls[0].Receiver != "obj" || calls[0].Method != "method" {
		t.Fatalf("Perl receiver call after brace-delimited regex was masked: %#v", calls)
	}
}

func TestPerlReceiverChainsIgnoreArgumentReceiverChains(t *testing.T) {
	calls := perlReceiverCalls(`$url->base($req->url->base->clone);
$url->base($req->url)->userinfo;
`)
	if len(calls) != 2 {
		t.Fatalf("perlReceiverCalls() = %#v, want two outer calls", calls)
	}
	if calls[0].Receiver != "url" || calls[0].Method != "base" {
		t.Fatalf("single outer call used nested argument receiver segment: %#v", calls[0])
	}
	if calls[1].Receiver != "url" || calls[1].Method != "userinfo" {
		t.Fatalf("multi-hop outer call not preserved: %#v", calls[1])
	}
}

func TestPerlLocalVarTypesIgnoreArgumentReceiverChains(t *testing.T) {
	types := perlLocalVarTypes(`$url = Mojo::URL->new;
$path = $url->base($req->url->base->clone);
$base = $url->base($req->url)->userinfo;
$mixed = $url->base($req) + $other->x;
`, func(hop, pkgType string) bool { return true })
	if got := types["path"]; got != "" {
		t.Fatalf("single outer receiver call with nested argument chain inferred path type %q from %#v", got, types)
	}
	if got := types["base"]; got != "Mojo::URL" {
		t.Fatalf("multi-hop outer fluent assignment inferred base type %q from %#v", got, types)
	}
	if got := types["mixed"]; got != "" {
		t.Fatalf("mixed expression inferred fluent type %q from %#v", got, types)
	}
}

func TestHaskellSemanticExtraction(t *testing.T) {
	// Haskell was promoted from inventory to the semantic tier (vendored
	// grammar); it must now extract top-level function bindings (one symbol
	// per name, even across multiple equations), data/newtype declarations,
	// type synonyms, and classes.
	repo := t.TempDir()
	writeFile(t, repo, "src/App.hs", `module App where

data Widget = Widget
  { widgetName :: String
  , widgetSize :: Int
  }

newtype Wrapper = Wrapper Int

type Alias = [Widget]

class Renderable a where
  render :: a -> String

helper :: Int -> Int
helper n = n + 1

multiEq :: Int -> Int
multiEq 0 = 0
multiEq n = multiEq (n - 1)

topValue :: Int
topValue = helper 3
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	count := map[string]int{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Haskell" {
			kinds[s.Name] = s.Kind
			count[s.Name]++
		}
	}
	for _, name := range []string{"helper", "multiEq", "topValue"} {
		if kinds[name] != "function" {
			t.Fatalf("Haskell top-level binding %q not extracted as function: %#v", name, kinds)
		}
	}
	if count["multiEq"] != 1 {
		t.Fatalf("Haskell multi-equation function should emit one symbol, got %d: %#v", count["multiEq"], kinds)
	}
	for _, name := range []string{"Widget", "Wrapper", "Alias"} {
		if kinds[name] != "type" {
			t.Fatalf("Haskell type declaration %q not extracted: %#v", name, kinds)
		}
	}
	if kinds["Renderable"] != "class" {
		t.Fatalf("Haskell class not extracted: %#v", kinds)
	}
	if kinds["Renderable.render"] == "" && kinds["render"] == "" {
		t.Fatalf("Haskell class method signature not extracted: %#v", kinds)
	}
}
func TestErlangSemanticExtraction(t *testing.T) {
	// Erlang was promoted from inventory to the semantic tier (vendored
	// grammar); it must now extract modules, records, and functions, folding a
	// multi-clause function into a single symbol named after the bare function.
	repo := t.TempDir()
	writeFile(t, repo, "src/geometry.erl", `-module(geometry).
-export([area/1, add/2]).
-record(point, {x = 0, y = 0}).

area(#point{x = X, y = Y}) when X > 0 ->
    X * Y;
area(_) ->
    0.

add(A, B) ->
    A + B.
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	names := map[string]int{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Erlang" {
			kinds[s.Name] = s.Kind
			names[s.Name]++
		}
	}
	if kinds["geometry"] != "module" {
		t.Fatalf("Erlang module attribute not extracted: %#v", kinds)
	}
	if kinds["point"] != "struct" {
		t.Fatalf("Erlang record not extracted: %#v", kinds)
	}
	if kinds["area"] != "function" || kinds["add"] != "function" {
		t.Fatalf("Erlang functions not extracted: %#v", kinds)
	}
	if names["area"] != 1 {
		t.Fatalf("Erlang multi-clause function should fold into one symbol, got %d: %#v", names["area"], kinds)
	}
}
func TestObjectiveCSemanticExtraction(t *testing.T) {
	// Objective-C was promoted from inventory to the semantic tier (vendored
	// tree-sitter-objc grammar for .m files); it must now extract classes from
	// @interface/@implementation, methods (named by the selector's first
	// segment), and plain C functions. .h files keep their existing C-path /
	// inventory handling.
	repo := t.TempDir()
	writeFile(t, repo, "Sources/Manager.m", `#import <Foundation/Foundation.h>

static NSString * EscapedString(NSString *string) {
    return string;
}

@interface Manager : NSObject
@end

@implementation Manager

- (void)startMonitoring {
    NSLog(@"%@", EscapedString(@"hi"));
}

- (instancetype)initWithBaseURL:(NSURL *)url sessionConfiguration:(NSURLSessionConfiguration *)configuration {
    return self;
}

@end
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Objective-C" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["EscapedString"] != "function" {
		t.Fatalf("Objective-C C function not extracted: %#v", kinds)
	}
	if kinds["Manager"] != "class" {
		t.Fatalf("Objective-C class not extracted: %#v", kinds)
	}
	if kinds["Manager.startMonitoring"] == "" && kinds["startMonitoring"] == "" {
		t.Fatalf("Objective-C unary-selector method not extracted: %#v", kinds)
	}
	if kinds["Manager.initWithBaseURL"] == "" && kinds["initWithBaseURL"] == "" {
		t.Fatalf("Objective-C multi-part-selector method not extracted: %#v", kinds)
	}
}

func TestObjectiveCHeaderSemanticExtraction(t *testing.T) {
	// Definition lookups anchor an Objective-C class at its .h header (the
	// @interface lives there; the .m only holds the @implementation), so a
	// header that sniffs as Objective-C must parse with the objc grammar and
	// emit the class from the header. Method prototypes stay skipped (the .m
	// implementation carries the method symbols), @class / @protocol forward
	// declarations stay unextracted, and @protocol blocks emit interfaces.
	repo := t.TempDir()
	writeFile(t, repo, "Sources/Manager.h", `#import <Foundation/Foundation.h>

@class NSURLSessionConfiguration;
@protocol ManagerObserving;

@protocol ManagerDelegate <NSObject>
- (void)managerDidFinish;
@end

@interface Manager : NSObject
@property (readonly, nonatomic, strong) NSURL *baseURL;
- (instancetype)initWithBaseURL:(NSURL *)url;
+ (instancetype)manager;
@end
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	type sym struct {
		kind, file, language string
	}
	symbols := map[string]sym{}
	for _, s := range snapshot.Symbols {
		symbols[s.Name] = sym{kind: s.Kind, file: s.FilePath, language: s.Language}
	}
	manager := symbols["Manager"]
	if manager.kind != "class" || manager.file != "Sources/Manager.h" || manager.language != "Objective-C" {
		t.Fatalf("Objective-C class not extracted from header: %#v (all: %#v)", manager, symbols)
	}
	delegate := symbols["ManagerDelegate"]
	if delegate.kind != "interface" || delegate.file != "Sources/Manager.h" || delegate.language != "Objective-C" {
		t.Fatalf("Objective-C protocol not extracted as interface: %#v (all: %#v)", delegate, symbols)
	}
	for _, forwardOnly := range []string{"NSURLSessionConfiguration", "ManagerObserving"} {
		if _, ok := symbols[forwardOnly]; ok {
			t.Fatalf("forward declaration %q must not emit a symbol: %#v", forwardOnly, symbols)
		}
	}
	for _, prototype := range []string{"initWithBaseURL", "Manager.initWithBaseURL", "manager", "Manager.manager", "managerDidFinish", "ManagerDelegate.managerDidFinish"} {
		if _, ok := symbols[prototype]; ok {
			t.Fatalf("header method prototype %q must not emit a symbol: %#v", prototype, symbols)
		}
	}
}

func TestCHeaderRoutingUnchangedByObjectiveCSniff(t *testing.T) {
	// A plain C header (no @interface/@implementation/#import markers) must
	// keep the existing C routing — C repos (linux, postgres, tmux) are full
	// of .h files that must be untouched by the Objective-C header dispatch.
	repo := t.TempDir()
	writeFile(t, repo, "include/queue.h", `#ifndef QUEUE_H
#define QUEUE_H

#include <stddef.h>

struct queue_entry {
	struct queue_entry *next;
	void *data;
};

static inline size_t queue_align(size_t n) {
	return (n + 7) & ~(size_t)7;
}

#endif
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language != "C" {
			t.Fatalf("plain C header must stay on the C path, got language %q for %q", s.Language, s.Name)
		}
		byName[s.Name] = s.Kind
	}
	if byName["queue_entry"] != "struct" {
		t.Fatalf("C struct not extracted from header: %#v", byName)
	}
	if byName["queue_align"] != "function" {
		t.Fatalf("C inline function not extracted from header: %#v", byName)
	}
}
func TestFSharpSemanticExtraction(t *testing.T) {
	// F# was promoted from inventory to the semantic tier (vendored
	// ionide/tree-sitter-fsharp grammar); it must now extract modules, types,
	// members, and top-level let bindings.
	repo := t.TempDir()
	writeFile(t, repo, "src/App.fs", `module MyApp.Core

let simpleValue = 42

let add x y = x + y

[<CustomEquality; NoComparison>]
type SemVerInfo =
    { Major: uint32 }
    override x.ToString() = string x.Major

type Widget(name: string) =
    member this.Describe (verbose: bool) =
        if verbose then name else "w"

module Nested =
    let helper a = add a 2
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "F#" {
			kinds[s.QualifiedName] = s.Kind
		}
	}
	if kinds["MyApp.Core"] != "module" || kinds["Nested"] != "module" {
		t.Fatalf("F# modules not extracted: %#v", kinds)
	}
	if kinds["add"] != "function" || kinds["helper"] != "function" {
		t.Fatalf("F# top-level let functions not extracted: %#v", kinds)
	}
	if kinds["simpleValue"] != "variable" {
		t.Fatalf("F# top-level let value not extracted: %#v", kinds)
	}
	// The attribute-decorated type must be named from its type_name, not the
	// leading attribute.
	if kinds["SemVerInfo"] != "type" || kinds["Widget"] != "type" {
		t.Fatalf("F# types not extracted: %#v", kinds)
	}
	if kinds["Widget.Describe"] != "method" {
		t.Fatalf("F# member not extracted as method: %#v", kinds)
	}
	if kinds["SemVerInfo.ToString"] != "method" {
		t.Fatalf("F# override member not extracted as method: %#v", kinds)
	}
}

func TestFSharpQualifiedCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Paket.Core/Installation/InstallProcess.fs", `module Paket.InstallProcess

let InstallIntoProjects(options, forceTouch, dependenciesFile, lockFile, projectsAndReferences, updatedGroups, touchedPackages) =
    ignore (options, forceTouch, dependenciesFile, lockFile, projectsAndReferences, updatedGroups, touchedPackages)
`)
	writeFile(t, repo, "src/Paket.Core/Installation/GarbageCollection.fs", `module Paket.GarbageCollection

let CleanUp(dependenciesFile, lockFile) =
    ignore (dependenciesFile, lockFile)
`)
	writeFile(t, repo, "src/Paket.Core/Installation/ScriptGeneration.fs", `module Paket.LoadingScripts

module ScriptGeneration =
    let constructScriptsFromData depCache groups frameworks scriptTypes =
        ignore (depCache, groups, frameworks, scriptTypes)
`)
	writeFile(t, repo, "src/Paket.Core/Installation/UpdateProcess.fs", `module Paket.UpdateProcess

let SelectiveUpdate(dependenciesFile, alternativeProjectRoot, updateMode, semVerUpdateMode, force) =
    ignore (dependenciesFile, alternativeProjectRoot, updateMode, semVerUpdateMode, force)

let SmartInstall(dependenciesFile, updateMode, options) =
    SelectiveUpdate(dependenciesFile, None, updateMode, None, false)
    InstallProcess.InstallIntoProjects(options, false, dependenciesFile, lockFile, [], [], None)
    GarbageCollection.CleanUp(dependenciesFile, lockFile)
    LoadingScripts.ScriptGeneration.constructScriptsFromData depCache [] [] []
`)
	writeFile(t, repo, "src/Paket.Core/PublicAPI.fs", `module Paket.PublicAPI

type Dependencies() =
    member private this.Install(options) =
        UpdateProcess.SmartInstall("deps", "Install", options)
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, fromOK := symbolsByID[r.FromID]
		to, toOK := symbolsByID[r.ToID]
		if !fromOK || !toOK || from.Language != "F#" || to.Language != "F#" {
			continue
		}
		calls[edge{from.FilePath + ":" + from.Name, to.FilePath + ":" + to.Name}] = true
	}
	for _, want := range []edge{
		{"src/Paket.Core/PublicAPI.fs:Install", "src/Paket.Core/Installation/UpdateProcess.fs:SmartInstall"},
		{"src/Paket.Core/Installation/UpdateProcess.fs:SmartInstall", "src/Paket.Core/Installation/UpdateProcess.fs:SelectiveUpdate"},
		{"src/Paket.Core/Installation/UpdateProcess.fs:SmartInstall", "src/Paket.Core/Installation/InstallProcess.fs:InstallIntoProjects"},
		{"src/Paket.Core/Installation/UpdateProcess.fs:SmartInstall", "src/Paket.Core/Installation/GarbageCollection.fs:CleanUp"},
		{"src/Paket.Core/Installation/UpdateProcess.fs:SmartInstall", "src/Paket.Core/Installation/ScriptGeneration.fs:constructScriptsFromData"},
	} {
		if !calls[want] {
			t.Fatalf("missing F# qualified CALLS edge %v in %v", want, calls)
		}
	}
}

func TestSwiftProtocolDeclarationsEmitted(t *testing.T) {
	// tree-sitter-swift emits protocol_declaration; an Objective-C-only gate
	// on that node type silently dropped every Swift protocol (regression
	// caught by the swift-argument-parser eval row: ParsableCommand,
	// ParsableArguments, ExpressibleByArgument all vanished).
	repo := t.TempDir()
	writeFile(t, repo, "Sources/App/Proto.swift", `public protocol ParsableThing {
    func parse() throws
}

struct Impl: ParsableThing {
    func parse() throws {}
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Swift" {
			kinds[s.Name] = s.Kind
		}
	}
	if kinds["ParsableThing"] != "protocol" {
		t.Fatalf("Swift protocol not extracted: %#v", kinds)
	}
	if kinds["Impl"] == "" {
		t.Fatalf("Swift struct missing: %#v", kinds)
	}
}

func TestRustPathCallResolvesToMethod(t *testing.T) {
	// `Type::name()` path calls and associated functions are written explicitly
	// in Rust and must resolve to the method by name (ported from
	// fix/rust-call-resolution ad6ca4d): methods may be name-call targets for
	// Rust, unlike Go/Python/JS/TS where a method call always has a receiver.
	repo := t.TempDir()
	writeFile(t, repo, "src/lib.rs", `pub struct Store {
    n: u32,
}

impl Store {
    pub fn open_default() -> Store {
        Store { n: 0 }
    }
}

pub fn boot() -> u32 {
    let s = Store::open_default();
    s.n
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, "boot") && strings.Contains(r.ToID, "Store.open_default") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Rust Type::method path call did not resolve to the method")
	}
}

func TestRustReceiverFactoryAssignmentTypesLocal(t *testing.T) {
	// Bun's bundler code often types a local through a receiver method
	// (`let c = ctx.c();`) before dispatching on that local. The receiver
	// method's declared return type should type the local for the next call.
	repo := t.TempDir()
	writeFile(t, repo, "src/lib.rs", `pub struct LinkerContext;
pub struct GenerateChunkCtx;

impl GenerateChunkCtx {
    pub fn c(&self) -> &mut LinkerContext {
        todo!()
    }
}

impl LinkerContext {
    pub fn break_output_into_pieces(&self) {}
}

pub fn post_process(ctx: GenerateChunkCtx) {
    let c = ctx.c();
    c.break_output_into_pieces();
}

pub fn post_process_typed(ctx: GenerateChunkCtx) {
    let c: &mut LinkerContext = ctx.c();
    c.break_output_into_pieces();
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.ToID, "LinkerContext.break_output_into_pieces") {
			if strings.Contains(r.FromID, "post_process_typed") {
				found["post_process_typed"] = true
			} else if strings.Contains(r.FromID, "post_process") {
				found["post_process"] = true
			}
		}
	}
	for _, name := range []string{"post_process", "post_process_typed"} {
		if !found[name] {
			t.Fatalf("missing Rust receiver-factory CALLS edge from %s to break_output_into_pieces; found=%v", name, found)
		}
	}
}

func TestRustTokioStyleBlockingSpawnEdges(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Cargo.toml", `[package]
name = "tokio-shape"
version = "0.1.0"
`)
	writeFile(t, repo, "src/lib.rs", `pub mod runtime;
pub mod task;
pub mod util;
`)
	writeFile(t, repo, "src/runtime/mod.rs", `pub mod blocking;
pub mod task;

pub struct Handle {
    pub inner: scheduler::Handle,
}

pub mod scheduler {
    pub enum Handle {}
}
`)
	writeFile(t, repo, "src/runtime/blocking/mod.rs", `pub mod pool;
`)
	writeFile(t, repo, "src/runtime/blocking/pool.rs", `use crate::runtime::task::Id;
use crate::util::trace::blocking_task;

pub struct Spawner;
pub struct JoinHandle;
pub struct BlockingTask<F>(F);

impl<F> BlockingTask<F> {
    pub fn new(func: F) -> Self {
        BlockingTask(func)
    }
}

impl Spawner {
    pub fn spawn_blocking_inner<F>(&self, func: F) -> JoinHandle {
        let id = Id::next();
        let _fut = blocking_task::<F, BlockingTask<F>>(BlockingTask::new(func), id.as_u64());
        JoinHandle
    }
}
`)
	writeFile(t, repo, "src/runtime/task.rs", `pub struct Id(u64);

impl Id {
    pub fn next() -> Id {
        Id(1)
    }

    pub fn as_u64(&self) -> u64 {
        self.0
    }
}
`)
	writeFile(t, repo, "src/task/mod.rs", `pub mod builder;
`)
	writeFile(t, repo, "src/task/builder.rs", `use crate::runtime::Handle;

pub struct Builder;

impl Builder {
    pub fn spawn_blocking_on(&self, handle: &Handle) {
        handle.inner.blocking_spawner().spawn_blocking_inner(|| {});
    }
}
`)
	writeFile(t, repo, "src/util/mod.rs", `pub mod trace;
`)
	writeFile(t, repo, "src/util/trace.rs", `pub fn blocking_task<Fn, Fut>(task: Fut, id: u64) -> Fut {
    task
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}
	for _, want := range []string{
		"Builder.spawn_blocking_on->Spawner.spawn_blocking_inner",
		"Spawner.spawn_blocking_inner->blocking_task",
		"Spawner.spawn_blocking_inner->Id.as_u64",
	} {
		if _, ok := calls[want]; !ok {
			t.Fatalf("missing Rust Tokio-style CALLS edge %s in %#v", want, calls)
		}
	}
	if got := calls["Builder.spawn_blocking_on->Spawner.spawn_blocking_inner"].Reason; got != "Rust returned receiver call matched globally unique method name" {
		t.Fatalf("spawn_blocking_inner resolved for the wrong reason %q", got)
	}
}

func TestRustUseBoundModulePathCallResolvesThroughAlias(t *testing.T) {
	// `use crate::strings; strings::index_of(...)` should resolve through the
	// public alias `pub use crate::string::immutable as strings`, not fall back
	// to a bare unique-name guess.
	repo := t.TempDir()
	writeFile(t, repo, "Cargo.toml", `[package]
name = "acme-core"
version = "0.1.0"
`)
	writeFile(t, repo, "src/lib.rs", `pub mod string;
pub use crate::string::immutable as strings;

use crate::strings;

pub fn caller() {
    strings::index_of();
}
`)
	writeFile(t, repo, "src/string/mod.rs", `pub mod immutable;
`)
	writeFile(t, repo, "src/string/immutable.rs", `pub fn index_of() {}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, "caller") && strings.Contains(r.ToID, "index_of") && strings.Contains(r.ToID, "src/string/immutable.rs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Rust use-bound module path call did not resolve through alias")
	}
}

func TestRustNestedCrateLibPathAliasResolves(t *testing.T) {
	// Bun's `bun_core` crate lives under `src/bun_core` and declares
	// `[lib] path = "lib.rs"`, so the crate root is not `src/bun_core/src`.
	// The manifest resolver must still index `pub use ... as strings`.
	repo := t.TempDir()
	writeFile(t, repo, "Cargo.toml", `[workspace]
members = ["src/bun_core", "src/bundler"]
`)
	writeFile(t, repo, "src/bun_core/Cargo.toml", `[package]
name = "bun_core"
version = "0.1.0"

[lib]
path = "lib.rs"
`)
	writeFile(t, repo, "src/bun_core/lib.rs", `pub mod string;
pub use crate::string::immutable as strings;
`)
	writeFile(t, repo, "src/bun_core/string/mod.rs", `pub mod immutable;
`)
	writeFile(t, repo, "src/bun_core/string/immutable.rs", `pub fn index_of() {}
`)
	writeFile(t, repo, "src/bundler/Cargo.toml", `[package]
name = "bundler"
version = "0.1.0"
`)
	writeFile(t, repo, "src/bundler/src/lib.rs", `use bun_core::strings;

pub fn caller() {
    strings::index_of();
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, "caller") && strings.Contains(r.ToID, "src/bun_core/string/immutable.rs:function:index_of") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Rust nested crate [lib] path alias call did not resolve")
	}
}

func TestElixirBareSameModuleCallsResolveToMethods(t *testing.T) {
	// Elixir functions inside a module are emitted as method symbols. Bare
	// same-module calls (`terminate_children(...)`, `monitor_children(...)`)
	// must therefore be allowed to resolve to methods.
	repo := t.TempDir()
	writeFile(t, repo, "lib/dynamic_supervisor.ex", `defmodule DynamicSupervisor do
  def handle_call({:terminate_child, pid}, _from, %{children: children} = state) do
    :ok = terminate_children(%{pid => :child}, state)
    {:reply, :ok, state}
  end

  def terminate(_, %{children: children} = state) do
    :ok = terminate_children(children, state)
  end

  defp terminate_children(children, state) do
    pids = monitor_children(children)
    wait_children(pids, state)
    report_error(:shutdown_error, state)
    :ok
  end

  defp monitor_children(children), do: children
  defp wait_children(pids, state), do: {pids, state}
  defp report_error(error, state), do: {error, state}
end
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, fromOK := symbolsByID[r.FromID]
		to, toOK := symbolsByID[r.ToID]
		if !fromOK || !toOK || from.Language != "Elixir" || to.Language != "Elixir" {
			continue
		}
		calls[edge{from.Name, to.Name}] = true
	}
	for _, want := range []edge{
		{"handle_call", "terminate_children"},
		{"terminate", "terminate_children"},
		{"terminate_children", "monitor_children"},
		{"terminate_children", "wait_children"},
		{"terminate_children", "report_error"},
	} {
		if !calls[want] {
			t.Fatalf("missing Elixir CALLS edge %v in %v", want, calls)
		}
	}
}

func TestCSharpNullableAndOutVarReceiversResolveCrossFile(t *testing.T) {
	// dotnet/runtime HttpConnectionPoolManager.SendAsyncCore: a local
	// declared with a nullable annotation ('HttpConnectionPool? pool;'),
	// assigned via 'out pool' / 'pool = new HttpConnectionPool(...)', then
	// invoked ('pool.SendAsync(...)') must resolve to the method declared
	// in another file, and the constructor call must emit CONSTRUCTS.
	repo := t.TempDir()
	writeFile(t, repo, "Net/HttpConnectionPool.cs", `namespace System.Net.Http
{
    internal sealed class HttpConnectionPool
    {
        public HttpConnectionPool(object owner) { }

        public string SendAsync(string request, bool async) { return request; }
    }
}
`)
	writeFile(t, repo, "Net/HttpConnectionPoolManager.cs", `namespace System.Net.Http
{
    internal sealed class HttpConnectionPoolManager
    {
        public string SendAsyncCore(string request, bool async)
        {
            HttpConnectionPool? pool;
            while (!_pools.TryGetValue(request, out pool))
            {
                pool = new HttpConnectionPool(this);
            }
            return pool.SendAsync(request, async);
        }

        public string SendViaOutDeclaration(string request)
        {
            if (_pools.TryGetValue(request, out HttpConnectionPool? cached))
            {
                return cached.SendAsync(request, false);
            }
            return request;
        }
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "HttpConnectionPoolManager.SendAsyncCore", "HttpConnectionPool.SendAsync") {
		t.Errorf("missing CALLS SendAsyncCore->HttpConnectionPool.SendAsync (nullable local + new assignment)")
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CONSTRUCTS", "HttpConnectionPoolManager.SendAsyncCore", "HttpConnectionPool") {
		t.Errorf("missing CONSTRUCTS SendAsyncCore->HttpConnectionPool")
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "HttpConnectionPoolManager.SendViaOutDeclaration", "HttpConnectionPool.SendAsync") {
		t.Errorf("missing CALLS SendViaOutDeclaration->HttpConnectionPool.SendAsync (out Type? declaration)")
	}
}

func TestCSharpStaticCallResolvesAcrossPartialClassFiles(t *testing.T) {
	// roslyn declares 'internal static partial class Contract' in both
	// Contract.cs (which holds ThrowIfFalse) and
	// Contract.InterpolatedStringHandlers.cs (which does not, and sorts
	// lexically first). A static type-qualified call
	// 'Contract.ThrowIfFalse(...)' must resolve to the partial
	// declaration that actually defines the method instead of being
	// dropped after probing only the first candidate.
	repo := t.TempDir()
	writeFile(t, repo, "Contracts/Contract.InterpolatedStringHandlers.cs", `namespace Roslyn.Utilities
{
    internal static partial class Contract
    {
        public readonly struct ThrowIfFalseInterpolatedStringHandler
        {
            public string GetFormattedText() { return ""; }
        }
    }
}
`)
	writeFile(t, repo, "Contracts/Contract.cs", `namespace Roslyn.Utilities
{
    internal static partial class Contract
    {
        public static void ThrowIfFalse(bool condition) { }

        public static void ThrowIfFalse(bool condition, string message) { }
    }
}
`)
	writeFile(t, repo, "Workspace/Workspace_Editor.cs", `namespace Microsoft.CodeAnalysis
{
    public partial class Workspace
    {
        private void UpdateCurrentContextMapping_NoLock(bool isCurrentContext)
        {
            Contract.ThrowIfFalse(isCurrentContext);
        }
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" &&
			strings.Contains(relation.FromID, "Workspace.UpdateCurrentContextMapping_NoLock") &&
			strings.Contains(relation.ToID, "Contracts/Contract.cs") &&
			strings.Contains(relation.ToID, "Contract.ThrowIfFalse") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing static type-qualified CALLS to the partial class that defines ThrowIfFalse: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func relationsOfType(relations []RelationRecord, relationType string) []RelationRecord {
	var out []RelationRecord
	for _, relation := range relations {
		if relation.Type == relationType {
			out = append(out, relation)
		}
	}
	return out
}

func TestPHPClosureBodyCallsResolveToEnclosingClassViaReceiverName(t *testing.T) {
	// laravel/framework Container.getClosure returns a closure whose body
	// calls $container->build(...) and $container->resolve(...). The calls
	// must be attributed to the enclosing named method, and the untyped
	// $container receiver (named after the enclosing class) must resolve
	// to the Container class's own methods.
	repo := t.TempDir()
	writeFile(t, repo, "src/Container.php", `<?php

namespace Illuminate\Container;

class Container
{
    protected function getClosure($abstract, $concrete)
    {
        return function ($container, $parameters = []) use ($abstract, $concrete) {
            if ($abstract == $concrete) {
                return $container->build($concrete);
            }

            return $container->resolve(
                $concrete, $parameters, raiseEvents: false
            );
        };
    }

    public function build($concrete)
    {
        return $concrete;
    }

    public function resolve($abstract, $parameters = [], $raiseEvents = true)
    {
        return $abstract;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Container.getClosure", "Container.resolve") {
		t.Errorf("missing CALLS Container.getClosure->Container.resolve (closure body, receiver named after enclosing class)")
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Container.getClosure", "Container.build") {
		t.Errorf("missing CALLS Container.getClosure->Container.build (closure body, receiver named after enclosing class)")
	}
}

func TestPHPAmbiguousBareCallEmitsCandidateEdges(t *testing.T) {
	// WordPress declares apply_filters() three times (plugin.php canonical,
	// a compat copy, and a wp-admin noop stub). The globally-unique gate
	// dropped the call entirely; ambiguity must not mean silence — emit
	// candidate edges to the same-name declarations instead.
	repo := t.TempDir()
	writeFile(t, repo, "src/wp-includes/plugin.php", `<?php

function apply_filters( $hook_name, $value ) {
    global $wp_filter;
    if ( ! isset( $wp_filter[ $hook_name ] ) ) {
        return $value;
    }
    return $wp_filter[ $hook_name ]->apply_filters( $value, func_get_args() );
}
`)
	writeFile(t, repo, "src/wp-admin/includes/noop.php", `<?php

function apply_filters( $hook_name, $value ) {
    return $value;
}
`)
	writeFile(t, repo, "src/wp-includes/rest-api/class-wp-rest-server.php", `<?php

class WP_REST_Server
{
    public function dispatch( $request ) {
        $result = apply_filters( 'rest_pre_dispatch', null, $this, $request );
        return $result;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var toPlugin, toNoop bool
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" || !strings.Contains(relation.FromID, "WP_REST_Server.dispatch") {
			continue
		}
		if strings.Contains(relation.ToID, "wp-includes/plugin.php") && strings.Contains(relation.ToID, "apply_filters") {
			toPlugin = true
		}
		if strings.Contains(relation.ToID, "noop.php") && strings.Contains(relation.ToID, "apply_filters") {
			toNoop = true
		}
	}
	if !toPlugin {
		t.Errorf("missing candidate CALLS edge to the canonical apply_filters in plugin.php: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !toNoop {
		t.Errorf("missing candidate CALLS edge to the noop.php apply_filters (ambiguous calls emit all candidates): %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestPHPChainedCallResolvesViaDocblockReturnType(t *testing.T) {
	// WordPress rest_do_request() calls rest_get_server()->dispatch($request).
	// rest_get_server() has no native return hint, only a docblock
	// '@return WP_REST_Server'; the chained call must resolve to
	// WP_REST_Server::dispatch via that inferred receiver type.
	repo := t.TempDir()
	writeFile(t, repo, "src/wp-includes/rest-api.php", `<?php

/**
 * Do a REST request.
 *
 * @param WP_REST_Request $request Request.
 * @return WP_REST_Response The response.
 */
function rest_do_request( $request ) {
    return rest_get_server()->dispatch( $request );
}

/**
 * Retrieves the current REST server instance.
 *
 * @return WP_REST_Server REST server instance.
 */
function rest_get_server() {
    global $wp_rest_server;
    return $wp_rest_server;
}
`)
	writeFile(t, repo, "src/wp-includes/rest-api/class-wp-rest-server.php", `<?php

class WP_REST_Server
{
    public function dispatch( $request ) {
        return $request;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "rest_do_request", "WP_REST_Server.dispatch") {
		t.Errorf("missing CALLS rest_do_request->WP_REST_Server.dispatch via docblock @return receiver type: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestPHPGeneratedFactoryCreateChainResolvesProductMethod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Session.php", `<?php

class Session
{
    /**
     * @var CustomerFactory
     */
    protected $_customerFactory;

    public function setCustomerDataAsLoggedIn($customer)
    {
        $customerModel = $this->_customerFactory->create()->updateData($customer);
        return $customerModel;
    }
}
`)
	writeFile(t, repo, "src/Customer.php", `<?php

class Customer
{
    public function updateData($customer)
    {
        return $this;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Session.setCustomerDataAsLoggedIn", "Customer.updateData") {
		t.Errorf("missing generated-factory chain CALLS edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestObjectiveCMessageSendCallsResolveToMethods(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Indicator.m", `@interface Indicator
- (void)setEnabled;
- (void)setCurrentState:(int)state;
- (void)cancelActivationDelayTimer;
@end

@implementation Indicator
- (void)setEnabled {
    [self setCurrentState:1];
}

- (void)setCurrentState:(int)state {
    [self cancelActivationDelayTimer];
}

- (void)cancelActivationDelayTimer {}
@end
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "setEnabled", "setCurrentState") {
		t.Errorf("missing Objective-C message CALLS setEnabled->setCurrentState: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "setCurrentState", "cancelActivationDelayTimer") {
		t.Errorf("missing Objective-C message CALLS setCurrentState->cancelActivationDelayTimer: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestObjectiveCMethodFallbackExtractsColonSelectors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "AFURLSessionManager.m", `@implementation AFURLSessionManager
- (void)invalidateSessionCancelingTasks:(BOOL)cancelPendingTasks resetSession:(BOOL)resetSession {
    if (cancelPendingTasks) {
        [self.session invalidateAndCancel];
    } else {
        [self.session finishTasksAndInvalidate];
    }
    if (resetSession) {
        self.session = nil;
    }
}
@end
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "invalidateSessionCancelingTasks") {
		t.Fatalf("missing Objective-C colon selector method: %#v", snapshot.Symbols)
	}
}

func TestClojureListHeadCallsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "core.clj", `(ns app.core
  (:require [app.urls :as urls]))

(defn dashcard-url [id] id)
(defn make-title-if-needed [] "title")
(defn render-pulse-card []
  (make-title-if-needed)
  (urls/dashcard-url 1))
(defn caller []
  (render-pulse-card))
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "caller", "render-pulse-card") {
		t.Errorf("missing Clojure CALLS caller->render-pulse-card: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "render-pulse-card", "make-title-if-needed") {
		t.Errorf("missing Clojure CALLS render-pulse-card->make-title-if-needed: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "render-pulse-card", "dashcard-url") {
		t.Errorf("missing Clojure namespaced CALLS render-pulse-card->dashcard-url: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestLuaTableFunctionDefinitionsAndDottedCallsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "vim.lua", `_G.vim = _G.vim or {}
local M = {}

function vim.gsplit(s, sep)
  return s
end

function vim.split(s, sep)
  return vim.gsplit(s, sep)
end

function M.joinpath(...)
  return table.concat({ ... }, "/")
end

function M.normalize(path)
  return M.joinpath(path, "child")
end
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vim.gsplit", "vim.split", "M.joinpath", "M.normalize"} {
		if !snapshotHasSymbol(snapshot, want) {
			t.Fatalf("missing Lua table function %s: %#v", want, snapshot.Symbols)
		}
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "split", "gsplit") {
		t.Errorf("missing Lua dotted CALLS split->gsplit: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "normalize", "joinpath") {
		t.Errorf("missing Lua module CALLS normalize->joinpath: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNamespaceImportsAndCallbackReferencesResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/compiler/core.ts", `export function noop() {}
`)
	writeFile(t, repo, "src/compiler/performance.ts", `export function mark(name: string) {}
export function measure(name: string, start: string, end: string) {}
`)
	writeFile(t, repo, "src/compiler/generated/ts.ts", `export { noop } from "../core";
export { createSourceFile } from "../parser";
`)
	writeFile(t, repo, "src/compiler/generated/ts.performance.ts", `export * from "../performance";
`)
	writeFile(t, repo, "src/compiler/parser.ts", `import { noop } from "./generated/ts.js";
import * as performance from "./generated/ts.performance.js";

export function createSourceFile() {
    performance.mark("beforeParse");
    Parser.parseSourceFile(noop);
    performance.measure("Parse", "beforeParse", "afterParse");
}

namespace Parser {
    export function parseSourceFile(done: () => void) {
        done();
    }

    function createSourceFile() {
        return undefined;
    }
}
`)
	writeFile(t, repo, "src/compiler/program.ts", `import { createSourceFile } from "./generated/ts.js";

export function createGetSourceFile() {
    return createSourceFile();
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
		IncludeFiles: []string{
			"src/compiler/core.ts",
			"src/compiler/parser.ts",
			"src/compiler/performance.ts",
			"src/compiler/program.ts",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "createGetSourceFile", "src/compiler/program.ts", "createSourceFile", "src/compiler/parser.ts") {
		t.Errorf("missing TypeScript generated namespace import CALLS createGetSourceFile->createSourceFile: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	for _, want := range []struct {
		name string
		file string
	}{
		{name: "parseSourceFile", file: "src/compiler/parser.ts"},
		{name: "mark", file: "src/compiler/performance.ts"},
		{name: "measure", file: "src/compiler/performance.ts"},
		{name: "noop", file: "src/compiler/core.ts"},
	} {
		if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "createSourceFile", "src/compiler/parser.ts", want.name, want.file) {
			t.Errorf("missing TypeScript createSourceFile->%s CALLS: %#v", want.name, relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

func TestSQLRoutineCallsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "policy.sql", `CREATE FUNCTION compress_chunk() RETURNS void LANGUAGE SQL AS $$
SELECT 1;
$$;

CREATE FUNCTION policy_compression_execute() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  PERFORM _timescaledb_internal.compress_chunk();
  PERFORM @extschema@.compress_chunk();
END
$$;

CREATE FUNCTION policy_compression() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  CALL policy_compression_execute();
END
$$;
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "policy_compression_execute", "compress_chunk") {
		t.Errorf("missing SQL schema-qualified CALLS policy_compression_execute->compress_chunk: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "policy_compression", "policy_compression_execute") {
		t.Errorf("missing SQL CALLS policy_compression->policy_compression_execute: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestSQLMigrationScriptCallsAlsoLinkCanonicalRoutine(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "sql/policy_internal.sql", `CREATE PROCEDURE _timescaledb_functions.policy_compression_execute(job_id INTEGER)
AS $$
BEGIN
  PERFORM @extschema@.compress_chunk();
END
$$ LANGUAGE PLPGSQL;

CREATE PROCEDURE _timescaledb_functions.policy_compression(job_id INTEGER)
AS $$
BEGIN
  CALL _timescaledb_functions.policy_compression_execute(job_id);
END
$$ LANGUAGE PLPGSQL;
`)
	writeFile(t, repo, "db/migrations/V2__policy_compression.sql", `CREATE PROCEDURE _timescaledb_functions.policy_compression_execute(job_id INTEGER)
AS $$
BEGIN
  PERFORM @extschema@.compress_chunk();
END
$$ LANGUAGE PLPGSQL;

CREATE PROCEDURE _timescaledb_functions.policy_compression(job_id INTEGER)
AS $$
BEGIN
  CALL _timescaledb_functions.policy_compression_execute(job_id);
END
$$ LANGUAGE PLPGSQL;
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "policy_compression", "db/migrations/V2__policy_compression.sql", "policy_compression_execute", "db/migrations/V2__policy_compression.sql") {
		t.Errorf("missing SQL migration local CALLS policy_compression->policy_compression_execute: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "policy_compression", "db/migrations/V2__policy_compression.sql", "policy_compression_execute", "sql/policy_internal.sql") {
		t.Errorf("missing SQL migration canonical CALLS policy_compression->canonical policy_compression_execute: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestZigBareAndReceiverCallsResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "analysis.zig", `const analysis = struct {};

const Type = struct {
    pub fn lookupSymbol(self: *Type) void {
        _ = self;
    }
};

fn resolveTypeOfNode() void {}

fn resolveVarDeclAlias(t: Type) void {
    analysis.resolveTypeOfNode();
    t.lookupSymbol();
}

fn definitionToken() void {
    analysis.resolveVarDeclAlias(Type{});
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "definitionToken", "resolveVarDeclAlias") {
		t.Errorf("missing Zig CALLS definitionToken->resolveVarDeclAlias: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "resolveVarDeclAlias", "resolveTypeOfNode") {
		t.Errorf("missing Zig CALLS resolveVarDeclAlias->resolveTypeOfNode: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "resolveVarDeclAlias", "lookupSymbol") {
		t.Errorf("missing Zig receiver CALLS resolveVarDeclAlias->lookupSymbol: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestGoSelfModuleImportResolvesCrossPackageCall(t *testing.T) {
	// entire-sem's own snapshot missed cli->sem edges: a package imported
	// via the repo's own module path ("github.com/acme/tool/internal/sem")
	// fell through to an external fallback because the module prefix was
	// never mapped back to the in-repo package directory.
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", `module github.com/acme/tool

go 1.24
`)
	writeFile(t, repo, "internal/sem/analyze.go", `package sem

func analyzeNothing() {}
`)
	writeFile(t, repo, "internal/sem/provider.go", `package sem

func StreamSnapshot(repo string) error {
	return nil
}
`)
	writeFile(t, repo, "internal/cli/root.go", `package cli

import (
	"github.com/acme/tool/internal/sem"
)

func runProviderRecords(repo string) error {
	return sem.StreamSnapshot(repo)
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "runProviderRecords", "StreamSnapshot") {
		t.Errorf("missing CALLS runProviderRecords->StreamSnapshot via self-module import: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A module-qualified dotted call (`acme.mod.run()`) must resolve to the named
// submodule ONLY. A sibling submodule (acme/service.py) that defines the same
// terminal name must NOT receive an edge via any parent-directory fallback: the
// dedicated dotted path uses a strict module-file matcher (the module's own
// source file or that package's __init__), so exactly one edge — to acme/mod.py
// — is emitted.
func TestPythonDottedSubmoduleCallDoesNotResolveToSibling(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme/__init__.py", "")
	writeFile(t, repo, "acme/mod.py", `def run():
    return 1
`)
	writeFile(t, repo, "acme/service.py", `def run():
    return 2
`)
	writeFile(t, repo, "consumer.py", `import acme.mod


def call_it():
    return acme.mod.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var runTargets []string
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "call_it" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "run" {
			runTargets = append(runTargets, to.FilePath)
		}
	}
	if len(runTargets) != 1 || runTargets[0] != "acme/mod.py" {
		t.Fatalf("call_it run targets = %#v, want exactly [acme/mod.py]", runTargets)
	}
}

// callItTargetsNamed collects the file paths of every CALLS target of `call_it`
// whose terminal name matches `name`, for the from-import composition tests.
func callItTargetsNamed(t *testing.T, snapshot ProviderSnapshot, name string) []string {
	t.Helper()
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var targets []string
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "call_it" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == name {
			targets = append(targets, to.FilePath)
		}
	}
	return targets
}

// `from pkg import service` records importsByName["service"]=["pkg"] (the
// submodule name is dropped), identical to `import pkg as service`. When the
// repo actually contains the submodule pkg/service/, the from-import reading is
// authoritative: `service.helper.fn()` must resolve to pkg/service/helper.py,
// never to the sibling pkg/helper.py that the alias-rename reading would name.
func TestPythonFromImportSubmoduleDottedCallResolvesToSubmodule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/__init__.py", "")
	writeFile(t, repo, "pkg/helper.py", `def fn():
    return 1
`)
	writeFile(t, repo, "pkg/service/__init__.py", "")
	writeFile(t, repo, "pkg/service/helper.py", `def fn():
    return 2
`)
	writeFile(t, repo, "consumer.py", `from pkg import service


def call_it():
    return service.helper.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := callItTargetsNamed(t, snapshot, "fn")
	if len(targets) != 1 || targets[0] != "pkg/service/helper.py" {
		t.Fatalf("call_it fn targets = %#v, want exactly [pkg/service/helper.py]", targets)
	}
}

// `import pkg as service` is an alias rename: `service.helper.fn()` means
// pkg.helper.fn. With only pkg/helper.py present, the edge resolves there.
func TestPythonAliasRenameDottedCallResolvesToRenamedModule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/__init__.py", "")
	writeFile(t, repo, "pkg/helper.py", `def fn():
    return 1
`)
	writeFile(t, repo, "consumer.py", `import pkg as service


def call_it():
    return service.helper.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := callItTargetsNamed(t, snapshot, "fn")
	if len(targets) != 1 || targets[0] != "pkg/helper.py" {
		t.Fatalf("call_it fn targets = %#v, want exactly [pkg/helper.py]", targets)
	}
}

// The genuinely ambiguous case: `import pkg as service` where BOTH pkg/helper.py
// and pkg/service/helper.py exist, but there is NO submodule marker for
// pkg.service (no pkg/service/__init__.py, no pkg/service.py). The from-import
// reading has no repo grounding, so the alias-rename reading wins and the edge
// resolves to pkg/helper.py — never the pkg/service/helper.py that a bare
// both-candidates composition would also (wrongly) match.
func TestPythonAliasRenameWinsWithoutSubmoduleMarker(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/__init__.py", "")
	writeFile(t, repo, "pkg/helper.py", `def fn():
    return 1
`)
	// A pkg/service/ directory with a same-named submodule file but NO __init__:
	// not a package the discriminator recognizes.
	writeFile(t, repo, "pkg/service/helper.py", `def fn():
    return 2
`)
	writeFile(t, repo, "consumer.py", `import pkg as service


def call_it():
    return service.helper.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	targets := callItTargetsNamed(t, snapshot, "fn")
	if len(targets) != 1 || targets[0] != "pkg/helper.py" {
		t.Fatalf("call_it fn targets = %#v, want exactly [pkg/helper.py]", targets)
	}
}

// A Python dotted call (`acme.mod.run()`) must resolve only to a Python symbol.
// symbolsByShortName is a workspace-wide, cross-language index and the strict
// module matcher trims the file extension, so a same-stem file in another
// language (acme/mod.go with `func run`) also matches the module path; without a
// language gate the dotted call would fabricate a cross-language edge to the Go
// `run`. The gate keeps resolution on the Python acme/mod.py target.
func TestPythonDottedCallDoesNotResolveCrossLanguage(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme/__init__.py", "")
	writeFile(t, repo, "acme/mod.py", `def run():
    return "py"
`)
	writeFile(t, repo, "acme/mod.go", `package acme

func run() string {
	return "go"
}
`)
	writeFile(t, repo, "consumer.py", `import acme.mod


def call_it():
    return acme.mod.run()
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "call_it", "consumer.py", "run", "acme/mod.go") {
		t.Fatalf("dotted Python call fabricated cross-language edge to Go run: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "call_it", "consumer.py", "run", "acme/mod.py") {
		t.Fatalf("missing dotted Python call to acme/mod.py run: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// In an ordinary repo (no vendored CPython stdlib) os.path.* calls must emit
// only the os.path external edge — never a bare os.<name> edge, nor a
// genericpath/posixpath/ntpath fan-out edge.
func TestPythonOSPathDottedCallsEmitOnlyOSPathExternals(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `import os


def use(x):
    if os.path.isdir(x):
        return os.path.join(x, "y")
    return x
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "use" {
			continue
		}
		if strings.HasPrefix(r.ToID, "external:symbol:") {
			got[strings.TrimPrefix(r.ToID, "external:symbol:")] = true
		}
	}
	want := map[string]bool{"os.path.isdir": true, "os.path.join": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("os.path external CALLS edges = %#v, want %#v", got, want)
	}
}

// A bare imported call (`from mymod import run; run(y)`) must keep its external
// edge even when a dotted call in the same block (`acme.mod.run()`) shares the
// terminal name and resolves locally. The name-keyed dotted merge must not
// steal the bare call's resolution.
func TestPythonDottedCallCollisionKeepsBareExternalEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme/__init__.py", "")
	writeFile(t, repo, "acme/mod.py", `def run():
    return 1
`)
	// A second run() elsewhere makes the name non-globally-unique, so the bare
	// call relies on the external fallback rather than a unique-name match.
	writeFile(t, repo, "other/thing.py", `def run():
    return 2
`)
	writeFile(t, repo, "app.py", `import acme.mod
from mymod import run


def f(x, y):
    acme.mod.run()
    return run(y)
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "f" && r.ToID == externalID("symbol", "mymod.run") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing external CALLS f -> mymod.run: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A submodule-qualified dotted call (`acme_pkg.service.run()`) targets the
// submodule only. If the package __init__.py also defines the terminal name it
// must not receive an edge via the parent-directory fallback.
func TestPythonDottedSubmoduleCallDoesNotResolveToPackageInit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme_pkg/__init__.py", `def run():
    return "init"
`)
	writeFile(t, repo, "acme_pkg/service.py", `def run():
    return "service"
`)
	writeFile(t, repo, "consumer.py", `import acme_pkg.service


def call_service():
    return acme_pkg.service.run()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var runTargets []string
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "call_service" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "run" {
			runTargets = append(runTargets, to.FilePath)
		}
	}
	if len(runTargets) != 1 || runTargets[0] != "acme_pkg/service.py" {
		t.Fatalf("call_service run targets = %#v, want exactly [acme_pkg/service.py]", runTargets)
	}
}

// A module-qualified dotted call (`acme.mod.helper()`) resolves through the
// imported module only. A same-file function of the same name must NOT capture
// it: the dedicated dotted-call path never consults same-file symbols unless
// the file matches the imported module, so the module target wins.
func TestPythonDottedImportedCallPrefersModuleOverSameFileDef(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme/__init__.py", "")
	writeFile(t, repo, "acme/mod.py", `def helper():
    return "remote"
`)
	writeFile(t, repo, "app.py", `import acme.mod


def helper():
    return "local"


def caller():
    return acme.mod.helper()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "caller", "app.py", "helper", "acme/mod.py") {
		t.Fatalf("dotted call must resolve to acme/mod.py:helper: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "caller", "app.py", "helper", "app.py") {
		t.Fatalf("dotted call wrongly captured by same-file app.py:helper: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A submodule-qualified dotted call (`acme.service.zonk()`) whose terminal name
// is NOT defined in the imported submodule must not fabricate an edge to a
// globally unique same-named symbol elsewhere in the workspace (here the
// package __init__.py). The dedicated path never reaches the globally-unique
// fallback; with no module-scoped local target it emits only the external edge.
func TestPythonDottedCallDoesNotFabricateGloballyUniqueEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "acme/__init__.py", `def zonk():
    return "init"
`)
	writeFile(t, repo, "acme/service.py", `def other():
    return 1
`)
	writeFile(t, repo, "consumer.py", `import acme.service


def call_it():
    return acme.service.zonk()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "call_it" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "zonk" {
			t.Fatalf("dotted call fabricated wrong edge call_it -> zonk @ %s (service.py has no zonk)", to.FilePath)
		}
	}
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "call_it" && r.ToID == externalID("symbol", "acme.service.zonk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing external CALLS call_it -> acme.service.zonk: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A chained R assignment `a <- b <- function() ...` binds BOTH names at the
// same scope: a and b are peers, not nested. Emitting the outer name must not
// walk the inner assignment target as if it lived inside a function body — that
// mis-marked the inner binding Local and hid it from cross-file resolution,
// rerouting its callers to an unrelated same-named decoy.
func TestRChainedAssignmentInnerBindingTopLevel(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "R/a.R", `helper <- validate <- function(x) x + 1

g <- function() {
  validate(10)
}
`)
	// A same-named decoy in another file: if `validate@a.R` is wrongly marked
	// Local, g's call reroutes here (name_only) instead of resolving in-file.
	writeFile(t, repo, "R/b.R", `validate <- function(x) x * 999
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}

	symByNameFile := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		if s.Language == "R" {
			symByNameFile[s.Name+"@"+s.FilePath] = s
		}
	}
	for _, key := range []string{"helper@R/a.R", "validate@R/a.R"} {
		s, ok := symByNameFile[key]
		if !ok {
			t.Fatalf("R chained-assignment symbol %s not extracted: %#v", key, symByNameFile)
		}
		if s.Kind != "function" {
			t.Fatalf("R chained-assignment symbol %s should be a function, got %q", key, s.Kind)
		}
		// Both names bind at top level; neither may be Local (which would exclude
		// it from cross-scope/cross-file name-match resolution).
		if s.Local {
			t.Fatalf("R chained-assignment symbol %s wrongly marked Local (hidden from resolution)", key)
		}
	}

	var toSameFile, toDecoy bool
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "g" || lastSegment(r.ToID) != "validate" {
			continue
		}
		switch {
		case strings.Contains(r.ToID, "R/a.R"):
			toSameFile = true
			if r.Resolution != "exact" {
				t.Fatalf("g -> validate@a.R should resolve exact, got %q", r.Resolution)
			}
		case strings.Contains(r.ToID, "R/b.R"):
			toDecoy = true
		}
	}
	if !toSameFile {
		t.Fatalf("expected g -> validate@a.R (exact same-file CALLS edge), got none: %#v", snapshot.Relations)
	}
	if toDecoy {
		t.Fatalf("g must not resolve to the decoy validate@b.R; the in-file binding was wrongly hidden")
	}
}

// A three-level chain `x <- y <- z <- function() ...` must leave every
// plain-identifier target non-Local (all bind at the same top-level scope).
func TestRChainedAssignmentThreeLevelTopLevel(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "R/chain.R", `x <- y <- z <- function() {
  1
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		if s.Language == "R" {
			seen[s.Name] = s
		}
	}
	for _, name := range []string{"x", "y", "z"} {
		s, ok := seen[name]
		if !ok {
			t.Fatalf("three-level chain target %q not extracted: %#v", name, seen)
		}
		if s.Kind != "function" {
			t.Fatalf("three-level chain target %q should be a function, got %q", name, s.Kind)
		}
		if s.Local {
			t.Fatalf("three-level chain target %q wrongly marked Local", name)
		}
	}
}

// A constructor immediately chained into a type-escaping hop
// (`my $x = Session->new->request`) must not type $x as the constructor class:
// $x is really an HTTP::Request (what `->request` returns), so `$x->send` must
// not resolve to Session::send. Because `send` is defined in two packages there
// is no globally-unique fallback either, so the correct outcome is NO send edge.
func TestPerlChainedConstructorDoesNotMistypeReceiver(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Session->new->request;
  $x->send;
}
`)
	writeFile(t, repo, "lib/Session.pm", `package Session;

sub new { return bless {}, shift }

sub request { return HTTP::Request->new }

sub send { return 1 }
`)
	writeFile(t, repo, "lib/HTTP/Request.pm", `package HTTP::Request;

sub new { return bless {}, shift }

sub send { return 2 }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "send" {
			t.Fatalf("chained-constructor mistype fabricated CALLS run -> send @ %s (reason %q)", to.FilePath, r.Reason)
		}
	}
}

// A constructor-typed local that is reassigned through an untracked call
// (`my $x = Foo->new; $x = build_widget(); $x->go`) must not keep the stale
// constructor type: $x really holds a Widget, so `$x->go` must not resolve to
// Foo::go. With go defined in both Foo and Widget there is no globally-unique
// fallback either, so the correct outcome is NO run -> go edge.
func TestPerlReassignedLocalDoesNotResolveViaStaleType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x = build_widget();
  $x->go;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new { return bless {}, shift }

sub go { return 1 }
`)
	writeFile(t, repo, "lib/Widget.pm", `package Widget;

sub new { return bless {}, shift }

sub go { return 2 }

sub build_widget { return Widget->new }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "go" {
			t.Fatalf("stale-type reassignment fabricated CALLS run -> go @ %s (reason %q)", to.FilePath, r.Reason)
		}
	}
}

// A constructor-typed local reassigned through a list assignment
// (`my $x = Foo->new; ($x, $y) = fetch(); $x->greet`) must not keep the stale
// constructor type: $x really holds fetch()'s first return value, not a Foo, so
// `$x->greet` must not resolve to Foo::greet. With greet defined in both Foo and
// Bar there is no globally-unique name-only fallback either, so the correct
// outcome is NO run -> greet edge. This is the exact end-to-end repro that the
// scalar-only reassignment scan missed (list-assignment LHS has no bare `$x =`).
func TestPerlListReassignedLocalDoesNotResolveViaStaleType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  ($x, $y) = fetch();
  return $x->greet;
}

sub fetch { return (1, 2); }
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new { return bless {}, shift }

sub greet { return 1 }
`)
	writeFile(t, repo, "lib/Bar.pm", `package Bar;

sub greet { return 2 }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "greet" {
			t.Fatalf("list-assignment stale-type fabricated CALLS run -> greet @ %s (reason %q)", to.FilePath, r.Reason)
		}
	}
}

// A multi-hop getter chain (`$url->path->to_string`) must not have its terminal
// method attributed to the head receiver's package. perlReceiverCalls flattens
// the chain to {url, to_string}, but to_string is invoked on the Mojo::Path
// returned by $url->path, not on $url, so type-inferred resolution must skip it.
// With to_string defined in both Mojo::URL and Mojo::Path there is no
// globally-unique fallback either, so the correct outcome is NO to_string edge.
// A genuine single-hop `$url->clone` in the same sub (clone also duplicated
// across both packages, so only type inference can resolve it) is the positive
// control that the head-receiver type is still used for real single-hop calls.
func TestPerlMultiHopChainDoesNotAttributeTerminalToHead(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub build {
  my $url = Mojo::URL->new;
  $url->clone;
  return $url->path->to_string;
}
`)
	writeFile(t, repo, "lib/Mojo/URL.pm", `package Mojo::URL;

sub new { return bless {}, shift }

sub path { return Mojo::Path->new }

sub clone { return shift }

sub to_string { return 'url' }
`)
	writeFile(t, repo, "lib/Mojo/Path.pm", `package Mojo::Path;

sub new { return bless {}, shift }

sub clone { return shift }

sub to_string { return 'path' }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	sawClone := false
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "build" {
			continue
		}
		to, ok := symbolsByID[r.ToID]
		if !ok {
			continue
		}
		if to.Name == "to_string" {
			t.Fatalf("multi-hop terminal misattributed: CALLS build -> to_string @ %s (reason %q)", to.FilePath, r.Reason)
		}
		if to.Name == "clone" {
			if to.FilePath != "lib/Mojo/URL.pm" || r.Resolution != "type_inferred" {
				t.Fatalf("single-hop $url->clone resolved wrong: %s (%s)", to.FilePath, r.Resolution)
			}
			sawClone = true
		}
	}
	if !sawClone {
		t.Fatal("genuine single-hop CALLS build -> clone @ lib/Mojo/URL.pm was dropped")
	}
}

// Minimal reproduction of the multi-hop misattribution: `$w->child->name` with
// name defined in both Widget and Container must not fabricate run -> name in
// the head receiver Widget's package (the real target is Container::name).
func TestPerlMultiHopChainMinimalReproNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/Run.pm", `package Run;

sub run {
  my $w = Widget->new;
  $w->child->name;
}
`)
	writeFile(t, repo, "lib/Widget.pm", `package Widget;

sub new { return bless {}, shift }

sub child { return Container->new }

sub name { return 'widget' }
`)
	writeFile(t, repo, "lib/Container.pm", `package Container;

sub new { return bless {}, shift }

sub name { return 'container' }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "name" {
			t.Fatalf("multi-hop terminal misattributed: CALLS run -> name @ %s (reason %q)", to.FilePath, r.Reason)
		}
	}
}

// A bare receiver type (`Foo`) must resolve only to a top-level package file
// (lib/Foo.pm), never to a same-basename file in a deeper namespace
// (lib/Deep/Foo.pm is package Deep::Foo). A `::` type must still match its
// full namespaced path.
func TestPerlSymbolFileMatchesTypeRequiresPathBoundary(t *testing.T) {
	if perlSymbolFileMatchesType("lib/Deep/Foo.pm", "Foo") {
		t.Fatal("bare type Foo must not match namespaced lib/Deep/Foo.pm")
	}
	if !perlSymbolFileMatchesType("lib/Mojo/URL.pm", "Mojo::URL") {
		t.Fatal("type Mojo::URL must match lib/Mojo/URL.pm")
	}
	if !perlSymbolFileMatchesType("lib/Foo.pm", "Foo") {
		t.Fatal("bare type Foo must match top-level lib/Foo.pm")
	}
}

func TestPerlBareReceiverTypeDoesNotMatchNamespacedFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x->render;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new {
  return bless {}, shift;
}
`)
	writeFile(t, repo, "lib/Deep/Foo.pm", `package Deep::Foo;

sub render {
  return 1;
}
`)
	writeFile(t, repo, "lib/Other.pm", `package Other;

sub render {
  return 2;
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "render" {
			t.Fatalf("emitted wrong CALLS run -> render @ %s (Foo has no render sub)", to.FilePath)
		}
	}
}

// A multi-hop getter-navigation chain (`$tx->req->url`) returns a different
// object than the receiver, so its assignment target must NOT inherit the
// receiver's package type. `url` is not a sub of Mojo::Transaction, so the
// package-membership gate rejects the fluent shortcut and $url stays untyped;
// a later `$url->path` therefore emits no edge (rather than a wrong edge to
// Mojo::Transaction::path). `path` is defined in two packages so it cannot
// resolve via the globally-unique fallback either.
func TestPerlGetterNavigationChainDoesNotInheritReceiverType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $tx  = Mojo::Transaction->new;
  my $url = $tx->req->url;
  $url->path;
}
`)
	writeFile(t, repo, "lib/Mojo/Transaction.pm", `package Mojo::Transaction;

sub new {
  return bless {}, shift;
}

sub req {
  return bless {}, 'Mojo::Message::Request';
}

sub path {
  return '/tx';
}
`)
	writeFile(t, repo, "lib/Mojo/URL.pm", `package Mojo::URL;

sub path {
  return '/url';
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || lastSegment(r.FromID) != "run" {
			continue
		}
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "path" {
			t.Fatalf("getter-navigation chain fabricated CALLS run -> path @ %s", to.FilePath)
		}
	}
}
