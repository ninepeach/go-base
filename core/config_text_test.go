package base

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestConfigDecodeTextUnmarshalerTime(t *testing.T) {
	want := time.Date(2026, 11, 18, 12, 34, 56, 0, time.UTC)
	cfg := snapshotConfig{root: map[string]any{"when": "2026-11-18T12:34:56Z"}}
	var got time.Time
	if err := cfg.Decode("when", &got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestConfigDecodeTextUnmarshalerFailureAtomic(t *testing.T) {
	before := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := snapshotConfig{root: map[string]any{
		"app": map[string]any{"name": "changed", "when": "not-a-time"},
	}}
	type app struct {
		Name string    `conf:"name"`
		When time.Time `conf:"when"`
	}
	dst := app{Name: "original", When: before}
	err := cfg.Decode("app", &dst)
	if !errors.Is(err, ErrConfigType) {
		t.Fatalf("expected ErrConfigType, got %v", err)
	}
	if dst.Name != "original" || !dst.When.Equal(before) {
		t.Fatalf("partial mutation: %+v", dst)
	}
}

func TestConfigDecodeTextUnmarshalerRejectsNonString(t *testing.T) {
	cfg := snapshotConfig{root: map[string]any{"when": int64(42)}}
	var got time.Time
	if err := cfg.Decode("when", &got); !errors.Is(err, ErrConfigType) {
		t.Fatalf("expected ErrConfigType, got %v", err)
	}
}

func TestConfigDecodeTextUnmarshalerNested(t *testing.T) {
	const timestamp = "2026-11-18T12:34:56Z"
	type nested struct {
		When    time.Time   `conf:"when"`
		Pointer *time.Time  `conf:"pointer"`
		Items   []time.Time `conf:"items"`
	}
	cfg := snapshotConfig{root: map[string]any{
		"app": map[string]any{
			"when":    timestamp,
			"pointer": timestamp,
			"items":   []any{timestamp, timestamp},
		},
	}}
	var dst nested
	if err := cfg.Decode("app", &dst); err != nil {
		t.Fatal(err)
	}
	expected, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if !dst.When.Equal(expected) || dst.Pointer == nil || !dst.Pointer.Equal(expected) ||
		!reflect.DeepEqual(dst.Items, []time.Time{expected, expected}) {
		t.Fatalf("nested decoding failed: %+v", dst)
	}
}
