#!/usr/bin/env python3
"""Check a shared synthetic workload and measure whole-process CLI elapsed time."""

import argparse
import json
import platform
import random
import shutil
import statistics
import subprocess
import sys
import time
from pathlib import Path


SEED = 20261124
INPUT_LIMIT_BYTES = 8 * 1024 * 1024
CHILD_TIMEOUT_SECONDS = 30


def bounded_integer(lower, upper):
    def parse(value):
        try:
            number = int(value)
        except ValueError as error:
            raise argparse.ArgumentTypeError("must be an integer") from error
        if not lower <= number <= upper:
            raise argparse.ArgumentTypeError(f"must be between {lower} and {upper}")
        return number

    return parse


def workload(rows, lookups):
    rng = random.Random(SEED)
    keys = list(range(1, rows + 1))
    rng.shuffle(keys)
    selected = [rng.randrange(1, rows + 1) for _ in range(lookups)]
    values = {key: f"value-{key:08d}" for key in keys}
    statements = ["BEGIN;"]
    statements.extend(
        f"INSERT INTO kv (id, value) VALUES ({key}, '{values[key]}');"
        for key in keys
    )
    statements.extend(["COMMIT;", "BEGIN;"])
    statements.extend(f"SELECT * FROM kv WHERE id={key};" for key in selected)
    statements.append("COMMIT;")
    native = ("\n".join(statements) + "\n").encode("utf-8")
    sqlite = b"CREATE TABLE kv(id INTEGER PRIMARY KEY,value TEXT);\n" + native
    if max(len(native), len(sqlite)) > INPUT_LIMIT_BYTES:
        raise ValueError("generated SQL exceeds the 8 MiB input bound")
    expected = [{"id": key, "value": values[key]} for key in selected]
    return native, sqlite, expected


def native_rows(output, rows, lookups):
    records = [json.loads(line) for line in output.decode("utf-8").splitlines()]
    kinds = ["begin"] + ["insert"] * rows + ["commit", "begin"]
    kinds.extend(["select"] * lookups)
    kinds.append("commit")
    if len(records) != len(kinds):
        raise ValueError("minisql emitted the wrong number of statement records")
    selected = []
    for index, (record, kind) in enumerate(zip(records, kinds)):
        if not isinstance(record, dict) or record.get("statement") != kind:
            raise ValueError(f"minisql statement {index + 1} has an unexpected kind")
        if kind == "insert" and record.get("affected") != 1:
            raise ValueError(f"minisql insert {index + 1} did not affect one row")
        if kind == "select":
            selected.append(record.get("rows"))
    return selected


def sqlite_rows(output):
    # The sqlite3 CLI emits one JSON array per SELECT, not one JSON document.
    text = output.decode("utf-8")
    decoder = json.JSONDecoder()
    offset = 0
    selected = []
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        result, offset = decoder.raw_decode(text, offset)
        if not isinstance(result, list):
            raise ValueError("sqlite3 emitted a non-array SELECT result")
        selected.append(result)
    return selected


def check_selected(selected, expected, label):
    if len(selected) != len(expected):
        raise ValueError(
            f"{label} returned {len(selected)} SELECT results, expected {len(expected)}"
        )
    for index, (result, row) in enumerate(zip(selected, expected)):
        if result != [row]:
            raise ValueError(f"{label} returned an incorrect row at SELECT {index + 1}")


def execute(command, script, expected, rows, native, timed):
    started = time.perf_counter_ns() if timed else None
    child = subprocess.run(
        command,
        input=script,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=CHILD_TIMEOUT_SECONDS,
        check=False,
    )
    # Decoding and correctness checks are deliberately outside this interval.
    elapsed_ms = (time.perf_counter_ns() - started) / 1_000_000 if timed else None
    label = "minisql" if native else "sqlite3"
    if child.returncode != 0:
        detail = child.stderr.decode("utf-8", errors="replace").strip()[:2000]
        raise ValueError(f"{label} exited {child.returncode}: {detail}")
    if child.stderr:
        raise ValueError(f"{label} unexpectedly wrote diagnostics during the workload")
    selected = (
        native_rows(child.stdout, rows, len(expected))
        if native
        else sqlite_rows(child.stdout)
    )
    check_selected(selected, expected, label)
    return elapsed_ms, len(child.stdout)


