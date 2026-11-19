package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	uploadSafetyMargin = 100 * time.Millisecond
	cleanupBatchTimeout = 5 * time.Second
	goleak.VerifyTestMain(m,
		// Idle keep-alive connections of http clients and servers are not leaks.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}

const testBucket = "files"

type fixture struct {
	svc  *Service
	s3   *fakeS3
	pool *pgxpool.Pool
	cfg  Config
}

// testDSN returns MEDIA_TEST_DSN or skips. Integration tests need a real PostgreSQL.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MEDIA_TEST_DSN")
	if dsn == "" {
		t.Skip("MEDIA_TEST_DSN not set")
	}
	return dsn
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema := "t_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	cfg := Config{
		Bucket:            testBucket,
		Prefix:            "objects",
		Schema:            schema,
		MaxSize:           1 << 20,
		MaxUploadDuration: 2 * time.Second,
		StaleAfter:        10 * time.Second,
		CleanupInterval:   time.Second,
	}
	if err := Migrate(ctx, pool, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`) })
	fake := newFakeS3(t, testBucket)
	svc, err := New(pool, fake.client(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{svc: svc, s3: fake, pool: pool, cfg: cfg}
}

func (f *fixture) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var state string
	err := f.pool.QueryRow(context.Background(), `SELECT state FROM `+f.svc.table+` WHERE id=$1`, id).Scan(&state)
	if err != nil {
		return "absent"
	}
	return state
}

func (f *fixture) onlyRow(t *testing.T) (uuid.UUID, string, string) {
	t.Helper()
	var id uuid.UUID
	var state, key string
	err := f.pool.QueryRow(context.Background(), `SELECT id, state, object_key FROM `+f.svc.table).Scan(&id, &state, &key)
	if err != nil {
		t.Fatalf("expected exactly one row: %v", err)
	}
	return id, state, key
}

// age makes every row look idle for longer than StaleAfter.
func (f *fixture) age(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE `+f.svc.table+` SET updated_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) rows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+f.svc.table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUploadReadDeleteRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data := []byte("hello media")
	file, err := f.svc.Upload(ctx, "greeting.txt", "text/plain", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if f.state(t, file.ID) != "ready" || file.CreatedAt.IsZero() {
		t.Fatalf("state=%s created=%v", f.state(t, file.ID), file.CreatedAt)
	}
	got, err := f.svc.Get(ctx, file.ID)
	if err != nil || got.Size != int64(len(data)) || got.Filename != "greeting.txt" {
		t.Fatalf("get: %+v %v", got, err)
	}
	r, _, err := f.svc.Open(ctx, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(body, data) {
		t.Fatalf("open: %q %v", body, err)
	}
	signer, err := New(f.pool, f.s3.signedClient(t), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.PresignedGet(ctx, file.ID, 8*24*time.Hour); err == nil {
		t.Fatal("expiry above 7 days must be rejected")
	}
	u, err := signer.PresignedGet(ctx, file.ID, time.Minute)
	if err != nil || !strings.Contains(u.String(), file.Key) {
		t.Fatalf("presign: %v %v", u, err)
	}
	if err := f.svc.Delete(ctx, file.ID); err != nil {
		t.Fatal(err)
	}
	if f.state(t, file.ID) != "absent" || f.s3.count() != 0 {
		t.Fatal("delete left row or object")
	}
	if _, err := f.svc.Get(ctx, file.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := f.svc.Delete(ctx, file.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestUploadRejectsInvalidInputWithoutSideEffects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cases := []struct {
		name, filename, ctype string
		size                  int64
		want                  error
	}{
		{"crlf filename", "a\r\nb", "text/plain", 1, ErrInvalidUpload},
		{"empty type", "a.txt", "", 1, ErrInvalidUpload},
		{"negative size", "a.txt", "text/plain", -1, ErrInvalidUpload},
		{"too large", "a.txt", "text/plain", f.cfg.MaxSize + 1, ErrTooLarge},
	}
	for _, c := range cases {
		_, err := f.svc.Upload(ctx, c.filename, c.ctype, strings.NewReader("x"), c.size)
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v", c.name, err)
		}
	}
	if f.rows(t) != 0 || f.s3.count() != 0 {
		t.Fatal("rejected uploads must not create rows or objects")
	}
}

func TestUploadInputLongerThanDeclaredIsRejected(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Upload(context.Background(), "a.bin", "application/octet-stream", strings.NewReader("0123456789"), 4)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("want ErrSizeMismatch, got %v", err)
	}
	id, state, _ := f.onlyRow(t)
	if state != "deleting" || f.s3.count() != 0 {
		t.Fatalf("want tombstone without object, state=%s objects=%d", state, f.s3.count())
	}
	if _, err := f.svc.Get(context.Background(), id); err == nil {
		t.Fatal("abandoned upload must not be readable")
	}
}

func TestUploadInputShorterThanDeclaredIsRejected(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Upload(context.Background(), "a.bin", "application/octet-stream", strings.NewReader("abc"), 10)
	if err == nil {
		t.Fatal("short input must fail")
	}
	if _, state, _ := f.onlyRow(t); state != "deleting" {
		t.Fatalf("state=%s", state)
	}
	if f.s3.count() != 0 {
		t.Fatal("short upload must not leave an object")
	}
}

func TestUploadStorageFailureTombstonesRow(t *testing.T) {
	f := newFixture(t)
	f.s3.setPut(func(string) int { return 500 })
	_, err := f.svc.Upload(context.Background(), "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err == nil {
		t.Fatal("expected storage failure")
	}
	if _, state, _ := f.onlyRow(t); state != "deleting" {
		t.Fatalf("state=%s", state)
	}
	// Cleanup finishes the tombstone once its lease expires.
	f.age(t)
	if err := f.svc.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.rows(t) != 0 {
		t.Fatal("cleanup must remove tombstone")
	}
}

func TestPendingUploadIsInvisibleAndUndeletable(t *testing.T) {
	f := newFixture(t)
	entered := make(chan string)
	release := make(chan struct{})
	f.s3.setPut(func(key string) int {
		entered <- key
		<-release
		return 0
	})
	result := make(chan error, 1)
	go func() {
		_, err := f.svc.Upload(context.Background(), "a.txt", "text/plain", strings.NewReader("abc"), 3)
		result <- err
	}()
	<-entered
	id, state, _ := f.onlyRow(t)
	if state != "pending" {
		t.Fatalf("state=%s", state)
	}
	if _, err := f.svc.Get(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending must be invisible, got %v", err)
	}
	if err := f.svc.Delete(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending must not be deletable, got %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if f.state(t, id) != "ready" {
		t.Fatal("upload must finalize after concurrent delete attempt")
	}
}

func TestFinalizeLosesToCleanupClaim(t *testing.T) {
	f := newFixture(t)
	f.s3.setPut(func(key string) int {
		// Simulate a cleanup instance claiming the row while the object is written.
		if _, err := f.pool.Exec(context.Background(), `UPDATE `+f.svc.table+` SET state='deleting'`); err != nil {
			t.Error(err)
		}
		return 0
	})
	_, err := f.svc.Upload(context.Background(), "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if !errors.Is(err, ErrUploadAbandoned) {
		t.Fatalf("want ErrUploadAbandoned, got %v", err)
	}
	if _, state, _ := f.onlyRow(t); state != "deleting" {
		t.Fatal("a claimed row must never become ready")
	}
	// The written object is removed immediately, even if cleanup's removal
	// already ran before the write landed.
	if n := f.s3.count(); n != 0 {
		t.Fatalf("orphaned objects: %d", n)
	}
}

func TestFinalizeLossReportsOrphanRemovalFailure(t *testing.T) {
	f := newFixture(t)
	f.s3.setPut(func(string) int {
		if _, err := f.pool.Exec(context.Background(), `UPDATE `+f.svc.table+` SET state='deleting'`); err != nil {
			t.Error(err)
		}
		return 0
	})
	f.s3.setDelete(func(string) int { return http.StatusForbidden }) // non-retryable
	_, err := f.svc.Upload(context.Background(), "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if !errors.Is(err, ErrUploadAbandoned) || !strings.Contains(err.Error(), "remove object of reclaimed upload") {
		t.Fatalf("want abandoned plus removal failure, got %v", err)
	}
	// The row stays in deleting, so cleanup still owns and retries the object.
	if _, state, _ := f.onlyRow(t); state != "deleting" {
		t.Fatalf("state %s", state)
	}
}

func TestUploadTimeoutIsBounded(t *testing.T) {
	f := newFixture(t)
	f.s3.setPut(func(string) int { time.Sleep(3 * time.Second); return 0 })
	start := time.Now()
	_, err := f.svc.Upload(context.Background(), "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(start); elapsed > f.cfg.MaxUploadDuration+time.Second {
		t.Fatalf("upload exceeded MaxUploadDuration: %v", elapsed)
	}
	if _, state, _ := f.onlyRow(t); state != "deleting" {
		t.Fatalf("state=%s", state)
	}
}

func TestDeleteStorageFailureIsRetriedByCleanup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file, err := f.svc.Upload(ctx, "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	f.s3.setDelete(func(string) int { return 500 })
	if err := f.svc.Delete(ctx, file.ID); err == nil {
		t.Fatal("expected delete failure")
	}
	if _, err := f.svc.Get(ctx, file.ID); !errors.Is(err, ErrDeleting) {
		t.Fatalf("want ErrDeleting, got %v", err)
	}
	f.s3.setDelete(nil)
	f.age(t)
	if err := f.svc.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if f.rows(t) != 0 || f.s3.count() != 0 {
		t.Fatal("cleanup must finish the delete")
	}
}

func TestCleanupReclaimsStalePendingAndLeavesFreshRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ready, err := f.svc.Upload(ctx, "keep.txt", "text/plain", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	stale := uuid.New()
	staleKey := "objects/" + stale.String()
	f.s3.put(testBucket, staleKey, []byte("partial"))
	if _, err := f.pool.Exec(ctx, `INSERT INTO `+f.svc.table+` (id,bucket,object_key,filename,content_type,size_bytes,state,updated_at)
		VALUES ($1,$2,$3,'x','text/plain',7,'pending', now() - interval '1 hour')`, stale, testBucket, staleKey); err != nil {
		t.Fatal(err)
	}
	fresh := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO `+f.svc.table+` (id,bucket,object_key,filename,content_type,size_bytes,state)
		VALUES ($1,$2,$3,'y','text/plain',1,'pending')`, fresh, testBucket, "objects/"+fresh.String()); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if f.state(t, stale) != "absent" {
		t.Fatal("stale pending row must be removed")
	}
	if _, ok := f.s3.object(testBucket, staleKey); ok {
		t.Fatal("stale pending object must be removed")
	}
	if f.state(t, fresh) != "pending" || f.state(t, ready.ID) != "ready" {
		t.Fatal("cleanup touched fresh or ready rows")
	}
}

func TestCleanupLeaseBlocksImmediateReclaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file, err := f.svc.Upload(ctx, "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	f.s3.setDelete(func(string) int { return 500 })
	_ = f.svc.Delete(ctx, file.ID)
	f.age(t)
	if err := f.svc.Cleanup(ctx); err == nil {
		t.Fatal("expected failing removal")
	}
	var attempts atomic.Int32
	f.s3.setDelete(func(string) int { attempts.Add(1); return 0 })
	if err := f.svc.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 0 || f.state(t, file.ID) != "deleting" {
		t.Fatal("a freshly claimed row must not be reclaimed before its lease expires")
	}
}

func TestConcurrentCleanupClaimsEachRowOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 60
	keys := make([]string, n)
	for i := range keys {
		id := uuid.New()
		keys[i] = "objects/" + id.String()
		f.s3.put(testBucket, keys[i], []byte("x"))
		if _, err := f.pool.Exec(ctx, `INSERT INTO `+f.svc.table+` (id,bucket,object_key,filename,content_type,size_bytes,state,updated_at)
			VALUES ($1,$2,$3,'x','text/plain',1,$4, now() - interval '1 hour')`, id, testBucket, keys[i], []string{"pending", "deleting"}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	// Several independent instances share the database and bucket.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		svc, err := New(f.pool, f.s3.client(t), f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.Cleanup(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.rows(t) != 0 || f.s3.count() != 0 {
		t.Fatalf("rows=%d objects=%d", f.rows(t), f.s3.count())
	}
	for _, k := range keys {
		if c := f.s3.removeCount(testBucket, k); c != 1 {
			t.Fatalf("%s removed %d times; each row must be claimed exactly once", k, c)
		}
	}
}

