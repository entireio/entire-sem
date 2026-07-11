package sem

import "testing"

// perlLocalVarTypes types a local as its receiver's package only for a genuine
// multi-hop fluent chain. A single-hop getter whose argument is itself a method
// chain (`$u->path($u->host->port->scheme)`) must not be counted as multi-hop:
// the nested `->` hops live inside argument parens, at paren depth >= 1.
func TestPerlLocalVarTypesIgnoresParenNestedHops(t *testing.T) {
	// Every hop resolves within its package, isolating this test to the
	// paren-nesting rule (the package-membership gate is exercised separately).
	allResolvable := func(hop, pkgType string) bool { return true }
	single := perlLocalVarTypes("my $u = Mojo::URL->new;\n"+
		"my $p = $u->path($u->host->port->scheme);\n", allResolvable)
	if single["u"] != "Mojo::URL" {
		t.Fatalf("receiver $u should be typed Mojo::URL, got %#v", single)
	}
	if got, ok := single["p"]; ok {
		t.Fatalf("single-hop getter $p should not be typed, got %q", got)
	}

	// A real multi-hop fluent chain (depth-0 hops base->base->userinfo) still
	// types the assignment target, even with a method chain as an argument.
	fluent := perlLocalVarTypes("my $url = Mojo::URL->new;\n"+
		"my $base = $url->base($req->url->base->clone)->base->userinfo(undef);\n", allResolvable)
	if fluent["base"] != "Mojo::URL" {
		t.Fatalf("fluent chain $base should be typed Mojo::URL, got %#v", fluent)
	}
}

// The package-membership gate distinguishes a fluent same-object chain from
// multi-hop getter navigation. `$tx->req->url` has two depth-0 hops (so the
// count rule alone would type it), but `url` is not a sub of the receiver's
// package Mojo::Transaction — it navigates to a different object — so dst must
// NOT inherit the receiver's type. A genuine fluent chain whose every hop lives
// in the receiver package is still typed.
func TestPerlLocalVarTypesGatesFluentOnPackageMembership(t *testing.T) {
	// `req` is a sub of Mojo::Transaction; `url` is not (it belongs to a
	// different package the chain navigates into).
	hopResolvable := func(hop, pkgType string) bool {
		switch {
		case pkgType == "Mojo::Transaction" && hop == "req":
			return true
		case pkgType == "Mojo::URL":
			return true
		default:
			return false
		}
	}
	navigation := perlLocalVarTypes("my $tx = Mojo::Transaction->new;\n"+
		"my $url = $tx->req->url;\n", hopResolvable)
	if got, ok := navigation["url"]; ok {
		t.Fatalf("getter-navigation $url must not be typed as the receiver, got %q", got)
	}
	if navigation["tx"] != "Mojo::Transaction" {
		t.Fatalf("receiver $tx should be typed Mojo::Transaction, got %#v", navigation)
	}

	fluent := perlLocalVarTypes("my $u = Mojo::URL->new;\n"+
		"my $base = $u->base->userinfo;\n", hopResolvable)
	if fluent["base"] != "Mojo::URL" {
		t.Fatalf("in-package fluent chain $base should be typed Mojo::URL, got %#v", fluent)
	}
}

// A constructor immediately chained into a type-escaping hop
// (`Session->new->request`) must NOT type the local as the constructor class:
// `->request` returns a different object, so typing $x as Session would route a
// later `$x->send` to the wrong package. A single trailing hop is ambiguous, so
// the gate leaves $x untyped; a genuine multi-hop fluent-builder chain whose
// every hop stays in the constructor package still types the target.
func TestPerlLocalVarTypesGatesChainedConstructor(t *testing.T) {
	allResolvable := func(hop, pkgType string) bool { return true }

	// Chained constructor with a single trailing hop: below the fluent threshold,
	// so untyped regardless of whether the hop resolves.
	single := perlLocalVarTypes("my $x = Session->new->request;\n", allResolvable)
	if typ, ok := single["x"]; ok {
		t.Fatalf("chained constructor $x must not be typed from the constructor alone, got %q", typ)
	}

	// A bare constructor (optionally with an empty argument list) still types the
	// local.
	bare := perlLocalVarTypes("my $u = Mojo::URL->new;\nmy $v = Mojo::URL->new();\n", allResolvable)
	if bare["u"] != "Mojo::URL" || bare["v"] != "Mojo::URL" {
		t.Fatalf("bare constructor should type the local Mojo::URL, got %#v", bare)
	}

	// A multi-hop fluent-builder chain whose every hop stays in the constructor
	// package still types the target.
	fluent := perlLocalVarTypes("my $b = Mojo::URL->new->scheme->host;\n", allResolvable)
	if fluent["b"] != "Mojo::URL" {
		t.Fatalf("fluent-builder constructor chain $b should be typed Mojo::URL, got %#v", fluent)
	}

	// If a hop after `new` escapes the constructor package, the target stays
	// untyped even with two hops.
	escapes := func(hop, pkgType string) bool { return hop != "request" }
	nav := perlLocalVarTypes("my $y = Session->new->clone->request;\n", escapes)
	if typ, ok := nav["y"]; ok {
		t.Fatalf("constructor chain with an escaping hop must not type $y, got %q", typ)
	}
}

