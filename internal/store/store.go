package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"

	"github.com/xmm2022/echo/internal/store/queries"
)

//go:embed schema/*.sql
var schemaFS embed.FS

type Store struct {
	DB *sql.DB
	*queries.Queries
}

type ImmediateTx struct {
	conn *sql.Conn
	done bool
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", withDefaultPragmas(dsn))
	if err != nil {
		return nil, err
	}
	if err := configure(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := MigrateUp(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, Queries: queries.New(db)}, nil
}

func withDefaultPragmas(dsn string) string {
	values := url.Values{}
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "busy_timeout(5000)")
	values.Add("_pragma", "journal_mode(WAL)")

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + values.Encode()
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *Store) BeginImmediateTx(ctx context.Context) (*ImmediateTx, *queries.Queries, error) {
	tx, err := BeginImmediateTx(ctx, s.DB)
	if err != nil {
		return nil, nil, err
	}
	return tx, queries.New(tx), nil
}

func BeginImmediateTx(ctx context.Context, db *sql.DB) (*ImmediateTx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, err
	}
	return &ImmediateTx{conn: conn}, nil
}

func (tx *ImmediateTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.conn.ExecContext(ctx, query, args...)
}

func (tx *ImmediateTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return tx.conn.PrepareContext(ctx, query)
}

func (tx *ImmediateTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}

func (tx *ImmediateTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.conn.QueryRowContext(ctx, query, args...)
}

func (tx *ImmediateTx) Commit(ctx context.Context) error {
	if tx.done {
		return sql.ErrTxDone
	}
	_, err := tx.conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	tx.done = true
	return tx.conn.Close()
}

func (tx *ImmediateTx) Rollback(ctx context.Context) error {
	if tx.done {
		return sql.ErrTxDone
	}
	tx.done = true
	_, err := tx.conn.ExecContext(ctx, "ROLLBACK")
	closeErr := tx.conn.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func MigrateUp(db *sql.DB) error {
	m, err := newMigrator(db)
	if err != nil {
		return err
	}
	// Do not call m.Close here: golang-migrate's sqlite driver closes the
	// caller-owned *sql.DB passed via sqlite.WithInstance.

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func MigrateDown(db *sql.DB) error {
	m, err := newMigrator(db)
	if err != nil {
		return err
	}
	// Do not call m.Close here: golang-migrate's sqlite driver closes the
	// caller-owned *sql.DB passed via sqlite.WithInstance.

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func newMigrator(db *sql.DB) (*migrate.Migrate, error) {
	sourceDriver, err := iofs.New(schemaFS, "schema")
	if err != nil {
		return nil, fmt.Errorf("open migration source: %w", err)
	}
	databaseDriver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		sourceDriver.Close()
		return nil, fmt.Errorf("open sqlite migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", databaseDriver)
	if err != nil {
		sourceDriver.Close()
		return nil, err
	}
	return m, nil
}

func configure(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return err
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	return nil
}
