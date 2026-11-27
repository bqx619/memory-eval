package suite

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/jsonstrict"
	"os"
	"path/filepath"
)

type ContainerInfo struct {
	Format       string `json:"format" jsonschema:"enum=bundle"`
	Version      string `json:"version" jsonschema:"enum=1"`
	EpisodesFile string `json:"episodes_file" jsonschema:"enum=episodes.jsonl"`
	IndexFile    string `json:"index_file" jsonschema:"enum=episodes.index.jsonl"`
	IndexSHA256  string `json:"index_sha256"`
	Episodes     int    `json:"episodes"`
	Events       int    `json:"events"`
	Cases        int    `json:"cases"`
}

// BundleHeader is a container envelope; SchemaVersion describes episode semantics.
// Packing a legacy v2 suite preserves v2 semantics and never implicitly upgrades it.
type BundleHeader struct {
	SchemaVersion string              `json:"schema_version" jsonschema:"enum=2.0,enum=3.0"`
	SuiteID       string              `json:"suite_id"`
	Track         string              `json:"track"`
	Provenance    map[string]any      `json:"provenance"`
	Limitations   []string            `json:"limitations"`
	Protocol      SuiteProtocol       `json:"protocol"`
	ProtocolRef   string              `json:"protocol_ref,omitempty"`
	License       *License            `json:"license,omitempty"`
	DataNature    string              `json:"data_nature,omitempty"`
	Taxonomy      *Taxonomy           `json:"taxonomy,omitempty"`
	Splits        map[string][]string `json:"splits,omitempty"`
	Container     ContainerInfo       `json:"container"`
}

func headerOf(s *Suite, c ContainerInfo) BundleHeader {
	return BundleHeader{s.SchemaVersion, s.SuiteID, s.Track, s.Provenance, s.Limitations, s.Protocol, s.ProtocolRef, s.License, s.DataNature, s.Taxonomy, s.Splits, c}
}
func (h BundleHeader) suite() *Suite {
	return &Suite{SchemaVersion: h.SchemaVersion, SuiteID: h.SuiteID, Track: h.Track, Provenance: h.Provenance, Limitations: h.Limitations, Protocol: h.Protocol, ProtocolRef: h.ProtocolRef, License: h.License, DataNature: h.DataNature, Taxonomy: h.Taxonomy, Splits: h.Splits}
}

