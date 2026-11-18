package media

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// fakeS3 is a minimal path-style S3 endpoint for anonymous single-part
// requests, with hooks for fault injection.
type fakeS3 struct {
	mu       sync.Mutex
	buckets  map[string]bool
	objects  map[string][]byte
	removes  map[string]int
	onPut    func(key string) int // returns an HTTP status; 0 means OK
	onDelete func(key string) int
	srv      *httptest.Server
}

func newFakeS3(t *testing.T, buckets ...string) *fakeS3 {
	t.Helper()
	f := &fakeS3{buckets: map[string]bool{}, objects: map[string][]byte{}, removes: map[string]int{}}
	for _, b := range buckets {
		f.buckets[b] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) client(t *testing.T) *minio.Client {
	return f.clientWith(t, "", "")
}

// signedClient can presign URLs; the fake does not verify signatures.
func (f *fakeS3) signedClient(t *testing.T) *minio.Client {
	return f.clientWith(t, "access", "secret-key-value")
}

func (f *fakeS3) clientWith(t *testing.T, access, secret string) *minio.Client {
	t.Helper()
	c, err := minio.New(strings.TrimPrefix(f.srv.URL, "http://"), &minio.Options{
		Creds:      credentials.NewStaticV4(access, secret, ""),
		Region:     "us-east-1",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fakeS3) setPut(h func(key string) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onPut = h
}

func (f *fakeS3) setDelete(h func(key string) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onDelete = h
}

func (f *fakeS3) object(bucket, key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[bucket+"/"+key]
	return b, ok
}

func (f *fakeS3) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}

func (f *fakeS3) removeCount(bucket, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removes[bucket+"/"+key]
}

func (f *fakeS3) put(bucket, key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[bucket+"/"+key] = data
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := parts[0]
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	f.mu.Lock()
	known := f.buckets[bucket]
	onPut, onDelete := f.onPut, f.onDelete
	f.mu.Unlock()
	if !known {
		http.Error(w, "<Error><Code>NoSuchBucket</Code></Error>", http.StatusNotFound)
		return
	}
	if key == "" {
		w.WriteHeader(http.StatusOK) // HEAD bucket
		return
	}
	full := bucket + "/" + key
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if onPut != nil {
			if status := onPut(key); status != 0 {
				http.Error(w, "<Error><Code>InternalError</Code></Error>", status)
				return
			}
		}
		f.put(bucket, key, body)
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		data, ok := f.object(bucket, key)
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			}
			return
		}
		w.Header().Set("ETag", `"fake"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case http.MethodDelete:
		if onDelete != nil {
			if status := onDelete(key); status != 0 {
				http.Error(w, "<Error><Code>InternalError</Code></Error>", status)
				return
			}
		}
		f.mu.Lock()
		delete(f.objects, full)
		f.removes[full]++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
