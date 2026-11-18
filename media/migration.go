package media

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrationSQL generates DDL but never executes it. Apply explicitly during deployment.
func MigrationSQL(cfg Config) (string, error) {
	table, err := qualifiedTable(cfg)
	if err != nil {
		return "", err
	}
	schema := cfg.Schema
	if schema == "" {
		schema = "public"
	}
	return fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS "%s";
CREATE TABLE IF NOT EXISTS %s (
	id UUID PRIMARY KEY,
	bucket TEXT NOT NULL,
	object_key TEXT NOT NULL,
	filename TEXT NOT NULL,
	content_type TEXT NOT NULL,
	size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
	state TEXT NOT NULL CHECK (state IN ('pending','ready','deleting')),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE(bucket,object_key)
);
CREATE INDEX IF NOT EXISTS "%s_cleanup_idx" ON %s (state,updated_at) WHERE state IN ('pending','deleting');`, schema, table, indexName(cfg), table), nil
}

func indexName(cfg Config) string {
	table := cfg.Table
	if table == "" {
		table = "media_objects"
	}
	return table
}

// Migrate explicitly executes Media DDL. It is never called from Component.Start.
func Migrate(ctx context.Context, db *pgxpool.Pool, cfg Config) error {
	if db == nil {
		return fmt.Errorf("media: nil migration database")
	}
	sql, err := MigrationSQL(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, sql)
	if err != nil {
		return fmt.Errorf("media: migration: %w", err)
	}
	return nil
}