// A constructor-typed local that is later reassigned through anything the typer
// does not model (`$x = build_widget()`) must lose its inferred type, so a
// following single-hop `$x->go` cannot be resolved against the stale
// constructor package. Over-invalidation is preferred to a wrong edge.
func TestPerlLocalVarTypesInvalidatesOnUntrackedReassignment(t *testing.T) {
	allResolvable := func(hop, pkgType string) bool { return true }

	// The core repro: ctor type Foo, then an opaque reassignment, then a call.
	reassigned := perlLocalVarTypes("my $x = Foo->new;\n$x = build_widget();\n$x->go;\n", allResolvable)
	if typ, ok := reassigned["x"]; ok {
		t.Fatalf("reassigned $x must lose its inferred type, got %q", typ)
	}

	// Other opaque reassignment shapes invalidate the same way.
	for _, rhs := range []string{"shift", "$h->{k}", "$other", "some::func()"} {
		got := perlLocalVarTypes("my $x = Foo->new;\n$x = "+rhs+";\n$x->go;\n", allResolvable)
		if typ, ok := got["x"]; ok {
			t.Fatalf("reassignment `$x = %s` must invalidate $x, got %q", rhs, typ)
		}
	}

	// Compound assignment operators are writes and must invalidate too — none of
	// these has a bare scalar sigil immediately before the `=`, so the old
	// scalar-only scan missed them and left the stale constructor type alive.
	// (`//=` is intentionally omitted: stripCodeLiteralsAndComments masks Perl's
	// `//` as a C++ line comment before this scan ever runs — a pre-existing strip
	// limitation, not this pass's concern.)
	for _, stmt := range []string{"$x .= q(z)", "$x ||= Bar->new", "$x += 1", "$x x= 2", "$x **= 2", "$x <<= 1"} {
		got := perlLocalVarTypes("my $x = Foo->new;\n"+stmt+";\n$x->go;\n", allResolvable)
		if typ, ok := got["x"]; ok {
			t.Fatalf("compound reassignment `%s` must invalidate $x, got %q", stmt, typ)
		}
	}

	// A list-assignment LHS writes every scalar in the group, so a
	// constructor-typed local reassigned as `($x, $y) = fetch()` is dropped even
	// though $x is followed by `,` (not `=`) and never matched the scalar scan.
	list := perlLocalVarTypes("my $x = Foo->new;\n($x, $y) = fetch();\n$x->go;\n", allResolvable)
	if typ, ok := list["x"]; ok {
		t.Fatalf("list-assignment `($x, $y) = fetch()` must invalidate $x, got %q", typ)
	}
	// The typed local can be anywhere in the group, not just first.
	listSecond := perlLocalVarTypes("my $x = Foo->new;\n($y, $x) = fetch();\n$x->go;\n", allResolvable)
	if typ, ok := listSecond["x"]; ok {
		t.Fatalf("list-assignment `($y, $x) = fetch()` must invalidate $x, got %q", typ)
	}

	// foreach aliasing rebinds the loop variable to each element (a write), so a
	// prior constructor type is stale inside and after the loop.
	for _, loop := range []string{"foreach my $x (@list) { }", "foreach $x (@list) { }", "for my $x (@list) { }", "for $x (@list) { }"} {
		got := perlLocalVarTypes("my $x = Foo->new;\n"+loop+"\n$x->go;\n", allResolvable)
		if typ, ok := got["x"]; ok {
			t.Fatalf("foreach aliasing `%s` must invalidate $x, got %q", loop, typ)
		}
	}

	// A continuation-line `=` is still a reassignment: the scan must cross
	// the newline (`\s*`, not `[ \t]*`) or the stale ctor type survives.
	continuation := perlLocalVarTypes("my $x = Foo->new;\n$x\n    = Bar->new;\n$x->go;\n", allResolvable)
	if typ, ok := continuation["x"]; ok {
		t.Fatalf("continuation-line reassignment must invalidate $x, got %q", typ)
	}

	// Two constructors of different packages is ambiguous (redeclaration keeps
	// both as consumed ctor spans), so the local is dropped.
	ambiguous := perlLocalVarTypes("my $x = Foo->new;\nmy $x = Bar->new;\n$x->go;\n", allResolvable)
	if typ, ok := ambiguous["x"]; ok {
		t.Fatalf("two different ctor packages must drop $x, got %q", typ)
	}

	// Re-declaring the same package is not ambiguous: both `my $x = Foo->new`
	// are consumed ctor spans, so the local stays typed Foo.
	sameCtor := perlLocalVarTypes("my $x = Foo->new;\nmy $x = Foo->new;\n$x->go;\n", allResolvable)
	if sameCtor["x"] != "Foo" {
		t.Fatalf("repeated same-package ctor should keep $x typed Foo, got %#v", sameCtor)
	}

	// A never-reassigned constructor local stays typed (positive control that
	// the invalidation pass does not over-fire on the ctor assignment itself).
	stable := perlLocalVarTypes("my $x = Foo->new;\n$x->go;\n", allResolvable)
	if stable["x"] != "Foo" {
		t.Fatalf("un-reassigned $x should stay typed Foo, got %#v", stable)
	}
}

