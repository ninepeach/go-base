// Package media manages file metadata in PostgreSQL and bytes in S3-compatible storage.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
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

// Upload stores the object first, then commits metadata. Failed metadata writes
// trigger a best-effort object removal with an independent cleanup deadline.
func (s *Service) Upload(ctx context.Context, filename, contentType string, input io.Reader, size int64) (File, error) {
	if input == nil || size < 0 || filename == "" || contentType == "" {
		return File{}, errors.New("media: invalid upload parameters")
	}
	id := uuid.New()
	key := path.Join(s.cfg.Prefix, id.String())
	info, err := s.store.PutObject(ctx, s.cfg.Bucket, key, input, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil { return File{}, fmt.Errorf("media: upload object: %w", err) }
	file := File{ID: id, Bucket: s.cfg.Bucket, Key: key, Filename: filename, ContentType: contentType, Size: info.Size}
	_, err = s.db.Exec(ctx, `INSERT INTO media_objects (id, bucket, object_key, filename, content_type, size_bytes, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'ready')`,
		file.ID, file.Bucket, file.Key, file.Filename, file.ContentType, file.Size)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if removeErr := s.store.RemoveObject(cleanup, file.Bucket, file.Key, minio.RemoveObjectOptions{}); removeErr != nil {
			return File{}, errors.Join(fmt.Errorf("media: persist metadata: %w", err), fmt.Errorf("media: rollback object %s: %w", file.Key, removeErr))
		}
		return File{}, fmt.Errorf("media: persist metadata: %w", err)
	}
	return file, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (File, error) {
	var f File
	var state string
	err := s.db.QueryRow(ctx, `SELECT id, bucket, object_key, filename, content_type, size_bytes, created_at, state
		FROM media_objects WHERE id=$1`, id).Scan(&f.ID, &f.Bucket, &f.Key, &f.Filename, &f.ContentType, &f.Size, &f.CreatedAt, &state)
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

// Delete marks metadata as deleting before removing the object. A failed object
// removal leaves a retryable 'deleting' row; repeated Delete calls resume it.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	var bucket, key string
	err := s.db.QueryRow(ctx, `UPDATE media_objects SET state='deleting' WHERE id=$1
		RETURNING bucket, object_key`, id).Scan(&bucket, &key)
	if errors.Is(err, pgx.ErrNoRows) { return ErrNotFound }
	if err != nil { return fmt.Errorf("media: mark deleting: %w", err) }
	if err := s.store.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("media: delete object: %w", err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM media_objects WHERE id=$1 AND state='deleting'`, id); err != nil {
		return fmt.Errorf("media: delete metadata: %w", err)
	}
	return nil
}
