package sem

import (
	"reflect"
	"strings"
	"testing"
)

const flowTypedJavaScriptSample = `/**
 * Copyright (c) Meta Platforms, Inc. and affiliates.
 *
 * @flow
 */

import type {Fiber, FiberRoot} from './ReactInternalTypes';
import typeof * as SchedulerType from 'scheduler';
import {REACT_CONTEXT_TYPE} from 'shared/ReactSymbols';

export type Lane = number;
export opaque type SuspenseContext: number = number;
opaque type Digest = ?string;

type Dependency<+T> = {
  +context: T,
  -setter: (T) => void,
  'quoted-key': boolean,
  +[key: string]: mixed,
  ...
};

export type Exact = {| tag: number |};
export type Updater<S, A> = A => void;
export type Handle = interface extends Element {_root?: FiberRoot};
export type Panel = component(
  ref: React.RefSetter<Handle>,
);

declare export function flushSync(void): void;

export function createContext<T>(defaultValue: T): {current: T} {
  const context = ({}: any);
  context.current = defaultValue;
  return context;
}

function beginWork(
  current: Fiber | null,
  renderLanes: ?Lane,
  init?: Lane => void,
  callback: ?(value: Fiber) => mixed,
  subscribe: (() => void) => () => void,
  event: MessageEvent<>,
): Fiber | null {
  const digest: ?string = null;
  const root = (current: any) as FiberRoot;
  return root == null ? null : current;
}

export const forwardRef = <T>(render: T): T => render;

export const scale =
  (min: number, max: number): ((value: number) => number) =>
  (value: number) =>
    (value - min) / (max - min);

class FiberRootNode {
  +tag: number;
  callbacks: Map<string, Lane => void>;

  constructor(tag: number) {
    this.tag = tag;
  }

  getTag(): number {
    return this.tag;
  }
}

export default beginWork;
`

