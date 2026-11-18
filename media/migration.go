package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrSchemaOutdated means the database schema is older than this code.
	// Run Migrate during deployment; Component.Start never migrates.
	ErrSchemaOutdated = errors.New("media: database schema is outdated; run media.Migrate")
	// ErrSchemaTooNew means the database was migrated by newer code. Migrate
	// refuses to touch it; it never downgrades.
	ErrSchemaTooNew = errors.New("media: database schema is newer than this code")
)

// Migration is one forward-only schema step. Versions start at 1 and are dense.
type Migration struct {
	Version     int
	Description string
	SQL         string
}

// names holds validated, quoted identifiers for rendering migration SQL.
type names struct {
	schema, table, versions, cleanupIndex string
}

// migrationSteps is the ordered schema history. Append only: never edit or
// reorder an existing step, because deployed databases have already run it.
// Each step must be transactional DDL (no CREATE INDEX CONCURRENTLY).
var migrationSteps = []struct {
	description string
	render      func(n names) string
}{
	{"create media objects table", func(n names) string {
		return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
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
CREATE INDEX IF NOT EXISTS %s ON %s (state,updated_at) WHERE state IN ('pending','deleting');`,
			n.table, n.cleanupIndex, n.table)
	}},
}

// SchemaVersion is the schema version this code requires.
func SchemaVersion() int { return len(migrationSteps) }

func migrationNames(cfg Config) (names, error) {
	table, err := qualifiedTable(cfg)
	if err != nil {
		return names{}, err
	}
	schema, base := cfg.Schema, cfg.Table
	if schema == "" {
		schema = "public"
	}
	if base == "" {
		base = "media_objects"
	}
	// PostgreSQL truncates identifiers above 63 bytes; derived names must fit.
	if len(base)+len("_schema_migrations") > 63 {
		return names{}, errors.New("media: table name too long")
	}
	return names{
		schema:       `"` + schema + `"`,
		table:        table,
		versions:     `"` + schema + `"."` + base + `_schema_migrations"`,
		cleanupIndex: `"` + base + `_cleanup_idx"`,
	}, nil
}

// Migrations returns the rendered schema history for use with an external
// migration runner. Record applied versions yourself if you do not use Migrate.
func Migrations(cfg Config) ([]Migration, error) {
	n, err := migrationNames(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, len(migrationSteps))
	for i, step := range migrationSteps {
		out[i] = Migration{Version: i + 1, Description: step.description, SQL: step.render(n)}
	}
	return out, nil
}

// MigrationSQL returns the complete schema as one script, without version
// bookkeeping. Prefer Migrate, which records versions.
func MigrationSQL(cfg Config) (string, error) {
	n, err := migrationNames(cfg)
	if err != nil {
		return "", err
	}
	ms, err := Migrations(cfg)
	if err != nil {
		return "", err
	}
	parts := []string{"CREATE SCHEMA IF NOT EXISTS " + n.schema + ";"}
	for _, m := range ms {
		parts = append(parts, m.SQL)
	}
	return strings.Join(parts, "\n"), nil
}

// Migrate applies pending migrations and is safe to run repeatedly and
// concurrently from several deploy jobs:
//   - a transaction-scoped advisory lock serializes concurrent runners;
//   - all pending steps and their version records commit in one
//     transaction, so a failure leaves the schema exactly as it was and a
//     rerun starts from the same version;
//   - databases created before versioning (table present, no version table)
//     are adopted, because the first step is idempotent.
//
// It is never called from Component.Start.
func Migrate(ctx context.Context, db *pgxpool.Pool, cfg Config) error {
	if db == nil {
		return errors.New("media: nil migration database")
	}
	n, err := migrationNames(cfg)
	if err != nil {
		return err
	}
	steps, err := Migrations(cfg)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("media: migration begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "go-base/media:"+n.table); err != nil {
		return fmt.Errorf("media: migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s;
CREATE TABLE IF NOT EXISTS %s (
	version INT PRIMARY KEY,
	description TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`, n.schema, n.versions)); err != nil {
		return fmt.Errorf("media: migration bookkeeping: %w", err)
	}
	var current int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM `+n.versions).Scan(&current); err != nil {
		return fmt.Errorf("media: read schema version: %w", err)
	}
	if current > len(steps) {
		return fmt.Errorf("%w: database at %d, code knows %d", ErrSchemaTooNew, current, len(steps))
	}
	for _, m := range steps[current:] {
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("media: migration %d (%s): %w", m.Version, m.Description, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+n.versions+` (version, description) VALUES ($1, $2)`, m.Version, m.Description); err != nil {
			return fmt.Errorf("media: record migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("media: migration commit: %w", err)
	}
	return nil
}

// CurrentVersion reports the applied schema version; 0 means never migrated.
func CurrentVersion(ctx context.Context, db *pgxpool.Pool, cfg Config) (int, error) {
	n, err := migrationNames(cfg)
	if err != nil {
		return 0, err
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, n.versions).Scan(&exists); err != nil {
		return 0, fmt.Errorf("media: read schema version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var v int
	err = db.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM `+n.versions).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("media: read schema version: %w", err)
	}
	return v, nil
}

// checkSchema fails when the database is older than this code. A newer
// schema is accepted: steps are expected to stay backward compatible so
// that old instances keep running during a rolling deployment.
func checkSchema(ctx context.Context, db *pgxpool.Pool, cfg Config) (int, error) {
	v, err := CurrentVersion(ctx, db, cfg)
	if err != nil {
		return 0, err
	}
	if v < SchemaVersion() {
		return v, fmt.Errorf("%w: database at %d, code requires %d", ErrSchemaOutdated, v, SchemaVersion())
	}
	return v, nil
}
