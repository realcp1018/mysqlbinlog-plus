package binlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
	"mysqlbinlog-plus/internal/filter"
	"mysqlbinlog-plus/internal/mysql"
)

const binlogStartPos = 4

var goMySQLLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

type RowEvent struct {
	File       string
	StartPos   uint32
	EndPos     uint32
	EventTime  time.Time
	DDLSQLText string
	Change     event.RowChange
}

type Reader struct {
	cfg config.Config
	// schemaResolver loads MySQL table metadata in online and mixed modes; it is nil in local mode.
	schemaResolver *mysql.SchemaResolver
}

// NewReader creates a binlog reader with an optional MySQL schema resolver.
func NewReader(cfg config.Config, schemaResolver *mysql.SchemaResolver) *Reader {
	return &Reader{cfg: cfg, schemaResolver: schemaResolver}
}

// ParseBinlogs parses configured local binlogs and handles row events.
func (r *Reader) ParseBinlogs(ctx context.Context, rowEventHandler func(RowEvent) error) error {
	fromTime, toTime, err := r.cfg.TimeRange()
	if err != nil {
		return err
	}

	parser := replication.NewBinlogParser()
	parser.SetParseTime(true)
	state := &parserState{}

	for _, file := range r.cfg.Binlogs {
		if err := parser.ParseFile(file, int64(binlogStartPos), func(e *replication.BinlogEvent) error {
			select {
			case <-ctx.Done():
				parser.Stop()
				return ctx.Err()
			default:
			}

			rowEvents, err := r.processEvent(file, e, fromTime, toTime, state)
			if err != nil {
				return err
			}
			for _, rowEvent := range rowEvents {
				if err := rowEventHandler(rowEvent); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			var stop stopParseError
			if errors.As(err, &stop) {
				break
			}
			return err
		}
	}
	return nil
}

// StreamOnline streams binlog events from an online MySQL position.
func (r *Reader) StreamOnline(ctx context.Context, position mysql.BinlogPosition, serverID uint32, rowEventHandler func(RowEvent) error) error {
	fromTime, toTime, err := r.cfg.TimeRange()
	if err != nil {
		return err
	}

	syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID:  serverID,
		Flavor:    "mysql",
		Host:      r.cfg.Host,
		Port:      uint16(r.cfg.Port),
		User:      r.cfg.User,
		Password:  r.cfg.Password,
		ParseTime: true,
		Logger:    goMySQLLogger,
	})
	defer syncer.Close()

	streamer, err := syncer.StartSync(gomysql.Position{Name: position.File, Pos: position.Pos})
	if err != nil {
		return err
	}
	state := &parserState{}
	currentFile := position.File

	for {
		e, err := streamer.GetEvent(ctx)
		if err != nil {
			return err
		}
		if rotate, ok := e.Event.(*replication.RotateEvent); ok && len(rotate.NextLogName) > 0 {
			currentFile = string(rotate.NextLogName)
		}
		rowEvents, err := r.processEvent(currentFile, e, fromTime, toTime, state)
		if err != nil {
			var stop stopParseError
			if errors.As(err, &stop) {
				return nil
			}
			return err
		}
		for _, rowEvent := range rowEvents {
			if err := rowEventHandler(rowEvent); err != nil {
				return err
			}
		}
	}
}

// FetchRemoteBinlogs reads configured online binlogs through the supplied master status snapshot.
func (r *Reader) FetchRemoteBinlogs(ctx context.Context, serverID uint32, snapshot mysql.BinlogPosition, rowEventHandler func(RowEvent) error) error {
	fromTime, toTime, err := r.cfg.TimeRange()
	if err != nil {
		return err
	}

	state := &parserState{}
	for _, file := range r.cfg.Binlogs {
		syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
			ServerID:  serverID,
			Flavor:    "mysql",
			Host:      r.cfg.Host,
			Port:      uint16(r.cfg.Port),
			User:      r.cfg.User,
			Password:  r.cfg.Password,
			ParseTime: true,
			Logger:    goMySQLLogger,
		})
		startPos := uint32(binlogStartPos)
		endPos := uint32(0)
		if file == snapshot.File {
			endPos = snapshot.Pos
		}
		err := r.fetchRemoteBinlog(ctx, syncer, file, startPos, endPos, fromTime, toTime, state, rowEventHandler)
		syncer.Close()
		if err != nil {
			return err
		}
		if endPos > 0 {
			return nil
		}
	}
	return nil
}

