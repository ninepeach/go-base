// Package media manages file metadata in PostgreSQL and bytes in S3-compatible storage.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	base "github.com/ninepeach/go-base/core"
	postgres "github.com/ninepeach/go-base/postgres"
	s3 "github.com/ninepeach/go-base/s3"
)

var (
	ErrNotFound = errors.New("media: file not found")
	ErrDeleting = errors.New("media: file is being deleted")
)

type Config struct {
	PostgresInstance string
	S3Instance string
	Bucket string
	Prefix string
	MaxSize int64
	MaxUploadDuration time.Duration
	Schema string
	Table string
	CleanupInterval time.Duration
	StaleAfter time.Duration
}

type File struct {
	ID uuid.UUID
	Bucket string
	Key string
	Filename string
	ContentType string
	Size int64
	CreatedAt time.Time
}

type Service struct {
	db *pgxpool.Pool
	store *minio.Client
	cfg Config
}

func New(db *pgxpool.Pool, store *minio.Client, cfg Config) (*Service, error) {
	if db == nil || store == nil || cfg.Bucket == "" {
		return nil, errors.New("media: database, storage and bucket are required")
	}
	if cfg.Prefix != "" {
		if strings.HasPrefix(cfg.Prefix, "/") || strings.Contains(cfg.Prefix, "..") || strings.Contains(cfg.Prefix, "\\") {
			return nil, errors.New("media: invalid object prefix")
		}
	}
	if _, err := qualifiedTable(cfg); err != nil { return nil, err }
	if cfg.MaxSize <= 0 { cfg.MaxSize = 100 << 20 }
	if cfg.StaleAfter <= 0 { cfg.StaleAfter = 15 * time.Minute }
	if cfg.MaxUploadDuration <= 0 { cfg.MaxUploadDuration = 10 * time.Minute }
	if cfg.MaxUploadDuration >= cfg.StaleAfter { return nil, errors.New("media: max upload duration must be less than stale age") }
	if cfg.CleanupInterval <= 0 { cfg.CleanupInterval = time.Minute }
	if cfg.CleanupInterval >= cfg.StaleAfter { return nil, errors.New("media: cleanup interval must be less than stale age") }
	return &Service{db: db, store: store, cfg: cfg}, nil
}

// From resolves already-started infrastructure providers. Register PostgreSQL and S3 first.
func From(rt *base.Runtime, cfg Config) (*Service, error) {
	db, ok, err := postgres.Lookup(rt, cfg.PostgresInstance)
	if err != nil { return nil, err }
	if !ok { return nil, fmt.Errorf("media: postgres instance %q not registered", cfg.PostgresInstance) }
	store, ok, err := s3.Lookup(rt, cfg.S3Instance)
	if err != nil { return nil, err }
	if !ok { return nil, fmt.Errorf("media: s3 instance %q not registered", cfg.S3Instance) }
	return New(db, store, cfg)
}

// Upload records a pending object before streaming bytes to S3.
// Pending rows remain available for later reconciliation if any step fails.
func (s *Service) Upload(ctx context.Context, filename, contentType string, input io.Reader, size int64) (File, error) {
	if input == nil || size < 0 || filename == "" || contentType == "" {
		return File{}, errors.New("media: invalid upload parameters")
	}
	if size > s.cfg.MaxSize {
		return File{}, errors.New("media: upload exceeds size limit")
	}
	id := uuid.New()
	key := path.Join(s.cfg.Prefix, id.String())
	file := File{ID: id, Bucket: s.cfg.Bucket, Key: key, Filename: filename, ContentType: contentType, Size: size}
	_, err := s.db.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (id, bucket, object_key, filename, content_type, size_bytes, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')`, s.table()),
		file.ID, file.Bucket, file.Key, file.Filename, file.ContentType, file.Size)
	if err != nil { return File{}, fmt.Errorf("media: create pending metadata: %w", err) }
	uploadCtx, cancelUpload := context.WithTimeout(ctx, s.cfg.MaxUploadDuration)
	defer cancelUpload()
	stopHeartbeat := s.heartbeat(uploadCtx, id)
	defer stopHeartbeat()
	info, err := s.store.PutObject(uploadCtx, s.cfg.Bucket, key, io.LimitReader(input, size+1), size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil { return File{}, fmt.Errorf("media: upload pending object %s: %w", id, err) }
	if info.Size != size { return File{}, fmt.Errorf("media: upload size mismatch for %s", id) }
	err = s.db.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET state='ready', updated_at=now()
		WHERE id=$1 AND state='pending' RETURNING created_at`, s.table()), id).Scan(&file.CreatedAt)
	if err != nil { return File{}, fmt.Errorf("media: finalize pending object %s: %w", id, err) }
	return file, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (File, error) {
	var f File
	var state string
	err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT id, bucket, object_key, filename, content_type, size_bytes, created_at, state
		FROM %s WHERE id=$1`, s.table()), id).Scan(&f.ID, &f.Bucket, &f.Key, &f.Filename, &f.ContentType, &f.Size, &f.CreatedAt, &state)
	if errors.Is(err, pgx.ErrNoRows) { return File{}, ErrNotFound }
	if err != nil { return File{}, fmt.Errorf("media: read metadata: %w", err) }
	if state != "ready" { return File{}, ErrDeleting }
	return f, nil
}

