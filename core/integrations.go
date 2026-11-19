package base

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	conf "github.com/ninepeach/go-conf"
	logger "github.com/ninepeach/go-logger/v2"
)

// ErrConfigParse reports a config file that exists but cannot be parsed.
var ErrConfigParse = errors.New("base: config parse failed")

// Parser messages may quote configuration values (and therefore secrets), so
// only these value-free details are extracted from them.
var (
	missingVarPattern = regexp.MustCompile(`variable reference for '([A-Za-z_][A-Za-z0-9_]*)' on line (\d+) can not be found`)
	linePattern       = regexp.MustCompile(`(?:on line |:)(\d+)(?::(\d+)\))?`)
)

// loadConfig parses the file into an immutable runtime snapshot. The parser's
// strict options prevent duplicate keys, include escapes and typed env injection.
//
// File access errors keep the OS error (path and reason). Parse errors never
// include parser text; they report the problem kind and line when known.
func loadConfig(path string) (snapshotConfig, error) {
	if path == "" {
		return snapshotConfig{root: map[string]any{}}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return snapshotConfig{}, fmt.Errorf("base: config file: %w", err)
	}
	_ = f.Close()
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
		return snapshotConfig{}, describeParseError(err)
	}
	return snapshotConfig{root: data}, nil
}

func describeParseError(err error) error {
	msg := err.Error()
	switch {
	case errors.Is(err, conf.ErrConfigTooLarge):
		return fmt.Errorf("%w: file exceeds 8 MiB", ErrConfigParse)
	case errors.Is(err, conf.ErrIncludeDepth):
		return fmt.Errorf("%w: include depth exceeded", ErrConfigParse)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: include file not readable", ErrConfigParse)
	}
	if m := missingVarPattern.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("%w: environment variable %s referenced on line %s is not set", ErrConfigParse, m[1], m[2])
	}
	if strings.Contains(msg, "duplicate key") {
		return fmt.Errorf("%w: duplicate key", ErrConfigParse)
	}
	if strings.Contains(msg, "escapes root") {
		return fmt.Errorf("%w: include path escapes the config directory", ErrConfigParse)
	}
	if strings.Contains(msg, "cyclic include") {
		return fmt.Errorf("%w: cyclic include", ErrConfigParse)
	}
	if m := linePattern.FindStringSubmatch(msg); m != nil {
		if m[2] != "" {
			return fmt.Errorf("%w: syntax error at line %s, column %s", ErrConfigParse, m[1], m[2])
		}
		return fmt.Errorf("%w: syntax error at line %s", ErrConfigParse, m[1])
	}
	return fmt.Errorf("%w: invalid syntax", ErrConfigParse)
}

func newRuntimeLogger() (*slog.Logger, error) {
	l, err := logger.New(nil)
	if err != nil {
		return nil, fmt.Errorf("base: logger initialization: %w", err)
	}
	return slog.New(logger.NewSlogHandler(l)), nil
}
