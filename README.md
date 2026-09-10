# mysqlbinlog-plus

[README: 中文文档点这里~](README_zh.md)

`mysqlbinlog-plus` is a MySQL binlog tool that generates original SQL or rollback SQL from MySQL row-based DML events and DDL query events.

## Build

Ready-to-use binaries for Windows, Linux, and macOS are available on the
[Releases page](https://github.com/realcp1018/mysqlbinlog-plus/releases/latest).
Download the archive for your operating system, extract it, and run `mbp`
(`mbp.exe` on Windows). On Linux and macOS, run `chmod +x mbp` once after
extracting the archive.

To build from source, `make` detects the operating system automatically and
supports Windows, Linux, and macOS:

```bash
make
```

## Examples

```
mbp generates original SQL or rollback SQL from MySQL row-based binlog events.

Usage:
  mbp [flags]

Examples:
  # Stream new events from the current online binlog.
  mbp --mode online --host 127.0.0.1 --user root

  # Parse selected remote binlog files and write original SQL.
  mbp --mode online --binlogs mysql-bin.000010,mysql-bin.000011 --output original.sql

  # Parse local binlog files without connecting to MySQL.
  mbp --mode local --binlogs mysql-bin.000010,mysql-bin.000011 --output original.sql

  # Split original SQL into files containing at most 100000 statements each.
  mbp --mode local --binlogs mysql-bin.000010 --output original.sql --output-chunk-size 100000

  # Generate rollback SQL from a local binlog file.
  mbp --mode local --binlogs mysql-bin.000010 --rollback --output rollback.sql

  # Split rollback SQL into files containing at most 100000 statements each.
  mbp --mode local --binlogs mysql-bin.000010 --rollback --output rollback.sql --output-chunk-size 100000

Flags:
      --mode string                 binlog mode: local files, local files with MySQL metadata, or online MySQL (local|mixed|online) (default "online")
  -h, --host string                 MySQL host (default "127.0.0.1")
  -P, --port int                    MySQL port (default 3306)
  -u, --user string                 MySQL user (default "root")
  -p, --password string             MySQL password (prompts when omitted)
      --list-binlogs                list online binlog files and exit
      --binlogs strings             binlogs to parse
      --from-time string            inclusive transaction start time
      --to-time string              exclusive transaction start time
      --from-pos uint32             inclusive transaction start position
      --to-pos uint32               exclusive transaction start position
      --table-patterns strings      table patterns to include (e.g. app.users,*.orders)
      --sql-type strings            SQL event types to include (insert|update|delete|ddl)
      --no-primary-key              omit primary key for INSERT SQL
      --rollback                    generate rollback SQL for DML row events; DDL statements are not included
      --rollback-cache-dir string   rollback SQLite cache dir (default ".mysqlbinlog-plus")
  -o, --output string               write output to a file; when chunking is enabled this path is used as the split file base name
      --output-chunk-size int       SQL rows per output chunk; -1 writes a single output file (default -1)
  -V, --version                     show version of mbp
  -?, --help                        help for mbp
```

## Output Behavior

When `--mode online` is used with `--binlogs`, mysqlbinlog-plus snapshots the
current binary log position when the task starts. Earlier selected files are
read to their end; if the selected files include the snapshot's current binlog,
reading stops at the snapshot position. This produces a finite, repeatable
result and does not wait for later events.

For normal use, specify `--binlogs` to select remote binlog files by name; local
binlog file paths are not required. Omitting `--binlogs` starts an unbounded live
stream, suitable for scenarios that need to continuously monitor the current
binlog.

Time and position ranges select transactions by their start event. The lower
bound is inclusive and the upper bound is exclusive; a selected transaction is
output in full, even when its commit is beyond the upper bound.

Byte values from columns with character-set metadata are rendered as decoded
text; values without text metadata remain hexadecimal literals to preserve
binary data.

For original SQL, mysqlbinlog-plus parses binlog events and writes each SQL
statement immediately. When `--output` and `--output-chunk-size` are set, it
switches to the next output file when the current chunk reaches its limit, while
preserving statement order.

Rollback SQL must be output in reverse event order, so it uses a separate flow.
While parsing, mysqlbinlog-plus generates rollback SQL and stores it in the
SQLite cache specified by `--rollback-cache-dir` (default `.mysqlbinlog-plus`). After all selected binlogs
have been parsed, it reads the cached rollback SQL in reverse order and writes
the final output. With chunked output enabled, independent rollback chunks are
read from SQLite and written concurrently.

When `--rollback-cache-dir` is omitted, mysqlbinlog-plus uses the default
`.mysqlbinlog-plus` directory and logs a warning. The rollback may fail if the
cache runs out of disk space.

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
- Online mode is the default. For normal use, specify `--binlogs` to select the remote binlog files by name; local binlog file paths are not required. Omitting `--binlogs` starts an unbounded live stream for scenarios that need to continuously monitor the current binlog.
- Online mode only reads binlog events from MySQL through the replication protocol.
- Mixed and online modes read column metadata from the current `information_schema.columns`. If DDL is seen in the selected range, later events fall back to binlog metadata instead of trusting the current table definition.
- `--rollback` cannot be combined with `--no-primary-key`; rollback must preserve primary key values.
- Rollback SQL does not include DDL statements. `--rollback --sql-type ddl` is rejected because there is no DML row event to roll back.
- GTID ranges are not supported yet.
- Online rollback streaming is intentionally disabled; use `--binlogs --rollback`.
