package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-sql-driver/mysql"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
)

type BinlogPosition struct {
	File string
	Pos  uint32
}

type BinaryLog struct {
	Name string
	Size uint64
}

type Client struct {
	db *sql.DB
}

// Open creates a MySQL client from CLI configuration.
func Open(cfg config.Config) (*Client, error) {
	mysqlCfg := mysql.NewConfig()
	mysqlCfg.Net = "tcp"
	mysqlCfg.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mysqlCfg.User = cfg.User
	mysqlCfg.Passwd = cfg.Password
	mysqlCfg.ParseTime = true
	mysqlCfg.Timeout = 5 * time.Second
	mysqlCfg.ReadTimeout = 30 * time.Second
	mysqlCfg.WriteTimeout = 30 * time.Second

	db, err := sql.Open("mysql", mysqlCfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	return &Client{db: db}, nil
}

// Close closes the underlying database handle.
func (c *Client) Close() error {
	return c.db.Close()
}

// Ping verifies that the MySQL connection is reachable.
func (c *Client) Ping(ctx context.Context) error {
	return c.db.PingContext(ctx)
}

// ShowMasterStatus returns the current MySQL binlog position.
func (c *Client) ShowMasterStatus(ctx context.Context) (BinlogPosition, error) {
	var serverVersion string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&serverVersion); err != nil {
		return BinlogPosition{}, fmt.Errorf("SELECT VERSION() failed: %w", err)
	}

	query := "SHOW MASTER STATUS"
	comparison, err := gomysql.CompareServerVersions(serverVersion, "8.4.0")
	if err != nil {
		return BinlogPosition{}, fmt.Errorf("parse MySQL version %q: %w", serverVersion, err)
	}
	if comparison >= 0 {
		query = "SHOW BINARY LOG STATUS"
	}

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return BinlogPosition{}, fmt.Errorf("%s failed: %w; check your log_bin setting", query, err)
	}
	defer rows.Close()

	columnNames, err := rows.Columns()
	if err != nil {
		return BinlogPosition{}, fmt.Errorf("read %s columns: %w", query, err)
	}
	if len(columnNames) < 2 {
		return BinlogPosition{}, fmt.Errorf("%s returned %d columns, expected at least 2", query, len(columnNames))
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return BinlogPosition{}, err
		}
		return BinlogPosition{}, fmt.Errorf("%s returned no rows", query)
	}

	var position BinlogPosition
	destinations := make([]any, len(columnNames))
	destinations[0] = &position.File
	destinations[1] = &position.Pos
	for i := 2; i < len(destinations); i++ {
		destinations[i] = new(any)
	}
	if err := rows.Scan(destinations...); err != nil {
		return BinlogPosition{}, err
	}
	return position, nil
}

