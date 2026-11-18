// Package media manages file metadata in PostgreSQL and bytes in S3-compatible storage.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	base "github.com/ninepeach/go-base/core"
	postgres "github.com/ninepeach/go-base/postgres"
	s3 "github.com/ninepeach/go-base/s3"
)

var (
	ErrNotFound      = errors.New("media: file not found")
	ErrDeleting      = errors.New("media: file is being deleted")
	ErrInvalidUpload = errors.New("media: invalid upload parameters")
	ErrTooLarge      = errors.New("media: upload exceeds size limit")
	ErrSizeMismatch  = errors.New("media: upload size does not match declared size")
	// ErrUploadAbandoned means cleanup reclaimed the pending record before the
	// upload could be finalized. The object is removed; the caller should retry.
	ErrUploadAbandoned = errors.New("media: upload was reclaimed before finalization")
)

// uploadSafetyMargin is the minimum gap between MaxUploadDuration and
// StaleAfter. It absorbs request latency and S3 commits that land just after
// a client-side cancellation. Tests override it.
var uploadSafetyMargin = time.Minute

// cleanupBatchTimeout bounds one cleanup pass and must stay below StaleAfter,
// because a claimed row's lease is its refreshed updated_at.
var cleanupBatchTimeout = 30 * time.Second

const (
	cleanupBatchSize = 100
	maxMetadataBytes = 255
)

type Config struct {
	PostgresInstance  string
	S3Instance        string
	Bucket            string
	Prefix            string
	MaxSize           int64
	MaxUploadDuration time.Duration
	Schema            string
	Table             string
	CleanupInterval   time.Duration
	StaleAfter        time.Duration
}

type File struct {
	ID          uuid.UUID
	Bucket      string
	Key         string
	Filename    string
	ContentType string
	Size        int64
	CreatedAt   time.Time
}

type Service struct {
	db    *pgxpool.Pool
	store *minio.Client
	cfg   Config
	table string
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
	table, err := qualifiedTable(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 100 << 20
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 15 * time.Minute
	}
	if cfg.MaxUploadDuration <= 0 {
		cfg.MaxUploadDuration = 10 * time.Minute
	}
	if cfg.MaxUploadDuration+uploadSafetyMargin > cfg.StaleAfter {
		return nil, fmt.Errorf("media: StaleAfter must be at least MaxUploadDuration + %s", uploadSafetyMargin)
	}
	if cleanupBatchTimeout >= cfg.StaleAfter {
		return nil, errors.New("media: StaleAfter must exceed the cleanup batch timeout")
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = time.Minute
	}
	if cfg.CleanupInterval >= cfg.StaleAfter {
		return nil, errors.New("media: cleanup interval must be less than stale age")
	}
	return &Service{db: db, store: store, cfg: cfg, table: table}, nil
}

// From resolves already-started infrastructure providers. Register PostgreSQL and S3 first.
func From(rt *base.Runtime, cfg Config) (*Service, error) {
	db, ok, err := postgres.Lookup(rt, cfg.PostgresInstance)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("media: postgres instance %q not registered", cfg.PostgresInstance)
	}
	store, ok, err := s3.Lookup(rt, cfg.S3Instance)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("media: s3 instance %q not registered", cfg.S3Instance)
	}
	return New(db, store, cfg)
}

func validMetadata(s string) bool {
	if s == "" || len(s) > maxMetadataBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Upload stores exactly size bytes from input.
//
// Protocol: a pending row is inserted first, so every object written to S3 is
// tracked. The upload is bounded by MaxUploadDuration, and StaleAfter is at
// least MaxUploadDuration plus a safety margin, so cleanup never reclaims a
// pending row while its upload can still be writing. The row becomes ready
// only via a compare-and-set from pending, after the object was stored and
// input was verified to contain exactly size bytes. input must reach EOF.
//
// On any failure after the pending row exists, the row is tombstoned
// (deleting) and the object removal is attempted immediately. The row itself
// is left for cleanup, which removes the object again after StaleAfter: an S3
// write whose client was cancelled may still commit late, and the second
// removal guarantees it cannot survive without metadata.
func (s *Service) Upload(ctx context.Context, filename, contentType string, input io.Reader, size int64) (File, error) {
	if input == nil || size < 0 || !validMetadata(filename) || !validMetadata(contentType) {
		return File{}, ErrInvalidUpload
	}
	if size > s.cfg.MaxSize {
		return File{}, ErrTooLarge
	}
	id := uuid.New()
	key := path.Join(s.cfg.Prefix, id.String())
	file := File{ID: id, Bucket: s.cfg.Bucket, Key: key, Filename: filename, ContentType: contentType, Size: size}
	_, err := s.db.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (id, bucket, object_key, filename, content_type, size_bytes, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')`, s.table),
		file.ID, file.Bucket, file.Key, file.Filename, file.ContentType, file.Size)
	if err != nil {
		return File{}, fmt.Errorf("media: create pending metadata: %w", err)
	}
	if err := s.putObject(ctx, file, input); err != nil {
		return File{}, errors.Join(err, s.abandon(id))
	}
	err = s.db.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET state='ready', updated_at=now()
		WHERE id=$1 AND state='pending' RETURNING created_at`, s.table), id).Scan(&file.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrUploadAbandoned
	}
	if err != nil {
		// The update may have committed without us seeing the result. The
		// caller never receives this ID, so the file is unreachable either way.
		return File{}, errors.Join(fmt.Errorf("media: finalize %s: %w", id, err), s.abandon(id))
	}
	return file, nil
}

func (s *Service) putObject(ctx context.Context, file File, input io.Reader) error {
	uploadCtx, cancel := context.WithTimeout(ctx, s.cfg.MaxUploadDuration)
	defer cancel()
	info, err := s.store.PutObject(uploadCtx, file.Bucket, file.Key, io.LimitReader(input, file.Size), file.Size,
		minio.PutObjectOptions{ContentType: file.ContentType})
	if err != nil {
		return fmt.Errorf("media: upload object %s: %w", file.ID, err)
	}
	if info.Size != file.Size {
		return fmt.Errorf("%w: stored %d of %d bytes", ErrSizeMismatch, info.Size, file.Size)
	}
	var probe [1]byte
	n, err := io.ReadFull(input, probe[:])
	if n > 0 {
		return fmt.Errorf("%w: input longer than %d bytes", ErrSizeMismatch, file.Size)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("media: read upload input: %w", err)
	}
	return nil
}

// abandon tombstones the row and removes the object on a fresh context,
// because the caller's context is often what failed.
func (s *Service) abandon(id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBatchTimeout)
	defer cancel()
	var bucket, key string
	err := s.db.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET state='deleting', updated_at=now()
		WHERE id=$1 AND state IN ('pending','ready') RETURNING bucket, object_key`, s.table), id).Scan(&bucket, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("media: tombstone %s (cleanup will retry): %w", id, err)
	}
	if err := s.store.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("media: remove abandoned object %s (cleanup will retry): %w", id, err)
	}
	return nil
}