// Open returns an object stream. The caller must close it.
func (s *Service) Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, File, error) {
	f, err := s.Get(ctx, id)
	if err != nil { return nil, File{}, err }
	object, err := s.store.GetObject(ctx, f.Bucket, f.Key, minio.GetObjectOptions{})
	if err != nil { return nil, File{}, fmt.Errorf("media: open object: %w", err) }
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, File{}, fmt.Errorf("media: stat object: %w", err)
	}
	return object, f, nil
}

// Delete moves metadata into a retryable state before removing object bytes.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	var bucket, key string
	err := s.db.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET state='deleting', updated_at=now() WHERE id=$1
		RETURNING bucket, object_key`, s.table()), id).Scan(&bucket, &key)
	if errors.Is(err, pgx.ErrNoRows) { return ErrNotFound }
	if err != nil { return fmt.Errorf("media: mark deleting: %w", err) }
	return s.removeObjectAndRow(ctx, id, bucket, key)
}

func (s *Service) removeObjectAndRow(ctx context.Context, id uuid.UUID, bucket, key string) error {
	if err := s.store.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("media: remove object: %w", err)
	}
	if _, err := s.db.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id=$1 AND state='deleting'`, s.table()), id); err != nil {
		return fmt.Errorf("media: remove metadata: %w", err)
	}
	return nil
}

// PresignedGet returns a short-lived URL for direct object downloads.
// Authentication and authorization must be checked by the application first.
func (s *Service) PresignedGet(ctx context.Context, id uuid.UUID, expiry time.Duration) (*url.URL, error) {
	if expiry <= 0 || expiry > 7*24*time.Hour { return nil, errors.New("media: invalid URL expiry") }
	file, err := s.Get(ctx, id)
	if err != nil { return nil, err }
	return s.store.PresignedGetObject(ctx, file.Bucket, file.Key, expiry, nil)
}

var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func qualifiedTable(cfg Config) (string, error) {
	schema, table := cfg.Schema, cfg.Table
	if schema == "" { schema = "public" }
	if table == "" { table = "media_objects" }
	if !identifierPattern.MatchString(schema) || !identifierPattern.MatchString(table) {
		return "", errors.New("media: invalid SQL schema or table identifier")
	}
	return `"` + schema + `"."` + table + `"`, nil
}

func (s *Service) table() string {
	table, _ := qualifiedTable(s.cfg)
	return table
}

// heartbeat refreshes the pending lease while a stream is in progress.
// If PostgreSQL becomes unavailable, reconciliation may still race a long
// upload; deployments must choose StaleAfter conservatively.
func (s *Service) heartbeat(ctx context.Context, id uuid.UUID) func() {
	interval := s.cfg.StaleAfter / 4
	if interval > 30*time.Second { interval = 30*time.Second }
	if interval < time.Second { interval = time.Second }
	worker, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-worker.Done(): return
			case <-ticker.C:
				ping, stop := context.WithTimeout(worker, interval)
				_, _ = s.db.Exec(ping, fmt.Sprintf(`UPDATE %s SET updated_at=now() WHERE id=$1 AND state='pending'`, s.table()), id)
				stop()
			}
		}
	}()
	return func() { cancel(); <-done }
}
