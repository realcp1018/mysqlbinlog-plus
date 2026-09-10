package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mysqlbinlog-plus/internal/filter"
	"mysqlbinlog-plus/internal/vars"
)

type Config struct {
	Host                      string
	Port                      int
	User                      string
	Password                  string
	Mode                      string
	ListBinlogs               bool
	Binlogs                   []string
	FromTime                  string
	ToTime                    string
	FromPos                   uint32
	ToPos                     uint32
	TablePatterns             []string
	SQLTypes                  []string
	Rollback                  bool
	NoPrimaryKey              bool
	Output                    string
	OutputChunkSize           int
	RollbackCacheDir          string
	RollbackCacheDirIsDefault bool
}

// ValidateAndNormalize validates user options and normalizes repeatable flags.
func (cfg *Config) ValidateAndNormalize() error {
	if cfg.Port <= 1000 || cfg.Port > 65535 {
		return fmt.Errorf("--port invalid value")
	}
	if cfg.Mode == "" {
		cfg.Mode = vars.ModeOnline
	}
	if cfg.Mode != vars.ModeLocal && cfg.Mode != vars.ModeMixed && cfg.Mode != vars.ModeOnline {
		return fmt.Errorf("--mode must be %q, %q, or %q", vars.ModeLocal, vars.ModeMixed, vars.ModeOnline)
	}
	if cfg.ListBinlogs && cfg.Mode != vars.ModeOnline {
		return fmt.Errorf("--list-binlogs requires --mode=online")
	}
	if !cfg.ListBinlogs && len(cfg.Binlogs) == 0 {
		if cfg.Mode != vars.ModeOnline {
			return fmt.Errorf("--mode=%s requires --binlogs", cfg.Mode)
		}
		if cfg.Rollback && cfg.FromTime == "" && cfg.ToTime == "" {
			return fmt.Errorf("--rollback requires --binlogs or --from-time/--to-time in online mode")
		}
	}
	if cfg.OutputChunkSize == 0 || cfg.OutputChunkSize < -1 {
		return fmt.Errorf("--output-chunk-size must be -1 or greater than 0")
	}
	if cfg.OutputChunkSize > 0 && cfg.Output == "" {
		return fmt.Errorf("--output-chunk-size requires --output")
	}
	cfg.RollbackCacheDirIsDefault = false
	if cfg.Rollback && cfg.RollbackCacheDir == "" {
		cfg.RollbackCacheDir = vars.DefaultRollbackCacheDir
		cfg.RollbackCacheDirIsDefault = true
	}
	if cfg.Rollback && cfg.Output != "" {
		if err := cfg.checkRollbackOutput(); err != nil {
			return err
		}
	}

	hasTimeRange := cfg.FromTime != "" || cfg.ToTime != ""
	hasPosRange := cfg.FromPos != 0 || cfg.ToPos != 0
	if hasTimeRange && hasPosRange {
		return fmt.Errorf("time range and position range are mutually exclusive, use one of them")
	}
	if hasPosRange && len(cfg.Binlogs) != 1 {
		return fmt.Errorf("--from-pos and --to-pos can only be used when --binlogs contains exactly one file")
	}
	if cfg.FromPos != 0 && cfg.ToPos != 0 && cfg.FromPos >= cfg.ToPos {
		return fmt.Errorf("--from-pos must be less than --to-pos")
	}
	if err := cfg.checkTimeRange(); err != nil {
		return err
	}

	if err := cfg.checkBinlogs(); err != nil {
		return err
	}

	patterns, err := filter.NormalizePatterns(cfg.TablePatterns)
	if err != nil {
		return err
	}
	cfg.TablePatterns = patterns

	if err := cfg.checkSQLTypes(); err != nil {
		return err
	}
	if cfg.Rollback && len(cfg.SQLTypes) == 1 && cfg.SQLTypes[0] == "ddl" {
		return fmt.Errorf("--rollback cannot be used with --sql-type=ddl only; rollback output does not include DDL statements")
	}

	return nil
}

// TimeRange converts configured transaction-start time bounds to time values.
func (cfg *Config) TimeRange() (from, to *time.Time, err error) {
	if cfg.FromTime != "" {
		parsed, err := time.ParseInLocation(vars.TimeFormat, cfg.FromTime, time.Local)
		if err != nil {
			return nil, nil, err
		}
		from = &parsed
	}
	if cfg.ToTime != "" {
		parsed, err := time.ParseInLocation(vars.TimeFormat, cfg.ToTime, time.Local)
		if err != nil {
			return nil, nil, err
		}
		to = &parsed
	}
	return from, to, nil
}

// checkTimeRange validates the configured half-open transaction-start time range.
func (cfg *Config) checkTimeRange() error {
	from, to, err := cfg.TimeRange()
	if err != nil {
		if cfg.FromTime != "" {
			if _, fromErr := time.ParseInLocation(vars.TimeFormat, cfg.FromTime, time.Local); fromErr != nil {
				return fmt.Errorf("--from-time must use format %q", vars.TimeFormat)
			}
		}
		return fmt.Errorf("--to-time must use format %q", vars.TimeFormat)
	}
	if from != nil && to != nil && !from.Before(*to) {
		return fmt.Errorf("--from-time must be less than --to-time")
	}
	return nil
}

// checkBinlogs trims and validates the selected binlog files.
func (cfg *Config) checkBinlogs() error {
	if len(cfg.Binlogs) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(cfg.Binlogs))
	seenNames := make(map[string]struct{}, len(cfg.Binlogs))
	normalized := make([]string, 0, len(cfg.Binlogs))
	for _, file := range cfg.Binlogs {
		name := strings.TrimSpace(file)
		if name == "" {
			return fmt.Errorf("--binlogs cannot contain empty values")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("--binlogs contains duplicate file %q", name)
		}
		seen[name] = struct{}{}
		binlogName := filepath.Base(name)
		if _, ok := seenNames[binlogName]; ok {
			return fmt.Errorf("--binlogs contains duplicate binlog name %q", binlogName)
		}
		seenNames[binlogName] = struct{}{}
		normalized = append(normalized, name)
	}
	cfg.Binlogs = normalized
	return nil
}

// checkRollbackOutput rejects output paths that cleanup would remove with the rollback cache.
func (cfg *Config) checkRollbackOutput() error {
	cacheDir, err := filepath.Abs(cfg.RollbackCacheDir)
	if err != nil {
		return fmt.Errorf("resolve --rollback-cache-dir: %w", err)
	}
	output, err := filepath.Abs(cfg.Output)
	if err != nil {
		return fmt.Errorf("resolve --output: %w", err)
	}
	rel, err := filepath.Rel(cacheDir, output)
	if err != nil {
		return nil
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("--output must not be inside --rollback-cache-dir")
	}
	return nil
}

// checkSQLTypes normalizes and validates SQL event type filters.
func (cfg *Config) checkSQLTypes() error {
	if len(cfg.SQLTypes) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(vars.SQLTypes))
	for _, typ := range vars.SQLTypes {
		allowed[typ] = struct{}{}
	}
	normalized := make([]string, 0, len(cfg.SQLTypes))
	seen := make(map[string]struct{}, len(cfg.SQLTypes))
	for _, typ := range cfg.SQLTypes {
		value := strings.ToLower(strings.TrimSpace(typ))
		if value == "" {
			return fmt.Errorf("--sql-type cannot contain empty values")
		}
		if _, ok := allowed[value]; !ok {
			return fmt.Errorf("--sql-type must be one of %s", strings.Join(vars.SQLTypes, ", "))
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	cfg.SQLTypes = normalized
	return nil
}
