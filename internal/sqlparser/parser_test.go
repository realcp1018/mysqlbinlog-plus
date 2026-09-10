package sqlparser

import "testing"

// TestParseDDLTargets covers the supported DDL target forms used for schema tracking.
func TestParseDDLTargets(t *testing.T) {
	parser := New()
	tests := []struct {
		name          string
		query         string
		defaultSchema string
		wantTables    []Target
		wantSchemas   []string
	}{
		{name: "alter table", query: "ALTER TABLE app.orders ADD COLUMN age INT", wantTables: []Target{{Schema: "app", Table: "orders"}}},
		{name: "alter table rename", query: "ALTER TABLE orders RENAME TO archived_orders", defaultSchema: "app", wantTables: []Target{{Schema: "app", Table: "orders"}, {Schema: "app", Table: "archived_orders"}}},
		{name: "alter table exchange", query: "ALTER TABLE app.part EXCHANGE PARTITION p WITH TABLE app.other", wantTables: []Target{{Schema: "app", Table: "part"}, {Schema: "app", Table: "other"}}},
		{name: "alter view", query: "ALTER VIEW app.active_users AS SELECT id FROM app.users", wantTables: []Target{{Schema: "app", Table: "active_users"}}},
		{name: "create index", query: "CREATE INDEX idx ON app.users (id)", wantTables: []Target{{Schema: "app", Table: "users"}}},
		{name: "drop index", query: "DROP INDEX idx ON app.users", wantTables: []Target{{Schema: "app", Table: "users"}}},
		{name: "drop tables", query: "DROP TABLE IF EXISTS app.old, app.archived", wantTables: []Target{{Schema: "app", Table: "old"}, {Schema: "app", Table: "archived"}}},
		{name: "drop view", query: "DROP VIEW IF EXISTS app.active_users", wantTables: []Target{{Schema: "app", Table: "active_users"}}},
		{name: "create view", query: "CREATE VIEW app.active_users AS SELECT id FROM app.users", wantTables: []Target{{Schema: "app", Table: "active_users"}}},
		{name: "rename tables", query: "RENAME TABLE app.old TO app.new, app.a TO app.b;", wantTables: []Target{{Schema: "app", Table: "old"}, {Schema: "app", Table: "new"}, {Schema: "app", Table: "a"}, {Schema: "app", Table: "b"}}},
		{name: "create database", query: "CREATE DATABASE IF NOT EXISTS app", wantSchemas: []string{"app"}},
		{name: "leading comments", query: "/* migration */ ALTER TABLE app.orders ADD COLUMN age INT", wantTables: []Target{{Schema: "app", Table: "orders"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.ParseDDLTargets(tt.query, tt.defaultSchema)
			if err != nil {
				t.Fatalf("ParseDDLTargets() returned error: %v", err)
			}
			if len(got.Tables) != len(tt.wantTables) {
				t.Fatalf("got %d table targets, want %d: %+v", len(got.Tables), len(tt.wantTables), got.Tables)
			}
			for i := range tt.wantTables {
				if got.Tables[i] != tt.wantTables[i] {
					t.Errorf("table target %d = %+v, want %+v", i, got.Tables[i], tt.wantTables[i])
				}
			}
			if len(got.Schemas) != len(tt.wantSchemas) {
				t.Fatalf("got %d schema targets, want %d: %v", len(got.Schemas), len(tt.wantSchemas), got.Schemas)
			}
			for i := range tt.wantSchemas {
				if got.Schemas[i] != tt.wantSchemas[i] {
					t.Errorf("schema target %d = %q, want %q", i, got.Schemas[i], tt.wantSchemas[i])
				}
			}
		})
	}
}

// TestParseDDLTargetsRejectsUnsupportedStatement verifies that non-DDL input is not treated as scoped.
func TestParseDDLTargetsRejectsUnsupportedStatement(t *testing.T) {
	if _, err := New().ParseDDLTargets("ALTER USER root IDENTIFIED BY 'secret'", "app"); err == nil {
		t.Fatal("ParseDDLTargets() returned nil error for unsupported DDL")
	}
}

// TestQualifyDDLAddsDefaultSchema verifies that restored table DDL is executable without a selected database.
func TestQualifyDDLAddsDefaultSchema(t *testing.T) {
	parser := New()
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "alter table", query: "ALTER TABLE cert MODIFY COLUMN name varchar(256) NOT NULL", want: "ALTER TABLE `app`.`cert` MODIFY COLUMN `name` VARCHAR(256) NOT NULL"},
		{name: "drop tables", query: "DROP TABLE cert, archived", want: "DROP TABLE `app`.`cert`, `app`.`archived`"},
		{name: "rename tables", query: "RENAME TABLE cert TO cert_new, app.old TO app.new", want: "RENAME TABLE `app`.`cert` TO `app`.`cert_new`, `app`.`old` TO `app`.`new`"},
		{name: "qualified table", query: "ALTER TABLE other.cert ADD COLUMN age INT", want: "ALTER TABLE `other`.`cert` ADD COLUMN `age` INT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.QualifyDDL(tt.query, "app")
			if err != nil {
				t.Fatalf("QualifyDDL() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("QualifyDDL() = %q, want %q", got, tt.want)
			}
		})
	}
}
