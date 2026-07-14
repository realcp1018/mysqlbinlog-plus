package vars

// time formats
const (
	// TimeFormat defines the accepted format for time range flags.
	TimeFormat = "2006-01-02 15:04:05"
)

// run modes
const (
	// ModeLocal parses local binlog files without connecting to MySQL.
	ModeLocal = "local"
	// ModeMixed parses local binlog files with MySQL table metadata.
	ModeMixed = "mixed"
	// ModeOnline streams or fetches binlog data from a MySQL server.
	ModeOnline = "online"
)

// sql event types
var SQLTypes = []string{"insert", "update", "delete", "ddl"}
