// Package dotenv uses godotenv syntax and preserves process-env precedence.
package dotenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/joho/godotenv"
)

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Parse delegates all value semantics, including variable expansion, to godotenv.
// The structural scan only detects duplicate keys; it never decodes a value.
func Parse(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("cannot read .env")
	}
	if strings.ContainsRune(string(data), 0) {
		return nil, fmt.Errorf("NUL in .env")
	}
	values, err := godotenv.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid .env syntax")
	} // library errors contain values
	for key, value := range values {
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid .env key name")
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("NUL in .env")
		}
	}
	if err := checkDuplicates(string(data)); err != nil {
		return nil, err
	}
	return values, nil
}

func checkDuplicates(src string) error {
	seen := map[string]bool{}
	for {
		src = strings.TrimLeftFunc(src, unicode.IsSpace)
		if src == "" {
			return nil
		}
		if src[0] == '#' {
			if n := strings.IndexByte(src, '\n'); n >= 0 {
				src = src[n+1:]
				continue
			}
			return nil
		}
		if strings.HasPrefix(src, "export") && len(src) > 6 {
			r, _ := utf8.DecodeRuneInString(src[6:])
			if envHorizontalSpace(r) {
				src = strings.TrimLeftFunc(src[6:], envHorizontalSpace)
			}
		}
		n := strings.IndexAny(src, "=:") // godotenv also accepts YAML-style assignment
		if n < 0 {
			return fmt.Errorf("invalid .env syntax")
		}
		key := strings.TrimSpace(src[:n])
		if !keyPattern.MatchString(key) {
			return fmt.Errorf("invalid .env key name")
		}
		if seen[key] {
			return fmt.Errorf("duplicate .env key %s", key)
		}
		seen[key] = true
		// Unlike statement whitespace, value whitespace does not consume newlines.
		src = strings.TrimLeftFunc(src[n+1:], envHorizontalSpace)
		if len(src) > 0 && (src[0] == '\'' || src[0] == '"') {
			quote, end := src[0], -1
			for i := 1; i < len(src); i++ {
				// Match godotenv's quote boundary rule, including multiline strings.
				if src[i] == quote && src[i-1] != '\\' {
					end = i
					break
				}
			}
			if end < 0 {
				return fmt.Errorf("invalid .env syntax")
			}
			src = src[end+1:]
		} else {
			if end := strings.IndexAny(src, "\r\n"); end >= 0 {
				src = src[end:]
			} else {
				return nil
			}
		}
	}
}

// Statement boundaries follow godotenv's horizontal-whitespace set.
func envHorizontalSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', '\r', ' ', 0x85, 0xA0:
		return true
	}
	return false
}

func Root(start string) string {
	p, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for dir := p; ; dir = filepath.Dir(dir) {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return p
		}
	}
}
func Read(path string, warnings io.Writer) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot open .env file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot stat .env file")
	}
	if st.Mode().Perm()&0044 != 0 && warnings != nil {
		fmt.Fprintln(warnings, "warning: .env is readable by group/other; use chmod 600")
	}
	return Parse(f)
}

type Settings struct {
	Root, Path string
	File       map[string]string
	Sources    map[string]string
}
type contextKey struct{}

func With(ctx context.Context, s *Settings) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func From(ctx context.Context) *Settings { s, _ := ctx.Value(contextKey{}).(*Settings); return s }

// Value also lets opt-in go tests read the repository file without exporting it.
func Value(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	cwd, _ := os.Getwd()
	values, err := Read(filepath.Join(Root(cwd), ".env"), nil)
	if err != nil {
		return ""
	}
	return values[key]
}
func Load(path string, warnings io.Writer) (*Settings, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	root := Root(cwd)
	if path == "" {
		path = filepath.Join(root, ".env")
	}
	values, err := Read(path, warnings)
	if err != nil {
		return nil, nil, err
	}
	s := &Settings{Root: root, Path: path, File: values, Sources: map[string]string{}}
	added := []string{}
	undo := func() {
		for _, key := range added {
			os.Unsetenv(key)
		}
	}
	for key, value := range values {
		if _, ok := os.LookupEnv(key); ok {
			s.Sources[key] = "env"
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			undo()
			return nil, nil, fmt.Errorf("cannot set .env key %s", key)
		}
		added = append(added, key)
		s.Sources[key] = ".env"
	}
	return s, undo, nil
}

