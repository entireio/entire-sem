package sem

import (
	"strings"
	"testing"
)

// Ruby call idioms the generic scanners miss (evidence: on discourse/discourse
// the focus method PostRevisor#revise! received zero inbound/outbound CALLS
// edges): constructor-chained receivers (`Klass.new(x).m`), local-variable
// constructor receivers, paren-less self/bare calls, and `!`/`?` method names.
func TestRubyReceiverCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "post_revisor.rb", `class PostRevisor
  def initialize(post)
    @post = post
  end

  def revise!(editor, fields)
    should_revise?
    perform_edit
    self.publish_changes
    true
  end

  def should_revise?
    true
  end

  def perform_edit
    1
  end

  def publish_changes
    2
  end
end
`)
	writeFile(t, repo, "posts_controller.rb", `class PostsController
  def update
    PostRevisor.new(post).revise!(current_user, params)
  end

  def destroy
    revisor = PostRevisor.new(post)
    revisor.revise!(current_user, params)
  end
end
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

	// PostRevisor.new(post).revise!(...) -> constructor-chained receiver call.
	if r, ok := calls["PostsController.update->PostRevisor.revise!"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("constructor-chained call not resolved: %#v", calls)
	}
	// revisor = PostRevisor.new(post); revisor.revise!(...) -> local-variable
	// constructor receiver.
	if r, ok := calls["PostsController.destroy->PostRevisor.revise!"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("local-variable constructor receiver call not resolved: %#v", calls)
	}
	// Klass.new resolves to the class's initialize constructor.
	if _, ok := calls["PostsController.update->PostRevisor.initialize"]; !ok {
		t.Fatalf("Klass.new not resolved to initialize: %#v", calls)
	}
	// Paren-less self.method call inside the same class.
	if r, ok := calls["PostRevisor.revise!->PostRevisor.publish_changes"]; !ok || r.Confidence != 0.9 {
		t.Fatalf("paren-less self call not resolved (0.9): %#v", calls)
	}
	// Paren-less bare call to a sibling method (implicit self).
	if _, ok := calls["PostRevisor.revise!->PostRevisor.perform_edit"]; !ok {
		t.Fatalf("bare sibling call not resolved: %#v", calls)
	}
	// Bare `?`-suffixed call: unambiguously a method call in Ruby.
	if _, ok := calls["PostRevisor.revise!->PostRevisor.should_revise?"]; !ok {
		t.Fatalf("?-suffixed bare call not resolved: %#v", calls)
	}
}

// Ruby `module Name ... end` declarations must emit a "module" symbol with
// nested instance methods qualified under it (evidence: on discourse/discourse
// `module PrettyText`, `module HasCustomFields`, and `module
// SiteSettingExtension` were absent from the symbol set while classes
// extracted fine — tree-sitter-ruby's `module` node had no entityFromNode
// case).
func TestRubyModuleDeclarationsEmitted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/pretty_text.rb", `module PrettyText
  def markdown(text)
    text
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	module := symbolByKindAndName(snapshot.Symbols, "module", "PrettyText")
	if module.ID == "" {
		t.Fatalf("missing module symbol PrettyText in %#v", snapshot.Symbols)
	}
	if module.FilePath != "lib/pretty_text.rb" {
		t.Fatalf("module file = %q", module.FilePath)
	}
	method := symbolByKindAndName(snapshot.Symbols, "method", "PrettyText.markdown")
	if method.ID == "" {
		t.Fatalf("nested method not qualified under module in %#v", snapshot.Symbols)
	}
	if method.ContainerID != module.ID {
		t.Fatalf("method container = %q, want %q", method.ContainerID, module.ID)
	}
}

