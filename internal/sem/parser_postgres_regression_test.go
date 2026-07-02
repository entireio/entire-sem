package sem

import (
	"strings"
	"testing"
)

// helper: count entities of a given kind+name.
func countEntity(entities []Entity, kind, name string) int {
	n := 0
	for _, e := range entities {
		if e.Kind == kind && e.Name == name {
			n++
		}
	}
	return n
}

// Blocker #1: a ';' inside a string literal in a CREATE POLICY must not truncate
// the statement and drop entities that follow it.
func TestPostgresSemicolonInStringDoesNotDropFollowingEntities(t *testing.T) {
	src := "CREATE TABLE before_tbl (id int);\n" +
		"CREATE POLICY pol ON t USING (note = 'has ; embedded semicolon');\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "before_tbl") != 1 {
		t.Errorf("before_tbl missing: %+v", entities)
	}
	if countEntity(entities, "table", "after_tbl") != 1 {
		t.Errorf("after_tbl dropped by ';' inside policy string literal: %+v", entities)
	}
	if countEntity(entities, "policy", "pol") != 1 {
		t.Errorf("policy pol missing or duplicated: %+v", entities)
	}
}

// A ';' inside a dollar-quoted function body must not terminate the statement
// early and drop following entities.
func TestPostgresSemicolonInDollarQuotedBodyDoesNotDropEntities(t *testing.T) {
	src := "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; PERFORM 2; END $$ LANGUAGE plpgsql;\n" +
		"CREATE TABLE tail_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "function", "f") != 1 {
		t.Errorf("function f missing: %+v", entities)
	}
	if countEntity(entities, "table", "tail_tbl") != 1 {
		t.Errorf("tail_tbl dropped by ';' inside dollar-quoted body: %+v", entities)
	}
}

// Blocker #2: commented-out DDL must not be extracted as a phantom entity.
func TestPostgresCommentedDDLNotExtracted(t *testing.T) {
	src := "-- CREATE FUNCTION commented_fn() RETURNS void AS $$ BEGIN END $$ LANGUAGE plpgsql;\n" +
		"/* CREATE POLICY commented_pol ON t USING (true); */\n" +
		"CREATE TABLE real_tbl (id int);\n"
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if countEntity(entities, "function", "commented_fn") != 0 {
		t.Errorf("phantom function from line comment: %+v", entities)
	}
	if countEntity(entities, "policy", "commented_pol") != 0 {
		t.Errorf("phantom policy from block comment: %+v", entities)
	}
	if countEntity(entities, "table", "real_tbl") != 1 {
		t.Errorf("real_tbl missing: %+v", entities)
	}
}

// Real function/policy after a comment that mentions DDL must still be extracted
// exactly once (the comment strip must preserve real entities + line numbers).
func TestPostgresRealEntitiesSurviveCommentStrip(t *testing.T) {
	src := "-- here we CREATE FUNCTION things\n" +
		"CREATE FUNCTION audit() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql;\n" +
		"CREATE POLICY p ON t USING (true);\n"
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if countEntity(entities, "function", "audit") != 1 {
		t.Errorf("real function audit should be extracted exactly once: %+v", entities)
	}
	if countEntity(entities, "policy", "p") != 1 {
		t.Errorf("real policy p should be extracted exactly once: %+v", entities)
	}
	for _, e := range entities {
		if e.Name == "audit" && e.StartLine != 2 {
			t.Errorf("comment strip must preserve line numbers; audit StartLine=%d want 2", e.StartLine)
		}
	}
}

func TestStripSQLCommentsPreservesLiteralsAndLength(t *testing.T) {
	in := "x = '-- not a comment'; -- real\n/* blk */ y\n"
	out := stripSQLComments(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d vs %d", len(out), len(in))
	}
	if !strings.Contains(out, "-- not a comment") {
		t.Errorf("comment marker inside a string literal was wrongly stripped: %q", out)
	}
	if strings.Contains(out, "real") || strings.Contains(out, "blk") {
		t.Errorf("real comments not stripped: %q", out)
	}
}

