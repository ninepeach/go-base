package base

import (
	"fmt"
	"log/slog"
	"path/filepath"

	conf "github.com/ninepeach/go-conf"
	logger "github.com/ninepeach/go-logger/v2"
)

// loadConfig parses the file into an immutable runtime snapshot. The parser's
// strict options prevent duplicate keys, include escapes and typed env injection.
func loadConfig(path string) (snapshotConfig, error) {
	if path == "" {
		return snapshotConfig{root: map[string]any{}}, nil
	}
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return snapshotConfig{}, fmt.Errorf("base: config directory: %w", err)
	}
	data, err := conf.ParseFileWithOptions(path, conf.Options{
		IncludeRoot:         root,
		EnvAsString:         true,
		RejectDuplicateKeys: true,
	})
	if err != nil {
		// Do not include parser errors here: they may contain secret values.
		return snapshotConfig{}, fmt.Errorf("base: config parse failed: %w", ErrConfigType)
	}
	return snapshotConfig{root: data}, nil
}

func newRuntimeLogger() (*slog.Logger, error) {
	l, err := logger.New(nil)
	if err != nil {
		return nil, fmt.Errorf("base: logger initialization: %w", err)
	}
	return slog.New(logger.NewSlogHandler(l)), nil
}
