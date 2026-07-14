package cmd

import (
	"fmt"
	"mysqlbinlog-plus/internal/app"
	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/vars"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var cfg config.Config

var version bool

var rootCmd = &cobra.Command{
	Use:   vars.AppName,
	Short: "MySQL binlog parser",
	Long:  fmt.Sprintf("%s generates original SQL or rollback SQL from MySQL row-based binlog events.", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		if version {
			printVersion()
			return nil
		}

		if err := cfg.ValidateAndNormalize(); err != nil {
			return err
		}
		if requiresMySQL(cfg) && !cmd.Flags().Changed("password") {
			password, err := promptPassword(cmd)
			if err != nil {
				return err
			}
			cfg.Password = password
		}
		// Choose execution branch from config:
		// - list online binlogs (mode=online)
		// - stream online binlog events (mode=online)
		// - parse selected binlogs and generate rollback SQL (mode=online/mixed/local)
		// - parse selected binlogs and generate original SQL (mode=online/mixed/local)
		if cfg.ListBinlogs {
			return app.ListOnlineBinlogs(cfg)
		}
		if len(cfg.Binlogs) == 0 {
			if cfg.Mode != vars.ModeOnline {
				return fmt.Errorf("--mode=%s requires --binlogs", cfg.Mode)
			}
			return app.StreamOnline(cfg)
		}
		if cfg.Rollback {
			return app.RunRollback(cfg)
		}
		return app.RunOriginal(cfg)
	},
}

// promptPassword reads a password without echoing it in an interactive terminal.
func promptPassword(cmd *cobra.Command) (string, error) {
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return "", fmt.Errorf("--password is required when standard input is not an interactive terminal")
	}

	fmt.Fprint(cmd.ErrOrStderr(), "Enter password: ")
	password, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(password), nil
}

// requiresMySQL reports whether the selected command path connects to MySQL.
func requiresMySQL(cfg config.Config) bool {
	return cfg.ListBinlogs || cfg.Mode != vars.ModeLocal
}

// initAll registers command flags and version handling.
func initAll() {
	initVersion()
	rootCmd.Flags().StringVar(&cfg.Mode, "mode", "mixed", "binlog mode: local files, local files with MySQL metadata, or online MySQL (local|mixed|online)")
	rootCmd.Flags().StringVar(&cfg.Host, "host", "127.0.0.1", "MySQL host")
	rootCmd.Flags().IntVar(&cfg.Port, "port", 3306, "MySQL port")
	rootCmd.Flags().StringVar(&cfg.User, "user", "root", "MySQL user")
	rootCmd.Flags().StringVar(&cfg.Password, "password", "", "MySQL password (prompts when omitted)")
	rootCmd.Flags().BoolVar(&cfg.ListBinlogs, "list-binlogs", false, "list online binlog files and exit")
	rootCmd.Flags().StringSliceVar(&cfg.Binlogs, "binlogs", nil, "binlogs to parse")
	rootCmd.Flags().StringVar(&cfg.FromTime, "from-time", "", "inclusive transaction start time")
	rootCmd.Flags().StringVar(&cfg.ToTime, "to-time", "", "exclusive transaction start time")
	rootCmd.Flags().Uint32Var(&cfg.FromPos, "from-pos", 0, "inclusive transaction start position")
	rootCmd.Flags().Uint32Var(&cfg.ToPos, "to-pos", 0, "exclusive transaction start position")
	rootCmd.Flags().StringSliceVar(&cfg.TablePatterns, "table-patterns", nil, "table patterns to include")
	rootCmd.Flags().StringSliceVar(&cfg.SQLTypes, "sql-type", nil, "SQL event types to include (insert|update|delete|ddl)")
	rootCmd.Flags().BoolVar(&cfg.Rollback, "rollback", false, "generate rollback SQL for DML row events; DDL statements are not included")
	rootCmd.Flags().BoolVar(&cfg.NoPrimaryKey, "no-primary-key", false, "omit primary key for INSERT SQL")
	rootCmd.Flags().StringVar(&cfg.Output, "output", "", "write output to a file; when chunking is enabled this path is used as the split file base name")
	rootCmd.Flags().IntVar(&cfg.OutputChunkSize, "output-chunk-size", -1, "SQL rows per output chunk; -1 writes a single output file")
	rootCmd.Flags().StringVar(&cfg.RollbackCacheDir, "rollback-cache-dir", "", "rollback SQLite cache dir; required with --rollback")
	rootCmd.MarkFlagsMutuallyExclusive("rollback", "no-primary-key")
}

// Execute initializes and runs the root command.
func Execute() {
	initAll()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
