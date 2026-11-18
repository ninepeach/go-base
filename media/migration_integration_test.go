package media

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	base "github.com/ninepeach/go-base/core"
)

func freshSchema(t *testing.T) (*pgxpool.Pool, Config) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema := "m_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`) })
	return pool, Config{Schema: schema, Bucket: testBucket}
}

func version(t *testing.T, pool *pgxpool.Pool, cfg Config) int {
	t.Helper()
	v, err := CurrentVersion(context.Background(), pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool, cfg := freshSchema(t)
	if v := version(t, pool, cfg); v != 0 {
		t.Fatalf("fresh database at version %d", v)
	}
	for i := 0; i < 3; i++ {
		if err := Migrate(context.Background(), pool, cfg); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if v := version(t, pool, cfg); v != SchemaVersion() {
		t.Fatalf("version %d, want %d", v, SchemaVersion())
	}
}

func TestConcurrentMigrateIsSerialized(t *testing.T) {
	pool, cfg := freshSchema(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Migrate(context.Background(), pool, cfg) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	n, _ := migrationNames(cfg)
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+n.versions).Scan(&rows); err != nil || rows != SchemaVersion() {
		t.Fatalf("version rows=%d err=%v", rows, err)
	}
}

// withStep temporarily appends a schema step, simulating a future release.
func withStep(t *testing.T, desc string, render func(names) string) {
	t.Helper()
	saved := migrationSteps
	migrationSteps = append(append(migrationSteps[:0:0], saved...), struct {
		description string
		render      func(n names) string
	}{desc, render})
	t.Cleanup(func() { migrationSteps = saved })
}

func TestFailedMigrationRollsBackCompletely(t *testing.T) {
	pool, cfg := freshSchema(t)
	if err := Migrate(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
	withStep(t, "add column then fail", func(n names) string {
		return `ALTER TABLE ` + n.table + ` ADD COLUMN checksum TEXT; SELECT 1/0;`
	})
	if err := Migrate(context.Background(), pool, cfg); err == nil {
		t.Fatal("broken step must fail")
	}
	if v := version(t, pool, cfg); v != 1 {
		t.Fatalf("failed migration changed version to %d", v)
	}
	n, _ := migrationNames(cfg)
	var exists bool
	_ = pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='media_objects' AND column_name='checksum')`, cfg.Schema).Scan(&exists)
	if exists {
		t.Fatalf("partial DDL survived a failed migration on %s", n.table)
	}
}

func TestForwardMigrationAndStartupVersionCheck(t *testing.T) {
	pool, cfg := freshSchema(t)
	if err := Migrate(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
	withStep(t, "add checksum column", func(n names) string {
		return `ALTER TABLE ` + n.table + ` ADD COLUMN IF NOT EXISTS checksum TEXT;`
	})
	if _, err := checkSchema(context.Background(), pool, cfg); !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("outdated schema: %v", err)
	}
	if err := Migrate(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
	if v := version(t, pool, cfg); v != 2 {
		t.Fatalf("version %d, want 2", v)
	}
	if _, err := checkSchema(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	pool, cfg := freshSchema(t)
	withStep(t, "future step", func(n names) string { return `SELECT 1;` })
	if err := Migrate(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
	migrationSteps = migrationSteps[:1] // run as older code; cleanup restores
	if err := Migrate(context.Background(), pool, cfg); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
	if _, err := checkSchema(context.Background(), pool, cfg); err != nil {
		t.Fatalf("older code must still start on a newer schema: %v", err)
	}
}

func TestMigrateAdoptsUnversionedDatabase(t *testing.T) {
	pool, cfg := freshSchema(t)
	sql, err := MigrationSQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
	if v := version(t, pool, cfg); v != 0 {
		t.Fatalf("unversioned database reports %d", v)
	}
	if err := Migrate(context.Background(), pool, cfg); err != nil {
		t.Fatal(err)
	}
	if v := version(t, pool, cfg); v != SchemaVersion() {
		t.Fatalf("version %d", v)
	}
}

func TestComponentRefusesUnmigratedDatabase(t *testing.T) {
	pool, cfg := freshSchema(t)
	_ = pool
	fake := newFakeS3(t, testBucket)
	cfg.PostgresInstance, cfg.S3Instance = "primary", "objects"
	f := &fixture{s3: fake}
	_, err := base.New(context.Background(), runtimeOptions(t, f, cfg))
	if !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("want ErrSchemaOutdated, got %v", err)
	}
}
