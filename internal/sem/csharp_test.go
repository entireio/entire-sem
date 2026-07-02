package sem

import (
	"strings"
	"testing"
)

// C# call idioms the generic scanners miss (evidence: on dotnet/efcore the
// focus method HistoryRepository.GetCreateCommands kept 1 of 4 outbound
// edges): one-hop typed-property chains (`Dependencies.ModelDiffer.
// GetDifferences(...)` where `Dependencies` is a class-level typed property
// and `ModelDiffer` an interface-typed property on its — record — type),
// single-hop typed-member receivers, and extension-method calls
// (`model.GetRelationalModel()` targeting a static method with a `this`
// first parameter on an unrelated static class).
func TestCSharpReceiverCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Migrations/HistoryRepository.cs", `// Licensed to the .NET Foundation under one or more agreements.

namespace Microsoft.EntityFrameworkCore.Migrations;

public abstract class HistoryRepository : IHistoryRepository
{
    private IModel? _model;

    protected HistoryRepository(HistoryRepositoryDependencies dependencies)
    {
        Dependencies = dependencies;
    }

    protected virtual HistoryRepositoryDependencies Dependencies { get; }

    protected virtual IReadOnlyList<MigrationCommand> GetCreateCommands()
    {
        var model = EnsureModel();

        var operations = Dependencies.ModelDiffer.GetDifferences(null, model.GetRelationalModel());
        var commandList = Dependencies.MigrationsSqlGenerator.Generate(operations, model);
        return commandList;
    }

    private IModel EnsureModel()
    {
        Dependencies.Validate();
        return _model;
    }
}
`)
	writeFile(t, repo, "src/Migrations/HistoryRepositoryDependencies.cs", `namespace Microsoft.EntityFrameworkCore.Migrations;

public sealed record HistoryRepositoryDependencies
{
    public HistoryRepositoryDependencies(
        IMigrationsModelDiffer modelDiffer,
        IMigrationsSqlGenerator migrationsSqlGenerator)
    {
        ModelDiffer = modelDiffer;
        MigrationsSqlGenerator = migrationsSqlGenerator;
    }

    public IMigrationsModelDiffer ModelDiffer { get; init; }

    public IMigrationsSqlGenerator MigrationsSqlGenerator { get; init; }

    public void Validate()
    {
    }
}
`)
	writeFile(t, repo, "src/Migrations/IMigrationsModelDiffer.cs", `namespace Microsoft.EntityFrameworkCore.Migrations;

public interface IMigrationsModelDiffer
{
    bool HasDifferences(IRelationalModel? source, IRelationalModel? target);

    IReadOnlyList<MigrationOperation> GetDifferences(IRelationalModel? source, IRelationalModel? target);
}
`)
	writeFile(t, repo, "src/Migrations/IMigrationsSqlGenerator.cs", `namespace Microsoft.EntityFrameworkCore.Migrations;

public interface IMigrationsSqlGenerator
{
    IReadOnlyList<MigrationCommand> Generate(
        IReadOnlyList<MigrationOperation> operations,
        IModel? model = null);
}
`)
	writeFile(t, repo, "src/Migrations/MigrationsSqlGenerator.cs", `namespace Microsoft.EntityFrameworkCore.Migrations;

public class MigrationsSqlGenerator : IMigrationsSqlGenerator
{
    public virtual IReadOnlyList<MigrationCommand> Generate(
        IReadOnlyList<MigrationOperation> operations,
        IModel? model = null)
        => new List<MigrationCommand>();
}
`)
	writeFile(t, repo, "src/Metadata/IModel.cs", `namespace Microsoft.EntityFrameworkCore.Metadata;

public interface IModel
{
    object? FindRuntimeAnnotationValue(string name);
}
`)
	writeFile(t, repo, "src/Extensions/RelationalModelExtensions.cs", `namespace Microsoft.EntityFrameworkCore;

public static class RelationalModelExtensions
{
    public static IRelationalModel GetRelationalModel(this IModel model)
        => (IRelationalModel)model.FindRuntimeAnnotationValue(RelationalAnnotationNames.RelationalModel);
}
`)
	writeFile(t, repo, "src/SqlServer/SqlServerHistoryRepository.cs", `namespace Microsoft.EntityFrameworkCore.SqlServer.Migrations.Internal;

