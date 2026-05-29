package main

import (
	"database/sql"
	"flag"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/xmm2022/echo/internal/store"
)

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(flag.CommandLine.Output())
	dsn := fs.String("dsn", "", "sqlite DSN")
	direction := fs.String("direction", "up", "migration direction: up or down")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return fmt.Errorf("migrate --dsn is required")
	}

	db, err := sql.Open("sqlite", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	switch *direction {
	case "up":
		return store.MigrateUp(db)
	case "down":
		return store.MigrateDown(db)
	default:
		return fmt.Errorf("unsupported migrate direction %q", *direction)
	}
}
