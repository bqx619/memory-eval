// Package fetch acquires pinned upstream files; evaluators never use its HTTP client.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"memeval/internal/report"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const DatasetID = "xiaowu0162/longmemeval-cleaned"
const SourceURL = "https://huggingface.co/datasets/" + DatasetID

var filenames = []string{"longmemeval_s_cleaned.json", "README.md"}
var revisionRE = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	ETag   string `json:"etag"`
}
type Lock struct {
	DatasetID  string         `json:"dataset_id"`
	SourceURL  string         `json:"source_url"`
	Revision   string         `json:"revision"`
	LicenseAck map[string]any `json:"license_ack"`
	Files      []File         `json:"files"`
	FetchedAt  string         `json:"fetched_at"`
}
type Options struct {
	Revision, Out                 string
	AckLicense, VerifyOnly, Force bool
}
type Client struct {
	BaseURL string
	HTTP    *http.Client
}
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
func hashFile(ctx context.Context, path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	return n, hex.EncodeToString(h.Sum(nil)), err
}
func ReadLock(dir string) (*Lock, error) {
	b, err := os.ReadFile(filepath.Join(dir, "source.lock.json"))
	if err != nil {
		return nil, err
	}
	var l Lock
	if err = json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.DatasetID != DatasetID || l.SourceURL != SourceURL || !revisionRE.MatchString(l.Revision) {
		return nil, fmt.Errorf("invalid source lock identity/revision")
	}
	if len(l.Files) != len(filenames) {
		return nil, fmt.Errorf("source lock must contain S data and README")
	}
	seen := map[string]bool{}
	for _, f := range l.Files {
		if f.Path != filenames[0] && f.Path != filenames[1] || seen[f.Path] || f.Bytes < 0 || !digestRE.MatchString(f.SHA256) {
			return nil, fmt.Errorf("invalid source lock file")
		}
		seen[f.Path] = true
	}
	return &l, nil
}
func Verify(ctx context.Context, dir, revision string) (*Lock, error) {
	l, err := ReadLock(dir)
	if err != nil {
		return nil, err
	}
	if revision != "" && l.Revision != revision {
		return nil, fmt.Errorf("existing lock revision differs")
	}
	for _, f := range l.Files {
		path := filepath.Join(dir, f.Path)
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("locked file must be regular: %s", f.Path)
		}
		n, digest, err := hashFile(ctx, path)
		if err != nil {
			return nil, err
		}
		if n != f.Bytes || digest != f.SHA256 {
			return nil, fmt.Errorf("locked file missing/modified: %s", f.Path)
		}
	}
	return l, nil
}
func (c Client) get(ctx context.Context, path string) (*http.Response, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://huggingface.co"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "memeval-fetch/1")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (c Client) getJSON(ctx context.Context, path string, out any) error {
	r, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return fmt.Errorf("upstream metadata too large")
	}
	return json.Unmarshal(b, out)
}
func (c Client) Fetch(ctx context.Context, name string, o Options) (_ *Lock, err error) {
	if name != "longmemeval-s" {
		return nil, fmt.Errorf("only longmemeval-s is supported")
	}
	if o.Out == "" {
		o.Out = "data/upstream/longmemeval-s"
	}
	if o.Revision != "" && !revisionRE.MatchString(o.Revision) {
		return nil, fmt.Errorf("revision must be an immutable 40-character SHA")
	}
	if o.VerifyOnly {
		return Verify(ctx, o.Out, o.Revision)
	}
	if !o.AckLicense {
		return nil, fmt.Errorf("review %s/blob/main/README.md and pass --ack-license; acknowledgement is not commercial clearance", SourceURL)
	}
	if _, e := os.Stat(filepath.Join(o.Out, "source.lock.json")); e == nil && !o.Force {
		l, e := Verify(ctx, o.Out, o.Revision)
		if e != nil {
			return nil, fmt.Errorf("refusing to overwrite existing lock/content without --force: %w", e)
		}
		return l, nil
	} else if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if !o.Force {
		for _, name := range filenames {
			if _, e := os.Lstat(filepath.Join(o.Out, name)); e == nil {
				return nil, fmt.Errorf("refusing to overwrite unlocked file without --force: %s", name)
			} else if !os.IsNotExist(e) {
				return nil, e
			}
		}
	}
	if err = os.MkdirAll(o.Out, 0755); err != nil {
		return nil, err
	}
	l := &Lock{DatasetID: DatasetID, SourceURL: SourceURL, Revision: o.Revision, Files: []File{}, LicenseAck: map[string]any{"by": "user", "at": time.Now().UTC().Format(time.RFC3339Nano), "ack": "User acknowledges reviewing the data card; acknowledgement is not commercial clearance"}}
	defer func() {
		if err != nil {
			_ = report.WriteJSON(context.WithoutCancel(ctx), filepath.Join(o.Out, "incomplete.lock.json"), map[string]any{"status": "incomplete", "lock": l, "error": err.Error()})
		}
	}()
	if l.Revision == "" {
		var meta struct {
			SHA string `json:"sha"`
		}
		if err = c.getJSON(ctx, "/api/datasets/"+DatasetID, &meta); err != nil {
			return nil, err
		}
		if !revisionRE.MatchString(meta.SHA) {
			return nil, fmt.Errorf("upstream did not resolve an immutable revision")
		}
		l.Revision = meta.SHA
	}
	var tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
		LFS  *struct {
			OID string `json:"oid"`
		} `json:"lfs"`
	}
	if err = c.getJSON(ctx, "/api/datasets/"+DatasetID+"/tree/"+l.Revision, &tree); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(o.Out, ".fetch-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	for _, name := range filenames {
		size := int64(-1)
		want := ""
		for _, entry := range tree {
			if entry.Path == name && entry.Type == "file" {
				if size >= 0 {
					return nil, fmt.Errorf("duplicate tree entry")
				}
				size = entry.Size
				if entry.LFS != nil {
					want = strings.TrimPrefix(entry.LFS.OID, "sha256:")
				}
			}
		}
		if size < 0 || size == math.MaxInt64 || want != "" && !digestRE.MatchString(want) || name == filenames[0] && want == "" {
			return nil, fmt.Errorf("missing/invalid tree size or LFS sha256 for %s", name)
		}
		r, e := c.get(ctx, "/datasets/"+DatasetID+"/resolve/"+l.Revision+"/"+name)
		if e != nil {
			return nil, e
		}
		target := filepath.Join(stage, name)
		f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			r.Body.Close()
			return nil, e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, r.Body}, size+1))
		closeErr := f.Close()
		r.Body.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		digest := hex.EncodeToString(h.Sum(nil))
		if n != size || want != "" && digest != want {
			return nil, fmt.Errorf("tree size/LFS sha256 mismatch for %s", name)
		}
		l.Files = append(l.Files, File{Path: name, Bytes: n, SHA256: digest, ETag: r.Header.Get("ETag")})
	}
	for _, f := range l.Files {
		if err = os.Rename(filepath.Join(stage, f.Path), filepath.Join(o.Out, f.Path)); err != nil {
			return nil, err
		}
	}
	l.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = report.WriteJSON(ctx, filepath.Join(o.Out, "source.lock.json"), l); err != nil {
		return nil, err
	}
	if err = os.Remove(filepath.Join(o.Out, "incomplete.lock.json")); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return l, nil
}
