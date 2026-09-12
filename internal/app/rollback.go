package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"mysqlbinlog-plus/internal/binlog"
	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
	"mysqlbinlog-plus/internal/spool"
)

type rollbackEventHandler struct {
	ctx               context.Context
	store             *spool.Store
	writer            *spool.Writer
	currentBinlogFile string
	noPrimaryKey      bool
}

// newRollbackEventHandler creates a stateful rollback record handler.
func newRollbackEventHandler(ctx context.Context, store *spool.Store, noPrimaryKey bool) *rollbackEventHandler {
	return &rollbackEventHandler{
		ctx:          ctx,
		store:        store,
		noPrimaryKey: noPrimaryKey,
	}
}

// Handle converts one row event to rollback SQL and stores it transactionally.
func (h *rollbackEventHandler) Handle(rowEvent binlog.RowEvent) error {
	if rowEvent.File != h.currentBinlogFile {
		if err := h.Close(); err != nil {
			return err
		}
		writer, err := h.store.NewWriter(h.ctx, rowEvent.File)
		if err != nil {
			return err
		}
		h.writer = writer
		h.currentBinlogFile = rowEvent.File
	}

	sqlText, err := rowEvent.Change.ToRollbackSQL(event.SQLOptions{NoPrimaryKey: h.noPrimaryKey})
	if err != nil {
		return err
	}
	return h.writer.Append(h.ctx, spool.Record{
		BinlogFile: rowEvent.File,
		StartPos:   rowEvent.StartPos,
		EndPos:     rowEvent.EndPos,
		EventTime:  rowEvent.EventTime,
		SchemaName: rowEvent.Change.Schema,
		TableName:  rowEvent.Change.Table,
		EventType:  string(rowEvent.Change.Type),
		SQLText:    sqlText,
	})
}

// Close commits and closes the active binlog writer.
func (h *rollbackEventHandler) Close() error {
	if h.writer == nil {
		return nil
	}
	err := h.writer.Close()
	h.writer = nil
	return err
}

// Abort rolls back and closes the active binlog writer.
func (h *rollbackEventHandler) Abort() error {
	if h.writer == nil {
		return nil
	}
	err := h.writer.Abort()
	h.writer = nil
	return err
}

type rollbackChunkPart struct {
	binlogFile string
	// lowID is the inclusive lower SQLite record ID bound.
	lowID int64
	// highID is the inclusive upper SQLite record ID bound.
	highID int64
}

type rollbackChunkPlan struct {
	// index is the global output chunk number, starting from 1; for example, 1 creates <output>.000001.
	index int
	// parts contains one range unless this output chunk crosses a binlog boundary, when it contains two.
	parts []rollbackChunkPart
}

// buildRollbackChunkPlans divides rollback records into reverse output chunks.
func buildRollbackChunkPlans(ctx context.Context, store *spool.Store, binlogFiles []string, chunkSize int) ([]rollbackChunkPlan, error) {
	var (
		plans      []rollbackChunkPlan
		current    rollbackChunkPlan
		currentLen int
	)
	current.index = 1

	for i := len(binlogFiles) - 1; i >= 0; i-- {
		binlogFile := binlogFiles[i]
		count, err := store.CountRecords(ctx, binlogFile)
		if err != nil {
			return nil, err
		}
		for consumed := 0; consumed < count; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			space := chunkSize - currentLen
			take := count - consumed
			if take > space {
				take = space
			}
			highID := int64(count - consumed)
			lowID := highID - int64(take) + 1
			current.parts = append(current.parts, rollbackChunkPart{
				binlogFile: binlogFile,
				lowID:      lowID,
				highID:     highID,
			})
			currentLen += take
			consumed += take

			if currentLen == chunkSize {
				plans = append(plans, current)
				current = rollbackChunkPlan{index: current.index + 1}
				currentLen = 0
			}
		}
	}
	if currentLen > 0 {
		plans = append(plans, current)
	}
	return plans, ctx.Err()
}

// writeRollbackChunks writes rollback output into fixed-size chunk files.
func writeRollbackChunks(ctx context.Context, store *spool.Store, cfg config.Config) error {
	plans, err := buildRollbackChunkPlans(ctx, store, cfg.Binlogs, cfg.OutputChunkSize)
	if err != nil {
		return err
	}

	dir := filepath.Dir(cfg.Output)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if len(plans) == 0 {
		return writeRollbackChunk(ctx, store, cfg.Output, rollbackChunkPlan{index: 1})
	}

	workerCount := runtime.NumCPU()
	if workerCount > len(plans) {
		workerCount = len(plans)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan rollbackChunkPlan)
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for plan := range jobs {
				if err := writeRollbackChunk(ctx, store, cfg.Output, plan); err != nil {
					setErr(err)
					return
				}
			}
		}()
	}

sendJobs:
	for _, plan := range plans {
		select {
		case <-ctx.Done():
			break sendJobs
		case jobs <- plan:
		}
	}
	close(jobs)
	wg.Wait()

	errMu.Lock()
	defer errMu.Unlock()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// writeRollbackChunk writes one planned rollback chunk to disk.
func writeRollbackChunk(ctx context.Context, store *spool.Store, output string, plan rollbackChunkPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := fmt.Sprintf("%s.%06d", output, plan.index)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	wroteSQL := false
	for _, part := range plan.parts {
		if err := store.ReadReverseRange(ctx, part.binlogFile, part.lowID, part.highID, func(record spool.Record) error {
			if !wroteSQL {
				if err := writeSQLPreamble(file); err != nil {
					return err
				}
				wroteSQL = true
			}
			_, err := fmt.Fprintln(file, appendEventTimeComment(record.SQLText, record.EventTime))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
