# Reproducible CLI comparison

Run from the repository root with Python 3, Go, and the `sqlite3` CLI installed:

```sh
make bench
```

The Make target compiles `build/minisql`, then runs the default workload: 2,000
inserts, 2,000 point lookups, and five measured repetitions per implementation.
To save the exact JSON printed to stdout:

```sh
python3 bench/compare_sqlite.py --binary build/minisql --output bench/observed.json
```

Use `--sqlite /path/to/sqlite3` to select a different SQLite executable. `--rows`
and `--lookups` each accept 1–20,000; `--repeats` accepts 1–10. Each child has a
30-second timeout and generated input is limited to 8 MiB. A small smoke run is:

```sh
python3 bench/compare_sqlite.py --rows 128 --lookups 128 --repeats 1
```

The script uses Python's seeded random generator with seed `20261124`. It inserts
unique integer keys in shuffled order inside one `BEGIN`/`COMMIT`, then performs
known-key `SELECT * FROM kv WHERE id=...` lookups inside another transaction.
Values are synthetic `value-00000001` strings. Both implementations receive the
same statements and order. SQLite additionally receives `CREATE TABLE kv(id
INTEGER PRIMARY KEY,value TEXT)` because MiniSQL's table exists implicitly.

Each implementation gets one unmeasured warmup before the repeated runs. Run
order alternates between implementations. Every warmup and measured run checks
all selected rows and their statement order against expected keys and values.
The decoder handles MiniSQL's JSONL statement records and SQLite's stream of
separate JSON arrays. Any child error, unexpected diagnostic, missing result, or
incorrect row aborts without a successful report.

The JSON records elapsed milliseconds for each run and each median, captured
stdout sizes, input sizes, correctness counts, seed, method, Go binary version,
SQLite version, Python version, and platform system/architecture. It does not
record a hostname. `--output` writes the same formatted JSON and trailing newline
as stdout; the script does not create parent directories.

These are **end-to-end subprocess measurements**, including startup, parsing,
insertion, transaction work, reads, serialization, pipe I/O, and exit. Decoding and
correctness validation happen after the timed interval. MiniSQL emits a JSON
record for inserts and transaction control as well as queries; SQLite emits query
arrays only. That output difference and the different front ends affect timing.
SQLite's table creation is timed too. This comparison does not isolate the
storage engines or support engine speed claims.

Both processes use in-memory databases. The workload measures no persistence,
recovery, disk durability, scans, updates, concurrency, or MVCC isolation behavior.
Host activity and scheduling can change results despite warmups and alternating
order. Retain the full JSON and environment metadata when discussing an observed
run, and describe it as this specific CLI workload.