public class SqlServerHistoryRepository : HistoryRepository
{
    protected virtual bool HasPendingChanges()
        => Dependencies.ModelDiffer.HasDifferences(null, null);
}
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

	// Typed property chain through a record declared in another file, landing
	// on the interface's method symbol (how the call is dispatched in C#).
	if r, ok := calls["HistoryRepository.GetCreateCommands->IMigrationsModelDiffer.GetDifferences"]; !ok || r.Reason != "method call resolved via typed property chain receiver" {
		t.Fatalf("typed property chain call (GetDifferences) not resolved: %#v", calls)
	}
	if _, ok := calls["HistoryRepository.GetCreateCommands->IMigrationsSqlGenerator.Generate"]; !ok {
		t.Fatalf("typed property chain call (Generate) not resolved: %#v", calls)
	}
	// The chain tail `MigrationsSqlGenerator.Generate(` must not be misread as
	// a static call on the concrete class of the same name (EF Core names
	// properties after their concrete type): the property is interface-typed.
	if _, ok := calls["HistoryRepository.GetCreateCommands->MigrationsSqlGenerator.Generate"]; ok {
		t.Fatalf("chain tail misresolved as static call on the concrete class: %#v", calls)
	}
	// Extension method: receiver typed IModel by the factory return, target is
	// the unique static method with a `this` first parameter.
	if r, ok := calls["HistoryRepository.GetCreateCommands->RelationalModelExtensions.GetRelationalModel"]; !ok || r.Reason != "method call resolved to the unique C# extension method" {
		t.Fatalf("extension method call not resolved: %#v", calls)
	}
	// Same-class bare call still resolves (regression guard for the eval's
	// fourth edge).
	if _, ok := calls["HistoryRepository.GetCreateCommands->HistoryRepository.EnsureModel"]; !ok {
		t.Fatalf("same-class call not resolved: %#v", calls)
	}
	// Single-hop member receiver: property typed by a record with the method
	// declared directly on it.
	if r, ok := calls["HistoryRepository.EnsureModel->HistoryRepositoryDependencies.Validate"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("typed member receiver call not resolved: %#v", calls)
	}
	// A subclass inherits the base class's typed property: the chain types
	// `Dependencies` by walking up the inheritance chain.
	if _, ok := calls["SqlServerHistoryRepository.HasPendingChanges->IMigrationsModelDiffer.HasDifferences"]; !ok {
		t.Fatalf("inherited typed property chain call not resolved: %#v", calls)
	}
	// The chain hops must not surface the intermediate property names as call
	// targets (the eval forbids Dependencies/ModelDiffer/... as outbound).
	for key := range calls {
		if strings.HasPrefix(key, "HistoryRepository.GetCreateCommands->") {
			for _, forbidden := range []string{"Dependencies", "ModelDiffer", "MigrationsSqlGenerator", "model", "operations", "commandList"} {
				if strings.HasSuffix(key, "."+forbidden) || strings.HasSuffix(key, ">"+forbidden) {
					t.Fatalf("forbidden outbound target %s: %#v", key, calls)
				}
			}
		}
	}
}

// C# record declarations are real types: the record surfaces as a class-kind
// symbol and its properties/methods are qualified members under it. Without
// this every `*Dependencies` parameter object is invisible.
func TestCSharpRecordSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Deps.cs", `namespace Example;

public sealed record ServiceDependencies
{
    public IServiceHelper Helper { get; init; }

    public void Validate()
    {
    }
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		kinds[s.Name+"|"+s.Kind] = s.ContainerID
	}
	if _, ok := kinds["ServiceDependencies|class"]; !ok {
		t.Fatalf("record not extracted as class symbol: %#v", kinds)
	}
	if container := kinds["Helper|field"]; !strings.HasSuffix(container, "ServiceDependencies") {
		t.Fatalf("record property not contained by the record: %#v", kinds)
	}
	if container := kinds["Validate|method"]; !strings.HasSuffix(container, "ServiceDependencies") {
		t.Fatalf("record method not contained by the record: %#v", kinds)
	}
}

