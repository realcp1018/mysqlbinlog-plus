// Package app implements the executable binlog flows used by the CLI. It wires
// MySQL/local binlog readers, SQL generation, output writers, and rollback
// cache handling for each selected mode.
package app

import (
	"context"
	"fmt"
	"log"
	"os"

	"mysqlbinlog-plus/internal/binlog"
	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
	"mysqlbinlog-plus/internal/mysql"
	"mysqlbinlog-plus/internal/spool"
	"mysqlbinlog-plus/internal/vars"
)

var rollbackLogger = log.New(os.Stderr, "", log.LstdFlags)

// RunOriginal parses selected binlog events and writes original SQL.
func RunOriginal(cfg config.Config) error {
	ctx := context.Background()
	writer, err := newSQLWriter(cfg)
	if err != nil {
		return err
	}
	defer writer.Close()

	rowEventHandler := newOriginalEventHandler(writer, cfg.NoPrimaryKey)

	switch cfg.Mode {
	case vars.ModeOnline:
		client, err := mysql.Open(cfg)
		if err != nil {
			return err
		}
		defer client.Close()

		if err := client.Ping(ctx); err != nil {
			return err
		}
		if err := client.CheckBinlogSettings(ctx); err != nil {
			return err
		}
		snapshot, err := client.ShowMasterStatus(ctx)
		if err != nil {
			return err
		}
		serverID := mysql.GenerateServerID()
		if len(cfg.Binlogs) == 0 {
			logs, err := client.ShowBinaryLogs(ctx)
			if err != nil {
				return err
			}
			reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
			cfg.Binlogs, err = reader.DiscoverRemoteBinlogs(ctx, serverID, logs, snapshot)
			if err != nil {
				return err
			}
		} else if err := client.CheckBinaryLogsExist(ctx, cfg.Binlogs); err != nil {
			return err
		}
		reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
		return reader.FetchRemoteBinlogs(ctx, serverID, snapshot, rowEventHandler)
	case vars.ModeMixed:
		client, err := mysql.Open(cfg)
		if err != nil {
			return err
		}
		defer client.Close()

		if err := client.Ping(ctx); err != nil {
			return err
		}
		if err := client.CheckBinlogSettings(ctx); err != nil {
			return err
		}
		reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
		return reader.ParseBinlogs(ctx, rowEventHandler)
	case vars.ModeLocal:
		reader := binlog.NewReader(cfg, nil)
		fmt.Fprintln(os.Stderr, "Warning: local mode requires the source binlog to be generated with binlog_format=ROW and "+
			"binlog_row_image=FULL, otherwise output may be incomplete or incorrect")
		return reader.ParseBinlogs(ctx, rowEventHandler)
	default:
		return fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
}

// RunRollback parses selected binlog events and writes rollback SQL.
func RunRollback(cfg config.Config) error {
	ctx := context.Background()
	rollbackLogger.Println("[INFO] Rollback started.")
	if cfg.RollbackCacheDirIsDefault {
		rollbackLogger.Printf("[WARN] Using default rollback cache dir %q. The rollback may fail if the cache runs out of disk space.", cfg.RollbackCacheDir)
	}
	store := spool.NewStore(cfg.RollbackCacheDir)
	if err := store.Init(); err != nil {
		return err
	}
	rollbackLogger.Printf("[INFO] Building rollback cache in %q...", cfg.RollbackCacheDir)

	rowEventHandler := newRollbackEventHandler(ctx, store, cfg.NoPrimaryKey)

	var readErr error
	switch cfg.Mode {
	case vars.ModeOnline:
		client, err := mysql.Open(cfg)
		if err != nil {
			return err
		}
		defer client.Close()

		if err := client.Ping(ctx); err != nil {
			return err
		}
		if err := client.CheckBinlogSettings(ctx); err != nil {
			return err
		}
		snapshot, err := client.ShowMasterStatus(ctx)
		if err != nil {
			return err
		}
		if err := client.CheckBinaryLogsExist(ctx, cfg.Binlogs); err != nil {
			return err
		}
		reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
		readErr = reader.FetchRemoteBinlogs(ctx, mysql.GenerateServerID(), snapshot, rowEventHandler.Handle)
	case vars.ModeMixed:
		client, err := mysql.Open(cfg)
		if err != nil {
			return err
		}
		defer client.Close()

		if err := client.Ping(ctx); err != nil {
			return err
		}
		if err := client.CheckBinlogSettings(ctx); err != nil {
			return err
		}
		reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
		readErr = reader.ParseBinlogs(ctx, rowEventHandler.Handle)
	case vars.ModeLocal:
		reader := binlog.NewReader(cfg, nil)
		fmt.Fprintln(os.Stderr, "Warning: local mode requires the source binlog to be generated with binlog_format=ROW and "+
			"binlog_row_image=FULL, otherwise output may be incomplete or incorrect")
		readErr = reader.ParseBinlogs(ctx, rowEventHandler.Handle)
	default:
		readErr = fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	if readErr != nil {
		_ = rowEventHandler.Abort()
		return readErr
	}
	if err := rowEventHandler.Close(); err != nil {
		return err
	}
	// After parsing selected binlogs, read cached rollback SQL from sqlite in reverse order and write output.
	if cfg.Output != "" && cfg.OutputChunkSize > 0 {
		rollbackLogger.Println("[INFO] Writing rollback SQL chunks...")
		if err := writeRollbackChunks(ctx, store, cfg); err != nil {
			return err
		}
	} else {
		rollbackLogger.Println("[INFO] Writing rollback SQL...")
		outputWriter, err := newSQLWriter(cfg)
		if err != nil {
			return err
		}

		if err := store.ReadReverse(ctx, cfg.Binlogs, func(record spool.Record) error {
			return outputWriter.Write(appendEventTimeComment(record.SQLText, record.EventTime))
		}); err != nil {
			_ = outputWriter.Close()
			return err
		}
		if err := outputWriter.Close(); err != nil {
			return err
		}
	}

	if err := store.Cleanup(); err != nil {
		return err
	}
	rollbackLogger.Println("[INFO] Rollback completed.")
	return nil
}

// ListOnlineBinlogs prints online MySQL binlog files.
func ListOnlineBinlogs(cfg config.Config) error {
	ctx := context.Background()
	client, err := mysql.Open(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Ping(ctx); err != nil {
		return err
	}
	logs, err := client.ShowBinaryLogs(ctx)
	if err != nil {
		return err
	}
	fmt.Println()
	for _, log := range logs {
		fmt.Fprintf(os.Stdout, "%s\t%d\n", log.Name, log.Size)
	}
	return nil
}

// StreamOnline streams online binlog events and writes original SQL.
func StreamOnline(cfg config.Config) error {
	ctx := context.Background()
	client, err := mysql.Open(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Ping(ctx); err != nil {
		return err
	}
	if err := client.CheckBinlogSettings(ctx); err != nil {
		return err
	}
	binlogStartPos, err := client.ShowMasterStatus(ctx)
	if err != nil {
		return err
	}

	writer, err := newSQLWriter(cfg)
	if err != nil {
		return err
	}
	defer writer.Close()

	reader := binlog.NewReader(cfg, mysql.NewSchemaResolver(client))
	rowEventHandler := newOriginalEventHandler(writer, cfg.NoPrimaryKey)
	return reader.StreamOnline(ctx, binlogStartPos, mysql.GenerateServerID(), rowEventHandler)
}

// newOriginalEventHandler creates a handler that renders original SQL and writes it.
func newOriginalEventHandler(writer *sqlWriter, noPrimaryKey bool) func(binlog.RowEvent) error {
	return func(rowEvent binlog.RowEvent) error {
		sqlText := rowEvent.DDLSQLText
		if rowEvent.Change.Type != event.DDL {
			var err error
			sqlText, err = rowEvent.Change.ToOriginalSQL(event.SQLOptions{NoPrimaryKey: noPrimaryKey})
			if err != nil {
				return err
			}
		}
		return writer.Write(appendEventTimeComment(sqlText, rowEvent.EventTime))
	}
}
