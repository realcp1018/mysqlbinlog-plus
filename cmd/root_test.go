package cmd

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/vars"
)

var initRootOnce sync.Once

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
