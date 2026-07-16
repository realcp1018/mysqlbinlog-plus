package filter

import (
	"fmt"
	"path"
	"strings"
)

// NormalizePatterns converts table patterns into database.table form.
func NormalizePatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		value := strings.TrimSpace(pattern)
		if value == "" {
			return nil, fmt.Errorf("--table-patterns cannot contain empty values")
		}
		if value == "*" {
			value = "*.*"
		} else if !strings.Contains(value, ".") {
			value = "*." + value
		}
		parts := strings.Split(value, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("table pattern %q must be database.table", pattern)
		}
		normalized = append(normalized, value)
	}

	return normalized, nil
}

// MatchAny reports whether a database.table name matches any configured pattern.
func MatchAny(patterns []string, database, table string) bool {
	if len(patterns) == 0 {
		return true
	}

	name := database + "." + table
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, name)
		if err == nil && matched {
			return true
		}
	}
	return false
}
