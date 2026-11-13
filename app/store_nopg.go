//go:build !server

package main

import "errors"

// Десктоп працює лише з SQLite; PostgreSQL є у вебсерверній збірці (-tags server).
func openPostgres(string, string) (*Store, error) {
	return nil, errors.New("PostgreSQL доступний лише у збірці з тегом server")
}
