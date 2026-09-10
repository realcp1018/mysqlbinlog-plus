# mysqlbinlog-plus

[README: Read the English version~](README.md)

`mysqlbinlog-plus` 是一个 MySQL binlog 工具，可根据 MySQL 行格式 DML 事件和 DDL 查询事件生成原始 SQL 或回滚 SQL。

## 构建

Windows、Linux 和 macOS 的可用文件已发布到
[Releases 页面](https://github.com/realcp1018/mysqlbinlog-plus/releases/latest)。
下载适用于当前操作系统的压缩包并解压。Windows 可直接运行 `mbp.exe`；Linux 和
macOS 首次运行前需要执行 `chmod +x mbp`。

如需从源码构建，`make` 会自动检测操作系统，支持 Windows、Linux 和 macOS：

```bash
make
```

## 示例

```
mbp generates original SQL or rollback SQL from MySQL row-based binlog events.

Usage:
  mbp [flags]

Examples:
  # 从当前 online binlog 流式读取新事件。
  mbp --mode online --host 127.0.0.1 --user root

  # 自动发现指定时间范围对应的远程 binlog。
  mbp --mode online --from-time "2026-09-09 10:00:00" --to-time "2026-09-10 10:00:00"

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
  -L, --list-binlogs                list online binlog files and exit
  -B, --binlogs strings             binlogs to parse
      --from-time string            inclusive transaction start time
      --to-time string              exclusive transaction start time
      --from-pos uint32             inclusive transaction start position
      --to-pos uint32               exclusive transaction start position
  -T, --table-patterns strings      table patterns to include (e.g. app.users,*.orders)
      --sql-type strings            SQL event types to include (insert|update|delete|ddl)
      --no-primary-key              omit primary key for INSERT SQL
  -R, --rollback                    generate rollback SQL for DML row events; DDL statements are not included
      --rollback-cache-dir string   rollback SQLite cache dir (default ".mysqlbinlog-plus")
  -o, --output string               write output to a file; when chunking is enabled this path is used as the split file base name
      --output-chunk-size int       SQL rows per output chunk; -1 writes a single output file (default -1)
  -V, --version                     show version of mbp
  -?, --help                        help for mbp
```

## 输出行为

`--mode online` 配合 `--binlogs` 使用时，任务启动后会快照当前 binlog 位置。
较早的指定文件会读取到文件结束；若指定文件包含快照时的当前 binlog，则读取到
该快照位置后结束。因此结果是有限且可重复的，不会等待后续 event。

正常使用时，建议通过 `--binlogs` 明确指定远程 binlog 文件名，不需要本地 binlog 文件路径。使用 `--from-time` 时也建议同时指定覆盖目标时间范围的远程 binlog，以避免探测历史文件并明确输入范围。未指定 `--binlogs` 且未指定时间范围时，会进入无界的实时流式读取，适用于需要持续监听当前 binlog 的场景。未指定 `--binlogs` 但指定了 `--from-time` 或 `--to-time` 时，会探测远程 binlog 中首个有效时间戳 event，自动选择保守的历史文件范围，并读取到任务启动时的快照位点或 `--to-time` 边界（以先到者为准）后结束，不等待后续 event。

时间和位置范围按事务起始事件筛选。下界为包含边界，上界为排除边界；只要事务被选中，即使其提交位置或时间超过上界，仍会完整输出该事务。

带字符集元数据的字节值会解码为文本输出；缺少文本元数据的值仍使用十六进制字面量，以保护二进制数据。

对于原始 SQL，`mysqlbinlog-plus` 会一边解析 binlog event，一边立即写出对应的 SQL。设置 `--output` 和 `--output-chunk-size` 后，当前分块达到上限时才切换到下一个输出文件，语句顺序保持不变。

回滚 SQL 必须按 event 的逆序输出，因此采用另一条处理链：解析时会生成回滚 SQL，并存入 `--rollback-cache-dir` 指定的 SQLite 缓存（默认为 `.mysqlbinlog-plus`）。所有指定 binlog 都解析完成后，`mysqlbinlog-plus` 才会从 SQLite 倒序读取这些回滚 SQL 并写出最终结果。启用分块输出时，各个独立的回滚分块会从 SQLite 并发读取并写入。

未显式指定 `--rollback-cache-dir` 时，`mysqlbinlog-plus` 会使用默认目录 `.mysqlbinlog-plus`，并打印一条 warning 日志。缓存空间不足可能导致磁盘爆满和回滚失败。

## MySQL 要求

使用行格式 binlog，并启用完整行镜像：

```sql
SHOW VARIABLES LIKE 'binlog_format';
SHOW VARIABLES LIKE 'binlog_row_image';
```

预期值：

```text
binlog_format    ROW
binlog_row_image FULL
```

MySQL 用户需要与所选模式对应的权限：

- `online`：用于流式读取 binlog 的 `REPLICATION SLAVE` 或等效复制权限。
- `online`：用于发现 binlog 的 `REPLICATION CLIENT` 权限。
- `mixed` 和 `online`：读取表元数据所需的 `information_schema.columns` 的 `SELECT` 权限。

## 限制

- 不支持 statement 格式的 binlog。
- 不完整行镜像（如 `binlog_row_image=MINIMAL`）无法可靠生成回滚 SQL。
- 无法确定真实字段名时，原始 SQL 会回退为 `column_1` 等生成名称；回滚 SQL 会拒绝使用生成字段名，因为生成的语句无法在真实表结构上执行。
- `online` 是默认模式：正常使用时建议通过 `--binlogs` 指定远程 binlog 文件名，不需要本地 binlog 文件路径。未指定 `--binlogs` 且未指定时间范围时进入无界实时流式读取；指定 `--from-time` 或 `--to-time` 时会自动发现有限的历史范围，并在启动快照位点或 `--to-time` 边界处停止（以先到者为准）。
- `online` 模式只通过复制协议读取 MySQL binlog。
- `mixed` 和 `online` 模式读取当前 `information_schema.columns` 的表元数据。如果在选中范围内遇到 DDL，后续事件会回退为 binlog 元数据，避免信任可能已经变化的当前表定义。
- `--rollback` 不能与 `--no-primary-key` 一起使用；回滚 SQL 必须保留主键值。
- 回滚 SQL 不包含 DDL 语句。由于没有可回滚的 DML 行事件，`--rollback --sql-type ddl` 会被拒绝。
- 暂不支持 GTID 范围。
- 在线回滚流式处理被刻意禁用；请使用 `--binlogs --rollback`。
