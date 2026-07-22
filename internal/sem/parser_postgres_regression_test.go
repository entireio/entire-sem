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

// Declarative partitioning the pgsql grammar rejects: `PARTITION BY {RANGE|LIST|
// HASH} (...)` trails a column list, and `PARTITION OF parent FOR VALUES ...`
// replaces the column list. Both used to leave tree-sitter error nodes and drop
// the table (postgres .sql failures). The masks blank/substitute the partition
// clause while the real table symbol is still extracted from the original bytes.
func TestPostgresDeclarativePartitioningParses(t *testing.T) {
	src := "CREATE TABLE measurement (city_id int, logdate date) PARTITION BY RANGE (logdate);\n" +
		"CREATE TABLE measurement_y2026 PARTITION OF measurement FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error on partitioned tables: %s", status.Detail)
	}
	for _, name := range []string{"measurement", "measurement_y2026", "after_tbl"} {
		if countEntity(entities, "table", name) != 1 {
			t.Errorf("table %s missing/duplicated (partition clause not masked): %+v", name, entities)
		}
	}
}

// COPY is a psql/dump construct the grammar rejects. `COPY t (...) FROM stdin;`
// is followed by tab-delimited data terminated by a `\.` line. Masking the COPY
// statement (and the stdin data block) keeps following statements parseable.
func TestPostgresCopyFromStdinParses(t *testing.T) {
	src := "COPY users (id, name) FROM stdin;\n1\tAlice\n2\tBob\n\\.\n" +
		"CREATE TABLE after_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("schema.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error on COPY block: %s", status.Detail)
	}
	if countEntity(entities, "table", "after_tbl") != 1 {
		t.Errorf("after_tbl dropped after COPY block: %+v", entities)
	}
}

// CREATE PROCEDURE must yield a symbol like CREATE FUNCTION does. TimescaleDB
// sql/ddl_api.sql and sql/maintenance_utils.sql define procedures with the
// @extschema@ placeholder and LANGUAGE C bodies (`AS '@MODULE_PATHNAME@',
// 'ts_...'`), and schema-qualified procedures under _timescaledb_functions
// (whose schema name contains the substring "function" — the old extractor
// keyed off that substring and produced a garbage name).
func TestPostgresCreateProcedureExtracted(t *testing.T) {
	src := "CREATE OR REPLACE PROCEDURE @extschema@.refresh_continuous_aggregate(\n" +
		"    continuous_aggregate     REGCLASS,\n" +
		"    window_start             \"any\",\n" +
		"    window_end               \"any\",\n" +
		"    force                    BOOLEAN = FALSE,\n" +
		"    options                  JSONB = NULL\n" +
		") LANGUAGE C AS '@MODULE_PATHNAME@', 'ts_continuous_agg_refresh';\n" +
		"\n" +
		"CREATE OR REPLACE PROCEDURE _timescaledb_functions.rebuild_columnstore(\n" +
		"    chunk REGCLASS\n" +
		") AS '@MODULE_PATHNAME@', 'ts_rebuild_columnstore' LANGUAGE C;\n" +
		"\n" +
		"CREATE OR REPLACE PROCEDURE plpgsql_proc() LANGUAGE plpgsql AS $$ BEGIN NULL; END $$;\n"
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("ddl_api.sql", src)
	for _, name := range []string{
		"@extschema@.refresh_continuous_aggregate",
		"_timescaledb_functions.rebuild_columnstore",
		"plpgsql_proc",
	} {
		if countEntity(entities, "function", name) != 1 {
			t.Errorf("procedure %s missing/duplicated: %+v", name, entities)
		}
	}
	if countEntity(entities, "function", "s.rebuild_columnstore") != 0 {
		t.Errorf("garbage name from 'function' substring inside schema name: %+v", entities)
	}
}

