package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/runner"
)

func TestScriptReadYourWritesAndRollback(t *testing.T) {
	sql := `INSERT INTO kv VALUES (1,'original'); BEGIN;
	UPDATE kv SET value='pending' WHERE id=1; SELECT * FROM kv;
	ROLLBACK; SELECT * FROM kv WHERE id=1;`
	var out, diagnostic bytes.Buffer
	if code := run(nil, strings.NewReader(sql), &out, &diagnostic); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	decoder := json.NewDecoder(&out)
	var results []runner.Result
	for {
		var result runner.Result
		if err := decoder.Decode(&result); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	if len(results) != 6 {
		t.Fatal(len(results))
	}
	if results[3].Rows[0].Value != "pending" || results[5].Rows[0].Value != "original" {
		t.Fatal(results)
	}
}

func TestFailuresAndParseBeforeExecution(t *testing.T) {
	for _, sql := range []string{"INSERT INTO kv VALUES(1,'a'); GARBAGE;", "BEGIN; INSERT INTO kv VALUES(1,'a');", "INSERT INTO kv VALUES(1,'a');INSERT INTO kv VALUES(1,'b');", "COMMIT;"} {
		var out, diagnostic bytes.Buffer
		if code := run(nil, strings.NewReader(sql), &out, &diagnostic); code != 1 || diagnostic.Len() == 0 {
			t.Fatal(sql, code, diagnostic.String())
		}
		if strings.Contains(sql, "GARBAGE") && out.Len() != 0 {
			t.Fatal("syntax error must precede execution")
		}
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"-bad"}, strings.NewReader(""), &out, &diagnostic); code != 2 {
		t.Fatal(code)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestOutputFailure(t *testing.T) {
	var diagnostic bytes.Buffer
	if code := run(nil, strings.NewReader("SELECT * FROM kv"), brokenWriter{}, &diagnostic); code != 1 {
		t.Fatal(code)
	}
}
