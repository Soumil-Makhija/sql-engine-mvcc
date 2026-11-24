// Package query parses the engine's deliberately small SQL dialect and selects
// a physical access path. The fixed kv(id INTEGER PRIMARY KEY, value TEXT) table
// exists implicitly; DDL, expressions and arbitrary table names are unsupported.
package query

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxInputBytes bounds both single statements and complete scripts.
const MaxInputBytes = 8 << 20

type Kind string

const (
	Begin    Kind = "begin"
	Commit   Kind = "commit"
	Rollback Kind = "rollback"
	Select   Kind = "select"
	Insert   Kind = "insert"
	Update   Kind = "update"
	Delete   Kind = "delete"
	Explain  Kind = "explain"
)

type Statement struct {
	Kind  Kind
	Key   *int64
	Value string
	Inner *Statement
}

// Plan names the access path, not an estimate of runtime or row count.
func (s Statement) Plan() string {
	switch s.Kind {
	case Select:
		if s.Key != nil {
			return "PrimaryKeyLookup"
		}
		return "FullScan"
	case Insert, Update, Delete:
		return "PrimaryKeyWrite"
	case Begin, Commit, Rollback:
		return "TransactionControl"
	case Explain:
		if s.Inner != nil && s.Inner.Kind == Select {
			return s.Inner.Plan()
		}
	}
	return "Unsupported"
}

// Parse accepts one statement, with at most one trailing semicolon. Comments and
// whitespace may follow that semicolon. Use ParseScript for multiple statements.
func Parse(input string) (Statement, error) {
	p, err := newParser(input)
	if err != nil {
		return Statement{}, err
	}
	s, err := p.statement()
	if err != nil {
		return Statement{}, err
	}
	if p.current.kind == semicolon {
		p.advance()
	}
	if p.err != nil {
		return Statement{}, p.err
	}
	if p.current.kind != end {
		return Statement{}, p.errorAt(p.current.start, "expected end of statement; use ParseScript for a script")
	}
	return s, nil
}