// Flow-typed .js (the `@flow` pragma or Flow-only syntax) must parse via the
// TSX grammar + Flow mask: tree-sitter-javascript errors on every annotation
// and, in large files, degrades mid-file so later declarations vanish
// (facebook/react lost beginWork, createContext, forwardRef, ...). The
// language label must stay "JavaScript".
func TestTreeSitterParserFlowTypedJavaScript(t *testing.T) {
	entities, language, status := TreeSitterParser{}.ParseWithStatus("ReactFiberBeginWork.js", flowTypedJavaScriptSample)
	if language != "JavaScript" {
		t.Fatalf("language = %q, want JavaScript", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	byName := map[string]Entity{}
	for _, e := range entities {
		byName[e.Name] = e
	}
	want := []struct {
		name      string
		kind      string
		startLine int
	}{
		{"createContext", "function", 32},
		{"beginWork", "function", 38},
		{"FiberRootNode", "class", 58},
		{"FiberRootNode.getTag", "method", 66},
	}
	for _, w := range want {
		entity, ok := byName[w.name]
		if !ok {
			t.Errorf("missing entity %q in %#v", w.name, entities)
			continue
		}
		if entity.Kind != w.kind {
			t.Errorf("%s kind = %q, want %q", w.name, entity.Kind, w.kind)
		}
		if entity.StartLine != w.startLine {
			t.Errorf("%s start line = %d, want %d (mask must preserve positions)", w.name, entity.StartLine, w.startLine)
		}
	}
	for _, name := range []string{"forwardRef", "scale"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing exported arrow entity %q in %#v", name, entities)
		}
	}
}

// The Flow mask must preserve every byte offset: same length, same line
// structure, and no changes at all outside masked Flow-only constructs.
func TestFlowJavaScriptMaskPositionPreserving(t *testing.T) {
	masked := maskFlowJavaScriptUnsupportedSyntax(flowTypedJavaScriptSample)
	if len(masked) != len(flowTypedJavaScriptSample) {
		t.Fatalf("mask changed length: %d -> %d", len(flowTypedJavaScriptSample), len(masked))
	}
	if strings.Count(masked, "\n") != strings.Count(flowTypedJavaScriptSample, "\n") {
		t.Fatalf("mask changed line count")
	}
}

const plainJavaScriptSample = `import {helper} from './helper';

export function renderApp(container, props) {
  return helper(container, props);
}

const cache = new Map();

export const memoized = value => {
  if (!cache.has(value)) {
    cache.set(value, renderApp(value));
  }
  return cache.get(value);
};

class Store {
  constructor(state) {
    this.state = state;
  }

  getState() {
    return this.state;
  }
}

export default Store;
`

// Plain (non-Flow) JavaScript must not sniff as Flow, must keep the
// tree-sitter-javascript grammar, and must extract byte-identically to the
// pre-routing behavior.
func TestTreeSitterParserPlainJavaScriptUnchangedByFlowRouting(t *testing.T) {
	if looksLikeFlowJavaScript(plainJavaScriptSample) {
		t.Fatalf("plain JavaScript sniffed as Flow")
	}
	entities, language, status := TreeSitterParser{}.ParseWithStatus("store.js", plainJavaScriptSample)
	if language != "JavaScript" {
		t.Fatalf("language = %q, want JavaScript", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	// Golden extraction from the plain-JS path (tree-sitter-javascript);
	// any drift means Flow routing leaked into non-Flow files.
	wantNames := map[string]string{
		"renderApp":      "function",
		"memoized":       "",
		"Store":          "class",
		"Store.getState": "method",
	}
	for name, kind := range wantNames {
		found := false
		for _, e := range entities {
			if e.Name == name {
				found = true
				if kind != "" && e.Kind != kind {
					t.Errorf("%s kind = %q, want %q", name, e.Kind, kind)
				}
			}
		}
		if !found {
			t.Errorf("missing plain-JS entity %q in %#v", name, entities)
		}
	}
	// The mask must be a no-op on plain JavaScript except for the arrow
	// parameter rewrite, which never runs because the file is not routed.
	again, _, _ := TreeSitterParser{}.ParseWithStatus("store.js", plainJavaScriptSample)
	if !reflect.DeepEqual(entities, again) {
		t.Fatalf("plain JavaScript extraction not deterministic")
	}
}

// TypeScript and TSX files must be untouched by Flow routing (it only
// applies to files labeled JavaScript).
func TestTreeSitterParserTypeScriptUnchangedByFlowRouting(t *testing.T) {
	tsSample := `export type Lane = number;
export function createRoot(container: Element): {render: () => void} {
  return {render: () => {}};
}
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("root.ts", tsSample)
	if language != "TypeScript" {
		t.Fatalf(".ts language = %q, want TypeScript", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected .ts parse status: %#v", status)
	}
	foundCreateRoot := false
	for _, e := range entities {
		if e.Name == "createRoot" && e.Kind == "function" {
			foundCreateRoot = true
		}
	}
	if !foundCreateRoot {
		t.Fatalf("missing createRoot in %#v", entities)
	}

	tsxSample := `export function App({title}: {title: string}) {
  return <div>{title}</div>;
}
`
	entities, language, status = TreeSitterParser{}.ParseWithStatus("App.tsx", tsxSample)
	if language != "TypeScript" {
		t.Fatalf(".tsx language = %q, want TypeScript", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected .tsx parse status: %#v", status)
	}
	foundApp := false
	for _, e := range entities {
		if e.Name == "App" && e.Kind == "function" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Fatalf("missing App in %#v", entities)
	}
}

// Flow-typed .jsx also routes through the mask (it already used the TSX
// grammar); a variance sigil must not fail the parse.
func TestTreeSitterParserFlowTypedJSX(t *testing.T) {
	sample := `// @flow
type Props = {
  +title: string,
  ...
};

export function Banner(props: Props): React$Node {
  return <header>{props.title}</header>;
}
`
	entities, language, status := TreeSitterParser{}.ParseWithStatus("Banner.jsx", sample)
	if language != "JavaScript" {
		t.Fatalf("language = %q, want JavaScript", language)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	found := false
	for _, e := range entities {
		if e.Name == "Banner" && e.Kind == "function" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing Banner in %#v", entities)
	}
}
