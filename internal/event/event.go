package event

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

type SQLType string

const (
	Insert SQLType = "insert"
	Update SQLType = "update"
	Delete SQLType = "delete"
	DDL    SQLType = "ddl"
)

type Column struct {
	Name string
	// PrimaryKey reports whether the column belongs to the primary key.
	PrimaryKey bool
	// GeneratedName reports whether Name is a fallback value rather than table metadata.
	GeneratedName bool
	// Charset is the MySQL character set for text values in this column.
	Charset string
}

type SQLOptions struct {
	NoPrimaryKey bool
}

type RowChange struct {
	Schema string
	Table  string
	Type   SQLType
	Cols   []Column
	Before []any
	After  []any
}

// ToOriginalSQL converts a row change into forward SQL.
func (rc *RowChange) ToOriginalSQL(opts SQLOptions) (string, error) {
	if err := rc.validateSQL(); err != nil {
		return "", err
	}

	switch rc.Type {
	case Insert:
		return rc.toInsertSQL(rc.After, opts)
	case Update:
		return rc.toUpdateSQL(rc.Before, rc.After)
	case Delete:
		return rc.toDeleteSQL(rc.Before)
	default:
		return "", fmt.Errorf("unsupported event type %q", rc.Type)
	}
}

// ToRollbackSQL converts a row change into rollback SQL.
func (rc *RowChange) ToRollbackSQL(opts SQLOptions) (string, error) {
	if err := rc.validateSQL(); err != nil {
		return "", err
	}
	if hasGeneratedColumnNames(rc.Cols) {
		return "", fmt.Errorf("rollback requires real column names; generated column_N metadata is not executable SQL")
	}

	switch rc.Type {
	case Insert:
		return rc.toDeleteSQL(rc.After)
	case Update:
		return rc.toUpdateSQL(rc.After, rc.Before)
	case Delete:
		return rc.toInsertSQL(rc.Before, opts)
	default:
		return "", fmt.Errorf("unsupported event type %q", rc.Type)
	}
}

// validateSQL checks whether a row change has enough metadata to render SQL.
func (rc *RowChange) validateSQL() error {
	if rc.Schema == "" || rc.Table == "" {
		return fmt.Errorf("schema and table are required")
	}
	if len(rc.Cols) == 0 {
		return fmt.Errorf("columns are required")
	}
	if len(rc.Before) != 0 && len(rc.Before) != len(rc.Cols) {
		return fmt.Errorf("before values do not match columns (possible cause: binlog_row_image is not FULL)")
	}
	if len(rc.After) != 0 && len(rc.After) != len(rc.Cols) {
		return fmt.Errorf("after values do not match columns (possible cause: binlog_row_image is not FULL)")
	}
	return nil
}

// toInsertSQL renders an INSERT statement from row values.
func (rc *RowChange) toInsertSQL(values []any, opts SQLOptions) (string, error) {
	if len(values) != len(rc.Cols) {
		return "", fmt.Errorf("insert values do not match columns")
	}

	columns := make([]string, 0, len(rc.Cols))
	sqlValues := make([]string, 0, len(rc.Cols))
	for i, col := range rc.Cols {
		if opts.NoPrimaryKey && col.PrimaryKey {
			continue
		}
		columns = append(columns, quoteIdent(col.Name))
		sqlValues = append(sqlValues, formatValue(values[i], col))
	}

	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
		quoteTable(rc.Schema, rc.Table),
		strings.Join(columns, ", "),
		strings.Join(sqlValues, ", "),
	), nil
}

