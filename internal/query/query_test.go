package query

import (
	"math"
	"strings"
	"testing"
)

func TestParseDialectAndPlans(t *testing.T) {
	tests := []struct {
		sql   string
		kind  Kind
		key   *int64
		value string
		plan  string
	}{
		{"SELECT * FROM kv", Select, nil, "", "FullScan"},
		{"sElEcT * fRoM KV wHeRe ID = -42; -- done", Select, ptr(-42), "", "PrimaryKeyLookup"},
		{"INSERT INTO kv VALUES (7, 'hello')", Insert, ptr(7), "hello", "PrimaryKeyWrite"},
		{"INSERT INTO kv (ID, VALUE) VALUES (+8, '')", Insert, ptr(8), "", "PrimaryKeyWrite"},
		{"UPDATE kv SET value='O''Brien; -- literal' WHERE id=0;", Update, ptr(0), "O'Brien; -- literal", "PrimaryKeyWrite"},
		{"DELETE FROM kv WHERE id=-9223372036854775808", Delete, ptr(math.MinInt64), "", "PrimaryKeyWrite"},
		{"SELECT * FROM kv WHERE id=9223372036854775807", Select, ptr(math.MaxInt64), "", "PrimaryKeyLookup"},
		{"BEGIN", Begin, nil, "", "TransactionControl"},
		{"begin transaction;", Begin, nil, "", "TransactionControl"},
		{"COMMIT;", Commit, nil, "", "TransactionControl"},
		{"ROLLBACK", Rollback, nil, "", "TransactionControl"},
		{"EXPLAIN SELECT * FROM kv", Explain, nil, "", "FullScan"},
		{"EXPLAIN SELECT * FROM kv WHERE id=3", Explain, nil, "", "PrimaryKeyLookup"},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			s, err := Parse(tt.sql)
			if err != nil {
				t.Fatal(err)
			}
			if s.Kind != tt.kind || s.Value != tt.value || s.Plan() != tt.plan {
				t.Fatalf("got kind=%s value=%q plan=%s", s.Kind, s.Value, s.Plan())
			}
			if (s.Key == nil) != (tt.key == nil) || s.Key != nil && *s.Key != *tt.key {
				t.Fatalf("wrong primary key: %+v", s.Key)
			}
			if s.Kind == Explain && (s.Inner == nil || s.Inner.Kind != Select) {
				t.Fatal("EXPLAIN must preserve its inner SELECT")
			}
		})
	}
}

func TestRejectUnsupportedAndMalformedSQL(t *testing.T) {
	inputs := []string{
		"", " -- comment", ";", "SELECT * FROM kv;;", "SELECT * FROM kv; SELECT * FROM kv",
		"SELECT id FROM kv", "SELECT * FROM other", "SELECT * FROM kv junk", "SELECT * FROM kv WHERE value='x'",
		"SELECT * FROM kv WHERE id=1 OR id=2", "SELECT * FROM kv WHERE id=1.5", "SELECT * FROM kv WHERE id=+ 1",
		"SELECT * FROM kv WHERE id=9223372036854775808", "SELECT * FROM kv WHERE id=-9223372036854775809",
		"INSERT INTO kv VALUES (1, \"text\")", "INSERT INTO kv VALUES (1, 'unterminated)",
		"INSERT INTO kv VALUES (1, 2)", "INSERT INTO kv VALUES (1, 'one'), (2, 'two')",
		"INSERT INTO kv(value,id) VALUES ('one',1)", "INSERT INTO kv(id,bogus) VALUES (1,'x')",
		"INSERT INTO kv VALUES(1,'x') RETURNING *", "UPDATE kv SET value='x'", "DELETE FROM kv",
		"UPDATE kv SET id=2 WHERE id=1", "DELETE FROM kv WHERE id != 1", "EXPLAIN DELETE FROM kv WHERE id=1",
		"EXPLAIN EXPLAIN SELECT * FROM kv", "CREATE TABLE kv(id INTEGER)", "BEGIN IMMEDIATE", "COMMIT TRANSACTION",
		"SELECT * FROM kv /* comment */", "SELECT * FROM kv WHERE id=1abc", "SELECT * FROM kv WHERE id=NULL",
		"INSERT INTO kv VALUES (1,'x' 'y')", "SELECT * FROM kv WHERE id=--1\n", "SELECT * FROM kv ORDER BY id",
	}
	for _, sql := range inputs {
		t.Run(sql, func(t *testing.T) {
			if _, err := Parse(sql); err == nil {
				t.Fatalf("accepted unsupported SQL %q", sql)
			}
		})
	}
}

func TestParseScriptPreservesTextAndComments(t *testing.T) {
	script := `-- leading comment; with fake SQL
; BEGIN TRANSACTION;
INSERT INTO kv(id,value) VALUES(-1, 'snow 雪; O''Brien -- text'); -- real ; comment
UPDATE kv SET value='first
second' WHERE id=-1;
SELECT * FROM kv WHERE id=-1; COMMIT;; -- trailing comment`
	statements, err := ParseScript(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 5 {
		t.Fatalf("got %d statements", len(statements))
	}
	if statements[1].Value != "snow 雪; O'Brien -- text" || statements[2].Value != "first\nsecond" {
		t.Fatalf("literal text changed: %#v", statements)
	}
	if statements[0].Kind != Begin || statements[4].Kind != Commit {
		t.Fatal("transaction statements lost")
	}
	for _, input := range []string{"", " \n -- comment only", ";;;;"} {
		s, err := ParseScript(input)
		if err != nil || len(s) != 0 {
			t.Fatalf("empty script: %v %v", s, err)
		}
	}
}

func TestScriptsRejectMissingSeparatorAndPartialInput(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM kv SELECT * FROM kv", "BEGIN; INSERT INTO kv VALUES(1,'x'); COMMIT junk",
		"SELECT * FROM kv; INSERT INTO kv VALUES(1,'x", "SELECT * FROM kv; ?", "BEGIN; DELETE FROM kv; COMMIT",
	} {
		if statements, err := ParseScript(sql); err == nil || statements != nil {
			t.Fatalf("invalid script returned partial success: %#v %v", statements, err)
		}
	}
}

func TestErrorLocationsAndLimit(t *testing.T) {
	_, err := Parse("-- leading\nSELECT * FROM kv WHERE id=999999999999999999999")
	if err == nil || !strings.Contains(err.Error(), "line 2, byte 27 (offset 37)") {
		t.Fatalf("unexpected location: %v", err)
	}
	large := strings.Repeat(" ", MaxInputBytes+1)
	if _, err := Parse(large); err == nil || !strings.Contains(err.Error(), "input limit") {
		t.Fatalf("expected size limit: %v", err)
	}
	if _, err := ParseScript(large); err == nil {
		t.Fatal("script input limit missing")
	}
	if _, err := ParseScript(large[:MaxInputBytes]); err != nil {
		t.Fatalf("exactly bounded input should be valid: %v", err)
	}
}

func TestUnsupportedConstructedPlan(t *testing.T) {
	for _, s := range []Statement{{}, {Kind: Explain}, {Kind: Explain, Inner: &Statement{Kind: Delete}}} {
		if s.Plan() != "Unsupported" {
			t.Fatalf("invalid AST had executable plan: %#v", s)
		}
	}
}

func ptr(value int64) *int64 { return &value }