// A dollar-quoted function body followed by `SET search_path TO ...;` (instead
// of `LANGUAGE ...;`) must still terminate the statement. TimescaleDB ends most
// plpgsql bodies with `$BODY$ SET search_path TO pg_catalog, pg_temp;`
// (sql/chunk_constraint.sql, sql/size_utils.sql); the body also nests `$$ ... $$`
// dollar quotes, which must not close the statement early.
func TestPostgresFunctionBodyWithSetSearchPathSuffix(t *testing.T) {
	src := "CREATE OR REPLACE FUNCTION _timescaledb_functions.chunk_constraint_add_table_constraint(\n" +
		"    chunk_id integer,\n" +
		"    constraint_name name\n" +
		")\n" +
		"    RETURNS VOID LANGUAGE PLPGSQL AS\n" +
		"$BODY$\n" +
		"BEGIN\n" +
		"    EXECUTE pg_catalog.format(\n" +
		"        $$ ALTER TABLE %I.%I ADD CONSTRAINT %I %s $$,\n" +
		"        chunk_row.schema_name, chunk_row.table_name, constraint_name, def\n" +
		"    );\n" +
		"END\n" +
		"$BODY$ SET search_path TO pg_catalog, pg_temp;\n" +
		"\n" +
		"CREATE OR REPLACE FUNCTION @extschema@.hypertable_detailed_size(\n" +
		"    hypertable              REGCLASS)\n" +
		"RETURNS TABLE (table_bytes BIGINT)\n" +
		"LANGUAGE PLPGSQL VOLATILE STRICT AS\n" +
		"$BODY$\n" +
		"BEGIN\n" +
		"        RETURN QUERY SELECT 1::bigint;\n" +
		"END;\n" +
		"$BODY$ SET search_path TO pg_catalog, pg_temp;\n"
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("size_utils.sql", src)
	for _, name := range []string{
		"_timescaledb_functions.chunk_constraint_add_table_constraint",
		"@extschema@.hypertable_detailed_size",
	} {
		if countEntity(entities, "function", name) != 1 {
			t.Errorf("function %s missing/duplicated (SET search_path suffix): %+v", name, entities)
		}
	}
}

// A dollar-quoted function that follows LANGUAGE C (`AS '...', '...'`) functions
// in the same file must not be swallowed: the old create-function pattern let
// `.*?` run across statement boundaries, so the match started at the first
// LANGUAGE C `CREATE FUNCTION` and ended at the later dollar-quoted body,
// attributing the whole span to the first name (timescaledb sql/time_bucket.sql:
// align_to_bucket was only found in an unrelated updates/ script).
func TestPostgresDollarBodyAfterExternalFunctionsNotSwallowed(t *testing.T) {
	src := "CREATE OR REPLACE FUNCTION @extschema@.time_bucket(bucket_width SMALLINT, ts SMALLINT) RETURNS SMALLINT\n" +
		"\tAS '@MODULE_PATHNAME@', 'ts_int16_bucket' LANGUAGE C IMMUTABLE PARALLEL SAFE STRICT;\n" +
		"CREATE OR REPLACE FUNCTION @extschema@.time_bucket(bucket_width INT, ts INT) RETURNS INT\n" +
		"\tAS '@MODULE_PATHNAME@', 'ts_int32_bucket' LANGUAGE C IMMUTABLE PARALLEL SAFE STRICT;\n" +
		"\n" +
		"CREATE OR REPLACE FUNCTION _timescaledb_functions.align_to_bucket(width interval, rng anyrange)\n" +
		"RETURNS anyrange AS\n" +
		"$body$\n" +
		"BEGIN\n" +
		"  RETURN @extschema@.time_bucket(width, lower(rng));\n" +
		"END\n" +
		"$body$\n" +
		"LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE\n" +
		"SET search_path TO pg_catalog, pg_temp;\n"
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("time_bucket.sql", src)
	if countEntity(entities, "function", "_timescaledb_functions.align_to_bucket") != 1 {
		t.Errorf("align_to_bucket missing/duplicated (swallowed by cross-statement match): %+v", entities)
	}
	if countEntity(entities, "function", "@extschema@.time_bucket") != 2 {
		t.Errorf("time_bucket overloads should extract twice: %+v", entities)
	}
}

// `SET search_path TO schema, public;` (the TO form with a comma list, standard
// in goose migrations) must mask away entirely — including the terminating
// `;`, which the grammar rejects as an empty statement — so surrounding DDL
// still extracts (entirehq/entire-api migrations).
func TestPostgresSetSearchPathToListMasked(t *testing.T) {
	src := "CREATE SCHEMA IF NOT EXISTS repo;\n" +
		"SET search_path TO repo, public;\n" +
		"CREATE TABLE repos (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("001_init.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "repos") != 1 {
		t.Errorf("repos missing after SET search_path TO list: %+v", entities)
	}
}

// ALTER DEFAULT PRIVILEGES is not in the grammar and must be masked.
func TestPostgresAlterDefaultPrivilegesMasked(t *testing.T) {
	src := "ALTER DEFAULT PRIVILEGES IN SCHEMA repo GRANT SELECT, INSERT, UPDATE ON TABLES TO repo_rw;\n" +
		"CREATE TABLE t (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("grants.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "t") != 1 {
		t.Errorf("t missing after ALTER DEFAULT PRIVILEGES: %+v", entities)
	}
}

// A column literally named `key` (a grammar keyword) must not derail the
// CREATE TABLE (entire-api repo_trail_monitor_results).
func TestPostgresKeyColumnNameParses(t *testing.T) {
	src := "CREATE TABLE repo_trail_monitor_results (\n" +
		"    id ulid PRIMARY KEY,\n" +
		"    key            TEXT NOT NULL,\n" +
		"    label          TEXT NOT NULL\n" +
		");\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("042.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "repo_trail_monitor_results") != 1 {
		t.Errorf("table missing with `key` column: %+v", entities)
	}
}