// fetchRemoteBinlog streams one online binlog until rotation, snapshot, or range end.
func (r *Reader) fetchRemoteBinlog(ctx context.Context, syncer *replication.BinlogSyncer, file string, startPos, endPos uint32, fromTime, toTime *time.Time, state *parserState, rowEventHandler func(RowEvent) error) error {
	streamer, err := syncer.StartSync(gomysql.Position{Name: file, Pos: startPos})
	if err != nil {
		return err
	}

	for {
		e, err := streamer.GetEvent(ctx)
		if err != nil {
			return err
		}
		if e.Header == nil {
			return fmt.Errorf("binlog event is missing header")
		}
		if _, ok := e.Event.(*replication.RotateEvent); ok {
			// MySQL sends a fake rotate event when the replication stream starts.
			if e.Header.Timestamp == 0 || e.Header.LogPos == 0 {
				continue
			}
			// A real rotate event marks the end of the current binlog.
			return nil
		}
		eventStartPos := e.Header.LogPos - e.Header.EventSize
		if endPos > 0 && eventStartPos >= endPos && (!state.inTransaction || state.transactionBeforeRange) {
			return nil
		}
		rowEvents, err := r.processEvent(file, e, fromTime, toTime, state)
		if err != nil {
			var stop stopParseError
			if errors.As(err, &stop) {
				return nil
			}
			return err
		}
		for _, rowEvent := range rowEvents {
			if err := rowEventHandler(rowEvent); err != nil {
				return err
			}
		}
		if endPos > 0 && e.Header.LogPos >= endPos && !state.inTransaction {
			return nil
		}
	}
}

// processEvent filters and converts one raw binlog event into output events.
func (r *Reader) processEvent(file string, e *replication.BinlogEvent, fromTime, toTime *time.Time, state *parserState) ([]RowEvent, error) {
	if e.Header == nil {
		return nil, fmt.Errorf("binlog event is missing header")
	}

	startPos := e.Header.LogPos - e.Header.EventSize
	if len(r.cfg.Binlogs) == 1 {
		if r.cfg.FromPos > 0 && startPos < r.cfg.FromPos {
			state.update(e, false)
			return nil, nil
		}
		if r.cfg.ToPos > 0 && startPos >= r.cfg.ToPos {
			if !state.inTransaction || state.transactionBeforeRange {
				return nil, stopParseError{}
			}
		}
	}

	eventTime := time.Unix(int64(e.Header.Timestamp), 0)
	if fromTime != nil && eventTime.Before(*fromTime) {
		state.update(e, false)
		return nil, nil
	}
	if toTime != nil && !eventTime.Before(*toTime) {
		if !state.inTransaction || state.transactionBeforeRange {
			return nil, stopParseError{}
		}
	}
	if state.inTransaction && state.transactionBeforeRange {
		state.update(e, false)
		return nil, nil
	}
	state.update(e, true)

	if ddl, ok := r.ddlEvent(file, startPos, e.Header.LogPos, eventTime, e); ok {
		return []RowEvent{ddl}, nil
	}

	rows, ok := e.Event.(*replication.RowsEvent)
	if !ok {
		return nil, nil
	}
	if rows.Table == nil {
		return nil, fmt.Errorf("rows event is missing table map metadata")
	}

	changeType, ok := convertType(rows.Type())
	if !ok || !r.matchSQLType(changeType) {
		return nil, nil
	}

	schema := string(rows.Table.Schema)
	table := string(rows.Table.Table)
	if !filter.MatchAny(r.cfg.TablePatterns, schema, table) {
		return nil, nil
	}

	fallbackColumns := convertColumns(rows.Table)
	columns := fallbackColumns
	if r.schemaResolver != nil && !state.schemaUnreliable {
		resolved, metadataMismatch, err := r.schemaResolver.Resolve(schema, table, fallbackColumns)
		if err != nil {
			return nil, err
		}
		if metadataMismatch && state.markMetadataMismatchWarned(schema, table) {
			fmt.Fprintf(os.Stderr, "Warning: current MySQL metadata for %s.%s is incompatible with this binlog event; using binlog metadata. Original SQL may use generated column_N names and rollback SQL may be unavailable.\n", schema, table)
		}
		columns = resolved
	}
	rowEvents := make([]RowEvent, 0, len(rows.Rows))
	switch changeType {
	case event.Insert:
		for _, row := range rows.Rows {
			rowEvents = append(rowEvents, r.rowEvent(file, startPos, e.Header.LogPos, eventTime, schema, table, changeType, columns, nil, row))
		}
	case event.Delete:
		for _, row := range rows.Rows {
			rowEvents = append(rowEvents, r.rowEvent(file, startPos, e.Header.LogPos, eventTime, schema, table, changeType, columns, row, nil))
		}
	case event.Update:
		if len(rows.Rows)%2 != 0 {
			return nil, fmt.Errorf("update rows event requires paired before and after images")
		}
		for i := 0; i < len(rows.Rows); i += 2 {
			rowEvents = append(rowEvents, r.rowEvent(file, startPos, e.Header.LogPos, eventTime, schema, table, changeType, columns, rows.Rows[i], rows.Rows[i+1]))
		}
	default:
		return nil, nil
	}
	return rowEvents, nil
}

// ddlEvent converts schema-changing query events into DDL output events.
func (r *Reader) ddlEvent(file string, startPos, endPos uint32, eventTime time.Time, e *replication.BinlogEvent) (RowEvent, bool) {
	if r.cfg.Rollback || !r.matchSQLType(event.DDL) {
		return RowEvent{}, false
	}
	queryEvent, ok := e.Event.(*replication.QueryEvent)
	if !ok {
		return RowEvent{}, false
	}
	query := strings.TrimSpace(string(queryEvent.Query))
	if !isSchemaChangingQuery(strings.ToUpper(query)) {
		return RowEvent{}, false
	}
	if !strings.HasSuffix(query, ";") {
		query += ";"
	}
	return RowEvent{
		File:       file,
		StartPos:   startPos,
		EndPos:     endPos,
		EventTime:  eventTime,
		DDLSQLText: query,
		Change: event.RowChange{
			Type: event.DDL,
		},
	}, true
}