def version_output(command):
    return subprocess.run(
        command,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=CHILD_TIMEOUT_SECONDS,
        check=True,
        text=True,
    ).stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="build/minisql", help="compiled minisql binary")
    parser.add_argument("--sqlite", help="sqlite3 executable (default: find on PATH)")
    parser.add_argument("--rows", type=bounded_integer(1, 20000), default=2000)
    parser.add_argument("--lookups", type=bounded_integer(1, 20000), default=2000)
    parser.add_argument("--repeats", type=bounded_integer(1, 10), default=5)
    parser.add_argument("--output", type=Path, help="also write the exact printed JSON here")
    args = parser.parse_args()

    binary = Path(args.binary).resolve()
    if not binary.is_file():
        raise ValueError("compiled minisql binary is missing; run make build first")
    sqlite = shutil.which(args.sqlite or "sqlite3")
    if sqlite is None:
        raise ValueError("sqlite3 executable is unavailable; use --sqlite PATH")
    native_script, sqlite_script, expected = workload(args.rows, args.lookups)
    sqlite_version = version_output([sqlite, "--version"])
    go = shutil.which("go")
    if go is None:
        raise ValueError("go executable is required to identify the compiled binary version")
    # Drop the local binary path from `go version path/to/binary` output.
    go_version = version_output([go, "version", str(binary)]).rsplit(": ", 1)[-1]
    commands = {
        "minisql": [str(binary)],
        "sqlite3": [sqlite, "-batch", "-bail", "-json", ":memory:"],
    }
    scripts = {"minisql": native_script, "sqlite3": sqlite_script}
    for label in commands:
        execute(commands[label], scripts[label], expected, args.rows, label == "minisql", False)

    measurements = {
        label: {"elapsed_ms": [], "stdout_bytes": []} for label in commands
    }
    for repeat in range(args.repeats):
        # Alternate order to reduce systematic first/second-process bias.
        order = ["minisql", "sqlite3"] if repeat % 2 == 0 else ["sqlite3", "minisql"]
        for label in order:
            elapsed_ms, size = execute(
                commands[label], scripts[label], expected, args.rows, label == "minisql", True
            )
            measurements[label]["elapsed_ms"].append(elapsed_ms)
            measurements[label]["stdout_bytes"].append(size)
    for measured in measurements.values():
        measured["median_ms"] = round(statistics.median(measured["elapsed_ms"]), 6)
        measured["elapsed_ms"] = [round(value, 6) for value in measured["elapsed_ms"]]

    report = {
        "schema_version": 1,
        "workload": {
            "seed": SEED,
            "rows": args.rows,
            "point_lookups": args.lookups,
            "repeats": args.repeats,
            "insertion_order": "deterministically shuffled unique integer keys 1..rows",
            "lookup_order": "deterministic random existing keys, with replacement",
            "value_format": "value-{key:08d}",
            "transactions_per_process": 2,
            "native_input_bytes": len(native_script),
            "sqlite_input_bytes": len(sqlite_script),
        },
        "environment": {
            "sqlite_version": sqlite_version,
            "go_version": go_version,
            "platform": {"system": platform.system(), "machine": platform.machine()},
            "python_version": platform.python_version(),
        },
        "method": {
            "scope": "end-to-end subprocess elapsed time with stdin SQL and captured stdout",
            "timer": "time.perf_counter_ns",
            "warmup_runs_per_implementation": 1,
            "run_order": "minisql first on even repeats, sqlite3 first on odd repeats (zero-based)",
            "sqlite_arguments": ["-batch", "-bail", "-json", ":memory:"],
            "sqlite_schema_setup": "CREATE TABLE kv(id INTEGER PRIMARY KEY,value TEXT);",
            "native_schema_setup": "implicit kv table",
            "validation": "every SELECT result must contain exactly its expected id/value row in statement order; validation time excluded",
        },
        "measurements": measurements,
        "correctness": {
            "all_selected_rows_validated": True,
            "selected_rows_per_process": args.lookups,
            "selected_rows_checked_per_implementation": args.lookups * (args.repeats + 1),
            "includes_warmup_validation": True,
        },
        "limits": {
            "rows_min_max": [1, 20000],
            "lookups_min_max": [1, 20000],
            "repeats_min_max": [1, 10],
            "input_bytes_max": INPUT_LIMIT_BYTES,
            "timeout_seconds_per_child": CHILD_TIMEOUT_SECONDS,
        },
        "limitations": [
            "This is not an isolated engine benchmark and does not establish engine speed claims.",
            "Elapsed time includes process startup, SQL parsing, inserts, commits, lookups, JSON serialization, pipe I/O and process exit.",
            "Front ends and output differ: minisql emits JSONL records for every statement; sqlite3 emits JSON arrays only for SELECTs. Captured output sizes are reported.",
            "SQLite creates its table inside the timed process; minisql has an implicit fixed schema.",
            "Both databases are in memory. This workload measures no disk durability, recovery or persistent-storage performance.",
            "The workload contains only insertion and successful primary-key reads; it does not compare SQL coverage, scans, updates, concurrent transactions or MVCC isolation guarantees.",
            "Warmups and alternating order reduce some noise but do not control host load, scheduling, CPU frequency or operating-system caches.",
        ],
    }
    rendered = json.dumps(report, indent=2, sort_keys=True, allow_nan=False) + "\n"
    if args.output is not None:
        args.output.write_text(rendered, encoding="utf-8")
    sys.stdout.write(rendered)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"comparison failed: {error}", file=sys.stderr)
        sys.exit(1)