// A fluent chain-assign's consumed span must end at its hop chain, not at the end
// of the physical line: perlChainAssignRe's greedy `[^\n;]*` tail also captures a
// same-line trailing statement (comma operator or `if $z = ...` modifier), and if
// that whole span were marked consumed the trailing `$z =` write would be hidden
// from the invalidation scan, leaving $z bound to a stale ctor package a later
// `$z->method` would resolve against.
func TestPerlLocalVarTypesNarrowsChainAssignConsumedSpan(t *testing.T) {
	hopResolvable := func(hop, pkgType string) bool {
		return (hop == "a" || hop == "b") && pkgType == "Bar"
	}

	// The repro: `$z = build()` rides the same line as the chain-assign to $x via
	// the comma operator. $x types Bar from the consumed hop chain, while $z must
	// be dropped (its Foo ctor type is stale after the trailing reassignment).
	got := perlLocalVarTypes("my $z = Foo->new;\nmy $y = Bar->new;\n"+
		"my $x = $y->a->b, $z = build();\n$z->go;\n", hopResolvable)
	if got["x"] != "Bar" {
		t.Fatalf("chain-assigned $x should type Bar, got %#v", got)
	}
	if got["y"] != "Bar" {
		t.Fatalf("$y should type Bar, got %#v", got)
	}
	if typ, ok := got["z"]; ok {
		t.Fatalf("same-line trailing `$z = build()` must invalidate $z, got %q", typ)
	}

	// The idiomatic statement-modifier variant (`... if $z = build()`) is the same
	// shape: the reassignment sits on the chain-assign line with no intervening `;`.
	modifier := perlLocalVarTypes("my $z = Foo->new;\nmy $y = Bar->new;\n"+
		"my $x = $y->a->b if $z = build();\n$z->go;\n", hopResolvable)
	if typ, ok := modifier["z"]; ok {
		t.Fatalf("statement-modifier `if $z = build()` must invalidate $z, got %q", typ)
	}

	// Positive control: with no trailing same-line write, the chain-assign still
	// types $x Bar — narrowing the consumed span must not break fluent typing.
	fluent := perlLocalVarTypes("my $y = Bar->new;\nmy $x = $y->a->b;\n$x->go;\n", hopResolvable)
	if fluent["x"] != "Bar" {
		t.Fatalf("fluent chain-assign should keep $x typed Bar, got %#v", fluent)
	}
}