// toUpdateSQL renders an UPDATE statement from before and after values.
func (rc *RowChange) toUpdateSQL(whereValues, setValues []any) (string, error) {
	if len(whereValues) != len(rc.Cols) || len(setValues) != len(rc.Cols) {
		return "", fmt.Errorf("update values do not match columns")
	}

	sets := make([]string, 0, len(rc.Cols))
	for i, col := range rc.Cols {
		if valuesEqual(whereValues[i], setValues[i]) {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = %s", quoteIdent(col.Name), formatValue(setValues[i], col)))
	}
	if len(sets) == 0 {
		return "", fmt.Errorf("update event has no changed columns")
	}

	return fmt.Sprintf("UPDATE %s SET %s WHERE %s;",
		quoteTable(rc.Schema, rc.Table),
		strings.Join(sets, ", "),
		whereClause(rc.Cols, whereValues),
	), nil
}

// toDeleteSQL renders a DELETE statement from row values.
func (rc *RowChange) toDeleteSQL(values []any) (string, error) {
	if len(values) != len(rc.Cols) {
		return "", fmt.Errorf("delete values do not match columns")
	}

	return fmt.Sprintf("DELETE FROM %s WHERE %s;",
		quoteTable(rc.Schema, rc.Table),
		whereClause(rc.Cols, values),
	), nil
}

// hasGeneratedColumnNames reports whether any column name came from fallback metadata.
func hasGeneratedColumnNames(columns []Column) bool {
	for _, col := range columns {
		if col.GeneratedName {
			return true
		}
	}
	return false
}

// whereClause builds a WHERE clause from primary keys or all columns.
func whereClause(columns []Column, values []any) string {
	indexes := primaryKeyIndexes(columns)
	if len(indexes) == 0 {
		indexes = make([]int, len(columns))
		for i := range columns {
			indexes[i] = i
		}
	}

	parts := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		if values[idx] == nil {
			parts = append(parts, fmt.Sprintf("%s IS NULL", quoteIdent(columns[idx].Name)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s = %s", quoteIdent(columns[idx].Name), formatValue(values[idx], columns[idx])))
	}
	return strings.Join(parts, " AND ")
}

// primaryKeyIndexes returns indexes of columns marked as primary keys.
func primaryKeyIndexes(columns []Column) []int {
	var indexes []int
	for i, col := range columns {
		if col.PrimaryKey {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

// quoteTable quotes a fully qualified table name.
func quoteTable(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

// quoteIdent quotes a MySQL identifier.
func quoteIdent(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

// formatValue renders a Go value as a MySQL SQL literal.
func formatValue(value any, column Column) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return formatTextValue(v, column.Charset)
	case []byte:
		return "X'" + strings.ToUpper(hex.EncodeToString(v)) + "'"
	case time.Time:
		return quoteString(v.Format("2006-01-02 15:04:05.999999"))
	case bool:
		if v {
			return "1"
		}
		return "0"
	case int:
		return strconv.FormatInt(int64(v), 10)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return quoteString(fmt.Sprint(v))
	}
}

func formatTextValue(value, charset string) string {
	charset = strings.ToLower(strings.TrimSpace(charset))
	if charset == "" || charset == "utf8" || charset == "utf8mb3" || charset == "utf8mb4" {
		if utf8.ValidString(value) {
			return quoteString(value)
		}
		return charsetHexLiteral("binary", []byte(value))
	}
	if charset == "binary" {
		return "X'" + strings.ToUpper(hex.EncodeToString([]byte(value))) + "'"
	}
	encoding, err := htmlindex.Get(charset)
	if err == nil {
		decoded, err := encoding.NewDecoder().String(value)
		if err == nil && utf8.ValidString(decoded) {
			return quoteString(decoded)
		}
	}
	return charsetHexLiteral(charset, []byte(value))
}

func charsetHexLiteral(charset string, value []byte) string {
	return "_" + charset + " 0x" + strings.ToUpper(hex.EncodeToString(value))
}

// quoteString quotes and escapes a MySQL string literal.
func quoteString(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"'", "''",
		"\x00", "\\0",
		"\n", "\\n",
		"\r", "\\r",
	)
	return "'" + replacer.Replace(value) + "'"
}

// valuesEqual compares row values with special handling for bytes and time values.
func valuesEqual(left, right any) bool {
	leftBytes, leftOK := left.([]byte)
	rightBytes, rightOK := right.([]byte)
	if leftOK || rightOK {
		return leftOK && rightOK && bytes.Equal(leftBytes, rightBytes)
	}

	leftTime, leftOK := left.(time.Time)
	rightTime, rightOK := right.(time.Time)
	if leftOK || rightOK {
		return leftOK && rightOK && leftTime.Equal(rightTime)
	}

	return reflect.DeepEqual(left, right)
}
