package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	mvcc "github.com/Soumil-Makhija/sql-engine-mvcc"
	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/query"
	"github.com/Soumil-Makhija/sql-engine-mvcc/internal/runner"
)

func run(args []string, in io.Reader, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("minisql", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	file := flags.String("file", "", "read a SQL script instead of stdin")
	flags.Usage = func() {
		fmt.Fprintln(diagnostic, "Usage: minisql [-file script.sql] < script.sql\nIn-memory kv(id INTEGER PRIMARY KEY, value TEXT); JSON lines on stdout.\nAll data disappears on exit. Scripts are limited to 8 MiB.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(diagnostic, "Unexpected positional argument")
		return 2
	}
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		defer f.Close()
		in = f
	}
	const limit = 8 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(in, limit+1))
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	if len(data) > limit {
		fmt.Fprintln(diagnostic, "SQL script exceeds 8 MiB")
		return 1
	}
	statements, err := query.ParseScript(string(data))
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	var db mvcc.DB
	session := runner.New(&db)
	defer session.Close()
	encoder := json.NewEncoder(out)
	for i, stmt := range statements {
		result, err := session.Execute(stmt)
		if err != nil {
			fmt.Fprintf(diagnostic, "Statement %d: %v\n", i+1, err)
			return 1
		}
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
	}
	if session.Close() {
		fmt.Fprintln(diagnostic, "Uncommitted transaction rolled back at end of script")
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
