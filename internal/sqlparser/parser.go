// Package sqlparser extracts schema targets from MySQL DDL statements.
package sqlparser

import (
	"errors"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// ErrUnsupportedDDL indicates that a parsed statement is not a supported DDL target form.
var ErrUnsupportedDDL = errors.New("unsupported DDL statement")

// Target identifies a table affected by a DDL statement.
type Target struct {
	Schema string
	Table  string
}

// Targets contains the tables and schemas affected by a DDL statement.
type Targets struct {
	Tables  []Target
	Schemas []string
}

// Parser parses MySQL DDL statements and extracts their affected objects.
type Parser struct {
	parser *parser.Parser
}

// New creates a reusable DDL parser.
func New() *Parser {
	return &Parser{parser: parser.New()}
}

// ParseDDLTargets parses one DDL statement and returns its affected objects.
func (p *Parser) ParseDDLTargets(query, defaultSchema string) (Targets, error) {
	normalized := NormalizeStatement(query)
	statement, err := p.parser.ParseOneStmt(normalized, "", "")
	if err != nil && strings.HasPrefix(strings.ToUpper(normalized), "ALTER VIEW ") {
		statement, err = p.parser.ParseOneStmt("CREATE "+normalized[len("ALTER "):], "", "")
	}
	if err != nil {
		return Targets{}, err
	}

	var targets Targets
	switch stmt := statement.(type) {
	case *ast.AlterDatabaseStmt:
		targets.Schemas = append(targets.Schemas, stmt.Name.String())
	case *ast.AlterTableStmt:
		appendTableTarget(&targets, stmt.Table, defaultSchema)
		for _, spec := range stmt.Specs {
			if spec.NewTable != nil {
				appendTableTarget(&targets, spec.NewTable, defaultSchema)
			}
		}
	case *ast.CreateDatabaseStmt:
		targets.Schemas = append(targets.Schemas, stmt.Name.String())
	case *ast.CreateIndexStmt:
		appendTableTarget(&targets, stmt.Table, defaultSchema)
	case *ast.CreateTableStmt:
		appendTableTarget(&targets, stmt.Table, defaultSchema)
	case *ast.CreateViewStmt:
		appendTableTarget(&targets, stmt.ViewName, defaultSchema)
	case *ast.DropDatabaseStmt:
		targets.Schemas = append(targets.Schemas, stmt.Name.String())
	case *ast.DropIndexStmt:
		appendTableTarget(&targets, stmt.Table, defaultSchema)
	case *ast.DropTableStmt:
		for _, table := range stmt.Tables {
			appendTableTarget(&targets, table, defaultSchema)
		}
	case *ast.RenameTableStmt:
		for _, rename := range stmt.TableToTables {
			appendTableTarget(&targets, rename.OldTable, defaultSchema)
			appendTableTarget(&targets, rename.NewTable, defaultSchema)
		}
	case *ast.TruncateTableStmt:
		appendTableTarget(&targets, stmt.Table, defaultSchema)
	default:
		return Targets{}, ErrUnsupportedDDL
	}
	if len(targets.Tables) == 0 && len(targets.Schemas) == 0 {
		return Targets{}, ErrUnsupportedDDL
	}
	return targets, nil
}

// appendTableTarget adds an AST table name using the event's default schema when needed.
func appendTableTarget(targets *Targets, table *ast.TableName, defaultSchema string) {
	if table == nil {
		return
	}
	schema := table.Schema.String()
	if schema == "" {
		schema = defaultSchema
	}
	targets.Tables = append(targets.Tables, Target{Schema: schema, Table: table.Name.String()})
}

// NormalizeStatement removes leading SQL comments before parsing a DDL statement.
func NormalizeStatement(query string) string {
	query = strings.TrimSpace(query)
	for strings.HasPrefix(query, "/*") {
		end := strings.Index(query[2:], "*/")
		if end < 0 {
			return query
		}
		comment := query[2 : end+2]
		query = strings.TrimSpace(query[end+4:])
		if strings.HasPrefix(comment, "!") {
			statement := strings.TrimLeft(comment[1:], "0123456789")
			if statement != "" {
				return strings.TrimSpace(statement)
			}
		}
	}
	return query
}