func TestConcurrentUploadsAndDeletes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf("payload-%d", i)
			file, err := f.svc.Upload(ctx, "f.txt", "text/plain", strings.NewReader(body), int64(len(body)))
			if err != nil {
				errs <- err
				return
			}
			if i%2 == 0 {
				errs <- f.svc.Delete(ctx, file.ID)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.rows(t) != 10 || f.s3.count() != 10 {
		t.Fatalf("rows=%d objects=%d, want 10/10", f.rows(t), f.s3.count())
	}
}

// A worker whose lease expired mid-operation may resume after another worker
// already finished the same row. Its remaining steps must be harmless.
func TestStaleCleanupWorkerResumingIsHarmless(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file, err := f.svc.Upload(ctx, "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.svc.Upload(ctx, "b.txt", "text/plain", strings.NewReader("def"), 3)
	if err != nil {
		t.Fatal(err)
	}
	// Worker A claims the row (ready -> deleting), then stalls past its lease.
	f.s3.setDelete(func(string) int { return 500 })
	_ = f.svc.Delete(ctx, file.ID)
	f.s3.setDelete(nil)
	f.age(t)
	// Worker B reclaims the expired lease and finishes the row.
	if err := f.svc.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if f.state(t, file.ID) != "absent" {
		t.Fatal("worker B must finish the row")
	}
	// Worker A resumes with its stale view of the row.
	if err := f.svc.removeObjectAndRow(ctx, file.ID, file.Bucket, file.Key); err != nil {
		t.Fatalf("stale worker must be a no-op, got %v", err)
	}
	if f.state(t, other.ID) != "ready" {
		t.Fatal("stale worker affected another file")
	}
	if _, ok := f.s3.object(other.Bucket, other.Key); !ok {
		t.Fatal("stale worker removed another object")
	}
}