// ShowBinaryLogs lists the binary logs available on the server.
func (c *Client) ShowBinaryLogs(ctx context.Context) ([]BinaryLog, error) {
	rows, err := c.db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, fmt.Errorf("SHOW BINARY LOGS failed: %w; check your log_bin setting", err)
	}
	defer rows.Close()
	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("read SHOW BINARY LOGS columns: %w", err)
	}
	columnCount := len(columnNames)
	if columnCount < 2 {
		return nil, fmt.Errorf("SHOW BINARY LOGS returned %d columns, expected at least 2", columnCount)
	}

	var logs []BinaryLog
	for rows.Next() {
		var log BinaryLog
		destinations := make([]any, columnCount)
		destinations[0] = &log.Name
		destinations[1] = &log.Size
		for i := 2; i < columnCount; i++ {
			destinations[i] = new(any)
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}

// CheckBinaryLogsExist verifies that all given binlogs exist.
func (c *Client) CheckBinaryLogsExist(ctx context.Context, binlogs []string) error {
	if len(binlogs) == 0 {
		return nil
	}
	logs, err := c.ShowBinaryLogs(ctx)
	if err != nil {
		return err
	}

	available := make(map[string]struct{}, len(logs))
	for _, log := range logs {
		available[log.Name] = struct{}{}
	}

	var unknown []string
	for _, binlog := range binlogs {
		if _, ok := available[binlog]; !ok {
			unknown = append(unknown, fmt.Sprintf("%q", binlog))
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("--binlogs contains unknown online binlogs: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// CheckBinlogSettings verifies mysql server binlog settings required for SQL generation.
func (c *Client) CheckBinlogSettings(ctx context.Context) error {
	format, err := c.showVariable(ctx, "binlog_format")
	if err != nil {
		return err
	}
	rowImage, err := c.showVariable(ctx, "binlog_row_image")
	if err != nil {
		return err
	}

	if !strings.EqualFold(format, "ROW") {
		return fmt.Errorf("binlog_format must be ROW, got %q", format)
	}
	if !strings.EqualFold(rowImage, "FULL") {
		return fmt.Errorf("binlog_row_image must be FULL, got %q", rowImage)
	}
	return nil
}

// showVariable reads a supported MySQL server variable.
func (c *Client) showVariable(ctx context.Context, name string) (string, error) {
	var (
		variableName string
		value        string
	)
	query := ""
	switch name {
	case "binlog_format":
		query = "SHOW VARIABLES LIKE 'binlog_format'"
	case "binlog_row_image":
		query = "SHOW VARIABLES LIKE 'binlog_row_image'"
	default:
		return "", fmt.Errorf("unsupported variable %q", name)
	}
	if err := c.db.QueryRowContext(ctx, query).Scan(&variableName, &value); err != nil {
		return "", err
	}
	return value, nil
}

// LoadTableColumns loads column names, numeric signedness, keys, and charsets.
func (c *Client) LoadTableColumns(ctx context.Context, schemaName, tableName string) ([]event.Column, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT c.COLUMN_NAME, c.COLUMN_KEY = 'PRI', c.CHARACTER_SET_NAME, c.COLUMN_TYPE
FROM information_schema.columns c
WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ?
ORDER BY c.ORDINAL_POSITION`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []event.Column
	for rows.Next() {
		var (
			col        event.Column
			charset    sql.NullString
			columnType string
		)
		if err := rows.Scan(&col.Name, &col.PrimaryKey, &charset, &columnType); err != nil {
			return nil, err
		}
		col.Charset = charset.String
		col.Unsigned = strings.HasSuffix(strings.ToLower(columnType), " unsigned") || strings.HasSuffix(strings.ToLower(columnType), " unsigned zerofill")
		columns = append(columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table metadata not found for %s.%s", schemaName, tableName)
	}
	return columns, nil
}

type SchemaResolver struct {
	client       *Client
	cache        map[string][]event.Column
	incompatible map[string]bool
}

// NewSchemaResolver creates a table metadata resolver backed by MySQL.
func NewSchemaResolver(client *Client) *SchemaResolver {
	return &SchemaResolver{
		client:       client,
		cache:        make(map[string][]event.Column),
		incompatible: make(map[string]bool),
	}
}

// Resolve returns table metadata and reports whether it is incompatible with the binlog metadata.
func (r *SchemaResolver) Resolve(schemaName, tableName string, fallback []event.Column) ([]event.Column, bool, error) {
	key := schemaName + "." + tableName
	if r.incompatible[key] {
		return fallback, true, nil
	}
	if columns, ok := r.cache[key]; ok {
		return columns, false, nil
	}
	columns, err := r.client.LoadTableColumns(context.Background(), schemaName, tableName)
	if err != nil {
		if len(fallback) > 0 {
			return fallback, false, nil
		}
		return nil, false, err
	}
	if !metadataCompatible(columns, fallback) {
		r.incompatible[key] = true
		return fallback, true, nil
	}
	r.cache[key] = columns
	return columns, false, nil
}

// GenerateServerID returns a nonzero replication server ID for binlog sync.
func GenerateServerID() uint32 {
	host, _ := os.Hostname()
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s:%d:%d", host, os.Getpid(), time.Now().UnixNano())))
	id := h.Sum32()
	if id == 0 {
		return 1
	}
	return id
}

func metadataCompatible(current, fallback []event.Column) bool {
	if len(current) != len(fallback) {
		return false
	}
	for i, column := range fallback {
		if !column.GeneratedName && current[i].Name != column.Name {
			return false
		}
		if column.PrimaryKey && !current[i].PrimaryKey {
			return false
		}
	}
	return true
}
