package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/vars"
)

var initRootOnce sync.Once

// TestRootCmdContextCancellation forwards command cancellation before creating output.
func TestRootCmdContextCancellation(t *testing.T) {
	saved := cfg
	t.Cleanup(func() { cfg = saved })
	cfg = config.Config{
		Mode: vars.ModeLocal, Port: 3306, Binlogs: []string{"mysql-bin.000001"},
		Output: filepath.Join(t.TempDir(), "output.sql"), OutputChunkSize: -1,
	}
	command := &cobra.Command{RunE: rootCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	command.SetArgs([]string{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteContext error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(cfg.Output); !os.IsNotExist(err) {
		t.Fatalf("unexpected output file: %v", err)
	}
}

func TestRootCmdRejectsMutuallyExclusiveFlags(t *testing.T) {
	initRootOnce.Do(initAll)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--rollback", "--no-primary-key"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("Execute returned nil error, want mutually exclusive flag error")
	}
	if !strings.Contains(err.Error(), "if any flags in the group") {
		t.Fatalf("error = %v, want mutually exclusive flag error", err)
	}
}

// TestRootCmdRegistersFlagShorthands verifies the compact options for common filters.
func TestRootCmdRegistersFlagShorthands(t *testing.T) {
	initRootOnce.Do(initAll)

	for name, want := range map[string]string{
		"binlogs":        "B",
		"list-binlogs":   "L",
		"rollback":       "R",
		"table-patterns": "T",
	} {
		flag := rootCmd.Flag(name)
		if flag == nil {
			t.Fatalf("%s flag is not registered", name)
		}
		if flag.Shorthand != want {
			t.Errorf("%s shorthand = %q, want %q", name, flag.Shorthand, want)
		}
	}
}

func TestRequiresMySQL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "local", cfg: config.Config{Mode: vars.ModeLocal}, want: false},
		{name: "mixed", cfg: config.Config{Mode: vars.ModeMixed}, want: true},
		{name: "online", cfg: config.Config{Mode: vars.ModeOnline}, want: true},
		{name: "list online binlogs", cfg: config.Config{Mode: vars.ModeLocal, ListBinlogs: true}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresMySQL(tt.cfg); got != tt.want {
				t.Fatalf("requiresMySQL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPromptPasswordRejectsNonInteractiveInput(t *testing.T) {
	command := rootCmd
	command.SetIn(strings.NewReader("password\n"))
	t.Cleanup(func() {
		command.SetIn(nil)
	})

	_, err := promptPassword(command)
	if err == nil {
		t.Fatal("promptPassword() returned nil error for non-interactive input")
	}
	if !strings.Contains(err.Error(), "not an interactive terminal") {
		t.Fatalf("promptPassword() error = %v, want non-interactive terminal error", err)
	}
}
