# mysqlbinlog-plus Functional Test Specification for Agents

This document is the full functional-test specification for coding agents. A human explicitly instructs an agent to run this test suite when full end-to-end verification is required. Humans use the document primarily to review test scope.

These are end-to-end tests only: use a real disposable MySQL instance, real binlog files, terminal interaction, and generated output files. Do not substitute unit tests or mocked clients for the scenarios in this document.

## Conventions

| Item | Convention |
|---|---|
| Program | `mbp`; record the exact version and commit for every run. |
| Database | `mbp_test` |
| Tables | `events` (with a primary key) and `no_pk` (without one) |
| Binlogs | Prepare at least two consecutive files: `mysql-bin.000001` and `mysql-bin.000002`. |
| Passwords | Omit `--password` for interactive tests. Use a test account and `--password` in scripts; never record a real password. |
| Evidence | Record the command, exit code, stdout/stderr, generated files, and MySQL settings. |

Unless a scenario says otherwise, configure MySQL with `log_bin=ON`, `binlog_format=ROW`, and `binlog_row_image=FULL`. Start every run with an empty `--rollback-cache-dir` and output directory.

## Agent Execution Requirements

When a human explicitly requests this full functional test, the agent must:

1. Read this entire document before testing.
2. Execute every listed scenario, including success paths, validation failures, and output-content checks.
3. Compare actual results with the stated expectations. For SQL-generation scenarios, validate generated SQL against the isolated verification database instead of checking only process exit codes.
4. Record commands, configuration, exit codes, relevant stdout/stderr, output paths, and pass/fail status in the test-run record or an attached evidence file.
5. Mark a scenario as blocked, not passed, when the required environment, privileges, or binlog fixture is unavailable. State the blocker and do not claim full functional verification.
6. Use only disposable test infrastructure. Never change binlog settings or execute generated rollback SQL on a non-disposable or production system, and never store production credentials.

When the user asks to extend this suite for new behavior, the agent must add the corresponding scenario and expected result before running the requested full test.

## Base Fixture

Force a binlog rotation so that the fixture spans at least two known consecutive files. The test account needs the `RELOAD` privilege for `FLUSH BINARY LOGS`; if it is unavailable, mark the full test as blocked rather than assuming that suitable files already exist. After each rotation, run `SHOW BINARY LOGS` and record the active file name.

Record each transaction's start and end positions, timestamp, and binlog file. The fixture must cover NULL, quotes and backslashes, Unicode, decimal, datetime, BLOB/TEXT, INSERT, UPDATE, DELETE, and DDL.

```sql
-- Record the active file as binlog A after this rotation.
FLUSH BINARY LOGS;

CREATE DATABASE mbp_test;
CREATE TABLE mbp_test.events (
  id BIGINT PRIMARY KEY,
  name VARCHAR(64) NULL,
  amount DECIMAL(10,2),
  updated_at DATETIME NULL,
  payload BLOB NULL
);
CREATE TABLE mbp_test.no_pk (value VARCHAR(64));

INSERT INTO mbp_test.events VALUES (1, 'alpha', 12.34, '2026-07-14 10:00:00', X'00FF');
UPDATE mbp_test.events SET name='O''Reilly', amount=56.78, payload=NULL WHERE id=1;
DELETE FROM mbp_test.events WHERE id=1;

-- Record the active file as binlog B after this rotation.
FLUSH BINARY LOGS;

INSERT INTO mbp_test.no_pk VALUES ('no primary key');
ALTER TABLE mbp_test.events ADD COLUMN note VARCHAR(64) NULL;
INSERT INTO mbp_test.events (id, name, amount, updated_at, note) VALUES (2, 'cross file', 1.00, NULL, 'after DDL');
```

Also prepare unmatched time/table filters, output chunks that cross a binlog boundary, and one binlog without DML. Confirm that the selected file list includes both binlog A and binlog B. For each successful SQL-generation scenario, apply the generated SQL to an isolated verification database and compare its state with the expected forward or reverse state.

## Row Image Configuration Matrix