type Binding struct {
	Flag, Env, Default string
	Secret             bool // no CLI flag for credentials
	Aliases            []string
	InvertBool         bool // positive environment setting bound to a negative flag
}

// Registry is the actual read-point table used by ApplyFlags and memeval env.
var Bindings = []Binding{
	{Flag: "sut-llm-base-url", Env: "MEMEVAL_SUT_LLM_BASE_URL"},
	{Env: "MEMEVAL_SUT_LLM_API_KEY", Secret: true},
	{Flag: "sut-llm-concurrency", Env: "MEMEVAL_SUT_LLM_CONCURRENCY", Default: "8"},
	{Flag: "sut-llm-rpm", Env: "MEMEVAL_SUT_LLM_RPM", Default: "0"},
	{Flag: "sut-llm-budget-tokens", Env: "MEMEVAL_SUT_LLM_BUDGET_TOKENS", Default: "0"},
	{Flag: "sut-llm-max-pause", Env: "MEMEVAL_SUT_LLM_MAX_PAUSE", Default: "30m"},
	{Flag: "sut-llm-model", Env: "MEMEVAL_SUT_LLM_MODEL", Default: "gpt-6-luna"},
	{Flag: "sut-llm-effort", Env: "MEMEVAL_SUT_LLM_REASONING_EFFORT", Default: "low"},
	{Flag: "docker-context", Env: "MEMEVAL_DOCKER_CONTEXT", Default: "orbstack"},
	{Flag: "docker-cli", Env: "MEMEVAL_DOCKER_CLI", Default: "docker"},
	{Flag: "linux-binary", Env: "MEMEVAL_LINUX_BINARY"},
	{Flag: "container-tmpfs-size", Env: "MEMEVAL_CONTAINER_TMPFS_SIZE", Default: "1g"},
	{Flag: "container-shm-size", Env: "MEMEVAL_CONTAINER_SHM_SIZE", Default: "512m"},
	{Flag: "no-logs", Env: "MEMEVAL_EPISODE_LOGS", Default: "1", InvertBool: true},
	{Flag: "log-file-cap", Env: "MEMEVAL_LOG_FILE_CAP_BYTES", Default: "33554432"},
	{Flag: "llm", Env: "MEMEVAL_LLM_MODE", Default: "replay"},
	{Flag: "cassette-dir", Env: "MEMEVAL_CASSETTE_DIR"},
	{Flag: "llm-parallel", Env: "MEMEVAL_LLM_PARALLEL", Default: "4"},
	{Flag: "prices", Env: "MEMEVAL_PRICES_FILE", Aliases: []string{"llm-prices"}},
	{Flag: "reader-model", Env: "MEMEVAL_READER_MODEL", Default: "gpt-6-luna"},
	{Flag: "anthropic-effort", Env: "MEMEVAL_ANTHROPIC_EFFORT", Default: "low"},
	{Flag: "judge-decision-model", Env: "MEMEVAL_JUDGE_DECISION_MODEL", Default: "jev-1.14.0"},
	{Flag: "cache-dir", Env: "MEMEVAL_CACHE_DIR"},
	{Flag: "ep-root", Env: "MEMEVAL_EP_ROOT", Default: "/private/tmp/memeval"},
	{Flag: "run-seed", Env: "MEMEVAL_RUN_SEED"},
	{Flag: "parallel", Env: "MEMEVAL_PARALLEL", Default: "1"},
}