// Multi-name DROP statements are rejected at the comma and must be masked.
func TestPostgresMultiNameDropMasked(t *testing.T) {
	src := "DROP VIEW IF EXISTS commits_readable, repos_readable;\n" +
		"DROP TABLE IF EXISTS sync_state, sessions, repos;\n" +
		"DROP DOMAIN IF EXISTS git_sha, ulid;\n" +
		"DROP TYPE IF EXISTS mood, status;\n" +
		"DROP MATERIALIZED VIEW IF EXISTS daily_rollup, weekly_rollup;\n" +
		"DROP FOREIGN TABLE IF EXISTS remote_a, remote_b;\n" +
		"DROP COLLATION IF EXISTS natural_sort, numeric_sort;\n" +
		"DROP STATISTICS IF EXISTS users_stats, repos_stats;\n" +
		"CREATE TABLE survivor (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("down.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "survivor") != 1 {
		t.Errorf("survivor missing after multi-name drops: %+v", entities)
	}
}

// SQL templates stored in ordinary string literals must not trigger statement
// masks. The DROP-list regex used to begin inside the string and consume through
// the enclosing CREATE TABLE's semicolon, removing its closing quote/paren and
// causing the following table to disappear behind an E_PARSE_ERROR.
func TestPostgresDDLTextInsideStringDoesNotTriggerMasks(t *testing.T) {
	src := "CREATE TABLE templates (\n" +
		"    body text DEFAULT 'DROP TABLE a, b',\n" +
		"    escaped text DEFAULT E'it\\'s DROP TYPE mood, status'\n" +
		");\n" +
		"CREATE TABLE survivor (id int);\n"
	masked := maskPostgresUnsupportedSyntax(src)
	if !strings.Contains(masked, "'DROP TABLE a, b'") || !strings.Contains(masked, "E'it\\'s DROP TYPE mood, status'") {
		t.Fatalf("DDL text inside string was masked:\n%s", masked)
	}
	entities, _, status := TreeSitterParser{}.ParseWithStatus("templates.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	for _, name := range []string{"templates", "survivor"} {
		if countEntity(entities, "table", name) != 1 {
			t.Errorf("table %s missing after DDL string: %+v", name, entities)
		}
	}
}

// DDL-looking prose inside comments — constraint keywords after a comma,
// apostrophes, `partition of relation "x"` — must not trigger the statement
// masks: their replacements were stomping bytes of real statements
// (entire-api 023_github_pull_request_meta.sql, partitions.sql).
func TestPostgresCommentProseDoesNotTriggerMasks(t *testing.T) {
	src := "-- keyed by the placement's repo ULID + the PR number (stable, unique within a repo). Fed by\n" +
		"-- the consumer, whose producer is the same per-jurisdiction transport that\n" +
		"-- partitions rejects EVERY insert -- `no partition of relation \"my_activity\"\n" +
		"-- found for row` (SQLSTATE 23514) -- so a skipped bootstrap silently fails.\n" +
		"CREATE TABLE github_pull_request_meta (\n" +
		"    repo_id ulid NOT NULL,\n" +
		"    pr_number INT NOT NULL\n" +
		");\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("023.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "github_pull_request_meta") != 1 {
		t.Errorf("table missing after comment prose: %+v", entities)
	}
}

// DDL keywords inside a dollar-quoted body's string literals (`EXECUTE
// format('... PARTITION OF ...')`) must not re-trigger statement masks after
// the body itself is blanked: the partition-of replacement was writing
// `(partition_dummy int)` into the middle of the blanked body.
func TestPostgresDollarBodyStringDDLNotRemasked(t *testing.T) {
	src := "CREATE OR REPLACE FUNCTION usr.ensure_partition(start_date date, end_date date)\n" +
		"RETURNS void AS $$\n" +
		"DECLARE\n" +
		"    part_name text := 'my_activity_p' || to_char(start_date, 'YYYYMM');\n" +
		"BEGIN\n" +
		"    EXECUTE format(\n" +
		"        'CREATE TABLE IF NOT EXISTS usr.%I PARTITION OF usr.my_activity FOR VALUES FROM (%L) TO (%L)',\n" +
		"        part_name, start_date, end_date);\n" +
		"END;\n" +
		"$$ LANGUAGE plpgsql;\n" +
		"CREATE TABLE tail_tbl (id int);\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("partitions.sql", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	if countEntity(entities, "function", "usr.ensure_partition") != 1 {
		t.Errorf("ensure_partition missing: %+v", entities)
	}
	if countEntity(entities, "table", "tail_tbl") != 1 {
		t.Errorf("tail_tbl missing after dollar-body string DDL: %+v", entities)
	}
}