// PostgreSQL statements the SQL grammar cannot parse (EXPLAIN option lists,
// ALTER OPERATOR FAMILY/CLASS, COMMENT ON non-table objects) must be masked so
// they do not raise parse errors or drop surrounding real entities.
func TestPostgresUnparseableStatementsMaskedWithoutDroppingEntities(t *testing.T) {
	src := "CREATE TABLE before_tbl (id int);\n" +
		"EXPLAIN (COSTS OFF, ANALYZE) SELECT * FROM before_tbl;\n" +
		"ALTER OPERATOR FAMILY int4_ops USING gin ADD OPERATOR 1 = (int4, int4);\n" +
		"COMMENT ON ACCESS METHOD bloom IS 'bloom index';\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("ext.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking: %s", status.Detail)
	}
	if countEntity(entities, "table", "before_tbl") != 1 {
		t.Errorf("before_tbl missing: %+v", entities)
	}
	if countEntity(entities, "table", "after_tbl") != 1 {
		t.Errorf("after_tbl dropped by unmasked PostgreSQL statement: %+v", entities)
	}
}

// Additional PostgreSQL DDL the SQL grammar cannot parse (foreign-data/event-
// trigger families, ALTER TEXT SEARCH, generic ALTER OPERATOR) must be masked
// without dropping surrounding real entities.
func TestPostgresForeignAndOperatorDDLMasked(t *testing.T) {
	src := "CREATE TABLE before_tbl (id int);\n" +
		"CREATE FOREIGN DATA WRAPPER dblink_fdw VALIDATOR dblink_fdw_validator;\n" +
		"CREATE EVENT TRIGGER t ON ddl_command_start EXECUTE FUNCTION f();\n" +
		"ALTER TEXT SEARCH DICTIONARY intdict (MAXLEN = 8);\n" +
		"ALTER OPERATOR >= (citext, citext) SET (RESTRICT = scalargesel);\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("ddl.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking: %s", status.Detail)
	}
	if countEntity(entities, "table", "before_tbl") != 1 || countEntity(entities, "table", "after_tbl") != 1 {
		t.Fatalf("surrounding tables dropped by unmasked DDL: %#v", entities)
	}
}

// CREATE DOMAIN/CAST/PROCEDURE and @extschema@ extension-template placeholders
// must be masked so .sql.in template statements parse.
func TestPostgresDomainCastProcedureAndPlaceholders(t *testing.T) {
	src := "CREATE TABLE before_tbl (id int);\n" +
		"CREATE DOMAIN earth AS @extschema:cube@.cube;\n" +
		"CREATE CAST (hstore AS jsonb) WITH FUNCTION hstore_to_jsonb(hstore);\n" +
		"CREATE PROCEDURE p1() LANGUAGE plpgsql AS $$ BEGIN NULL; END $$;\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("ext.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error after masking: %s", status.Detail)
	}
	if countEntity(entities, "table", "before_tbl") != 1 || countEntity(entities, "table", "after_tbl") != 1 {
		t.Fatalf("surrounding tables dropped: %#v", entities)
	}
}

// Regression: a Unicode character that grows under strings.ToLower (e.g. 'İ' ->
// "i̇", +1 byte) used to drift the keyword offsets in the Postgres SQL masking
// past the end of the original content and panic with an index-out-of-range.
// asciiLowerString keeps the byte length stable so offsets stay valid. Reproduces
// the mono repo crash on GHTDB.DATA.PostgreSQL.sql.
func TestPostgresUnicodeLowercaseDoesNotPanic(t *testing.T) {
	src := "-- " + strings.Repeat("İ", 200) + "\n" +
		"CREATE TABLE t (id int, CONSTRAINT pk PRIMARY KEY (id));\n" +
		"CREATE POLICY İpol ON t USING (true);\n"
	// Must not panic.
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if countEntity(entities, "table", "t") != 1 {
		t.Errorf("table t missing after unicode-laden comment: %+v", entities)
	}
}

func TestAsciiLowerStringPreservesByteLength(t *testing.T) {
	cases := []string{
		"PRIMARY KEY",
		strings.Repeat("İ", 200),
		"ß DROP TABLE Ω",
		"plain ascii",
		"",
	}
	for _, in := range cases {
		out := asciiLowerString(in)
		if len(out) != len(in) {
			t.Errorf("length changed for %q: %d -> %d", in, len(in), len(out))
		}
	}
	if asciiLowerString("CREATE Policy İ") != "create policy İ" {
		t.Errorf("ASCII not lowercased / non-ASCII altered: %q", asciiLowerString("CREATE Policy İ"))
	}
}
