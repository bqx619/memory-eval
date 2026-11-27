package episodelog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"memeval/internal/config/redact"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestCapExactBytes(t *testing.T) {
	for _, n := range []int{0, 127, 256, 257, 1000, 100000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			const cap = 256
			data := []byte(strings.Repeat("0123456789\n", (n+10)/11))[:n]
			path := filepath.Join(t.TempDir(), "stream")
			w := newWriter(path, cap, nil, redact.Redactor{})
			for off := 0; off < n; off += 17 {
				if _, e := w.Write(data[off:min(n, off+17)]); e != nil {
					t.Fatal(e)
				}
			}
			if e := w.Close(); e != nil {
				t.Fatal(e)
			}
			if e := w.Close(); e != nil {
				t.Fatal(e)
			}
			got := read(t, path)
			want := data
			omitted := 0
			if n > cap {
				// An independent fixed-point calculation includes marker bytes in cap.
				tail := cap / 2
				for {
					marker := fmt.Sprintf("\n[memeval: %d bytes omitted; head 128 + tail %d retained]\n", n-128-tail, tail)
					if 128+len(marker)+tail <= cap {
						want = append(append(bytes.Clone(data[:128]), []byte(marker)...), data[n-tail:]...)
						omitted = n - 128 - tail
						break
					}
					tail--
				}
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("want %q\ngot %q", want, got)
			}
			if st := w.Stats(); st != (FileStats{int64(n), int64(len(want)), int64(omitted)}) {
				t.Fatal(st)
			}
			if len(got) > cap {
				t.Fatal("cap exceeded")
			}
		})
	}
}

