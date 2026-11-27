package suite

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"memeval/internal/procexec"
	"memeval/internal/testoracle"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var bundleBenchPath = flag.String("bundle-bench-path", "", "local bundle for explicit streaming validation benchmark")
var bundleLegacyDir = flag.String("bundle-legacy-dir", "", "scratch directory containing original P2 singles for full byte roundtrip acceptance")

func TestBundleP2OriginalBytes(t *testing.T) {
	if *bundleLegacyDir == "" {
		t.Skip("explicit scratch originals required")
	}
	for _, name := range []string{"locomo10", "personamem-v1-32k", "personamem-v1-32k-nopersona"} {
		t.Run(name, func(t *testing.T) {
			old, err := os.ReadFile(filepath.Join(*bundleLegacyDir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var before Suite
			if err = json.Unmarshal(old, &before); err != nil {
				t.Fatal(err)
			}
			b, err := Open(t.Context(), testoracle.Dataset(t, filepath.Join("normalized", before.SuiteID)))
			if err != nil {
				t.Fatal(err)
			}
			copy := b.bundle.header.suite()
			if err = b.Iter(t.Context(), func(ep *Episode) error {
				copy.Episodes = append(copy.Episodes, *ep)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			enc := json.NewEncoder(&out)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err = enc.Encode(copy); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(old, out.Bytes()) {
				t.Fatal("original single byte roundtrip differs")
			}
		})
	}
}

func BenchmarkBundleValidate(b *testing.B) {
	if *bundleBenchPath == "" {
		b.Skip("set -bundle-bench-path")
	}
	for i := 0; i < b.N; i++ {
		s, err := Open(b.Context(), *bundleBenchPath)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = Validate(b.Context(), s, ValidateOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

func bundleFixture(t *testing.T) (*Suite, string) {
	t.Helper()
	s := minimalV3("qa")
	ep := s.Episodes[0]
	ep.ID += "-two"
	ep.Cases = append([]Case(nil), ep.Cases...)
	ep.Cases[0].ID += "-two"
	s.Episodes = append(s.Episodes, ep)
	s.Splits = map[string][]string{"all": {s.Episodes[0].Cases[0].ID, s.Episodes[1].Cases[0].ID}}
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := Pack(t.Context(), s, dir); err != nil {
		t.Fatal(err)
	}
	return s, dir
}
func TestBundleRoundTripAndRandomAccess(t *testing.T) {
	s, dir := bundleFixture(t)
	b, err := Open(t.Context(), filepath.Join(dir, "suite.json"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Container() != "bundle" || b.Episodes != nil {
		t.Fatal("eager episodes")
	}
	root, err := SHA256(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(t.Context(), b, ValidateOptions{ExpectedSHA256: root}); err != nil {
		t.Fatal(err)
	}
	copy := *b
	copy.bundle = nil
	copy.Episodes = []Episode{}
	if err = b.Iter(t.Context(), func(ep *Episode) error {
		random, e := b.Episode(t.Context(), ep.ID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(ep, random) {
			t.Fatal("random access differs")
		}
		copy.Episodes = append(copy.Episodes, *ep)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, _ := CanonicalJSON(s)
	c, _ := CanonicalJSON(&copy)
	if !bytes.Equal(a, c) {
		t.Fatal("single canonical round trip differs")
	}
	dest := filepath.Join(t.TempDir(), "repacked")
	if err = Pack(t.Context(), b, dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"suite.json", "episodes.index.jsonl", "episodes.jsonl"} {
		a, _ := os.ReadFile(filepath.Join(dir, name))
		c, _ := os.ReadFile(filepath.Join(dest, name))
		if !bytes.Equal(a, c) {
			t.Fatal(name, "roundtrip differs")
		}
	}
	indexes := b.Index()
	*indexes[0].Bytes = 0
	if *b.Index()[0].Bytes == 0 {
		t.Fatal("mutable index alias")
	}
	if _, err = b.Episode(t.Context(), "unknown"); err == nil {
		t.Fatal("unknown episode accepted")
	}
	// SHA256 does not read/decode the large episodes file.
	path := filepath.Join(dir, "episodes.jsonl")
	if err = os.Rename(path, path+".hidden"); err != nil {
		t.Fatal(err)
	}
	after, err := SHA256(b)
	if err != nil || after != root {
		t.Fatal(after, err)
	}
	os.Rename(path+".hidden", path)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python unavailable")
	}
	script, _ := filepath.Abs("../../scripts/verify_p2_5.py")
	// The embedded oracle only verifies this temporary bundle. Repository-wide
	// inventory checks belong to manual acceptance and race with live runs.
	outDir := t.TempDir()
	cmd := exec.Command(python, "-B", script, dir, root, "--no-protected-check", "--out", outDir)
	cmd.Dir = t.TempDir()
	if out, err := procexec.CombinedOutput(cmd); err != nil {
		t.Fatal(err, string(out))
	}
	for _, name := range []string{"protected-before.json", "protected-after.json", "protected-check.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); !os.IsNotExist(err) {
			t.Fatalf("embedded oracle unexpectedly created %s: %v", name, err)
		}
	}
}
func TestBundleTampering(t *testing.T) {
	for _, name := range []string{"episodes.jsonl", "episodes.index.jsonl", "suite.json"} {
		t.Run(name, func(t *testing.T) {
			_, dir := bundleFixture(t)
			b, _ := Open(t.Context(), dir)
			root, _ := SHA256(b)
			path := filepath.Join(dir, name)
			data, _ := os.ReadFile(path)
			if name == "suite.json" {
				var h map[string]any
				json.Unmarshal(data, &h)
				h["track"] = "tampered"
				data, _ = CanonicalJSON(h)
				data = append(data, '\n')
			} else {
				data[10] ^= 1
			}
			os.WriteFile(path, data, 0600)
			b, err := Open(t.Context(), dir)
			if err == nil {
				_, err = Validate(t.Context(), b, ValidateOptions{ExpectedSHA256: root})
			}
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatal("tamper not located", err)
			}
		})
	}
}
func TestBundleCanonicalAndSemanticRejection(t *testing.T) {
	for _, kind := range []string{"whitespace", "unknown", "duplicate-case", "missing-content", "v2-smuggling", "cross-split", "extra-line"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := bundleFixture(t)
			b, _ := Open(t.Context(), dir)
			index := b.Index()
			data, _ := os.ReadFile(filepath.Join(dir, "episodes.jsonl"))
			lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
			switch kind {
			case "whitespace":
				lines[0] = append([]byte{' '}, lines[0]...)
			case "unknown":
				var m map[string]any
				json.Unmarshal(lines[0], &m)
				m["unknown"] = true
				lines[0], _ = CanonicalJSON(m)
			case "missing-content":
				var m map[string]any
				json.Unmarshal(lines[0], &m)
				delete(m["events"].([]any)[0].(map[string]any), "content")
				lines[0], _ = CanonicalJSON(m)
			case "duplicate-case":
				ep, _ := b.Episode(t.Context(), index[1].ID)
				ep.Cases[0].ID = s.Episodes[0].Cases[0].ID
				lines[1], _ = CanonicalJSON(ep)
			case "v2-smuggling":
				s.SchemaVersion = "2.0"
			case "cross-split":
				s.Splits["bad"] = []string{"absent"}
			case "extra-line":
				os.WriteFile(filepath.Join(dir, "episodes.jsonl"), append(data, '\n'), 0600)
			}
			if kind != "extra-line" {
				offset := int64(0)
				for i, line := range lines {
					n, off := int64(len(line)), offset
					index[i].Offset = &off
					index[i].Bytes = &n
					index[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(line))
					offset += n + 1
				}
				os.WriteFile(filepath.Join(dir, "episodes.jsonl"), append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600)
				if err := writeIndexHeader(dir, s, index, b.bundle.header.Container.Events, b.bundle.header.Container.Cases); err != nil {
					t.Fatal(err)
				}
			}
			b, err := Open(t.Context(), dir)
			if err == nil {
				_, err = Validate(t.Context(), b, ValidateOptions{})
			}
			if err == nil {
				t.Fatal("invalid bundle accepted", kind)
			}
		})
	}
}
func TestBundleWriterAtomicAndReindex(t *testing.T) {
	s := minimalV3("qa")
	dir := filepath.Join(t.TempDir(), "new")
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	if err = w.WriteEpisode(&s.Episodes[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("published incomplete bundle")
	}
	if err = w.Close(s); err != nil {
		t.Fatal(err)
	}
	before, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := SHA256(before)
	data, _ := os.ReadFile(filepath.Join(dir, "episodes.jsonl"))
	os.WriteFile(filepath.Join(dir, "episodes.index.jsonl"), []byte("broken"), 0600)
	after, err := Reindex(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := SHA256(after)
	if h != root {
		t.Fatal("reindex changed root without content change")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "episodes.jsonl"))
	if !bytes.Equal(data, got) {
		t.Fatal("reindex rewrote episodes")
	}
	if _, err = NewWriter(dir); err == nil {
		t.Fatal("existing destination accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	w, err = NewWriter(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	ep := s.Episodes[0]
	ep.Cases[0].Cutoff = 999
	if err = w.WriteEpisode(&ep); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(s); err == nil {
		t.Fatal("invalid episode published")
	}
	if _, err = os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("invalid bundle visible")
	}
}
func TestCanonicalEpisodeLexicalParity(t *testing.T) {
	for _, text := range []string{"a\nb\t\\\"<>& café", "\x00\x01\x1f\b\f\r", "你好 \u2028 \u2029", "emoji 😀"} {
		s := minimalV3("qa")
		s.Episodes[0].Events[0].Content = text
		b, err := CanonicalJSON(s.Episodes[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decodeCanonicalEpisode(b, "3.0"); err != nil {
			t.Fatal(string(b), err)
		}
	}
}

func TestBundleExecutionDefaultsPreserveRoot(t *testing.T) {
	s, dir := bundleFixture(t)
	path := filepath.Join(dir, "suite.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	if err = json.Unmarshal(data, &h); err != nil {
		t.Fatal(err)
	}
	h["protocol"].(map[string]any)["ingestion"] = ""
	h["limitations"] = nil
	canonical, err := CanonicalJSON(h)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(canonical, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "episodes.index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(append(append(canonical, '\n'), index...)))
	b, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Protocol.Ingestion = ""
	s.Limitations = nil
	if _, err = Validate(t.Context(), s, ValidateOptions{}); err != nil {
		t.Fatal(err)
	}
	if b.Protocol.Ingestion != s.Protocol.Ingestion || !reflect.DeepEqual(b.Limitations, s.Limitations) {
		t.Fatal("single execution defaults differ")
	}
	if _, err = Validate(t.Context(), b, ValidateOptions{ExpectedSHA256: want}); err != nil {
		t.Fatal(err)
	}
}
