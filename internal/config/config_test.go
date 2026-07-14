package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqlbinlog-plus/internal/vars"
)

func TestValidateAndNormalizeTrimsBinlogsAndPreservesOrder(t *testing.T) {
	cfg := Config{
		Port:             3306,
		User:             "repl",
		Binlogs:          []string{" mysql-bin-new.000001 ", "mysql-bin.000123"},
		OutputChunkSize:  -1,
		RollbackCacheDir: ".mysqlbinlog-plus",
	}
	if err := cfg.ValidateAndNormalize(); err != nil {
		t.Fatalf("ValidateAndNormalize returned error: %v", err)
	}

	want := []string{"mysql-bin-new.000001", "mysql-bin.000123"}
	if !reflect.DeepEqual(cfg.Binlogs, want) {
		t.Fatalf("Binlogs = %v, want %v", cfg.Binlogs, want)
	}
}

func TestValidateAndNormalizeRejectsDuplicateBinlogs(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Binlogs = []string{"mysql-bin.000010", " mysql-bin.000010 "}
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate error", err)
	}
}

func TestValidateAndNormalizeRejectsDuplicateBinlogNames(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Binlogs = []string{
			filepath.Join("first", "mysql-bin.000010"),
			filepath.Join("second", "mysql-bin.000010"),
		}
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "duplicate binlog name") {
		t.Fatalf("error = %v, want duplicate binlog name error", err)
	}
}

func TestValidateAndNormalizeRejectsEmptyBinlogs(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Binlogs = []string{"mysql-bin.000010", " "}
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "empty values") {
		t.Fatalf("error = %v, want empty values error", err)
	}
}

func TestValidateAndNormalizeRejectsMixedRanges(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Binlogs = []string{"mysql-bin.000010"}
		cfg.FromTime = "2026-06-30 10:00:00"
		cfg.FromPos = 120
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want mutually exclusive error", err)
	}
}

func TestValidateAndNormalizeRejectsPositionRangeWithMultipleFiles(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Binlogs = []string{"mysql-bin.000010", "mysql-bin.000011"}
		cfg.FromPos = 120
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "exactly one file") {
		t.Fatalf("error = %v, want exactly one file error", err)
	}
}

func TestValidateAndNormalizeRejectsZeroOutputChunkSize(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.OutputChunkSize = 0
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--output-chunk-size must be -1 or greater than 0") {
		t.Fatalf("error = %v, want output chunk size error", err)
	}
}

func TestValidateAndNormalizeRejectsChunkSizeWithoutOutput(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.OutputChunkSize = 100
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--output-chunk-size requires --output") {
		t.Fatalf("error = %v, want output required error", err)
	}
}

func TestValidateAndNormalizeRollbackRequiresCacheDir(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Rollback = true
		cfg.RollbackCacheDir = ""
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--rollback requires --rollback-cache-dir") {
		t.Fatalf("error = %v, want rollback cache dir error", err)
	}
	if !strings.Contains(err.Error(), "make sure you have enough disk space for cache") {
		t.Fatalf("error = %v, want disk space hint", err)
	}
}

func TestValidateAndNormalizeRejectsOutputInsideRollbackCacheDir(t *testing.T) {
	cacheDir := t.TempDir()
	cfg := validConfig(func(cfg *Config) {
		cfg.Rollback = true
		cfg.RollbackCacheDir = cacheDir
		cfg.Output = filepath.Join(cacheDir, "rollback.sql")
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "must not be inside") {
		t.Fatalf("error = %v, want output inside rollback cache error", err)
	}
}

func TestValidateAndNormalizeRejectsInvalidTimeRange(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.FromTime = "2026-06-30 11:00:00"
		cfg.ToTime = "2026-06-30 10:00:00"
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--from-time must be less than --to-time") {
		t.Fatalf("error = %v, want time order error", err)
	}
}

func TestValidateAndNormalizeNormalizesSQLTypes(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.SQLTypes = []string{"INSERT", "update", "DDL", "insert"}
	})
	if err := cfg.ValidateAndNormalize(); err != nil {
		t.Fatalf("ValidateAndNormalize returned error: %v", err)
	}

	want := []string{"insert", "update", "ddl"}
	if !reflect.DeepEqual(cfg.SQLTypes, want) {
		t.Fatalf("SQLTypes = %v, want %v", cfg.SQLTypes, want)
	}
}

func TestValidateAndNormalizeRejectsInvalidSQLType(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.SQLTypes = []string{"replace"}
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--sql-type must be one of insert, update, delete, ddl") {
		t.Fatalf("error = %v, want sql type choices error", err)
	}
}

func TestValidateAndNormalizeRejectsRollbackDDLOnly(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Rollback = true
		cfg.SQLTypes = []string{"ddl"}
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "rollback output does not include DDL") {
		t.Fatalf("error = %v, want rollback DDL-only error", err)
	}
}

func TestValidateAndNormalizeRejectsInvalidMode(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Mode = "s3"
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--mode must be") {
		t.Fatalf("error = %v, want --mode must be error", err)
	}
}

func TestValidateAndNormalizeAcceptsMixedMode(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Mode = vars.ModeMixed
	})
	if err := cfg.ValidateAndNormalize(); err != nil {
		t.Fatalf("ValidateAndNormalize returned error: %v", err)
	}
}

func TestValidateAndNormalizeListBinlogsRequiresOnlineMode(t *testing.T) {
	cfg := validConfig(func(cfg *Config) {
		cfg.Mode = vars.ModeLocal
		cfg.ListBinlogs = true
	})
	err := cfg.ValidateAndNormalize()
	if err == nil || !strings.Contains(err.Error(), "--list-binlogs requires --mode=online") {
		t.Fatalf("error = %v, want --list-binlogs mode error", err)
	}
}

func TestValidateAndNormalizeDefaultsModeToMixed(t *testing.T) {
	cfg := validConfig()
	if err := cfg.ValidateAndNormalize(); err != nil {
		t.Fatalf("ValidateAndNormalize returned error: %v", err)
	}
	if cfg.Mode != vars.ModeMixed {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, vars.ModeMixed)
	}
}

func validConfig(mutators ...func(*Config)) Config {
	cfg := Config{
		Port:             3306,
		User:             "repl",
		OutputChunkSize:  -1,
		RollbackCacheDir: ".mysqlbinlog-plus",
	}
	for _, mutate := range mutators {
		mutate(&cfg)
	}
	return cfg
}