| # | MySQL configuration | Mode | Valid entry points | Expected result |
|---|---|---|---|---|
| 1 | `ROW` + `FULL` | `local` | Original SQL, rollback, chunks | No MySQL setting check; a local-mode warning is printed. If table-map metadata contains column names, original and rollback SQL must be executable. Otherwise original SQL uses `column_N` and rollback must be rejected. |
| 2 | `ROW` + `FULL` | `mixed` | Original SQL, rollback, chunks | Success. Local files are parsed and current MySQL metadata supplies column names. |
| 3 | `ROW` + `FULL` | `online` | Listing, selected-binlog original/rollback, live stream | Success. Selected-binlog output matches the applicable local/mixed result. Live streaming produces original SQL only. |
| 4 | `ROW` + `MINIMAL` | `local` | Original SQL, rollback | The program does not reject it. Record the actual SQL and executability; never label rollback as reliable. |
| 5 | `ROW` + `NOBLOB` | `local` | Original SQL, rollback | The program does not reject it. Record results for BLOB/TEXT updates; never label rollback as reliable. |
| 6 | `ROW` + non-`FULL` | `mixed` | Original SQL, rollback | Fails after connecting: `binlog_row_image must be FULL`. No output or SQLite cache may be created. |
| 7 | `ROW` + non-`FULL` | `online` | Selected-binlog original/rollback, live stream | Fails after connecting: `binlog_row_image must be FULL`. No output or SQLite cache may be created. |
| 8 | `ROW` + non-`FULL` | `online` | `--list-binlogs` | Succeeds and lists binlogs. This path currently does not check binlog format or row image. |

Run `MINIMAL` and `NOBLOB` separately. Rows 4 and 5 confirm current behavior, not a reliability guarantee.

## Mode and Main Flow Matrix (`ROW` + `FULL`)

| # | Mode | Command condition | Expected result |
|---|---|---|---|
| 1 | local | One local file, original SQL | Success; no MySQL connection or password prompt. |
| 2 | local | Multiple consecutive local files, original SQL | Success; output is in binlog and event order. |
| 3 | local | Rollback with cache directory | With real table-map column names, succeeds in global reverse event order. Otherwise fails because generated `column_N` names cannot be used for rollback. After success, the cache directory is empty. |
| 4 | mixed | One or more local files, original SQL | Success; first connection prompts for a hidden password; column names come from MySQL. |
| 5 | mixed | Rollback with cache directory | Success; reverse SQL is executable. |
| 6 | online | `--list-binlogs` | Success; prints binlog names and sizes only. |
| 7 | online | One or more remote binlogs, original SQL | Success; forward SQL matches the applicable local/mixed output. |
| 8 | online | One or more remote binlogs, rollback with cache directory | Success; reverse SQL matches local/mixed output. |
| 9 | online | No `--binlogs` | Starts from `SHOW MASTER STATUS` and continuously emits new events; terminate by cancellation. |
| 10 | online | No `--binlogs` with `--rollback` | Fails: online streaming does not support rollback. |

## Filters and SQL Content

Run these scenarios for every successful SQL-generation path in the Mode and Main Flow Matrix. If local mode uses `column_N` because binlog metadata lacks names, record its original SQL and rollback failure separately; do not classify that as a mixed/online regression.

| # | Condition | Verification |
|---|---|---|
| 1 | No filter | INSERT, UPDATE, DELETE, and DDL are emitted in forward order; rollback excludes DDL. |
| 2 | Test `--sql-type insert`, `update`, `delete`, and `ddl` separately | Only the requested type is emitted; DDL uses `DDLSQLText`. |
| 3 | Multiple `--sql-type` values, mixed case, and duplicates | Values are normalized and deduplicated; output is their union. |
| 4 | `--table-patterns mbp_test.events` | Only target-table DML is emitted; record current DDL matching behavior. |
| 5 | Unmatched table filter | Original single-file output is empty; rollback creates no SQLite file. Chunked rollback still creates one empty `.000001` file. |
| 6 | `--from-pos` and `--to-pos` | Inclusive lower bound and exclusive upper bound, based on transaction start position; selected transactions are emitted in full. |
| 7 | `--from-time` and `--to-time` | The same half-open range semantics as the position test. |
| 8 | DML after DDL | Mixed/online must fall back to binlog metadata for later events; validate columns and SQL executability. |
| 9 | Table without a primary key | Original SQL succeeds; record rollback WHERE clauses and executability. |
| 10 | `--no-primary-key` | Only original INSERT omits primary keys; the rollback combination is rejected during validation. |

