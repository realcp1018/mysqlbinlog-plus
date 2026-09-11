package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/vars"
)

type sqlWriter struct {
	stdout         *os.File
	file           *os.File
	output         string
	chunkSize      int
	rowCount       int
	fileIndex      int
	charsetWritten bool
}

// newSQLWriter creates a SQL writer for stdout, one file, or split files.
func newSQLWriter(cfg config.Config) (*sqlWriter, error) {
	writer := &sqlWriter{
		stdout:    os.Stdout,
		output:    cfg.Output,
		chunkSize: cfg.OutputChunkSize,
	}
	if cfg.Output != "" {
		dir := filepath.Dir(cfg.Output)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
	}
	if cfg.Output != "" && cfg.OutputChunkSize > 0 {
		return writer, nil
	}
	if cfg.Output != "" {
		file, err := os.Create(cfg.Output)
		if err != nil {
			return nil, err
		}
		writer.file = file
		return writer, nil
	}
	return writer, nil
}

// Write writes one SQL statement to the configured destination.
func (w *sqlWriter) Write(sqlText string) error {
	if w.output != "" && w.chunkSize > 0 {
		if w.file == nil || w.rowCount >= w.chunkSize {
			if err := w.rotate(); err != nil {
				return err
			}
		}
		if err := w.writeOutputPreamble(); err != nil {
			return err
		}
		w.rowCount++
		_, err := fmt.Fprintln(w.file, sqlText)
		return err
	}
	if w.file != nil {
		if err := w.writeOutputPreamble(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w.file, sqlText)
		return err
	}
	if err := w.writeOutputPreamble(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w.stdout, sqlText)
	return err
}

// rotate opens the next split output file.
func (w *sqlWriter) rotate() error {
	if err := w.Close(); err != nil {
		return err
	}
	w.fileIndex++
	w.rowCount = 0
	w.charsetWritten = false
	path := fmt.Sprintf("%s.%06d", w.output, w.fileIndex)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	w.file = file
	return nil
}

// writeOutputPreamble writes the configuration required before generated SQL text.
func (w *sqlWriter) writeOutputPreamble() error {
	if w.charsetWritten {
		return nil
	}
	output := w.stdout
	if w.file != nil {
		output = w.file
	}
	if err := writeSQLPreamble(output); err != nil {
		return err
	}
	w.charsetWritten = true
	return nil
}

// writeSQLPreamble writes the session settings required by generated SQL.
func writeSQLPreamble(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "SET NAMES utf8mb4;"); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, "SET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');")
	return err
}

// Close closes the current output file if one is open.
func (w *sqlWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// appendEventTimeComment adds the source binlog event time to generated SQL.
func appendEventTimeComment(sqlText string, eventTime time.Time) string {
	if eventTime.IsZero() {
		return sqlText
	}
	return fmt.Sprintf("%s -- %s", sqlText, eventTime.Format(vars.TimeFormat))
}