// ParseScript parses semicolon-separated statements. Empty statements, blank
// scripts and -- comments are accepted. Semicolons inside literals are text.
func ParseScript(input string) ([]Statement, error) {
	p, err := newParser(input)
	if err != nil {
		return nil, err
	}
	statements := make([]Statement, 0)
	for p.err == nil && p.current.kind != end {
		if p.current.kind == semicolon {
			p.advance()
			continue
		}
		s, err := p.statement()
		if err != nil {
			return nil, err
		}
		statements = append(statements, s)
		if p.current.kind != end && p.current.kind != semicolon {
			return nil, p.errorAt(p.current.start, "expected ';' between statements")
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return statements, nil
}

type tokenKind uint8

const (
	end tokenKind = iota
	word
	number
	text
	star
	equals
	leftParen
	rightParen
	comma
	semicolon
)

type token struct {
	kind  tokenKind
	text  string
	start int
}

type parser struct {
	input   string
	pos     int
	current token
	err     error
}

func newParser(input string) (*parser, error) {
	if len(input) > MaxInputBytes {
		return nil, fmt.Errorf("SQL exceeds %d-byte input limit", MaxInputBytes)
	}
	if !utf8.ValidString(input) {
		return nil, fmt.Errorf("SQL must be valid UTF-8")
	}
	p := &parser{input: input}
	p.advance()
	return p, p.err
}

// Locations count bytes, so they remain precise even for non-ASCII literals.
func (p *parser) errorAt(offset int, message string) error {
	line := strings.Count(p.input[:offset], "\n") + 1
	lastNewline := strings.LastIndexByte(p.input[:offset], '\n')
	return fmt.Errorf("SQL line %d, byte %d (offset %d): %s", line, offset-lastNewline, offset, message)
}

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *parser) advance() {
	if p.err != nil {
		return
	}
	for p.pos < len(p.input) {
		switch p.input[p.pos] {
		case ' ', '\t', '\r', '\n', '\f', '\v':
			p.pos++
			continue
		case '-':
			if p.pos+1 < len(p.input) && p.input[p.pos+1] == '-' {
				for p.pos < len(p.input) && p.input[p.pos] != '\n' {
					p.pos++
				}
				continue
			}
		}
		break
	}
	start := p.pos
	if start == len(p.input) {
		p.current = token{kind: end, start: start}
		return
	}
	c := p.input[p.pos]
	p.pos++
	punctuation := map[byte]tokenKind{'*': star, '=': equals, '(': leftParen, ')': rightParen, ',': comma, ';': semicolon}
	if kind, ok := punctuation[c]; ok {
		p.current = token{kind: kind, start: start}
		return
	}
	if isLetter(c) {
		for p.pos < len(p.input) && (isLetter(p.input[p.pos]) || isDigit(p.input[p.pos])) {
			p.pos++
		}
		p.current = token{kind: word, text: p.input[start:p.pos], start: start}
		return
	}
	if isDigit(c) || (c == '-' || c == '+') && p.pos < len(p.input) && isDigit(p.input[p.pos]) {
		for p.pos < len(p.input) && isDigit(p.input[p.pos]) {
			p.pos++
		}
		p.current = token{kind: number, text: p.input[start:p.pos], start: start}
		return
	}
	if c == '\'' {
		var value strings.Builder
		for p.pos < len(p.input) {
			if p.input[p.pos] != '\'' {
				value.WriteByte(p.input[p.pos])
				p.pos++
				continue
			}
			p.pos++
			if p.pos < len(p.input) && p.input[p.pos] == '\'' {
				value.WriteByte('\'')
				p.pos++
				continue
			}
			p.current = token{kind: text, text: value.String(), start: start}
			return
		}
		p.err = p.errorAt(start, "unterminated single-quoted text literal")
		return
	}
	p.err = p.errorAt(start, "unsupported character; use single quotes for text and the documented SQL subset")
}

func (p *parser) keyword(want string) bool {
	return p.current.kind == word && strings.EqualFold(p.current.text, want)
}

func (p *parser) expectKeyword(want string) {
	if p.err != nil {
		return
	}
	if !p.keyword(want) {
		p.err = p.errorAt(p.current.start, "expected "+want)
		return
	}
	p.advance()
}

func (p *parser) expect(kind tokenKind, description string) {
	if p.err != nil {
		return
	}
	if p.current.kind != kind {
		p.err = p.errorAt(p.current.start, "expected "+description)
		return
	}
	p.advance()
}

func (p *parser) key() *int64 {
	if p.err != nil {
		return nil
	}
	if p.current.kind != number {
		p.err = p.errorAt(p.current.start, "expected signed 64-bit integer id")
		return nil
	}
	value, err := strconv.ParseInt(p.current.text, 10, 64)
	if err != nil {
		p.err = p.errorAt(p.current.start, "integer id is outside the signed 64-bit range")
		return nil
	}
	p.advance()
	return &value
}

func (p *parser) value() string {
	if p.err != nil {
		return ""
	}
	if p.current.kind != text {
		p.err = p.errorAt(p.current.start, "expected single-quoted text value")
		return ""
	}
	value := p.current.text
	p.advance()
	return value
}

func (p *parser) where() *int64 {
	p.expectKeyword("WHERE")
	p.expectKeyword("id")
	p.expect(equals, "'='")
	return p.key()
}

func (p *parser) selectStatement() Statement {
	p.expectKeyword("SELECT")
	p.expect(star, "'*'")
	p.expectKeyword("FROM")
	p.expectKeyword("kv")
	s := Statement{Kind: Select}
	if p.keyword("WHERE") {
		s.Key = p.where()
	}
	return s
}

func (p *parser) statement() (Statement, error) {
	if p.err != nil {
		return Statement{}, p.err
	}
	var s Statement
	switch {
	case p.keyword("SELECT"):
		s = p.selectStatement()
	case p.keyword("INSERT"):
		s.Kind = Insert
		p.advance()
		p.expectKeyword("INTO")
		p.expectKeyword("kv")
		if p.current.kind == leftParen {
			p.advance()
			p.expectKeyword("id")
			p.expect(comma, "','")
			p.expectKeyword("value")
			p.expect(rightParen, "')'")
		}
		p.expectKeyword("VALUES")
		p.expect(leftParen, "'('")
		s.Key = p.key()
		p.expect(comma, "','")
		s.Value = p.value()
		p.expect(rightParen, "')'")
	case p.keyword("UPDATE"):
		s.Kind = Update
		p.advance()
		p.expectKeyword("kv")
		p.expectKeyword("SET")
		p.expectKeyword("value")
		p.expect(equals, "'='")
		s.Value = p.value()
		s.Key = p.where()
	case p.keyword("DELETE"):
		s.Kind = Delete
		p.advance()
		p.expectKeyword("FROM")
		p.expectKeyword("kv")
		s.Key = p.where()
	case p.keyword("BEGIN"):
		s.Kind = Begin
		p.advance()
		if p.keyword("TRANSACTION") {
			p.advance()
		}
	case p.keyword("COMMIT"):
		s.Kind = Commit
		p.advance()
	case p.keyword("ROLLBACK"):
		s.Kind = Rollback
		p.advance()
	case p.keyword("EXPLAIN"):
		s.Kind = Explain
		p.advance()
		inner := p.selectStatement()
		s.Inner = &inner
	default:
		return Statement{}, p.errorAt(p.current.start, "expected SELECT, INSERT, UPDATE, DELETE, BEGIN, COMMIT, ROLLBACK or EXPLAIN SELECT")
	}
	if p.err != nil {
		return Statement{}, p.err
	}
	return s, nil
}
