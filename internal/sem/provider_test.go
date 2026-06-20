package sem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
spec:
  template:
    spec:
      serviceAccountName: api-runner
      volumes:
        - name: credentials
          secret:
            secretName: api-secret
        - name: cache
          persistentVolumeClaim:
            claimName: api-cache
      containers:
        - name: api
          image: example/api:latest
          ports:
            - containerPort: 8080
          env:
            - name: LOG_LEVEL
              value: debug
          envFrom:
            - configMapRef:
                name: api-config
            - secretRef:
                name: api-env
`)
	writeFile(t, repo, "k8s/configmap.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
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
`)
	writeFile(t, repo, "k8s/service-account.yaml", `apiVersion: v1
kind: ServiceAccount
metadata:
  name: api-runner
`)
	writeFile(t, repo, "k8s/pvc.yaml", `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: api-cache
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"external:config:kubernetes/configmap/api-config",
		"external:config:kubernetes/secret/api-secret",
		"external:config:kubernetes/secret/api-env",
		"external:config:kubernetes/serviceaccount/api-runner",
		"external:config:kubernetes/persistentvolumeclaim/api-cache",
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
		"Secret.api-secret",
		"Secret.api-env",
		"ServiceAccount.api-runner",
		"PersistentVolumeClaim.api-cache",
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Deployment.api", target) {
			t.Fatalf("missing exact Kubernetes resource dependency Deployment.api -> %s in %#v", target, snapshot.Relations)
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

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	if !hasRelationByLastSegment(snapshot.Relations, "RESOURCE_DEPENDS_ON", "Service.api", "Deployment.api") {
		t.Fatalf("missing Service.api -> Deployment.api selector dependency in %#v", snapshot.Relations)
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
`)
	writeFile(t, repo, "k8s/service-account.yaml", `apiVersion: v1
kind: ServiceAccount
metadata:
  name: api-runner
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

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{
		{"HorizontalPodAutoscaler.api", "Deployment.api"},
		{"Ingress.api", "Service.api"},
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
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, service := range []string{"compose.service.api", "compose.service.db", "compose.service.redis"} {
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
	_ = raw.Balance
	other.Mystery = 1
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	reads, writes := map[string]bool{}, map[string]bool{}
	for _, r := range snapshot.Relations {
		switch r.Type {
		case "READS_FIELD":
			reads[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = true
		case "WRITES_FIELD":
			writes[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = true
			if r.Confidence < 0.85 {
				t.Fatalf("field write confidence too low: %#v", r)
			}
		}
	}

	// a.Balance read and write inside Deposit resolve via the Go receiver.
	if !reads["Account.Deposit->Account.Balance"] {
		t.Fatalf("missing READS_FIELD Deposit->Balance: %v", reads)
	}
	if !writes["Account.Deposit->Account.Balance"] {
		t.Fatalf("missing WRITES_FIELD Deposit->Balance: %v", writes)
	}
	// raw.Balance (raw is a map, not Account) and other.Mystery (no such field)
	// must not produce edges — the field is not a known member of the receiver.
	for edge := range reads {
		if strings.HasPrefix(edge, "leak->") {
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
	if !hasRelationByLastSegment(snapshot.Relations, "HANDLES_ROUTE", "register", "/health") {
		t.Fatalf("missing HANDLES_ROUTE endpoint relation: %#v", snapshot.Relations)
	}
	var bridge RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && lastSegment(relation.FromID) == "ping" && lastSegment(relation.ToID) == "register" {
			bridge = relation
			break
		}
	}
	if bridge.FromID == "" {
		t.Fatalf("missing route bridge CALLS ping->register: %#v", snapshot.Relations)
	}
	if bridge.Resolution != "pattern" || bridge.TargetKind != "symbol" || bridge.Confidence > 0.72 {
		t.Fatalf("unexpected bridge metadata: %#v", bridge)
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

  String ping(RestTemplate restTemplate) {
    return restTemplate.getForObject("/api/users/{id}", String.class);
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
	if !hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "UserController.ping", "/api/users/{id}") {
		t.Fatalf("missing RestTemplate HTTP_CALLS relation: %#v", snapshot.Relations)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "UserController.ping", "UserController.show") {
		t.Fatalf("missing route bridge CALLS ping->show: %#v", snapshot.Relations)
	}
	if hasRelationByLastSegment(snapshot.Relations, "HTTP_CALLS", "UserController", "/api/users/{id}") {
		t.Fatalf("class body misclassified as HTTP_CALLS caller: %#v", snapshot.Relations)
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
	// other.mystery(): receiver type unknown -> no fabricated edge.
	for key := range inferred {
		if strings.Contains(key, "mystery") {
			t.Fatalf("fabricated edge for unknown receiver: %s", key)
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
			calls = relation
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

func TestCapabilitiesAdvertiseExpandedLanguageSet(t *testing.T) {
	caps := Capabilities()
	if caps.SchemaVersion != SchemaVersion || caps.Provider != ProviderName {
		t.Fatalf("capabilities identity = %#v", caps)
	}
	if len(caps.SupportedLanguages) < 158 {
		t.Fatalf("supported language/filetype count = %d, want at least 158: %#v", len(caps.SupportedLanguages), caps.SupportedLanguages)
	}
	seen := map[string]bool{}
	for _, language := range caps.SupportedLanguages {
		seen[language] = true
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
	writeFile(t, repo, "app/main.dart", "void main() {\n  print('hi');\n}\n")
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
		{"app/main.dart", "Dart", "main"},
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
	for _, language := range []string{"Dart", "Zig", "Bicep", "Dockerfile", "YAML", "Kustomize"} {
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
