package media

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	_, err := New(nil, nil, Config{Bucket: "files"})
	if err == nil { t.Fatal("expected missing dependency error") }
}

func TestNewRejectsMissingBucket(t *testing.T) {
	_, err := New(nil, nil, Config{})
	if err == nil { t.Fatal("expected missing bucket error") }
}

func TestMediaSentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrNotFound, ErrDeleting) { t.Fatal("sentinel errors must be distinct") }
	if uuid.Nil == uuid.New() { t.Fatal("generated UUID must not be nil") }
}
