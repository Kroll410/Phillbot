//go:build server

package main

import (
	"database/sql"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // чистий Go, без CGO
)

// openPostgres — PostgreSQL за рядком підключення (postgres://user:pass@host/db?sslmode=…).
func openPostgres(url, channel string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return initStore(db, true, channel)
}
