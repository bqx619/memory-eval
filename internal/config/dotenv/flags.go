package dotenv

import (
	"fmt"
	flag "github.com/spf13/pflag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

func Explicit(args []string) map[string]bool {
	m := map[string]bool{}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			key, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			m[key] = true
		}
	}
	return m
}
func Default(b Binding) string {
	if b.Flag == "linux-binary" {
		return filepath.Join("bin", "memeval-linux-"+runtime.GOARCH)
	}
	if b.InvertBool {
		v, _ := strconv.ParseBool(b.Default)
		return strconv.FormatBool(!v)
	}
	if b.Flag == "cassette-dir" || b.Flag == "cache-dir" {
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".cache", "memeval")
		if b.Flag == "cassette-dir" {
			path = filepath.Join(path, "cassettes")
		}
		return path
	}
	return b.Default
}
func ApplyFlags(f *flag.FlagSet, args []string) error {
	explicit := Explicit(args)
	for _, b := range Bindings {
		if f.Lookup(b.Flag) == nil {
			continue
		}
		names := append([]string{b.Flag}, b.Aliases...)
		if slices.ContainsFunc(names, func(s string) bool { return explicit[s] }) {
			continue
		}
		value, ok := os.LookupEnv(b.Env)
		if !ok || value == "" {
			continue
		}
		if b.InvertBool {
			v, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid configuration for %s", b.Env)
			}
			value = strconv.FormatBool(!v)
		}
		if err := f.Lookup(b.Flag).Value.Set(value); err != nil {
			return fmt.Errorf("invalid configuration for %s", b.Env)
		}
	}
	return nil
}

func Public(key string) bool {
	if strings.HasSuffix(key, "_ACCEPTANCE") {
		return true
	}
	for _, b := range Bindings {
		if b.Env == key {
			return !b.Secret && b.Flag != "linux-binary" && b.Flag != "docker-cli" && b.Flag != "cassette-dir" && b.Flag != "prices" && b.Flag != "cache-dir" && b.Flag != "ep-root"
		}
	}
	return false
}
func (s *Settings) Secrets() []string {
	values := []string{}
	for key, value := range s.File {
		if value != "" && !Public(key) {
			values = append(values, value)
		}
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && strings.HasPrefix(key, "MEMEVAL_") && !Public(key) {
			values = append(values, value)
		}
	}
	return values
}