// rowEvent builds a DML row event from decoded binlog row images.
func (r *Reader) rowEvent(file string, startPos, endPos uint32, eventTime time.Time, schema, table string, typ event.SQLType, columns []event.Column, before, after []any) RowEvent {
	return RowEvent{
		File:      file,
		StartPos:  startPos,
		EndPos:    endPos,
		EventTime: eventTime,
		Change: event.RowChange{
			Schema: schema,
			Table:  table,
			Type:   typ,
			Cols:   columns,
			Before: before,
			After:  after,
		},
	}
}

// matchSQLType reports whether an event type passes the configured SQL filter.
func (r *Reader) matchSQLType(typ event.SQLType) bool {
	if len(r.cfg.SQLTypes) == 0 {
		return true
	}
	for _, sqlType := range r.cfg.SQLTypes {
		if sqlType == string(typ) {
			return true
		}
	}
	return false
}

type parserState struct {
	// inTransaction reports whether parsing is currently inside a transaction.
	inTransaction bool
	// transactionBeforeRange reports whether the current transaction began before the selected range.
	transactionBeforeRange bool
	// schemaUnreliable reports whether in-range DDL makes current table metadata unsafe to use.
	schemaUnreliable bool
	// metadataMismatchWarned tracks tables already reported for current metadata incompatibility.
	metadataMismatchWarned map[string]struct{}
}

// markMetadataMismatchWarned reports whether this is the first metadata mismatch for a table.
func (s *parserState) markMetadataMismatchWarned(schema, table string) bool {
	if s.metadataMismatchWarned == nil {
		s.metadataMismatchWarned = make(map[string]struct{})
	}
	key := schema + "." + table
	if _, ok := s.metadataMismatchWarned[key]; ok {
		return false
	}
	s.metadataMismatchWarned[key] = struct{}{}
	return true
}

// update updates state for Reader's binlog event parsing flow.
func (s *parserState) update(e *replication.BinlogEvent, inRange bool) {
	switch event := e.Event.(type) {
	case *replication.QueryEvent:
		query := strings.ToUpper(strings.TrimSpace(string(event.Query)))
		switch query {
		case "BEGIN":
			s.inTransaction = true
			s.transactionBeforeRange = !inRange
		case "COMMIT", "ROLLBACK":
			s.inTransaction = false
			s.transactionBeforeRange = false
		default:
			if inRange && isSchemaChangingQuery(query) && !strings.HasPrefix(query, "TRUNCATE TABLE") {
				s.schemaUnreliable = true
			}
		}
	case *replication.XIDEvent:
		s.inTransaction = false
		s.transactionBeforeRange = false
	case *replication.RowsEvent:
		if !s.inTransaction {
			s.inTransaction = true
			s.transactionBeforeRange = !inRange
		}
	}
}

// stopParseError signals that parsing should stop.
type stopParseError struct{}

// Error returns the sentinel stop parsing message.
func (e stopParseError) Error() string {
	return "stop parsing"
}

// isSchemaChangingQuery reports whether a query is a supported DDL statement.
func isSchemaChangingQuery(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	for _, prefix := range []string{
		"ALTER TABLE",
		"ALTER VIEW",
		"CREATE DATABASE",
		"CREATE INDEX",
		"CREATE TABLE",
		"CREATE VIEW",
		"DROP DATABASE",
		"DROP INDEX",
		"DROP TABLE",
		"DROP VIEW",
		"RENAME TABLE",
		"TRUNCATE TABLE",
	} {
		if strings.HasPrefix(query, prefix) {
			return true
		}
	}
	return false
}

// convertType maps a rows event type to an internal SQL event type.
func convertType(typ replication.EnumRowsEventType) (event.SQLType, bool) {
	switch typ {
	case replication.EnumRowsEventTypeInsert:
		return event.Insert, true
	case replication.EnumRowsEventTypeUpdate:
		return event.Update, true
	case replication.EnumRowsEventTypeDelete:
		return event.Delete, true
	default:
		return "", false
	}
}

// convertColumns converts table-map metadata into internal column metadata.
func convertColumns(table *replication.TableMapEvent) []event.Column {
	count := int(table.ColumnCount)
	names := table.ColumnNameString()
	primaryKeys := make(map[int]struct{}, len(table.PrimaryKey))
	for _, idx := range table.PrimaryKey {
		primaryKeys[int(idx)] = struct{}{}
	}

	columns := make([]event.Column, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("column_%d", i+1)
		if i < len(names) && names[i] != "" {
			name = names[i]
		}
		_, primaryKey := primaryKeys[i]
		generatedName := false
		if i >= len(names) || names[i] == "" {
			generatedName = true
		}
		columns[i] = event.Column{Name: name, PrimaryKey: primaryKey, GeneratedName: generatedName}
	}
	return columns
}
