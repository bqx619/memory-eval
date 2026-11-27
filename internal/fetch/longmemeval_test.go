package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchLocalHF(t *testing.T) {
	for _, kind := range []string{"success", "hash", "short", "long", "http"} {
		t.Run(kind, func(t *testing.T) {
			data, card := []byte("[{\"question\":\"fixture\"}]"), []byte("license: mit\n")
			h := sha256.Sum256(data)
			digest := hex.EncodeToString(h[:])
			revision := strings.Repeat("a", 40)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/api/datasets/" + DatasetID:
					fmt.Fprintf(w, `{"sha":%q}`, revision)
				case "/api/datasets/" + DatasetID + "/tree/" + revision:
					want := digest
					if kind == "hash" {
						want = strings.Repeat("0", 64)
					}
					fmt.Fprintf(w, `[{"type":"file","path":"longmemeval_s_cleaned.json","size":%d,"lfs":{"oid":%q}},{"type":"file","path":"README.md","size":%d}]`, len(data), want, len(card))
				case "/datasets/" + DatasetID + "/resolve/" + revision + "/longmemeval_s_cleaned.json":
					w.Header().Set("ETag", `"xet-not-the-sha256"`)
					if kind == "http" {
						http.Error(w, "failure", 503)
						return
					}
					if kind == "short" {
						w.Write(data[:len(data)-1])
					} else {
						w.Write(data)
						if kind == "long" {
							w.Write([]byte("!"))
						}
					}
				case "/datasets/" + DatasetID + "/resolve/" + revision + "/README.md":
					w.Write(card)
				default:
					t.Errorf("unexpected URL %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c := Client{BaseURL: server.URL, HTTP: server.Client()}
			dir := t.TempDir()
			o := Options{Out: dir, AckLicense: true}
			l, err := c.Fetch(context.Background(), "longmemeval-s", o)
			if kind != "success" {
				if err == nil {
					t.Fatal("expected failure")
				}
				if _, err = os.Stat(filepath.Join(dir, "incomplete.lock.json")); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(filepath.Join(dir, "source.lock.json")); !os.IsNotExist(err) {
					t.Fatal("published failed lock")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.Files[0].SHA256 != digest || l.Files[0].ETag != `"xet-not-the-sha256"` {
				t.Fatal(l)
			}
			before := calls
			if _, err = c.Fetch(context.Background(), "longmemeval-s", Options{Out: dir, VerifyOnly: true}); err != nil {
				t.Fatal(err)
			}
			if calls != before {
				t.Fatal("verify-only used HTTP")
			}
			if _, err = c.Fetch(context.Background(), "longmemeval-s", o); err != nil {
				t.Fatal(err)
			}
			if calls != before {
				t.Fatal("existing pinned data redownloaded")
			}
			os.WriteFile(filepath.Join(dir, filenames[0]), []byte("tampered"), 0600)
			if _, err = c.Fetch(context.Background(), "longmemeval-s", o); err == nil {
				t.Fatal("overwrote changed content")
			}
			if calls != before {
				t.Fatal("refusal made HTTP request")
			}
			o.Force = true
			if _, err = c.Fetch(context.Background(), "longmemeval-s", o); err != nil {
				t.Fatal(err)
			}
			o.Force = false
			o.Revision = strings.Repeat("b", 40)
			if _, err = c.Fetch(context.Background(), "longmemeval-s", o); err == nil {
				t.Fatal("overwrote different revision")
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "source.lock.json"))
			var lock map[string]any
			if json.Unmarshal(raw, &lock) != nil {
				t.Fatal("invalid lock")
			}
		})
	}
}
func TestFetchNeedsAcknowledgement(t *testing.T) {
	_, err := (Client{BaseURL: "http://unused.invalid"}).Fetch(context.Background(), "longmemeval-s", Options{Out: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), SourceURL) {
		t.Fatal(err)
	}
}
