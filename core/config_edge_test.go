package base

import (
	"errors"
	"math"
	"testing"
)

func TestConfigRejectsPrecisionLoss(t *testing.T) {
	cfg := snapshotConfig{root: map[string]any{
		"large": int64(9007199254740993),
		"nan":   math.NaN(),
		"inf":   math.Inf(1),
	}}
	for _, path := range []string{"large", "nan", "inf"} {
		var result float64
		if err := cfg.Decode(path, &result); !errors.Is(err, ErrConfigType) {
			t.Fatalf("%s: expected ErrConfigType, got %v", path, err)
		}
	}
}

func TestConfigRejectsStringToInteger(t *testing.T) {
	cfg := snapshotConfig{root: map[string]any{"port": "5432"}}
	port := 1234
	if err := cfg.Decode("port", &port); !errors.Is(err, ErrConfigType) {
		t.Fatalf("expected type error, got %v", err)
	}
	if port != 1234 {
		t.Fatalf("destination changed on failure: %d", port)
	}
}

func TestConfigViewsAreIsolated(t *testing.T) {
	cfg := snapshotConfig{root: map[string]any{
		"components": map[string]any{
			"postgres": map[string]any{
				"primary":  map[string]any{"dsn": "primary"},
				"readonly": map[string]any{"dsn": "readonly"},
			},
		},
	}}
	primary := cfg.child("components.postgres.primary")
	var dsn string
	if err := primary.Decode("dsn", &dsn); err != nil || dsn != "primary" {
		t.Fatalf("primary config: %q %v", dsn, err)
	}
	if err := primary.Decode("readonly.dsn", &dsn); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("cross-instance access should fail: %v", err)
	}
}