// Bare-word scanning must not invent calls from comments, heredocs, %w
// literals, local variables, parameters, hash keys, or `!=` comparisons.
func TestRubyBareCallScanPrecision(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "worker.rb", `class Worker
  def cleanup
    1
  end

  def refresh!
    2
  end

  def payload
    3
  end

  def run(payload)
    # cleanup happens in a comment, not a call site
    doc = <<~TEXT
      cleanup refresh! inside a heredoc
    TEXT
    words = %w[cleanup refresh!]
    cleanup = 4
    if payload != cleanup
      { cleanup: 1, refresh: doc, words: words }
    end
  end
end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		if strings.HasPrefix(lastSegment(r.FromID), "Worker.run") {
			t.Fatalf("false call edge from Worker.run: %#v", r)
		}
	}
}

// Ruby text stripping masks # comments, heredoc bodies, and %w/%i literals.
func TestStripRubyCodeText(t *testing.T) {
	input := `x = 1 # trailing comment(call)
sql = <<~SQL
  SELECT fake_call(1)
SQL
names = %w[alpha beta]
run!
`
	stripped := stripRubyCodeText(input)
	for _, gone := range []string{"trailing", "comment", "SELECT", "fake_call", "alpha", "beta"} {
		if strings.Contains(stripped, gone) {
			t.Fatalf("expected %q to be masked, got:\n%s", gone, stripped)
		}
	}
	if !strings.Contains(stripped, "run!") {
		t.Fatalf("real code must survive stripping, got:\n%s", stripped)
	}
	if len(stripped) != len(input) {
		t.Fatalf("stripping must preserve length: %d != %d", len(stripped), len(input))
	}
}

func TestRubySuffixedCallIdentifiers(t *testing.T) {
	got := rubySuffixedCallIdentifiers(`def go(a)
  save!
  valid?
  a != b
  obj.dirty?
  send(:reload!)
  defined?(thing)
end
`)
	for _, want := range []string{"save!", "valid?"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	for name := range got {
		switch name {
		case "save!", "valid?":
		default:
			// a != b is a comparison, obj.dirty? has a receiver, :reload! is a
			// symbol, defined? is a keyword.
			t.Fatalf("unexpected suffixed call identifier %q in %v", name, got)
		}
	}
}

func TestRubyLocalVarTypes(t *testing.T) {
	got := rubyLocalVarTypes(`def run
  revisor = PostRevisor.new(post)
  scoped = Admin::Guardian.new(user)
  flip = First.new
  flip = Second.new
end
`)
	if got["revisor"] != "PostRevisor" {
		t.Fatalf("revisor: %v", got)
	}
	if got["scoped"] != "Guardian" {
		t.Fatalf("namespaced constructor should use the last segment: %v", got)
	}
	if _, ok := got["flip"]; ok {
		t.Fatalf("conflicting constructor assignments must be dropped: %v", got)
	}
}

func TestRubyReceiverCallsParenLessAndBang(t *testing.T) {
	calls := rubyReceiverCalls(`def go
  revisor.revise!(user, fields)
  post.save
  record.title = "x"
  a.b != c
end
`)
	byKey := map[string]receiverCall{}
	for _, c := range calls {
		byKey[c.Receiver+"."+c.Method] = c
	}
	if _, ok := byKey["revisor.revise!"]; !ok {
		t.Fatalf("bang receiver call missing: %v", calls)
	}
	if _, ok := byKey["post.save"]; !ok {
		t.Fatalf("paren-less receiver call missing: %v", calls)
	}
	if _, ok := byKey["record.title"]; ok {
		t.Fatalf("attr-writer assignment must not be a call: %v", calls)
	}
	if _, ok := byKey["a.b!"]; ok {
		t.Fatalf("a.b != c is a comparison on b, not a call to b!: %v", calls)
	}
	if _, ok := byKey["a.b"]; !ok {
		t.Fatalf("a.b != c should still record the b call: %v", calls)
	}
}

func TestRubyChainedConstructorCalls(t *testing.T) {
	calls := rubyChainedConstructorCalls(`def go
  PostRevisor.new(post).revise!(user)
  Guardian.new.can_see?
end
`)
	byKey := map[string]bool{}
	for _, c := range calls {
		byKey[c.TypeName+"."+c.Method] = true
	}
	if !byKey["PostRevisor.revise!"] {
		t.Fatalf("Klass.new(args).method! chain missing: %v", calls)
	}
	if !byKey["Guardian.can_see?"] {
		t.Fatalf("Klass.new.method? chain (no ctor args) missing: %v", calls)
	}
}