type IndexEntry struct {
	ID        string `json:"id"`
	ClusterID string `json:"cluster_id"`
	Line      int    `json:"line"`
	Offset    *int64 `json:"offset,omitempty"`
	Bytes     *int64 `json:"bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Events    int    `json:"events"`
	Cases     int    `json:"cases"`
}
type bundleData struct {
	dir             string
	header          BundleHeader
	canonicalHeader []byte
	indexBytes      []byte
	index           []IndexEntry
	byID            map[string]int
}

func (b *bundleData) digest(s *Suite) (string, error) {
	// Hash the persisted header object, including explicit null/empty fields.
	// A reopened bundle is required after changing its files.
	h := sha256.New()
	h.Write(b.canonicalHeader)
	h.Write([]byte{'\n'})
	h.Write(b.indexBytes)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (s *Suite) Container() string {
	if s.bundle != nil {
		return "bundle"
	}
	return "single"
}
func Load(ctx context.Context, path string) (*Suite, error) { return Open(ctx, path) }
func Open(ctx context.Context, path string) (*Suite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return openBundle(ctx, path)
	}
	if filepath.Base(path) == "suite.json" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(data, &probe) == nil && probe["container"] != nil {
			return openBundle(ctx, filepath.Dir(path))
		}
	}
	return loadSingle(ctx, path)
}
func openBundle(ctx context.Context, dir string) (*Suite, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "suite.json"))
	if err != nil {
		return nil, err
	}
	var h BundleHeader
	if err = jsonstrict.Decode(raw, &h); err != nil {
		return nil, fmt.Errorf("suite.json: %w", err)
	}
	if err = uniqueJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("suite.json: %w", err)
	}
	c := h.Container
	if c.Format != "bundle" || c.Version != "1" || c.EpisodesFile != "episodes.jsonl" || c.IndexFile != "episodes.index.jsonl" || c.Episodes < 1 || c.Events < 0 || c.Cases < 0 {
		return nil, fmt.Errorf("suite.json: invalid container")
	}
	if h.SchemaVersion != "2.0" && h.SchemaVersion != "3.0" {
		return nil, fmt.Errorf("suite.json: unsupported schema_version")
	}
	index, err := os.ReadFile(filepath.Join(dir, c.IndexFile))
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(index)) != c.IndexSHA256 {
		return nil, fmt.Errorf("episodes.index.jsonl: index_sha256 mismatch")
	}
	var generic any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err = d.Decode(&generic); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(generic)
	if err != nil {
		return nil, err
	}
	b := &bundleData{dir: dir, header: h, canonicalHeader: canonical, indexBytes: index, byID: map[string]int{}}
	r := bufio.NewReader(bytes.NewReader(index))
	offset := int64(0)
	events, cases := 0, 0
	for line := 0; ; line++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		row, e := r.ReadBytes('\n')
		if e == io.EOF && len(row) == 0 {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("episodes.index.jsonl line %d: missing newline", line)
		}
		var entry IndexEntry
		if err = jsonstrict.Decode(row, &entry); err != nil {
			return nil, fmt.Errorf("episodes.index.jsonl line %d: %w", line, err)
		}
		if err = uniqueJSONKeys(row); err != nil {
			return nil, fmt.Errorf("episodes.index.jsonl line %d: %w", line, err)
		}
		_, duplicate := b.byID[entry.ID]
		if entry.ID == "" || duplicate || entry.Line != line || entry.Offset == nil || *entry.Offset != offset || entry.Bytes == nil || *entry.Bytes < 2 || entry.Events < 0 || entry.Cases < 0 || !validHexSHA(entry.SHA256) {
			return nil, fmt.Errorf("episodes.index.jsonl line %d: invalid index entry", line)
		}
		b.byID[entry.ID] = line
		b.index = append(b.index, entry)
		offset += *entry.Bytes + 1
		if offset < 0 {
			return nil, fmt.Errorf("index byte offset overflow")
		}
		events += entry.Events
		cases += entry.Cases
	}
	if len(b.index) != c.Episodes || events != c.Events || cases != c.Cases {
		return nil, fmt.Errorf("suite.json: container/index count mismatch")
	}
	info, err := os.Stat(filepath.Join(dir, c.EpisodesFile))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != offset {
		return nil, fmt.Errorf("episodes.jsonl: byte length differs from index")
	}
	var s Suite
	if err = json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	// Match single's execution defaults without changing the persisted Merkle
	// header, which is already retained separately in canonicalHeader.
	if s.Protocol.Ingestion == "" {
		s.Protocol.Ingestion = "per_event"
	}
	if s.Limitations == nil {
		s.Limitations = []string{}
	}
	s.bundle = b
	return &s, nil
}
func validHexSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s *Suite) Index() []IndexEntry {
	if s.bundle != nil {
		out := append([]IndexEntry(nil), s.bundle.index...)
		for i := range out {
			a, b := *out[i].Offset, *out[i].Bytes
			out[i].Offset = &a
			out[i].Bytes = &b
		}
		return out
	}
	out := make([]IndexEntry, 0, len(s.Episodes))
	for i, e := range s.Episodes {
		out = append(out, IndexEntry{ID: e.ID, ClusterID: e.ClusterID, Line: i, Events: len(e.Events), Cases: len(e.Cases)})
	}
	return out
}
func (s *Suite) Iter(ctx context.Context, fn func(*Episode) error) error {
	return s.iter(ctx, true, fn)
}
func (s *Suite) iter(ctx context.Context, verify bool, fn func(*Episode) error) error {
	if s.bundle == nil {
		for i := range s.Episodes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(&s.Episodes[i]); err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(filepath.Join(s.bundle.dir, "episodes.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for _, entry := range s.bundle.index {
		if err = ctx.Err(); err != nil {
			return err
		}
		line, e := r.ReadBytes('\n')
		if e != nil {
			return fmt.Errorf("episodes.jsonl line %d: %w", entry.Line, e)
		}
		ep, e := s.decodeLine(line, entry, verify)
		if e != nil {
			return e
		}
		if e = fn(ep); e != nil {
			return fmt.Errorf("episodes.jsonl line %d (%s): %w", entry.Line, entry.ID, e)
		}
	}
	if _, err = r.ReadByte(); err != io.EOF {
		return fmt.Errorf("episodes.jsonl: extra bytes after indexed rows")
	}
	return nil
}
func (s *Suite) Episode(ctx context.Context, id string) (*Episode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.bundle == nil {
		if i, ok := s.episodeIDs[id]; ok && i < len(s.Episodes) && s.Episodes[i].ID == id {
			return &s.Episodes[i], nil
		}
		// Single suites remain mutable for importer construction. Rebuild on a
		// miss or a stale entry so later appended/renamed episodes stay visible.
		s.episodeIDs = make(map[string]int, len(s.Episodes))
		for i := range s.Episodes {
			s.episodeIDs[s.Episodes[i].ID] = i
		}
		if i, ok := s.episodeIDs[id]; ok {
			return &s.Episodes[i], nil
		}
		return nil, fmt.Errorf("episode %q not found", id)
	}
	i, ok := s.bundle.byID[id]
	if !ok {
		return nil, fmt.Errorf("episode %q not found", id)
	}
	entry := s.bundle.index[i]
	f, err := os.Open(filepath.Join(s.bundle.dir, "episodes.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	line := make([]byte, *entry.Bytes+1)
	if _, err = f.ReadAt(line, *entry.Offset); err != nil {
		return nil, fmt.Errorf("episodes.jsonl line %d: %w", entry.Line, err)
	}
	return s.decodeLine(line, entry, true)
}
func (s *Suite) decodeLine(line []byte, entry IndexEntry, verify bool) (*Episode, error) {
	fail := func(e error) (*Episode, error) {
		return nil, fmt.Errorf("episodes.jsonl line %d (%s): %w", entry.Line, entry.ID, e)
	}
	if int64(len(line)) != *entry.Bytes+1 || line[len(line)-1] != '\n' {
		return fail(fmt.Errorf("indexed byte length/newline mismatch"))
	}
	line = line[:len(line)-1]
	if verify && fmt.Sprintf("%x", sha256.Sum256(line)) != entry.SHA256 {
		return fail(fmt.Errorf("sha256 mismatch"))
	}
	ep, err := decodeCanonicalEpisode(line, s.SchemaVersion)
	if err != nil {
		return fail(err)
	}
	if ep.ID != entry.ID || ep.ClusterID != entry.ClusterID || len(ep.Events) != entry.Events || len(ep.Cases) != entry.Cases {
		return fail(fmt.Errorf("index identity/count mismatch"))
	}
	normalizeEpisode(ep)
	return ep, nil
}

func normalizeEpisode(ep *Episode) {
	if ep.ClusterID == "" {
		ep.ClusterID = ep.ID
	}
	for i := range ep.Events {
		e := &ep.Events[i]
		if e.Role == "" {
			e.Role = "observation"
		}
		if e.Attributes == nil {
			e.Attributes = map[string]any{}
		}
	}
	for i := range ep.Cases {
		c := &ep.Cases[i]
		if c.ClusterID == "" {
			c.ClusterID = ep.ClusterID
		}
		if c.Scope == nil {
			c.Scope = []string{}
		}
	}
}

type Writer struct {
	dir, stage    string
	file          *os.File
	index         []IndexEntry
	ids           map[string]bool
	offset        int64
	events, cases int
	closed        bool
}

func NewWriter(dir string) (*Writer, error) {
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return nil, fmt.Errorf("bundle destination must not exist: %s", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".suite-bundle-")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stage, "episodes.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	return &Writer{dir: dir, stage: stage, file: f, ids: map[string]bool{}}, nil
}
func (w *Writer) Abort() error {
	if w.closed {
		return nil
	}
	w.closed = true
	w.file.Close()
	return os.RemoveAll(w.stage)
}
func (w *Writer) WriteEpisode(ep *Episode) error {
	if w.closed {
		return fmt.Errorf("writer closed")
	}
	if ep.ID == "" || w.ids[ep.ID] {
		return fmt.Errorf("empty/duplicate episode id")
	}
	b, err := CanonicalJSON(ep)
	if err != nil {
		return err
	}
	offset, n := w.offset, int64(len(b))
	if _, err = w.file.Write(append(b, '\n')); err != nil {
		return err
	}
	w.index = append(w.index, IndexEntry{ep.ID, ep.ClusterID, len(w.index), &offset, &n, fmt.Sprintf("%x", sha256.Sum256(b)), len(ep.Events), len(ep.Cases)})
	w.offset += n + 1
	w.events += len(ep.Events)
	w.cases += len(ep.Cases)
	w.ids[ep.ID] = true
	return nil
}
func (w *Writer) Close(header *Suite) error {
	if w.closed {
		return fmt.Errorf("writer closed")
	}
	if len(w.index) == 0 {
		return fmt.Errorf("episodes must be nonempty")
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(w.stage, "episodes.tmp"), filepath.Join(w.stage, "episodes.jsonl")); err != nil {
		return err
	}
	if err := writeIndexHeader(w.stage, header, w.index, w.events, w.cases); err != nil {
		return err
	}
	staged, err := Open(context.Background(), w.stage)
	if err != nil {
		return err
	}
	if _, err = Validate(context.Background(), staged, ValidateOptions{SkipLeakScan: true}); err != nil {
		return err
	}
	if _, err := os.Stat(w.dir); !os.IsNotExist(err) {
		return fmt.Errorf("bundle destination already exists")
	}
	if err := os.Rename(w.stage, w.dir); err != nil {
		return err
	}
	w.closed = true
	return nil
}
func writeIndexHeader(dir string, s *Suite, index []IndexEntry, events, cases int) error {
	var lines bytes.Buffer
	for _, entry := range index {
		b, e := CanonicalJSON(entry)
		if e != nil {
			return e
		}
		lines.Write(b)
		lines.WriteByte('\n')
	}
	c := ContainerInfo{"bundle", "1", "episodes.jsonl", "episodes.index.jsonl", fmt.Sprintf("%x", sha256.Sum256(lines.Bytes())), len(index), events, cases}
	h, err := CanonicalJSON(headerOf(s, c))
	if err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"episodes.index.jsonl", lines.Bytes()}, {"suite.json", append(h, '\n')}} {
		f, e := os.CreateTemp(dir, ".bundle-")
		if e != nil {
			return e
		}
		name := f.Name()
		if _, e = f.Write(file.data); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = os.Rename(name, filepath.Join(dir, file.name))
		}
		if e != nil {
			os.Remove(name)
			return e
		}
	}
	return nil
}
func Pack(ctx context.Context, s *Suite, dir string) error {
	if _, err := Validate(ctx, s, ValidateOptions{SkipLeakScan: true}); err != nil {
		return err
	}
	w, err := NewWriter(dir)
	if err != nil {
		return err
	}
	defer w.Abort()
	if err = s.Iter(ctx, w.WriteEpisode); err != nil {
		return err
	}
	return w.Close(s)
}

// Reindex is an explicit repair operation: episodes are never rewritten, and a
// new root must be reviewed before updating any registry trust anchor.
func Reindex(ctx context.Context, dir string) (*Suite, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "suite.json"))
	if err != nil {
		return nil, err
	}
	var h BundleHeader
	if err = jsonstrict.Decode(raw, &h); err != nil {
		return nil, err
	}
	if err = uniqueJSONKeys(raw); err != nil {
		return nil, err
	}
	if h.Container.Format != "bundle" || h.Container.Version != "1" || h.Container.EpisodesFile != "episodes.jsonl" || h.Container.IndexFile != "episodes.index.jsonl" {
		return nil, fmt.Errorf("invalid bundle header")
	}
	f, err := os.Open(filepath.Join(dir, "episodes.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	index := []IndexEntry{}
	offset := int64(0)
	events, cases := 0, 0
	seen := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		line, e := r.ReadBytes('\n')
		if e == io.EOF && len(line) == 0 {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("episodes.jsonl line %d: missing newline", len(index))
		}
		b := line[:len(line)-1]
		ep, e := decodeCanonicalEpisode(b, h.SchemaVersion)
		if e != nil {
			return nil, fmt.Errorf("episodes.jsonl line %d: %w", len(index), e)
		}
		if seen[ep.ID] {
			return nil, fmt.Errorf("duplicate episode id")
		}
		seen[ep.ID] = true
		n, off := int64(len(b)), offset
		index = append(index, IndexEntry{ep.ID, ep.ClusterID, len(index), &off, &n, fmt.Sprintf("%x", sha256.Sum256(b)), len(ep.Events), len(ep.Cases)})
		offset += n + 1
		events += len(ep.Events)
		cases += len(ep.Cases)
	}
	if len(index) == 0 {
		return nil, fmt.Errorf("episodes must be nonempty")
	}
	// Validate the rebuilt view before publishing either the index or header.
	s := h.suite()
	s.bundle = &bundleData{dir: dir, index: index}
	if _, err = Validate(ctx, s, ValidateOptions{SkipLeakScan: true}); err != nil {
		return nil, err
	}
	if err = writeIndexHeader(dir, s, index, events, cases); err != nil {
		return nil, err
	}
	return Open(ctx, dir)
}
