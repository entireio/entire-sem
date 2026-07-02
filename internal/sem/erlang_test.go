package sem

import (
	"strings"
	"testing"
)

func TestStripErlangCodeText(t *testing.T) {
	// `%` comments, string literals, quoted atoms, and `$c` character
	// literals must be masked so their contents never register as call
	// sites; newlines survive so offsets keep line context.
	in := "run() ->\n" +
		"    % commented_call(1)\n" +
		"    io:format(\"fake_call(~p)\", ['quoted_call']),\n" +
		"    C = $%, % char literal must not open a comment mask\n" +
		"    real_call(C).\n"
	out := stripErlangCodeText(in)
	for _, gone := range []string{"commented_call", "fake_call", "quoted_call"} {
		if strings.Contains(out, gone) {
			t.Fatalf("stripErlangCodeText left %q in:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"io:format", "real_call(C)"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("stripErlangCodeText dropped %q from:\n%s", kept, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatalf("stripErlangCodeText changed line count:\n%s", out)
	}
}

func TestErlangCallSites(t *testing.T) {
	block := `delete(Q, ActingUser) ->
    Fun(Q),
    ?LOG_INFO(Q),
    ?MODULE:notify(Q),
    rabbit_db_queue:internal_delete(Q, false),
    rabbit_binding : process_deletions(Q),
    F = fun rabbit_misc:const/1,
    case filter(Q) of
        true -> helper(Q);
        false -> ok
    end.
`
	sites := erlangCallSites(block)
	want := map[erlangCallSite]bool{
		{Module: "rabbit_db_queue", Name: "internal_delete"}:  true,
		{Module: "rabbit_binding", Name: "process_deletions"}: true,
		{Name: "notify"}: true, // ?MODULE: qualifier is the enclosing module
		{Name: "filter"}: true,
		{Name: "helper"}: true,
	}
	got := map[erlangCallSite]bool{}
	for _, site := range sites {
		got[site] = true
	}
	for site := range want {
		if !got[site] {
			t.Fatalf("missing call site %+v in %+v", site, sites)
		}
	}
	// Not calls: the clause head (column 0), a variable call `Fun(...)`, a
	// macro expansion, `fun mod:name/arity` references (no argument list),
	// and keywords.
	for _, site := range sites {
		if site.Module == "" && (site.Name == "delete" || site.Name == "const" || site.Name == "case" || site.Name == "fun") {
			t.Fatalf("bogus local call site %+v in %+v", site, sites)
		}
		if site.Name == "LOG_INFO" || site.Name == "Fun" {
			t.Fatalf("macro/variable call site %+v in %+v", site, sites)
		}
		if site.Module == "rabbit_misc" {
			t.Fatalf("`fun mod:name/arity` reference treated as call: %+v", sites)
		}
	}
}

// Erlang CALLS extraction (evidence: on rabbitmq/rabbitmq-server the focus
// function rabbit_amqqueue:internal_delete had zero inbound/outbound CALLS).
// Remote `mod:fun(Args)` calls resolve to the function named `fun` in the file
// defining module `mod` (module name = file basename by Erlang convention,
// backed by the extracted -module attribute symbol); bare `fun(Args)` calls
// resolve within the same file only. Erlang folds functions per name/arity, so
// a call may land on every same-name arity symbol in the target module.
func TestErlangCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/rabbit_store.erl", `-module(rabbit_store).
-export([internal_delete/1, internal_delete/2]).

-spec internal_delete(term()) -> ok.

internal_delete(Q) ->
    internal_delete(Q, undefined).

internal_delete(Q, ActingUser) ->
    audit(Q, ActingUser),
    rabbit_metrics:queue_deleted(Q).

audit(_Q, _ActingUser) ->
    ok.
`)
	writeFile(t, repo, "src/rabbit_worker.erl", `-module(rabbit_worker).
-export([drop_queue/1]).

drop_queue(Q) ->
    Fun = fun rabbit_store:internal_delete/1,
    _ = Fun,
    % rabbit_store:internal_delete(commented_out),
    rabbit_store:internal_delete(Q, ?MODULE).
`)
	writeFile(t, repo, "src/rabbit_metrics.erl", `-module(rabbit_metrics).
-export([queue_deleted/1]).

queue_deleted(_Q) ->
    ok.
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
		to, ok := symbolsByID[r.ToID]
		if !ok || to.Language != "Erlang" {
			continue
		}
		from, ok := symbolsByID[r.FromID]
		if !ok {
			// The only non-symbol CALLS source is the file-level top-level
			// scan; Erlang's "top level" is all attributes (-spec, -export)
			// whose payloads must not register as call sites.
			t.Fatalf("file-level CALLS edge into Erlang symbol %s should not exist", to.Name)
		}
		calls[edge{from.FilePath + ":" + from.Name, to.FilePath + ":" + to.Name}] = true
	}
	for _, want := range []edge{
		// Remote call from another module resolves into rabbit_store.erl.
		{"src/rabbit_worker.erl:drop_queue", "src/rabbit_store.erl:internal_delete"},
		// Outbound remote call from the focus module.
		{"src/rabbit_store.erl:internal_delete", "src/rabbit_metrics.erl:queue_deleted"},
		// Local (same-module) call.
		{"src/rabbit_store.erl:internal_delete", "src/rabbit_store.erl:audit"},
		// Arity chaining: internal_delete/1 -> internal_delete/2.
		{"src/rabbit_store.erl:internal_delete", "src/rabbit_store.erl:internal_delete"},
	} {
		if !calls[want] {
			t.Fatalf("missing Erlang CALLS edge %v in %v", want, calls)
		}
	}
	// rabbit_metrics defines no call sites: the `fun mod:name/arity`
	// reference in rabbit_worker and the commented-out call must not
	// fabricate edges out of (or into) it beyond the real call site.
	for e := range calls {
		if strings.HasPrefix(e.from, "src/rabbit_metrics.erl") {
			t.Fatalf("unexpected Erlang CALLS edge %v", e)
		}
	}
}
