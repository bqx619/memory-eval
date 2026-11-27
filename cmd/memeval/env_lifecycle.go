package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/sandbox/container"
	"memeval/internal/sut"
	"path/filepath"
	"text/tabwriter"
)

func addEnvLifecycle(parent *cobra.Command, ctx context.Context, out io.Writer) {
	for _, name := range []string{"install", "list", "check", "destroy"} {
		cmd := &cobra.Command{Use: name + " [manifest|name]"}
		f := cmd.Flags()
		cache := f.String("cache-dir", "", "environment cache root")
		force := f.Bool("force", false, "replace an installed environment")
		offline := f.Bool("offline", false, "require existing dependency lock and --pull=false; wheel downloads may still use network")
		links := f.String("find-links", "", "local wheel directory (no index)")
		all := f.Bool("all", false, "destroy all installed environments")
		asJSON := f.Bool("json", false, "emit JSON summaries")
		full := f.Bool("full", false, "emit complete locks (requires --json)")
		cmd.RunE = func(command *cobra.Command, args []string) error {
			failure := func(e error) error {
				var missing *sut.NotInstalled
				if *asJSON && errors.As(e, &missing) {
					if err := emit(out, missing); err != nil {
						return err
					}
				}
				return e
			}
			if name == "list" {
				if len(args) != 0 {
					return fmt.Errorf("env list takes no arguments")
				}
				ls, e := sut.List(*cache)
				if e != nil {
					return e
				}
				if *full {
					if !*asJSON {
						return fmt.Errorf("--full requires --json")
					}
					return emit(out, ls)
				}
				rows := []sut.EnvSummary{}
				for _, l := range ls {
					s, e := sut.Summarize(l)
					if e != nil {
						return e
					}
					rows = append(rows, s)
				}
				if *asJSON {
					return emit(out, rows)
				}
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tLOCK_SHA\tRUNTIME\tPLATFORM\tINSTALLED_AT\tSIZE_BYTES\tSTATUS")
				for _, s := range rows {
					fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t%s\t%d\t%v\n", s.Name, s.LockSHA, s.RuntimeKind, s.RuntimeVersion, s.Platform, s.InstalledAt, s.Size, map[bool]string{true: "orphan", false: "installed"}[s.Orphan])
				}
				return w.Flush()
			}
			if name == "destroy" {
				if *all && len(args) > 0 || !*all && len(args) != 1 {
					return fmt.Errorf("env destroy requires one name or --all")
				}
				n := ""
				if len(args) > 0 {
					n = args[0]
				}
				if filepath.Ext(n) == ".json" {
					m, e := sut.Load(n)
					if e != nil {
						return e
					}
					n = m.Name
				}
				r, e := sut.DestroyEnvironments(ctx, container.New(container.DefaultConfig()), *cache, n, *all)
				if *asJSON {
					if err := emit(out, r); err != nil {
						return err
					}
				} else {
					w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
					fmt.Fprintln(w, "ACTION\tKIND\tNAME\tRESULT")
					for _, s := range r.Steps {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Action, s.Kind, s.Name, s.Result)
					}
					if err := w.Flush(); err != nil {
						return err
					}
					fmt.Fprintf(out, "clean=%v remaining=%v\n", r.Clean, r.Remaining)
				}
				return e
			}
			if len(args) != 1 {
				return fmt.Errorf("env %s requires one manifest or installed name", name)
			}
			if name == "check" && filepath.Ext(args[0]) != ".json" {
				ls, e := sut.List(*cache)
				if e != nil {
					return e
				}
				found := false
				for _, l := range ls {
					if l.Name == args[0] || filepath.Base(l.EnvDir) == args[0] {
						found = true
						if e = sut.CheckEnv(&l); e != nil {
							return failure(e)
						}
					}
				}
				if !found {
					return failure(&sut.NotInstalled{Manifest: args[0], Reason: "image_missing", Detail: "no installed image/lock with this name"})
				}
				return emit(out, map[string]any{"valid": true})
			}
			m, e := loadSUT(ctx, args[0])
			if e != nil {
				return e
			}
			m.CacheDir = *cache
			var l *sut.Lock
			if name == "install" {
				l, e = installEnvironment(ctx, m, sut.InstallOptions{Cache: *cache, Force: *force, Offline: *offline, FindLinks: *links, Warn: func(s string) { fmt.Fprintln(command.ErrOrStderr(), "warning: "+s) }})
			} else {
				l, e = sut.CheckLock(m)
			}
			if e != nil {
				return failure(e)
			}
			return emit(out, map[string]any{"env": l.EnvDir, "lock": filepath.Join(l.EnvDir, "env.lock.json"), "lock_sha256": l.LockSHA256, "valid": true})
		}
		parent.AddCommand(cmd)
	}
}
