// Package mvcc implements an in-memory integer-key/text-value database with
// snapshot isolation. It has no disk durability or SQL compatibility promise.
package mvcc

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/btree"
)

var (
	ErrClosed         = errors.New("transaction is closed")
	ErrDuplicateKey   = errors.New("duplicate primary key")
	ErrConflict       = errors.New("snapshot write conflict; transaction aborted")
	ErrClockExhausted = errors.New("commit timestamp exhausted")
	ErrInvalidText    = errors.New("text must be valid UTF-8")
)

type version struct {
	ts      uint64
	value   string
	deleted bool
}

type pending struct {
	value   string
	deleted bool
}

// DB's zero value is ready to use. A mutex serializes engine operations, not
// transaction lifetimes. Transactions can overlap and keep different snapshots.
type DB struct {
	mu    sync.Mutex
	clock uint64
	index btree.Tree[[]version]
}

// Tx sees commits up to its start timestamp plus its own pending writes.
// Methods are safe to call concurrently, but ordering within a shared Tx is
// determined by lock acquisition; use separate transactions for independent work.
type Tx struct {
	db       *DB
	snapshot uint64
	writes   map[int64]pending
	closed   bool
}

type Row struct {
	ID    int64  `json:"id"`
	Value string `json:"value"`
}

func (db *DB) Begin() *Tx {
	db.mu.Lock()
	defer db.mu.Unlock()
	return &Tx{db: db, snapshot: db.clock, writes: make(map[int64]pending)}
}

func visible(chain []version, snapshot uint64) (string, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].ts <= snapshot {
			return chain[i].value, !chain[i].deleted
		}
	}
	return "", false
}

func (tx *Tx) lookup(key int64) (string, bool) {
	if write, ok := tx.writes[key]; ok {
		return write.value, !write.deleted
	}
	chain, _ := tx.db.index.Get(key)
	return visible(chain, tx.snapshot)
}

func (tx *Tx) Get(key int64) (string, bool, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return "", false, ErrClosed
	}
	value, found := tx.lookup(key)
	return value, found, nil
}

// Scan returns the transaction's visible rows sorted by primary key. Pending
// inserts, updates and tombstones are merged with the B-tree snapshot view.
func (tx *Tx) Scan() ([]Row, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return nil, ErrClosed
	}
	merged := make(map[int64]string)
	tx.db.index.Ascend(func(key int64, chain []version) bool {
		if value, ok := visible(chain, tx.snapshot); ok {
			merged[key] = value
		}
		return true
	})
	for key, write := range tx.writes {
		if write.deleted {
			delete(merged, key)
		} else {
			merged[key] = write.value
		}
	}
	rows := make([]Row, 0, len(merged))
	for key, value := range merged {
		rows = append(rows, Row{ID: key, Value: value})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (tx *Tx) Insert(key int64, value string) error {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return ErrClosed
	}
	if !utf8.ValidString(value) {
		return ErrInvalidText
	}
	if _, exists := tx.lookup(key); exists {
		return fmt.Errorf("%w: %d", ErrDuplicateKey, key)
	}
	tx.writes[key] = pending{value: value}
	return nil
}

func (tx *Tx) Update(key int64, value string) (bool, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return false, ErrClosed
	}
	if !utf8.ValidString(value) {
		return false, ErrInvalidText
	}
	if _, exists := tx.lookup(key); !exists {
		return false, nil
	}
	tx.writes[key] = pending{value: value}
	return true, nil
}

func (tx *Tx) Delete(key int64) (bool, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return false, ErrClosed
	}
	if _, exists := tx.lookup(key); !exists {
		return false, nil
	}
	tx.writes[key] = pending{deleted: true}
	return true, nil
}

// Commit first validates every written key, then publishes every version at one
// new timestamp under the same lock. A conflict aborts the whole transaction.
// Reads and disjoint writes are not validated: snapshot isolation permits skew.
func (tx *Tx) Commit() error {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return ErrClosed
	}
	tx.closed = true
	defer func() { tx.writes = nil }()
	if len(tx.writes) == 0 {
		return nil
	}
	for key := range tx.writes {
		chain, found := tx.db.index.Get(key)
		if found && chain[len(chain)-1].ts > tx.snapshot {
			return fmt.Errorf("%w on key %d", ErrConflict, key)
		}
	}
	if tx.db.clock == math.MaxUint64 {
		return ErrClockExhausted
	}
	tx.db.clock++
	for key, write := range tx.writes {
		chain, _ := tx.db.index.Get(key)
		chain = append(chain, version{ts: tx.db.clock, value: write.value, deleted: write.deleted})
		tx.db.index.Set(key, chain)
	}
	return nil
}

func (tx *Tx) Rollback() error {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	if tx.closed {
		return ErrClosed
	}
	tx.closed = true
	tx.writes = nil
	return nil
}