func TestCredentialCarryAndLongLines(t *testing.T) {
	secret := "credential-with-no-prefix-123456789"
	dotenvSecret := "private\"value\n123456789"
	for _, text := range []string{
		"dataset 汉字 " + secret + " preserved\n",
		strings.Repeat("a", lineBuffer-10) + secret + " dataset尾\n" + strings.Repeat("z", 2*lineBuffer),
		"dataset\n" + strings.Repeat("a", lineBuffer) + " token=" + strings.Repeat("q", 3*lineBuffer) + " preserved\n",
		"Bearer " + strings.Repeat("b", 3*lineBuffer) + " dataset remains\n",
		"sk-" + strings.Repeat("b", 3*lineBuffer) + "; dataset remains\n",
		"quoted " + dotenvSecret + " and " + string(mustJSON(t, dotenvSecret)) + "\n",
	} {
		for _, chunk := range []int{1, 11, 4096, lineBuffer - 3, lineBuffer + 7, len(text)} {
			path := filepath.Join(t.TempDir(), "stream")
			r := redact.New([]string{dotenvSecret})
			w := newWriter(path, 1<<20, []string{secret}, r)
			for off := 0; off < len(text); off += chunk {
				w.Write([]byte(text[off:min(len(text), off+chunk)]))
				if len(w.pending) > lineBuffer+w.carry {
					t.Fatalf("unbounded line: %d", len(w.pending))
				}
			}
			if e := w.Close(); e != nil {
				t.Fatal(e)
			}
			want := r.Text(jsonrpc.Redact(text, []string{secret}))
			if got := string(read(t, path)); got != want {
				t.Fatalf("chunk=%d got len=%d want len=%d (credential streaming differs)", chunk, len(got), len(want))
			}
		}
	}
	// Every split of a credential between exactly two writes is covered.
	for i := 1; i < len(secret); i++ {
		p := filepath.Join(t.TempDir(), "split")
		w := newWriter(p, 4096, []string{secret}, redact.Redactor{})
		w.Write([]byte("before " + secret[:i]))
		w.Write([]byte(secret[i:] + " after\n"))
		w.Close()
		if got := string(read(t, p)); got != "before [REDACTED] after\n" {
			t.Fatal(got)
		}
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestKnownSecretOverlappingHeuristic(t *testing.T) {
	secret := "opaque-prefix-sk-1234567890-tail"
	text := strings.Repeat("a", 65470) + secret + strings.Repeat("z", 4*lineBuffer) + " dataset stays\n"
	p := filepath.Join(t.TempDir(), "stream")
	w := newWriter(p, 1<<20, []string{secret}, redact.Redactor{})
	for off := 0; off < len(text); off += 4096 {
		w.Write([]byte(text[off:min(len(text), off+4096)]))
		if len(w.pending) > lineBuffer+32 || len(w.raw) > lineBuffer+2*w.carry {
			t.Fatal("unbounded carry")
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	if got, want := string(read(t, p)), jsonrpc.Redact(text, []string{secret}); got != want {
		t.Fatal("overlapping token split a secret or redacted unrelated data")
	}
}

func TestStreamingRedactionPreservesTraceJSON(t *testing.T) {
	key := "sk-proj-0123456789abcdefghijklmnopqrstuvwxyz"
	text := strings.Repeat("x", lineBuffer) + ` password=abc" tail Flask-Login Bearer xyz" (` + key + ")"
	raw := append(mustJSON(t, map[string]string{"text": text}), '\n')
	for _, chunk := range []int{1, 4096, lineBuffer - 1} {
		path := filepath.Join(t.TempDir(), "stream")
		w := newWriter(path, 1<<20, nil, redact.Redactor{})
		for off := 0; off < len(raw); off += chunk {
			w.Write(raw[off:min(len(raw), off+chunk)])
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got := read(t, path)
		if !json.Valid(got) || string(got) != jsonrpc.Redact(string(raw), nil) {
			t.Fatalf("chunk %d: redaction corrupted JSON or differs from diagnostic redaction", chunk)
		}
		if strings.Contains(string(got), key) || !strings.Contains(string(got), "Flask-Login") {
			t.Fatal("incorrect credential boundaries")
		}
	}
}

func TestRunPermissionsIndexRPCAndSummary(t *testing.T) {
	secret := "fixture\"credential\nlong"
	ctx := redact.With(t.Context(), redact.New([]string{"dotenv-private-value"}))
	out := t.TempDir()
	r, e := New(ctx, out, Options{FileCap: 512, Secrets: []string{secret}})
	if e != nil {
		t.Fatal(e)
	}
	for _, ns := range []string{"describe", "paeval_123"} {
		ep, e := r.Episode(ns)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Fprint(ep.Writer("sut.stderr.log"), strings.Repeat("dataset line\n", 100))
		fmt.Fprint(ep.Sidecar("db"), secret+" dotenv-private-value dataset stays\n")
		fmt.Fprint(ep.Init("db", 1), "init data\n")
		ep.RPCError("retrieve", &jsonrpc.RPCError{Code: -42, Message: secret + " dataset stays", Data: json.RawMessage(`{"type":"fixture"}`)})
		if e = ep.Close(Meta{EpisodeDir: "/fixture/ep", Keep: true}); e != nil {
			t.Fatal(e)
		}
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	lines := bytes.Split(bytes.TrimSpace(read(t, filepath.Join(out, "logs/index.jsonl"))), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatal(len(lines))
	}
	for i, line := range lines {
		var row Record
		if e = json.Unmarshal(line, &row); e != nil {
			t.Fatal(e)
		}
		if row.Seq != i+1 || !row.Keep || row.Files["sut.stderr.log"].Omitted == 0 {
			t.Fatal(row)
		}
		if !bytes.Equal(append(bytes.Clone(line), '\n'), read(t, filepath.Join(out, "logs", row.Dir, "episode.json"))) {
			t.Fatal("metadata/index differ")
		}
	}
	files := 0
	var size int64
	e = filepath.WalkDir(filepath.Join(out, "logs"), func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		perm := fs.FileMode(0600)
		if d.IsDir() {
			perm = 0700
		} else {
			files++
			size += info.Size()
			b := read(t, p)
			if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, mustJSON(t, secret)) || bytes.Contains(b, []byte("dotenv-private-value")) {
				return fmt.Errorf("unredacted credential in %s", p)
			}
		}
		if info.Mode().Perm() != perm {
			return fmt.Errorf("mode %o %s", info.Mode().Perm(), p)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s := r.Summary()
	if s.Episodes != 2 || s.Files != files || s.Bytes != size || s.TruncatedFiles != 2 || s.KeptEPSUnredactedStderr != 2 {
		t.Fatalf("summary=%+v filesystem files=%d bytes=%d", s, files, size)
	}
	// Use a larger cap to assert complete structured RPC data is retained as JSON.
	r, e = New(ctx, t.TempDir(), Options{Secrets: []string{secret}})
	if e != nil {
		t.Fatal(e)
	}
	ep, _ := r.Episode("rpc")
	ep.RPCError("retrieve", &jsonrpc.RPCError{Code: -42, Message: secret + " dataset stays", Data: mustJSON(t, map[string]any{"type": "fixture", "private": secret})})
	r.Close()
	var row map[string]any
	if e = json.Unmarshal(read(t, filepath.Join(ep.Dir(), "rpc-errors.jsonl")), &row); e != nil {
		t.Fatal(e)
	}
	rpc := row["rpc_error"].(map[string]any)
	if rpc["message"] != "[REDACTED] dataset stays" || rpc["code"] != float64(-42) || rpc["data"].(map[string]any)["private"] != "[REDACTED]" {
		t.Fatal(row)
	}
	if _, ok := row["params"]; ok {
		t.Fatal("RPC parameters persisted")
	}
}

func TestNilDisabledAndFailures(t *testing.T) {
	var r *Run
	ep, e := r.Episode("describe")
	if ep != nil || e != nil {
		t.Fatal(ep, e)
	}
	ep.Writer("anything").Write([]byte("ignored"))
	ep.Sidecar("x").Write(nil)
	ep.Init("x", 1).Write(nil)
	ep.RPCError("x", errors.New("unused"))
	var w *Writer
	w.Write(nil)
	if ep.Close(Meta{}) != nil || r.Close() != nil || w.Close() != nil || !r.Summary().Disabled || ep.Dir() != "" {
		t.Fatal("nil unsafe")
	}
	out := t.TempDir()
	r, e = New(t.Context(), out, Options{Disabled: true})
	if e != nil || r != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(out, "logs")); !os.IsNotExist(e) {
		t.Fatal("disabled created logs")
	}
	if _, e = New(t.Context(), out, Options{FileCap: -1}); e == nil {
		t.Fatal("invalid cap")
	}
	r, e = New(t.Context(), out, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, e = New(t.Context(), out, Options{}); e == nil {
		t.Fatal("existing logs overwritten")
	}
	if _, e = r.Episode("../escape"); e == nil {
		t.Fatal("namespace path traversal")
	}
	ep, _ = r.Episode("safe")
	ep.Writer("../../escape")
	if e = r.Close(); e == nil {
		t.Fatal("writer failure swallowed")
	}
}
