package runner

import (
	"errors"
	"testing"

	mvcc "github.com/Soumil-Makhija/sql-engine-mvcc"
	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/query"
)

func exec(t *testing.T, s *Session, sql string) Result {
	t.Helper()
	stmt, err := query.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Execute(stmt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTwoSQLSessionsKeepSnapshots(t *testing.T) {
	var db mvcc.DB
	a, b := New(&db), New(&db)
	defer a.Close()
	defer b.Close()
	exec(t, a, "INSERT INTO kv VALUES(1,'before')")
	exec(t, a, "BEGIN")
	exec(t, b, "UPDATE kv SET value='after' WHERE id=1")
	if rows := exec(t, a, "SELECT * FROM kv WHERE id=1").Rows; len(rows) != 1 || rows[0].Value != "before" {
		t.Fatal(rows)
	}
	if result := exec(t, a, "EXPLAIN SELECT * FROM kv WHERE id=1"); result.Plan != "PrimaryKeyLookup" {
		t.Fatal(result)
	}
	exec(t, a, "UPDATE kv SET value='local' WHERE id=1")
	stmt, _ := query.Parse("COMMIT")
	if _, err := a.Execute(stmt); !errors.Is(err, mvcc.ErrConflict) {
		t.Fatal(err)
	}
	if rows := exec(t, a, "SELECT * FROM kv").Rows; len(rows) != 1 || rows[0].Value != "after" {
		t.Fatal(rows)
	}
}

func TestSessionErrorsRollbackAndAffectedRows(t *testing.T) {
	var db mvcc.DB
	s := New(&db)
	for _, sql := range []string{"COMMIT", "ROLLBACK"} {
		stmt, _ := query.Parse(sql)
		if _, err := s.Execute(stmt); err == nil {
			t.Fatal(sql)
		}
	}
	exec(t, s, "BEGIN")
	stmt, _ := query.Parse("BEGIN")
	if _, err := s.Execute(stmt); err == nil {
		t.Fatal("nested begin")
	}
	if r := exec(t, s, "INSERT INTO kv VALUES(-2,'x')"); r.Affected != 1 {
		t.Fatal(r)
	}
	if r := exec(t, s, "UPDATE kv SET value='x' WHERE id=99"); r.Affected != 0 {
		t.Fatal(r)
	}
	if r := exec(t, s, "DELETE FROM kv WHERE id=99"); r.Affected != 0 {
		t.Fatal(r)
	}
	if !s.Close() || s.Close() {
		t.Fatal("close rollback status")
	}
	if rows := exec(t, s, "SELECT * FROM kv").Rows; len(rows) != 0 {
		t.Fatal(rows)
	}
	if r := exec(t, s, "EXPLAIN SELECT * FROM kv"); r.Plan != "FullScan" {
		t.Fatal(r)
	}
}