// Multi-line verbatim and raw strings must not feed the call scanners: the
// generic stripper only contains string masking to a single line, so SQL
// bodies (ubiquitous in EF Core providers) would otherwise register call
// sites and member chains.
func TestCSharpStringAndCommentMasking(t *testing.T) {
	block := `
    protected virtual string GetCreateScript()
    {
        var sql = @"CREATE TABLE ""History"" (
            Id INT, -- VALUES(Fake.Chain.Call(1))
            Path NVARCHAR(100) -- C:\temp\
        );";
        var raw = """
            Fake.Deep.Call(2)
            "quoted"
            """;
        var interp = $@"SELECT {Fake.Chain.Masked(3)}
            FROM x";
        // comment: Doc.Sample.Snippet(4) and a stray @" quote
        return Real.Chain.Call(sql);
    }
`
	chains := csharpMemberChainCalls(block)
	if len(chains) != 1 || chains[0].Detail != "Real.Chain.Call" {
		t.Fatalf("expected only the real chain call, got %#v", chains)
	}
	// Comments are left for the generic stripper; assert on the combined
	// masking exactly as the extractors consume it.
	masked := stripCodeLiteralsAndComments(maskCSharpTextBlocks(block))
	for _, gone := range []string{"Fake", "VALUES", "quoted", "SELECT", "Snippet"} {
		if strings.Contains(masked, gone) {
			t.Fatalf("string/comment content %q not masked:\n%s", gone, masked)
		}
	}
	if !strings.Contains(masked, "Real.Chain.Call(sql)") {
		t.Fatalf("code after masked strings lost:\n%s", masked)
	}
	if strings.Count(masked, "\n") != strings.Count(block, "\n") {
		t.Fatalf("line structure not preserved")
	}
	// A "" escape inside a verbatim string does not terminate it early, and a
	// backslash before the closing quote does not extend it.
	verbatim := `var a = @"say ""hi"" \"; var b = Next.Call.Site(1);`
	maskedVerbatim := maskCSharpTextBlocks(verbatim)
	if strings.Contains(maskedVerbatim, "hi") || !strings.Contains(maskedVerbatim, "Next.Call.Site(1)") {
		t.Fatalf("verbatim escape handling wrong: %q", maskedVerbatim)
	}
}

// The chain extractor is straight-line conservative: deeper chains, call
// results, and null-conditional hops are skipped rather than guessed.
func TestCSharpMemberChainCallExtraction(t *testing.T) {
	block := `
    void Run()
    {
        this.Deps.Differ.GetDifferences(null, x);
        A.B.C.D(1);
        Foo().Bar.Baz(2);
        list[0].Prop.Method(3);
        deps?.Differ.HasDifferences(null, null);
        Deps.Differ.GetDifferences(null, y);
    }
`
	chains := csharpMemberChainCalls(block)
	if len(chains) != 1 {
		t.Fatalf("expected exactly one conservative chain, got %#v", chains)
	}
	if chains[0].Receiver != "Deps" || chains[0].Property != "Differ" || chains[0].Method != "GetDifferences" {
		t.Fatalf("unexpected chain parts: %#v", chains[0])
	}
}

// Member types come from field symbols' `name Type` signatures; nullable
// markers are stripped and generic/qualified/builtin types are dropped.
func TestCSharpFieldSignatureType(t *testing.T) {
	cases := []struct {
		name, signature, want string
		ok                    bool
	}{
		{"Dependencies", "Dependencies HistoryRepositoryDependencies", "HistoryRepositoryDependencies", true},
		{"_model", "_model IModel?", "IModel", true},
		{"TableName", "TableName string", "", false},
		{"Logger", "Logger IDiagnosticsLogger<DbLoggerCategory.Migrations>", "", false},
		{"Pair", "Pair (int, string)", "", false},
		{"Bare", "Bare", "", false},
	}
	for _, c := range cases {
		got, ok := csharpFieldSignatureType(c.name, c.signature)
		if got != c.want || ok != c.ok {
			t.Fatalf("csharpFieldSignatureType(%q, %q) = %q, %v; want %q, %v", c.name, c.signature, got, ok, c.want, c.ok)
		}
	}
}

// The extension fallback requires the workspace-unique method with that name
// to be extension-shaped (static + `this` first parameter): same-named
// instance methods and overloaded extension groups never match.
func TestCSharpUniqueExtensionMethod(t *testing.T) {
	ext := SymbolRecord{ID: "a", Kind: "method", Language: "C#", Signature: "public static IRelationalModel GetRelationalModel(this IModel model)"}
	inst := SymbolRecord{ID: "b", Kind: "method", Language: "C#", Signature: "public virtual IRelationalModel GetRelationalModel()"}
	if m, ok := csharpUniqueExtensionMethod([]SymbolRecord{ext}); !ok || m.ID != "a" {
		t.Fatalf("unique extension method not matched")
	}
	if _, ok := csharpUniqueExtensionMethod([]SymbolRecord{inst}); ok {
		t.Fatalf("instance method must not match the extension fallback")
	}
	if _, ok := csharpUniqueExtensionMethod([]SymbolRecord{ext, inst}); ok {
		t.Fatalf("ambiguous short name must not match")
	}
	if !csharpExtensionMethodSignature("static void Ext(this StringBuilder sb, int x)") {
		t.Fatalf("extension signature not detected")
	}
	if csharpExtensionMethodSignature("static void Helper(StringBuilder sb)") {
		t.Fatalf("non-this static method misdetected as extension")
	}
	if csharpExtensionMethodSignature("void Ext(this StringBuilder sb)") {
		t.Fatalf("non-static method misdetected as extension")
	}
}
