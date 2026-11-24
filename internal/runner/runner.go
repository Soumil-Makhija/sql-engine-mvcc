// Package runner connects the bounded SQL grammar to the MVCC engine.
package runner

import (
	"errors"

	mvcc "github.com/Soumil-Makhija/sql-engine-mvcc"
	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/query"
)

type Result struct {
	Statement query.Kind `json:"statement"`
	Plan      string     `json:"plan"`
	Rows      []mvcc.Row `json:"rows"`
	Affected  int        `json:"affected"`
}

// Session is single-threaded. The underlying DB can be shared by other sessions.
type Session struct {
	db     *mvcc.DB
	active *mvcc.Tx
}

func New(db *mvcc.DB) *Session { return &Session{db: db} }

// Close rolls back an unfinished explicit transaction and reports that fact.
func (s *Session) Close() bool {
	if s.active == nil {
		return false
	}
	s.active.Rollback()
	s.active = nil
	return true
}

func (s *Session) Execute(stmt query.Statement) (Result, error) {
	result := Result{Statement: stmt.Kind, Plan: stmt.Plan()}
	switch stmt.Kind {
	case query.Begin:
		if s.active != nil {
			return result, errors.New("nested BEGIN is unsupported")
		}
		s.active = s.db.Begin()
		return result, nil
	case query.Commit, query.Rollback:
		if s.active == nil {
			return result, errors.New("no active transaction")
		}
		tx := s.active
		s.active = nil
		if stmt.Kind == query.Commit {
			return result, tx.Commit()
		}
		return result, tx.Rollback()
	case query.Explain:
		return result, nil
	}
	tx := s.active
	auto := tx == nil
	if auto {
		tx = s.db.Begin()
	}
	var err error
	switch stmt.Kind {
	case query.Select:
		result.Rows = make([]mvcc.Row, 0)
		if stmt.Key == nil {
			result.Rows, err = tx.Scan()
		} else {
			var value string
			var exists bool
			value, exists, err = tx.Get(*stmt.Key)
			if exists {
				result.Rows = append(result.Rows, mvcc.Row{ID: *stmt.Key, Value: value})
			}
		}
	case query.Insert:
		err = tx.Insert(*stmt.Key, stmt.Value)
		if err == nil {
			result.Affected = 1
		}
	case query.Update:
		var changed bool
		changed, err = tx.Update(*stmt.Key, stmt.Value)
		if changed {
			result.Affected = 1
		}
	case query.Delete:
		var changed bool
		changed, err = tx.Delete(*stmt.Key)
		if changed {
			result.Affected = 1
		}
	default:
		err = errors.New("unsupported statement")
	}
	if auto {
		if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}
	return result, err
}