// A reassignment of a DIFFERENT already-typed local nested inside a fluent
// chain's method-call argument parens (`$u->set($w = get())`) must still
// invalidate that local. The chain-assign to $v records a consumed span whose
// argument-paren interior is punched out, so the nested `$w = get()` stays
// visible to the invalidation scan and drops $w's stale Foo ctor type while the
// receiver $u and the chain target $v keep theirs.
func TestPerlLocalVarTypesInvalidatesWriteNestedInChainArgs(t *testing.T) {
	hopResolvable := func(hop, pkgType string) bool {
		switch pkgType {
		case "Bar":
			return hop == "set" || hop == "go"
		case "Foo":
			return hop == "render"
		}
		return false
	}

	got := perlLocalVarTypes("my $w = Foo->new;\nmy $u = Bar->new;\n"+
		"my $v = $u->set($w = get())->go;\n$w->render;\n", hopResolvable)
	if typ, ok := got["w"]; ok {
		t.Fatalf("write nested in chain args `$u->set($w = get())` must invalidate $w, got %q", typ)
	}
	if got["u"] != "Bar" {
		t.Fatalf("receiver $u should stay typed Bar, got %#v", got)
	}
	if got["v"] != "Bar" {
		t.Fatalf("chain-assigned $v should type Bar, got %#v", got)
	}

	// Positive control: the same chain with a write-free argument keeps every
	// type — punching out the arg interior must not drop a local never written.
	clean := perlLocalVarTypes("my $w = Foo->new;\nmy $u = Bar->new;\n"+
		"my $v = $u->set(cfg())->go;\n$w->render;\n", hopResolvable)
	if clean["w"] != "Foo" {
		t.Fatalf("un-reassigned $w should stay typed Foo, got %#v", clean)
	}
	if clean["u"] != "Bar" || clean["v"] != "Bar" {
		t.Fatalf("$u and $v should stay typed Bar, got %#v", clean)
	}
}

// Operators that share the `=` lead byte are not assignments: `$x == $y`
// comparison, `$x =~ /re/` bind, and a hash `$x => 1` fat comma must not be
// read as reassignments and must not invalidate a constructor-typed local.
func TestPerlLocalVarTypesIgnoresNonAssignmentOperators(t *testing.T) {
	allResolvable := func(hop, pkgType string) bool { return true }

	cmp := perlLocalVarTypes("my $x = Foo->new;\nif ($x == $y) { return; }\n$x->go;\n", allResolvable)
	if cmp["x"] != "Foo" {
		t.Fatalf("`$x == $y` comparison must not invalidate $x, got %#v", cmp)
	}

	bind := perlLocalVarTypes("my $x = Foo->new;\n$x =~ /re/;\n$x->go;\n", allResolvable)
	if bind["x"] != "Foo" {
		t.Fatalf("`$x =~ /re/` bind must not invalidate $x, got %#v", bind)
	}

	fatComma := perlLocalVarTypes("my $x = Foo->new;\nmy %h = ($x => 1);\n$x->go;\n", allResolvable)
	if fatComma["x"] != "Foo" {
		t.Fatalf("`$x => 1` fat comma must not invalidate $x, got %#v", fatComma)
	}

	// Comparison operators that end in `=` (`<=`, `>=`, `!=`, `<=>`) are rejected
	// by the operator chunk itself (`<`, `>`, `!` cannot form the assignment
	// lead), never by the after-byte filter, so none of them invalidate $x.
	for _, stmt := range []string{"if ($x <= 3) { }", "if ($x >= 3) { }", "if ($x != $y) { }", "my $c = $x <=> $y"} {
		got := perlLocalVarTypes("my $x = Foo->new;\n"+stmt+";\n$x->go;\n", allResolvable)
		if got["x"] != "Foo" {
			t.Fatalf("comparison `%s` must not invalidate $x, got %#v", stmt, got)
		}
	}

	// Pure reads never invalidate: an argument position, a method call on the
	// receiver, and interpolation (masked before the scan) all keep $x typed.
	reads := perlLocalVarTypes("my $x = Foo->new;\nprint foo($x);\n$x->probe;\nmy $s = \"got $x now\";\n$x->go;\n", allResolvable)
	if reads["x"] != "Foo" {
		t.Fatalf("reads `foo($x)`, `$x->probe`, interpolation must not invalidate $x, got %#v", reads)
	}
}
