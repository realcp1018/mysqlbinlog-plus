# mysqlbinlog-plus

[README: Read the English version~](README.md)

`mysqlbinlog-plus` 是一个 MySQL binlog 工具，可根据 MySQL 行格式 DML 事件和 DDL 查询事件生成原始 SQL 或回滚 SQL。

## 构建

`make`会自动检测操作系统进行编译，支持的操作系统为: windows,linux,macos

```bash
make
```

## 示例

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

从当前主库位置开始持续读取新的行事件：

```bash
mbp --mode online --host 127.0.0.1 --user root
```

`--mode online` 配合 `--binlogs` 使用时，任务启动后会快照一次
`SHOW MASTER STATUS`。较早的指定文件会读取到文件结束；若指定文件包含快照时
的当前 binlog，则读取到该快照位置后结束。因此结果是有限且可重复的，不会等待
后续 event。未指定 `--binlogs` 的 online 模式仍会持续实时读取。

列出在线 MySQL 的 binlog 文件：

```bash
mbp --mode online --list-binlogs --host 127.0.0.1 --user root
```

使用默认的 `mixed` 模式解析本地 binlog，并从 MySQL 读取表字段元数据：

```bash
mbp --binlogs mysql-bin.000010,mysql-bin.000011 --host 127.0.0.1 --user root
```

解析指定本地 binlog 并输出原始 SQL：

```bash
mbp --mode local --binlogs mysql-bin.000010,mysql-bin.000011
```

只输出 DDL 语句：

```bash
mbp --mode local --binlogs mysql-bin.000010 --sql-type ddl
```

按事务起始位置解析单个本地 binlog：

```bash
mbp \
  --mode local \
  --binlogs mysql-bin.000010 \
  --from-pos 120 \
  --to-pos 900
```

生成回滚 SQL：

```bash
mbp --mode local --binlogs mysql-bin.000010 --rollback --rollback-cache-dir .mysqlbinlog-plus
```

将输出写入分块文件：

```bash
mbp --mode local --binlogs mysql-bin.000010 --output out.sql --output-chunk-size 100000
```

## 密码输入

需要连接 MySQL 的命令（`mixed`、`online` 和 `--list-binlogs`）未指定
`--password` 时，会隐藏回显提示输入密码；直接按回车允许使用空密码。本地模式不会提示输入密码。

非交互场景请显式提供 `--password`。使用 `--password=""` 可以明确指定空密码且不触发提示。

## 输出行为

时间和位置范围按事务起始事件筛选。下界为包含边界，上界为排除边界；只要事务被选中，即使其提交位置或时间超过上界，仍会完整输出该事务。

对于原始 SQL，`mysqlbinlog-plus` 会一边解析 binlog event，一边立即写出对应的 SQL。设置 `--output` 和 `--output-chunk-size` 后，当前分块达到上限时才切换到下一个输出文件，语句顺序保持不变。

回滚 SQL 必须按 event 的逆序输出，因此采用另一条处理链：解析时会生成回滚 SQL，并存入 `--rollback-cache-dir` 指定的 SQLite 缓存。所有指定 binlog 都解析完成后，`mysqlbinlog-plus` 才会从 SQLite 倒序读取这些回滚 SQL 并写出最终结果。启用分块输出时，各个独立的回滚分块会从 SQLite 并发读取并写入。

`--output` 不能位于 `--rollback-cache-dir` 内，因为任务成功后会清理回滚缓存目录。每个 binlog 的缓存文件以 binlog 基名命名，因此 `--binlogs` 不能包含基名相同的两个路径。

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
- `mixed` 是默认模式：解析本地 binlog 并读取 MySQL 表元数据。
- `online` 模式只通过复制协议读取 MySQL binlog。
- `mixed` 和 `online` 模式读取当前 `information_schema.columns` 的表元数据。如果在选中范围内遇到 DDL，后续事件会回退为 binlog 元数据，避免信任可能已经变化的当前表定义。
- `--rollback` 不能与 `--no-primary-key` 一起使用；回滚 SQL 必须保留主键值。
- 回滚 SQL 不包含 DDL 语句。由于没有可回滚的 DML 行事件，`--rollback --sql-type ddl` 会被拒绝。
- 暂不支持 GTID 范围。
- 在线回滚流式处理被刻意禁用；请使用 `--binlogs --rollback`。
