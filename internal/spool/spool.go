package spool

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Record struct {
	ID         int64
	BinlogFile string
	StartPos   uint32
	EndPos     uint32
	EventTime  time.Time
	SchemaName string
	TableName  string
	EventType  string
	SQLText    string
}

type Store struct {
	dir string
}

// NewStore creates a rollback spool store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Init prepares an empty spool directory for a run.
func (s *Store) Init() error {
	if s.dir == "" {
		return fmt.Errorf("spool directory is required")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("spool directory %q must be empty", s.dir)
	}
	return nil
}

// Cleanup removes all spool files while keeping the spool directory.
func (s *Store) Cleanup() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(s.dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// NewWriter opens a transactional writer for one binlog file.
func (s *Store) NewWriter(ctx context.Context, binlogFile string) (*Writer, error) {
	if binlogFile == "" {
		return nil, fmt.Errorf("binlog file is required")
	}

	db, err := s.open(binlogFile)
	if err != nil {
		return nil, err
	}
	if err := initSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO rollback_records (
	binlog_file, start_pos, end_pos, event_time, schema_name, table_name, event_type, sql_text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, err
	}

	return &Writer{binlogFile: binlogFile, db: db, tx: tx, stmt: stmt}, nil
}

// ReadReverse reads all records in reverse binlog and row order.
func (s *Store) ReadReverse(ctx context.Context, binlogFiles []string, emit func(Record) error) error {
	for i := len(binlogFiles) - 1; i >= 0; i-- {
		if err := s.ReadReverseRange(ctx, binlogFiles[i], 0, 0, emit); err != nil {
			return err
		}
	}
	return nil
}

// CountRecords returns the number of rollback records for one binlog file.
func (s *Store) CountRecords(ctx context.Context, binlogFile string) (int, error) {
	exists, err := s.hasBinlogFile(binlogFile)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	db, err := s.open(binlogFile)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rollback_records`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ReadReverseRange reads a row ID range in descending order.
func (s *Store) ReadReverseRange(ctx context.Context, binlogFile string, lowID, highID int64, emit func(Record) error) error {
	exists, err := s.hasBinlogFile(binlogFile)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	db, err := s.open(binlogFile)
	if err != nil {
		return err
	}
	err = readReverseFile(ctx, db, lowID, highID, emit)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// hasBinlogFile reports whether a rollback SQLite file exists for binlogFile.
func (s *Store) hasBinlogFile(binlogFile string) (bool, error) {
	_, err := os.Stat(filepath.Join(s.dir, filepath.Base(binlogFile)+".sqlite"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// open opens the SQLite spool file for one binlog file.
func (s *Store) open(binlogFile string) (*sql.DB, error) {
	path := filepath.Join(s.dir, filepath.Base(binlogFile)+".sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	return db, nil
}

type Writer struct {
	binlogFile string
	db         *sql.DB
	tx         *sql.Tx
	stmt       *sql.Stmt
}

// Append writes one rollback record through the active transaction.
func (w *Writer) Append(ctx context.Context, record Record) error {
	if record.SQLText == "" {
		return fmt.Errorf("sql text is required")
	}
	if record.BinlogFile == "" {
		record.BinlogFile = w.binlogFile
	}
	if record.BinlogFile != w.binlogFile {
		return fmt.Errorf("writer for %q cannot append record for %q", w.binlogFile, record.BinlogFile)
	}

	_, err := w.stmt.ExecContext(ctx,
		record.BinlogFile,
		record.StartPos,
		record.EndPos,
		record.EventTime.Format(time.RFC3339Nano),
		record.SchemaName,
		record.TableName,
		record.EventType,
		record.SQLText,
	)
	return err
}

// Close commits the transaction and closes writer resources.
func (w *Writer) Close() error {
	var firstErr error
	if w.stmt != nil {
		if err := w.stmt.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.stmt = nil
	}
	if w.tx != nil {
		if firstErr != nil {
			_ = w.tx.Rollback()
		} else if err := w.tx.Commit(); err != nil {
			firstErr = err
		}
		w.tx = nil
	}
	if w.db != nil {
		if err := w.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.db = nil
	}
	return firstErr
}

// Abort rolls back the transaction and closes writer resources.
func (w *Writer) Abort() error {
	var firstErr error
	if w.stmt != nil {
		if err := w.stmt.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.stmt = nil
	}
	if w.tx != nil {
		if err := w.tx.Rollback(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.tx = nil
	}
	if w.db != nil {
		if err := w.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.db = nil
	}
	return firstErr
}

// initSchema configures SQLite and creates the rollback table.
func initSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = OFF`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous = OFF`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA temp_store = MEMORY`); err != nil {
		return err
	}

	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS rollback_records (
	id INTEGER PRIMARY KEY,
	binlog_file TEXT,
	start_pos INTEGER,
	end_pos INTEGER,
	event_time TEXT,
	schema_name TEXT,
	table_name TEXT,
	event_type TEXT,
	sql_text TEXT NOT NULL
)`)
	return err
}

// readReverseFile emits rollback records from one SQLite file in reverse order.
func readReverseFile(ctx context.Context, db *sql.DB, lowID, highID int64, emit func(Record) error) error {
	query := `
SELECT id, binlog_file, start_pos, end_pos, event_time, schema_name, table_name, event_type, sql_text
FROM rollback_records`

	var (
		rows *sql.Rows
		err  error
	)
	if lowID > 0 && highID > 0 {
		rows, err = db.QueryContext(ctx, query+` WHERE id BETWEEN ? AND ? ORDER BY id DESC`, lowID, highID)
	} else {
		rows, err = db.QueryContext(ctx, query+` ORDER BY id DESC`)
	}
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			record    Record
			eventTime string
		)
		if err := rows.Scan(
			&record.ID,
			&record.BinlogFile,
			&record.StartPos,
			&record.EndPos,
			&eventTime,
			&record.SchemaName,
			&record.TableName,
			&record.EventType,
			&record.SQLText,
		); err != nil {
			return err
		}
		if eventTime != "" {
			parsed, err := time.Parse(time.RFC3339Nano, eventTime)
			if err != nil {
				return err
			}
			record.EventTime = parsed
		}
		if err := emit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}
