package mysql

import (
	"strings"
	"testing"
)

func TestMigrationsAddDiagnosticsTableWithoutAlteringExistingTables(t *testing.T) {
	statements := Migrations()
	var diagnostics int
	for _, statement := range statements {
		if strings.Contains(statement, "bridge_module_diagnostics") {
			diagnostics++
			if !strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE IF NOT EXISTS bridge_module_diagnostics") {
				t.Fatalf("diagnostics migration must only create its own table: %s", statement)
			}
		}
	}
	if diagnostics != 1 {
		t.Fatalf("diagnostics migrations=%d", diagnostics)
	}
	if !strings.Contains(statements[len(statements)-1], "bridge_module_diagnostics") {
		t.Fatal("diagnostics table must be created after every existing table")
	}
	for _, statement := range moduleDiagnosticMigrations {
		upper := strings.ToUpper(statement)
		if strings.Contains(upper, "ALTER ") || strings.Contains(upper, "DROP ") {
			t.Fatalf("diagnostics migration alters schema: %s", statement)
		}
	}
}
