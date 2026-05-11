package store

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	pgvectorpgx "github.com/pgvector/pgvector-go/pgx"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// NewDB opens a pgx connection pool and runs all pending migrations.
//
// dsn is a PostgreSQL connection string, e.g.
// "postgres://user:pass@localhost:5432/alphathesis?sslmode=disable"
func NewDB(ctx context.Context, dsn string) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("database dsn is required")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgvectorpgx.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open db pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	db := &DB{pool: pool}
	if err := db.migrate(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

// Pool returns the underlying pgx pool for direct use in store methods.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

// Close closes the connection pool.
func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

func (db *DB) migrate() error {
	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("load migration files: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(db.pool)
	defer sqlDB.Close()

	dbDriver, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("create migrate db driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "pgx5", dbDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}