// Get returns metadata for a ready file. Pending uploads are not visible.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (File, error) {
	var f File
	var state string
	err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT id, bucket, object_key, filename, content_type, size_bytes, created_at, state
		FROM %s WHERE id=$1`, s.table), id).Scan(&f.ID, &f.Bucket, &f.Key, &f.Filename, &f.ContentType, &f.Size, &f.CreatedAt, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("media: read metadata: %w", err)
	}
	switch state {
	case "ready":
		return f, nil
	case "deleting":
		return File{}, ErrDeleting
	default:
		return File{}, ErrNotFound
	}
}

// Open returns an object stream. The caller must close it.
func (s *Service) Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, File, error) {
	f, err := s.Get(ctx, id)
	if err != nil {
		return nil, File{}, err
	}
	object, err := s.store.GetObject(ctx, f.Bucket, f.Key, minio.GetObjectOptions{})
	if err != nil {
		return nil, File{}, fmt.Errorf("media: open object: %w", err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, File{}, fmt.Errorf("media: stat object: %w", err)
	}
	return object, f, nil
}

// Delete removes a ready file. Pending uploads cannot be deleted (ErrNotFound),
// since removing an object that is still being written could orphan it.
// If object removal fails, the row stays in deleting and cleanup retries it;
// the file is no longer readable either way.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	var bucket, key string
	err := s.db.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET state='deleting', updated_at=now()
		WHERE id=$1 AND state IN ('ready','deleting') RETURNING bucket, object_key`, s.table), id).Scan(&bucket, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("media: mark deleting: %w", err)
	}
	return s.removeObjectAndRow(ctx, id, bucket, key)
}

func (s *Service) removeObjectAndRow(ctx context.Context, id uuid.UUID, bucket, key string) error {
	if err := s.store.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("media: remove object: %w", err)
	}
	if _, err := s.db.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id=$1 AND state='deleting'`, s.table), id); err != nil {
		return fmt.Errorf("media: remove metadata: %w", err)
	}
	return nil
}

// PresignedGet returns a short-lived URL for direct object downloads.
// Authentication and authorization must be checked by the application first.
func (s *Service) PresignedGet(ctx context.Context, id uuid.UUID, expiry time.Duration) (*url.URL, error) {
	if expiry <= 0 || expiry > 7*24*time.Hour {
		return nil, errors.New("media: invalid URL expiry")
	}
	file, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.store.PresignedGetObject(ctx, file.Bucket, file.Key, expiry, nil)
}

// Cleanup reconciles expired pending and deleting records and is safe to run
// concurrently from many instances. Rows are claimed in one statement with
// FOR UPDATE SKIP LOCKED; claiming refreshes updated_at, which acts as a lease
// of length StaleAfter. A crashed claimant's rows become claimable again once
// that lease expires. Expired pending rows are deleted, never finalized,
// because their completeness is unknown. All S3 removals are idempotent.
func (s *Service) Cleanup(ctx context.Context) error {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`UPDATE %[1]s SET state='deleting', updated_at=now()
		WHERE id IN (
			SELECT id FROM %[1]s
			WHERE state IN ('pending','deleting') AND updated_at < now() - ($1 * interval '1 second')
			ORDER BY updated_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED)
		RETURNING id, bucket, object_key`, s.table), s.cfg.StaleAfter.Seconds(), cleanupBatchSize)
	if err != nil {
		return fmt.Errorf("media: claim cleanup candidates: %w", err)
	}
	type item struct {
		id          uuid.UUID
		bucket, key string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.bucket, &it.key); err != nil {
			rows.Close()
			return fmt.Errorf("media: scan cleanup candidate: %w", err)
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("media: claim cleanup candidates: %w", err)
	}
	var failures []error
	for _, it := range items {
		if err := s.removeObjectAndRow(ctx, it.id, it.bucket, it.key); err != nil {
			failures = append(failures, fmt.Errorf("media: cleanup %s: %w", it.id, err))
		}
	}
	return errors.Join(failures...)
}

var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func qualifiedTable(cfg Config) (string, error) {
	schema, table := cfg.Schema, cfg.Table
	if schema == "" {
		schema = "public"
	}
	if table == "" {
		table = "media_objects"
	}
	if !identifierPattern.MatchString(schema) || !identifierPattern.MatchString(table) {
		return "", errors.New("media: invalid SQL schema or table identifier")
	}
	return `"` + schema + `"."` + table + `"`, nil
}
