package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"io"
	"memeval/internal/config/dotenv"
	"memeval/internal/config/redact"
	"os"
	"sort"
	"strconv"
	"strings"
)

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	path := ""
	clean := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			clean = append(clean, args[i:]...)
			break
		}
		if strings.HasPrefix(args[i], "--env-file=") {
			path = strings.TrimPrefix(args[i], "--env-file=")
			continue
		}
		if args[i] == "--env-file" {
			i++
			if i == len(args) {
				return fmt.Errorf("--env-file needs a path")
			}
			path = args[i]
			continue
		}
		clean = append(clean, args[i])
	}
	s, undo, err := dotenv.Load(path, errOut)
	if err != nil {
		return err
	}
	defer undo()
	ctx = dotenv.With(ctx, s)
	ctx = redact.With(ctx, redact.New(s.Secrets()))
	if len(clean) > 0 && clean[0] == "env" && (len(clean) == 1 || strings.HasPrefix(clean[1], "-")) {
		return runCommand(ctx, clean, out, errOut)
	}
	r := redact.From(ctx)
	err = runCommand(ctx, clean, &redactingWriter{out, r}, &redactingWriter{errOut, r})
	if err != nil {
		return &redactedError{err: err, text: redact.From(ctx).Text(err.Error())}
	}
	return nil
}

type redactingWriter struct {
	io.Writer
	r redact.Redactor
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	_, err := w.Writer.Write(w.r.Encoded(p))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

type redactedError struct {
	err  error
	text string
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }
func newEnvCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "env"}
	addEnvLifecycle(cmd, ctx, out)
	f := cmd.Flags()
	values := map[string]*string{}
	for _, b := range dotenv.Bindings {
		if b.Flag == "" {
			continue
		}
		v := f.String(b.Flag, dotenv.Default(b), "effective default")
		values[b.Env] = v
		for _, alias := range b.Aliases {
			f.StringVar(v, alias, dotenv.Default(b), "alias")
		}
	}
	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if f.NArg() != 0 {
			return fmt.Errorf("env takes flags only")
		}
		s := dotenv.From(ctx)
		explicit := map[string]bool{}
		f.Visit(func(v *flag.Flag) { explicit[v.Name] = true })
		data := map[string]map[string]any{}
		for _, b := range dotenv.Bindings {
			source := "default"
			if v, ok := os.LookupEnv(b.Env); ok && v != "" {
				source = "env"
				if s.Sources[b.Env] == ".env" {
					source = ".env"
				}
			}
			if explicit[b.Flag] {
				source = "flag"
			}
			for _, alias := range b.Aliases {
				if explicit[alias] {
					source = "flag"
				}
			}
			value := os.Getenv(b.Env)
			if values[b.Env] != nil {
				value = *values[b.Env]
			}
			if b.InvertBool {
				v, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("invalid configuration for %s", b.Env)
				}
				value = strconv.FormatBool(!v)
			}
			data[b.Env] = masked(b.Env, value, source)
			if b.Secret && value != "" {
				data[b.Env]["prefix"] = "****"
			}
		}
		// Provider/test keys are discovered from .env.example, rather than maintaining
		// a second independent list of accepted variables.
		example, err := dotenv.Read(s.Root+"/.env.example", nil)
		if err != nil {
			return err
		}
		for key := range s.File {
			example[key] = ""
		}
		for key := range example {
			if data[key] != nil {
				continue
			}
			v, ok := os.LookupEnv(key)
			source := "default"
			if ok {
				source = "env"
			}
			if s.Sources[key] == ".env" {
				source = ".env"
			}
			data[key] = masked(key, v, source)
		}
		keys := []string{}
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		rows := []map[string]any{}
		for _, key := range keys {
			rows = append(rows, data[key])
		}
		return emit(out, map[string]any{"configuration": rows})

	}
	return cmd
}

func masked(key, value, source string) map[string]any {
	runes := []rune(value)
	prefix := ""
	if len(runes) > 4 {
		prefix = string(runes[:4])
	} else if len(runes) > 0 {
		prefix = strings.Repeat("*", len(runes))
	}
	return map[string]any{"key": key, "source": source, "length": len(runes), "prefix": prefix}
}
