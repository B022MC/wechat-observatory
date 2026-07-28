package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func EnsureDatabase(ctx context.Context, dsn string) error {
	cfg, err := parseMySQLConfig(dsn)
	if err != nil {
		return err
	}
	dbName := strings.TrimSpace(cfg.DBName)
	if dbName == "" {
		return fmt.Errorf("mysql dsn must include a database name")
	}
	cfg.DBName = ""
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		quoteIdentifier(dbName),
	))
	return err
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
