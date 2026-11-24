package mvcc

import (
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
)

func seed(t *testing.T, db *DB, rows ...Row) {
	t.Helper()
	tx := db.Begin()
	for _, row := range rows {
		if err := tx.Insert(row.ID, row.Value); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, tx *Tx, key int64, value string, found bool) {
	t.Helper()
	got, ok, err := tx.Get(key)
	if err != nil || ok != found || (ok && got != value) {
		t.Fatalf("key %d: got %q/%v/%v want %q/%v", key, got, ok, err, value, found)
	}
}

func TestSnapshotAndOwnWrites(t *testing.T) {
	var db DB
	seed(t, &db, Row{1, "before"})
	old := db.Begin()
	writer := db.Begin()
	if ok, err := writer.Update(1, "after"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := writer.Insert(2, "new"); err != nil {
		t.Fatal(err)
	}
	expect(t, writer, 1, "after", true)
	expect(t, old, 1, "before", true)
	expect(t, old, 2, "", false)
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, old, 1, "before", true)
	expect(t, old, 2, "", false)
	rows, err := old.Scan()
	if err != nil || !reflect.DeepEqual(rows, []Row{{1, "before"}}) {
		t.Fatal(rows, err)
	}
	fresh := db.Begin()
	defer fresh.Rollback()
	expect(t, fresh, 1, "after", true)
	expect(t, fresh, 2, "new", true)
	if err := old.Insert(-1, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Delete(1); err != nil {
		t.Fatal(err)
	}
	rows, err = old.Scan()
	if err != nil || !reflect.DeepEqual(rows, []Row{{-1, "local"}}) {
		t.Fatal(rows, err)
	}
	if err := old.Rollback(); err != nil {
		t.Fatal(err)
	}
	expect(t, fresh, -1, "", false)
}

func TestAtomicConflictAndClosedState(t *testing.T) {
	var db DB
	seed(t, &db, Row{1, "base"}, Row{2, "untouched"})
	a, b := db.Begin(), db.Begin()
	a.Update(1, "winner")
	b.Update(1, "loser")
	b.Update(2, "must not publish")
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	read := db.Begin()
	defer read.Rollback()
	expect(t, read, 1, "winner", true)
	expect(t, read, 2, "untouched", true)
	if _, _, err := b.Get(1); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := b.Scan(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := b.Insert(3, "x"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := b.Update(1, "x"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := b.Delete(1); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := b.Commit(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := b.Rollback(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestInsertConflictTombstonesAndMissingRows(t *testing.T) {
	var db DB
	a, b := db.Begin(), db.Begin()
	if err := a.Insert(1, "a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Insert(1, "b"); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	old := db.Begin()
	defer old.Rollback()
	del := db.Begin()
	del.Delete(1)
	if err := del.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, old, 1, "a", true)
	fresh := db.Begin()
	if ok, err := fresh.Update(1, "no"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := fresh.Delete(1); ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := fresh.Insert(1, "reborn"); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Insert(1, "duplicate"); !errors.Is(err, ErrDuplicateKey) {
		t.Fatal(err)
	}
	if err := fresh.Commit(); err != nil {
		t.Fatal(err)
	}
	read := db.Begin()
	defer read.Rollback()
	expect(t, read, 1, "reborn", true)
}

// Snapshot isolation intentionally permits write skew. Two transactions see
// two on-call people, then each turns off a different person and both commit.
func TestWriteSkewIsDocumentedLimit(t *testing.T) {
	var db DB
	seed(t, &db, Row{1, "on"}, Row{2, "on"})
	a, b := db.Begin(), db.Begin()
	expect(t, a, 2, "on", true)
	expect(t, b, 1, "on", true)
	a.Update(1, "off")
	b.Update(2, "off")
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	c := db.Begin()
	defer c.Rollback()
	expect(t, c, 1, "off", true)
	expect(t, c, 2, "off", true)
}

func TestRollbackDoesNotAdvanceClockAndOverflowIsAtomic(t *testing.T) {
	var db DB
	tx := db.Begin()
	tx.Insert(1, "rolled back")
	tx.Rollback()
	if db.clock != 0 {
		t.Fatal(db.clock)
	}
	read := db.Begin()
	if err := read.Commit(); err != nil {
		t.Fatal(err)
	}
	if db.clock != 0 {
		t.Fatal(db.clock)
	}
	db.clock = math.MaxUint64
	tx = db.Begin()
	tx.Insert(2, "never committed")
	if err := tx.Commit(); !errors.Is(err, ErrClockExhausted) {
		t.Fatal(err)
	}
	read = db.Begin()
	defer read.Rollback()
	expect(t, read, 2, "", false)
}

func TestConcurrentDisjointWriters(t *testing.T) {
	var db DB
	const count = 64
	var ready, done sync.WaitGroup
	ready.Add(count)
	done.Add(count)
	start := make(chan struct{})
	for i := 0; i < count; i++ {
		go func(key int64) {
			defer done.Done()
			tx := db.Begin()
			ready.Done()
			<-start
			if err := tx.Insert(key, "value"); err != nil {
				t.Error(err)
				return
			}
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
		}(int64(i))
	}
	ready.Wait()
	close(start)
	done.Wait()
	tx := db.Begin()
	defer tx.Rollback()
	rows, err := tx.Scan()
	if err != nil || len(rows) != count {
		t.Fatal(len(rows), err)
	}
	for i, row := range rows {
		if row.ID != int64(i) {
			t.Fatal(row)
		}
	}
}

func TestConcurrentSameKeyOnlyOneWinner(t *testing.T) {
	var db DB
	a, b := db.Begin(), db.Begin()
	a.Insert(1, "a")
	b.Insert(1, "b")
	errs := make(chan error, 2)
	go func() { errs <- a.Commit() }()
	go func() { errs <- b.Commit() }()
	x, y := <-errs, <-errs
	if !((x == nil && errors.Is(y, ErrConflict)) || (y == nil && errors.Is(x, ErrConflict))) {
		t.Fatal(x, y)
	}
}
