# mysqlbinlog-plus

[README: 中文文档点这里~](README_zh.md)

`mysqlbinlog-plus` is a MySQL binlog tool that generates original SQL or rollback SQL from MySQL row-based DML events and DDL query events.

## Build

`make` detects OS automatically, OS supported: windows, linux, macos

```bash
make
```

## Examples

```
mbp generates original SQL or rollback SQL from MySQL row-based binlog events.

Usage:
  mbp [flags]

Flags:
      --binlogs strings             binlogs to parse
      --from-pos uint32             inclusive transaction start position
      --from-time string            inclusive transaction start time
  -h, --help                        help for mbp
      --host string                 MySQL host (default "127.0.0.1")
      --list-binlogs                list online binlog files and exit
      --mode string                 binlog mode: local files, local files with MySQL metadata, or online MySQL (local|mixed|online) (default "mixed")
      --no-primary-key              omit primary key for INSERT SQL
      --output string               write output to a file; when chunking is enabled this path is used as the split file base name
      --output-chunk-size int       SQL rows per output chunk; -1 writes a single output file (default -1)
      --password string             MySQL password (prompts when omitted)
      --port int                    MySQL port (default 3306)
      --rollback                    generate rollback SQL for DML row events; DDL statements are not included
      --rollback-cache-dir string   rollback SQLite cache dir; required with --rollback
      --sql-type strings            SQL event types to include (insert|update|delete|ddl)
      --table-patterns strings      table patterns to include
      --to-pos uint32               exclusive transaction start position
      --to-time string              exclusive transaction start time
      --user string                 MySQL user (default "root")
  -V, --version                     show version of mbp
```

Stream new row events from the current master position:

```bash
mbp --mode online --host 127.0.0.1 --user root
```

When `--mode online` is used with `--binlogs`, mysqlbinlog-plus snapshots
`SHOW MASTER STATUS` when the task starts. Earlier selected files are read to
their end; if the selected files include the snapshot's current binlog, reading
stops at the snapshot position. This produces a finite, repeatable result and
does not wait for later events. Online mode without `--binlogs` remains a live
stream.

List online binlog files:

```bash
mbp --mode online --list-binlogs --host 127.0.0.1 --user root
```

Parse local binlogs in the default `mixed` mode, using MySQL table metadata:

```bash
mbp --binlogs mysql-bin.000010,mysql-bin.000011 --host 127.0.0.1 --user root
```

Parse selected local binlogs and output original SQL:

```bash
mbp --mode local --binlogs mysql-bin.000010,mysql-bin.000011
```

Output only DDL statements:

```bash
mbp --mode local --binlogs mysql-bin.000010 --sql-type ddl
```

Parse transactions from a single local binlog by their start position:

```bash
mbp \
  --mode local \
  --binlogs mysql-bin.000010 \
  --from-pos 120 \
  --to-pos 900
```

Generate rollback SQL:

```bash
mbp --mode local --binlogs mysql-bin.000010 --rollback --rollback-cache-dir .mysqlbinlog-plus
```

Write output to chunked files:

```bash
mbp --mode local --binlogs mysql-bin.000010 --output out.sql --output-chunk-size 100000
```

## Password Input

Commands that connect to MySQL (`mixed`, `online`, and `--list-binlogs`) prompt
for a password when `--password` is omitted. Input is not echoed, and an empty
password is allowed by pressing Enter. Local mode does not prompt for a password.

For non-interactive use, provide `--password` explicitly. Use `--password=""`
to explicitly select an empty password without prompting.

## Output Behavior

Time and position ranges select transactions by their start event. The lower
bound is inclusive and the upper bound is exclusive; a selected transaction is
output in full, even when its commit is beyond the upper bound.

Original SQL is written directly while binlog events are parsed. When `--output`
and `--output-chunk-size` are set, the direct writer rotates files sequentially
as statements are produced.

Rollback SQL is different because rollback output must be written in reverse
event order. During parsing, rollback statements are stored in the SQLite cache
specified by `--rollback-cache-dir` without final output chunking. After parsing
finishes, mysqlbinlog-plus reads the cache in reverse order. If chunked output is
enabled, rollback chunks are written concurrently.

`--output` must not be inside `--rollback-cache-dir`, because the rollback cache
directory is removed after a successful run. Each binlog cache file is named
from the binlog basename, so `--binlogs` cannot contain two paths with the same
basename.

## MySQL Requirements

Use row-based binlog with full row images:

```sql
SHOW VARIABLES LIKE 'binlog_format';
SHOW VARIABLES LIKE 'binlog_row_image';
```

Expected values:

```text
binlog_format    ROW
binlog_row_image FULL
```

The MySQL user needs privileges for the selected mode:

- `online`: `REPLICATION SLAVE` or equivalent replication privilege for streaming binlog events.
- `online`: `REPLICATION CLIENT` for binlog discovery.
- `mixed` and `online`: `SELECT` on `information_schema.columns` for table metadata.

## Limitations

- Statement-based binlog is not supported.
- Incomplete row images such as `binlog_row_image=MINIMAL` are not supported for reliable rollback.
- If real column names cannot be determined, original SQL falls back to generated names such as `column_1`. Rollback SQL rejects generated column names because the output would not be executable against the real table.
- Mixed mode is the default. It parses local binlog files and reads column metadata from MySQL.
- Online mode only reads binlog events from MySQL through the replication protocol.
- Mixed and online modes read column metadata from the current `information_schema.columns`. If DDL is seen in the selected range, later events fall back to binlog metadata instead of trusting the current table definition.
- `--rollback` cannot be combined with `--no-primary-key`; rollback must preserve primary key values.
- Rollback SQL does not include DDL statements. `--rollback --sql-type ddl` is rejected because there is no DML row event to roll back.
- GTID ranges are not supported yet.
- Online rollback streaming is intentionally disabled; use `--binlogs --rollback`.
