package configpaths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var invalidIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// Expand resolves explicit paths and glob patterns in argument order. Matches
// inside each pattern are sorted by filepath.Glob, and duplicate absolute paths
// keep only their first occurrence.
func Expand(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no config paths or patterns were provided")
	}

	seen := make(map[string]struct{})
	var paths []string
	for _, input := range inputs {
		matches, err := filepath.Glob(input)
		if err != nil {
			return nil, fmt.Errorf("invalid config pattern %q: %w", input, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("config path or pattern %q matched no files", input)
		}

		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, fmt.Errorf("inspect config %q: %w", match, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("config %q is not a regular file", match)
			}

			absolute, err := filepath.Abs(match)
			if err != nil {
				return nil, fmt.Errorf("make config path absolute %q: %w", match, err)
			}
			absolute = filepath.Clean(absolute)
			canonical, err := filepath.EvalSymlinks(absolute)
			if err != nil {
				return nil, fmt.Errorf("resolve config path %q: %w", match, err)
			}
			if _, ok := seen[canonical]; ok {
				continue
			}
			seen[canonical] = struct{}{}
			paths = append(paths, canonical)
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no config files were found")
	}
	return paths, nil
}

// BackendID returns a stable, human-readable identifier without inspecting the
// config contents.
func BackendID(configPath string) string {
	base := strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	base = strings.Trim(invalidIDChars.ReplaceAllString(base, "-"), "-")
	if base == "" {
		base = "backend"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(configPath)))
	return fmt.Sprintf("%s-%x", base, sum[:4])
}
