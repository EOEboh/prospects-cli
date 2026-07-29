package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadDotEnv reads a .env file and sets any variable that is not already
// present in the real environment. Real env always wins, so a shell export
// overrides the file without editing it.
//
// A missing file is not an error: .env is a local-dev convenience and
// production sets real env vars.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	vars, err := parseDotEnv(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for k, v := range vars {
		if _, ok := os.LookupEnv(k); ok {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	return nil
}

// parseDotEnv handles the subset of .env syntax worth supporting: KEY=VALUE,
// optional "export " prefix, # comments, and single- or double-quoted values.
// Escape sequences are only expanded inside double quotes, matching sh.
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")

		key, raw, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", line)
		}
		vars[key] = unquote(strings.TrimSpace(raw))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			return expandEscapes(v[1 : len(v)-1])
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1] // single quotes are literal, as in sh
		}
	}
	// Unquoted values run to the first inline comment.
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func expandEscapes(v string) string {
	r := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\"`, `"`, `\\`, `\`)
	return r.Replace(v)
}
