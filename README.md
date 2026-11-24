# sql-engine-mvcc

A small Go database built from scratch: a SQL parser, an access-path planner,
a degree-8 B-tree index, and multi-version snapshot transactions. It stores one
fixed table, `kv(id INTEGER PRIMARY KEY, value TEXT)`, entirely in memory.

**This is a learning engine, not durable storage.** Data disappears when the
process exits. There are no disk pages, WAL, crash recovery, network server,
version garbage collection or compatibility guarantees. The custom B-tree holds
committed version chains; it is not a wrapper around a database or an ordered-map
library. The standard library is the only Go dependency.

## Run

Go 1.22 or newer:

```sh
make build
./build/minisql -file examples/demo.sql
printf "INSERT INTO kv VALUES (1, 'hello'); SELECT * FROM kv WHERE id=1;" | ./build/minisql
make test check
```

`minisql` reads a complete script from a file or stdin, parses it before executing
anything, and writes one JSON result per statement. It is a batch CLI, not an
interactive prompt. Maximum script size is 8 MiB. Values use single quotes and
double a quote to include it: `'it''s text'`. `--` comments and semicolons inside
strings are handled by the lexer. Keywords and the fixed table/column names are
case-insensitive. Keys must fit signed 64-bit integers; values are text only.
Scripts and text values must be valid UTF-8, so JSON output can preserve them.

Each statement outside an explicit transaction is autocommitted. A script error
stops execution; pending explicit work is rolled back. Ending a script with an
open transaction also rolls it back and exits with an error. A successful result
inside an explicit transaction is provisional until `COMMIT`. An earlier
autocommit is not undone by a later error, but all data still disappears on exit.
Exit codes: 0 success/help, 1 script/I/O error, 2 invalid CLI options.

## SQL subset and plans

The table exists implicitly; there is no `CREATE TABLE` command.

| Statement | Access path |
| --- | --- |
| `SELECT * FROM kv` | `FullScan`: B-tree traversal and visible-version filtering |
| `SELECT * FROM kv WHERE id = 1` | `PrimaryKeyLookup`: B-tree lookup |
| `INSERT INTO kv [(id, value)] VALUES (1, 'text')` | `PrimaryKeyWrite` |
| `UPDATE kv SET value = 'text' WHERE id = 1` | `PrimaryKeyWrite` |
| `DELETE FROM kv WHERE id = 1` | `PrimaryKeyWrite` |
| `BEGIN [TRANSACTION]`, `COMMIT`, `ROLLBACK` | `TransactionControl` |
| `EXPLAIN SELECT ...` | Returns the selected access path without running it |

Brackets in the table mean optional syntax, not literal SQL. `SELECT` results are
sorted by key. `INSERT` rejects a primary key visible to its transaction;
`UPDATE`/`DELETE` of an absent visible row affect zero rows. There are no joins,
aggregates, expressions, arbitrary predicates, `NULL`, secondary indexes,
parameters, quoted identifiers, DDL, range queries, or cost-based optimization.
`EXPLAIN` is supported for `SELECT` only and makes no runtime-cost estimates.

## Snapshot isolation

```go
var db mvcc.DB // import mvcc "github.com/Soumil-Makhija/sql-engine-mvcc"
seed := db.Begin()
seed.Insert(1, "before")
seed.Commit() // check errors in application code

reader := db.Begin()
writer := db.Begin()
writer.Update(1, "after")
writer.Commit()
value, _, _ := reader.Get(1) // "before": stable start-time snapshot
reader.Rollback()
```

Every transaction captures the current commit timestamp at `Begin`. Reads see
the newest committed version at or before that timestamp, plus their own pending
writes. Scans use the same visibility rule, so later inserts and deletes do not
change an existing reader's view. Deletes publish tombstones rather than removing
keys from the B-tree.

At commit, every written key is checked for a version newer than the transaction's
snapshot. If any conflict exists, the whole transaction aborts with `ErrConflict`;
no pending writes publish. Otherwise all its versions publish at one timestamp.
Read-only commits do not advance the clock. A closed transaction rejects further
operations. A duplicate insert is a statement error; the Go API leaves the
transaction open so the caller can decide to roll back. The CLI stops on errors.

**Snapshot isolation permits write skew.** Disjoint writes can both commit even
when the transactions read overlapping facts. The tests reproduce an on-call
example where both people become off-call. This is not serializable isolation,
and applications cannot rely on cross-row invariants without stronger control.
See the [PostgreSQL explanation of snapshot-style isolation and serialization
anomalies](https://www.postgresql.org/docs/current/transaction-iso.html) for context;
this engine implements its own smaller semantics, including a snapshot at `Begin`.

Engine operations use one mutex. Independent transactions overlap in lifetime,
but individual operations and commits execute serially under that lock. The Go
engine API is concurrency-safe; CLI sessions are single-threaded. Do not copy a
`DB` after first use. There is no transaction timeout, persistence or resource
quota beyond the CLI input limit. Historical versions accumulate until the DB is
discarded, including deleted keys. Long-running workloads need future version GC.

## Verify and inspect

```sh
go test ./...
go vet ./...
go test -race ./...
```

Tests exercise B-tree occupancy/order/equal leaf depth after 12,000-key ordered
and randomized insertions; replacement and early traversal stop; parser bounds,
quoting and malformed syntax; snapshot point reads/scans; own writes; rollback;
atomic multi-key conflicts; tombstones/reinsertion; concurrent writers; timestamp
exhaustion; and the intentional write-skew limit. CLI tests execute actual scripts
and check error handling. CI runs on macOS and Linux with Go 1.22.

Code: `engine.go` is the MVCC layer, `internal/btree` is the ordered index,
`internal/query` parses/plans SQL, and `internal/runner` connects SQL to transactions.
The planner is a small explicit choice between key lookup and full scan.

## SQLite comparison

With Python 3 and the `sqlite3` CLI installed:

```sh
make bench
python3 bench/compare_sqlite.py --binary build/minisql --output bench/observed.json
```

The [method and limits](bench/README.md) and [recorded local run](bench/observed.json)
are included. That run inserted 2,000 synthetic rows and performed 2,000 point
reads per process, with one warmup and five measured repetitions. All 12,000
selected rows per implementation were checked against known answers.

| CLI | Median whole-process elapsed time |
| --- | ---: |
| MiniSQL, Go 1.27.1, macOS arm64 | 12.111 ms |
| SQLite 3.43.2, macOS arm64 | 11.828 ms |

These numbers describe this particular in-memory CLI workload. They include
startup, parsing, inserts/commits, reads, JSON serialization and pipe I/O. MiniSQL
emits a record for every statement; SQLite emits query results only (361,207 vs
76,905 stdout bytes in this run). SQLite also creates its table in the timed
process. This does not isolate engine performance or compare durability and SQL
coverage. Host load changes the result; rerun rather than generalize the numbers.

## Next checkpoints

Persistent pages/WAL, crash recovery, garbage collection, richer SQL and stronger
isolation remain future work. No production durability is claimed.