## Output and Rollback Cache

| # | Scenario | Expected result |
|---|---|---|
| 1 | No `--output` | SQL is written to stdout. |
| 2 | `--output result.sql` | One file is created; unmatched events create an empty file. |
| 3 | `--output result.sql --output-chunk-size 2`, original SQL | Creates forward files such as `result.sql.000001`; unmatched events create no chunk file. |
| 4 | Same options, rollback SQL | Event order is globally reversed both within and across chunks; verify a chunk spanning two binlogs. |
| 5 | Chunked rollback with no matching events | Creates one empty `result.sql.000001`. |
| 6 | Relative path, absolute path, and missing output parent directory | Valid paths create required parent directories and output files. |
| 7 | Rollback `--output` inside `--rollback-cache-dir` | Validation fails: `--output must not be inside --rollback-cache-dir`. |
| 8 | Missing, empty, and non-empty rollback cache directories | Missing is created; empty succeeds; non-empty fails without deleting existing contents. |
| 9 | Two `--binlogs` paths with the same basename | Validation fails with duplicate binlog name. |
| 10 | Rollback parse failure or manual interruption | Writer transaction is rolled back. Record cache/output retention and ensure no partial result is claimed as successful. |

## Validation, Connectivity, and Passwords

| # | Scenario | Expected result |
|---|---|---|
| 1 | Omit `--mode` | Defaults to `mixed`. |
| 2 | Invalid mode, port, or SQL type | Fails before connecting; error identifies the relevant flag. |
| 3 | `--list-binlogs` with non-online mode | Fails. |
| 4 | Mix time and position ranges | Fails. |
| 5 | Position range with multiple binlogs, or lower bound not less than upper bound | Fails. |
| 6 | Chunk size `0`, a negative value other than `-1`, or no output path | Fails. |
| 7 | `--rollback` without cache directory; with `--no-primary-key`; or with only `--sql-type ddl` | Each fails. |
| 8 | Mixed/online without `--password` in an interactive terminal | Prints `Enter password:`; input is not echoed; Enter permits an empty password. |
| 9 | Mixed/online with `--password=""` | Does not prompt and uses an empty password. |
| 10 | Mixed/online without password when stdin is a pipe or CI input | Fails with a password-required error; must not block. |
| 11 | Local mode without `--password` | Does not prompt and still runs. |
| 12 | Unreachable server, wrong password, and missing replication/metadata privileges | No SQL output; distinguish connection, authentication, and privilege errors. |

## Additional Configuration Regression

| # | Configuration | Mode | Expected result |
|---|---|---|---|
| 1 | `binlog_format=STATEMENT` | Mixed/online parsing entry points | Fails: `binlog_format must be ROW`. |
| 2 | `binlog_format=MIXED` | Mixed/online parsing entry points | Fails identically. |
| 3 | `binlog_format!=ROW` | local | No server-side validation; prints the local-mode warning. Record whether only DDL or no DML is processed. |
| 4 | `binlog_format!=ROW` | `online --list-binlogs` | Still lists binlogs. |

## Test Run Record

| Date | Version/commit | MySQL version | `binlog_format` | `binlog_row_image` | Scenario | Result | Evidence path / notes |
|---|---|---|---|---|---|---|---|
|  |  |  |  |  |  | Not run / passed / failed |  |

When actual behavior differs from this plan, preserve the command, full error, and generated artifacts before deciding whether the discrepancy is an implementation defect, documentation issue, or fixture issue.
