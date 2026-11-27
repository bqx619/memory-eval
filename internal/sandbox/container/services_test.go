package container

import (
	"context"
	"io"
	"memeval/internal/sandbox"
	"strings"
	"testing"
	"time"
)

func TestServiceReadinessTimeoutsAndCleanup(t *testing.T) {
	for _, check := range []string{"exec", "tcp", "http"} {
		t.Run(check, func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Services = []sandbox.Service{{Name: "test"}}
			h, err := b.Prepare(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Destroy(context.Background(), h)
			svc := sandbox.Service{Name: "test", Transport: "tcp", Port: 9999, TimeoutMS: 100, Env: s.Env, Command: []string{f.Python, "-c", "import time;time.sleep(100)"}}
			switch check {
			case "exec":
				svc.Ready.Exec = []string{f.Python, "-c", "raise SystemExit(1)"}
			case "tcp":
				svc.Ready.TCP = "127.0.0.1:9999"
			case "http":
				svc.Ready.HTTP = &sandbox.HTTPReady{URL: "http://127.0.0.1:9999/readyz", Status: 200}
			}
			child, err := b.PrepareServiceCommand(t.Context(), h, svc, svc.Command, svc.Env, "sidecar")
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.Spawn(t.Context(), child)
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.CloseReaders(p)
			go io.Copy(io.Discard, p.Stdout())
			go io.Copy(io.Discard, p.Stderr())
			f.Control(t, map[string]string{"FAKE_DOCKER_READY_FAIL": "1"})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			err = b.WaitService(ctx, svc, p)
			cancel()
			if err == nil {
				t.Fatal("not-ready accepted")
			}
			f.Control(t, map[string]string{})
			if err = b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			if err = b.ServiceStopped(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestServiceDestroyReverseOrderAndResidue(t *testing.T) {
	for _, leave := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "residue"}[leave], func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Services = []sandbox.Service{{Name: "first"}, {Name: "second"}}
			h, err := b.Prepare(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := b.episode(h)
			ids := []string{root.containerID}
			for _, name := range []string{"first", "second"} {
				svc := sandbox.Service{Name: name}
				c, e := b.PrepareServiceCommand(t.Context(), h, svc, s.Command, s.Env, "sidecar")
				if e != nil {
					t.Fatal(e)
				}
				ep, _ := b.episode(c)
				ids = append(ids, ep.containerID)
			}
			if leave {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_SIDECAR": "1"})
			}
			err = b.Destroy(t.Context(), h)
			if (err != nil) != leave {
				t.Fatal(err)
			}
			if leave && !strings.Contains(err.Error(), "resources remain") {
				t.Fatal(err)
			}
			got := []string{}
			for _, a := range f.Calls(t) {
				if len(a) > 3 && a[2] == "rm" {
					id := a[len(a)-1]
					for _, want := range ids {
						if id == want {
							got = append(got, id)
						}
					}
				}
			}
			want := []string{ids[0], ids[2], ids[1]}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatal("wrong destroy order", got, want)
			}
			f.Control(t, map[string]string{})
			for _, id := range ids {
				if _, e := b.call(t.Context(), "rm", "-f", id); e != nil {
					t.Fatal(e)
				}
			}
			if err = b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
		})
	}
}
