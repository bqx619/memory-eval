package results

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateCards = flag.Bool("update-cards", false, "update synthetic result card goldens")

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("testdata", name+".report.json"))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestCardGoldensAndDeterminism(t *testing.T) {
	for _, name := range []string{"bm25-pmb", "mem0-pmb", "hindsight-lme", "personamem-fixed"} {
		t.Run(name, func(t *testing.T) {
			raw := fixture(t, name)
			var r result.Report
			if e := json.Unmarshal(raw, &r); e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			o := CardOptions{RepoRoot: root, ReportPath: filepath.Join(root, "reports", name, "report.json"), ReportJSON: raw}
			var upstream *Upstream
			if name != "bm25-pmb" {
				upstream = &Upstream{Project: "Fixture", Repo: "https://github.com/example/fixture", Package: r.SUT.Name, Version: "1.0.0", License: "MIT"}
				o.VariantNotes = "合成变体说明"
			}
			if name == "personamem-fixed" {
				o.Primary = "mcq"
			}
			a, e := BuildCard(&r, upstream, o)
			if e != nil {
				t.Fatal(e)
			}
			b, e := JSON(a)
			if e != nil {
				t.Fatal(e)
			}
			again, e := BuildCard(&r, upstream, o)
			if e != nil {
				t.Fatal(e)
			}
			second, _ := JSON(again)
			if !bytes.Equal(b, second) {
				t.Fatal("nondeterministic card")
			}
			gold := filepath.Join("testdata", name+".card.json")
			if *updateCards {
				if e = os.WriteFile(gold, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			expected, e := os.ReadFile(gold)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(expected, b) {
				t.Fatalf("card differs: %s", b)
			}
			for _, private := range []string{"PRIVATE_QUESTION", "PRIVATE_RESPONSE", "FAKE_SECRET", "PRIVATE_LOG", "PRIVATE_PATH", "PRIVATE_EPISODE", "discarded_timing"} {
				if bytes.Contains(b, []byte(private)) {
					t.Fatal("private input leaked", private)
				}
			}
			if !bytes.Contains(b, []byte(`9007199254740993`)) {
				t.Fatal("large integer rounded")
			}
		})
	}
}
func TestCardMissingIsNullAndGoldRule(t *testing.T) {
	raw := []byte(`{"run_id":"legacy","summary":{},"cases":[{"gold_evidence_count":9,"metrics":{"recall":null,"empty_expected":false}},{"metrics":{"recall":0}},{"metrics":{"recall":1}},{"metrics":{}}]}`)
	c, e := BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw, RepoRoot: t.TempDir(), ReportPath: "/outside/report.json"})
	if e != nil {
		t.Fatal(e)
	}
	if at(c, "dataset", "cases_with_gold") != 2 {
		t.Fatal(c)
	}
	for _, key := range []string{"parallel", "started_at_utc", "resume_count", "wall_seconds", "memeval_commit"} {
		if at(c, "run", key) != nil {
			t.Fatal(key, c)
		}
	}
	if at(c, "source", "report_path") != "/outside/report.json" {
		t.Fatal(c)
	}
	if at(c, "run", "isolation", "selftest_passed") != nil {
		t.Fatal("missing selftest became true")
	}
	if selftestPassed(map[string]any{"checks": map[string]any{"a": map[string]any{"passed": true}, "b": map[string]any{"passed": false}}}) != false {
		t.Fatal("negative check ignored")
	}
}

func TestCardCostScalarTotals(t *testing.T) {
	for _, tc := range []struct{ name, billing, want string }{
		{"totals only", `{
			"sut":{"calls":80,"prompt_tokens":9007199254740993,"cached_tokens":20,"completion_tokens":30,"retries":5,"rate_limited_responses":4,"reasoning_tokens":7,"unmetered_calls":2,"client_disconnects":1,"circuit_opens":0,"queue_wait_p95_ms":0.1234567890123456789,"by_episode":[{"ns":"PRIVATE_EPISODE","calls":80}],"previously_consumed_tokens":999,"extra_total":123},
			"evaluator":{"http_429":1,"http_4xx":2,"http_5xx":3,"calls":4,"prompt_tokens":9007199254740993,"completion_tokens":5,"reasoning_tokens":6,"cost_usd_new":0.000000000000123456789,"cost_usd_original":null,"cassette_hits":0,"by_episode":[{"ns":"PRIVATE_EPISODE"}],"by_purpose":{"reader":{"calls":4}},"details":"PRIVATE_DETAIL"}
		}`, `{
			"sut":{"calls":80,"prompt_tokens":9007199254740993,"cached_tokens":20,"completion_tokens":30,"retries":5,"rate_limited_responses":4,"reasoning_tokens":7,"unmetered_calls":2,"client_disconnects":1,"circuit_opens":0,"queue_wait_p95_ms":0.1234567890123456789},
			"evaluator":{"http_429":1,"http_4xx":2,"http_5xx":3,"calls":4,"prompt_tokens":9007199254740993,"completion_tokens":5,"reasoning_tokens":6,"cost_usd_new":0.000000000000123456789,"cost_usd_original":null,"cassette_hits":0}
		}`},
		{"missing", `{}`, `{"sut":null,"evaluator":null}`},
		{"unknown", `{"sut":null,"evaluator":null}`, `{"sut":null,"evaluator":null}`},
		{"unknown total", `{"sut":{"calls":null},"evaluator":{"cost_usd_new":null}}`, `{"sut":{"calls":null},"evaluator":{"cost_usd_new":null}}`},
		{"empty", `{"sut":{},"evaluator":{}}`, `{"sut":{},"evaluator":{}}`},
		{"non scalar", `{"sut":{"calls":[1],"prompt_tokens":{"total":2}},"evaluator":{"calls":{"total":1}}}`, `{"sut":{},"evaluator":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"run_id":"cost","summary":{},"billing":` + tc.billing + `}`)
			original := bytes.Clone(raw)
			var expected any
			if err := decode([]byte(tc.want), &expected); err != nil {
				t.Fatal(err)
			}
			want, _ := JSON(expected)
			for range 2 {
				card, err := BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw})
				if err != nil {
					t.Fatal(err)
				}
				got, err := JSON(card["cost"])
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("cost = %s, want %s (error: %v)", got, want, err)
				}
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("source report changed")
			}
		})
	}
}

func TestCardLongMemEvalScoreDeduplication(t *testing.T) {
	var src map[string]any
	if err := decode(fixture(t, "hindsight-lme"), &src); err != nil {
		t.Fatal(err)
	}
	official := map[string]any{
		"judge":    map[string]any{"mean": json.Number("0.8"), "n": json.Number("50")},
		"fidelity": "partial", "deviations": []any{"fixture"},
	}
	object(src["scores"])["official"] = official
	raw, err := JSON(src)
	if err != nil {
		t.Fatal(err)
	}
	c, err := BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	scores := object(at(c, "results", "scores"))
	if _, exists := scores["memory_only"]; exists || len(scores) != 1 {
		t.Fatalf("memory-only duplicates remain: %v", scores)
	}
	got, _ := JSON(scores["official"])
	want, _ := JSON(official)
	if !bytes.Equal(got, want) {
		t.Fatalf("official changed: %s", got)
	}
	got, _ = JSON(at(c, "results", "metrics"))
	want, _ = JSON(at(src, "summary", "metrics"))
	if !bytes.Equal(got, want) {
		t.Fatalf("metrics changed: %s", got)
	}
	after, _ := JSON(src)
	if !bytes.Equal(raw, after) {
		t.Fatal("source mutated")
	}
}

func TestCardScoreDeduplicationBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, metrics, scores, want string }{
		{"direct", `{"recall":{"mean":1,"n":2}}`, `{"recall":{"mean":1.0,"n":2e0}}`, `{}`},
		{"null mean", `{"recall":{"mean":null,"n":0}}`, `{"memory_only":{"recall":{"mean":null,"n":0}}}`, `{}`},
		{"different n", `{"recall":{"mean":1,"n":2}}`, `{"memory_only":{"recall":{"mean":1,"n":3}}}`, `{"memory_only":{"recall":{"mean":1,"n":3}}}`},
		{"precision", `{"recall":{"mean":0.123456789012345678901,"n":2}}`, `{"memory_only":{"recall":{"mean":0.123456789012345678902,"n":2}}}`, `{"memory_only":{"recall":{"mean":0.123456789012345678902,"n":2}}}`},
		{"extra metadata", `{"recall":{"mean":1,"n":2}}`, `{"memory_only":{"recall":{"mean":1,"n":2,"unit":"turn"}}}`, `{"memory_only":{"recall":{"mean":1,"n":2,"unit":"turn"}}}`},
		{"prefix only", `{"recall":{"mean":1,"n":2}}`, `{"official":{"memory_only":{"recall":{"mean":1,"n":2}}}}`, `{"official":{"memory_only":{"recall":{"mean":1,"n":2}}}}`},
		{"partial groups", `{"nested.recall":{"mean":1,"n":2},"flat.recall":{"mean":1,"n":2}}`, `{"memory_only":{"nested":{"recall":{"mean":1,"n":2},"ndcg":{"mean":0.5,"n":2}},"flat.recall":{"mean":1,"n":2}},"locomo_official":{"token_f1":{"mean":0.7500,"n":2}},"official":null,"notes":["keep"]}`, `{"memory_only":{"nested":{"ndcg":{"mean":0.5,"n":2}}},"locomo_official":{"token_f1":{"mean":0.7500,"n":2}},"official":null,"notes":["keep"]}`},
		{"unknown scores", `{}`, `null`, `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"run_id":"dedup","summary":{"metrics":` + tc.metrics + `},"scores":` + tc.scores + `}`)
			c, err := BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := JSON(at(c, "results", "scores"))
			var expected any
			if err := decode([]byte(tc.want), &expected); err != nil {
				t.Fatal(err)
			}
			want, _ := JSON(expected)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestIndexDedupeSortMetadataAndWriters(t *testing.T) {
	root := t.TempDir()
	var errs bytes.Buffer
	o := IndexOptions{RepoRoot: root, Stderr: &errs, Now: func() time.Time { return time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC) }, Commit: "known-build-commit"}
	protocol := []byte(`{"protocol_id":"personamem","version":"1","variants":{"official":{"params":{}}},"scoring":{"primary":"mcq"},"upstream":{},"fidelity_rules":{}}`)
	write(t, filepath.Join(root, "protocols/personamem.json"), protocol)
	raw := fixture(t, "personamem-fixed")
	var r map[string]any
	if e := decode(raw, &r); e != nil {
		t.Fatal(e)
	}
	object(at(r, "protocol", "benchmark"))["sha256"] = digest(protocol)
	object(r["sut"])["name"] = "fixture"
	object(r["sut"])["variant"] = "v1"
	// Arbitrary text in excluded sections never propagates; script-sensitive
	// allowed metadata must remain valid in both JSON and the JS wrapper.
	r["warnings"] = []string{"</ScRiPt><script>alert(1)</script>"}
	for _, v := range []struct {
		dir, id, date string
		resume        int
	}{{"a", "same", "2026-01-02T00:00:00Z", 0}, {"b", "same", "2026-01-02T00:00:00Z", 2}, {"c", "newest", "2026-01-03T00:00:00Z", 0}} {
		r["run_id"], r["started_at_utc"], r["resume_count"] = v.id, v.date, v.resume
		b, _ := JSON(r)
		write(t, filepath.Join(root, "reports", v.dir, "report.json"), b)
	}
	write(t, filepath.Join(root, "reports/rejected/report.json"), []byte(`{"reason":"PRIVATE_LOG"}`))
	write(t, filepath.Join(root, "suts/fixture/manifest.json"), []byte(`{"name":"fixture","variant":"v1","notes":"manifest notes","process":{"env_file":"MUST_NOT_READ"}}`))
	if e := os.Symlink(filepath.Join(root, "reports"), filepath.Join(root, "reports-alias")); e != nil {
		t.Fatal(e)
	}
	a, e := BuildIndex(o, filepath.Join(root, "reports-alias"), filepath.Join(root, "reports/a"))
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Cards) != 2 || a.Cards[0]["id"] != "newest" || resumeCount(a.Cards[1]) != 2 {
		t.Fatal(a.Cards)
	}
	if !strings.Contains(errs.String(), "skipping rejected") || !strings.Contains(errs.String(), "upstream=null") {
		t.Fatal(errs.String())
	}
	if at(a.Cards[0], "subject", "variant_notes") != "manifest notes" || at(a.Cards[0], "results", "headline").([]string)[0] != "mcq" {
		t.Fatal(a.Cards[0])
	}
	b, e := BuildIndex(o, filepath.Join(root, "reports"))
	if e != nil {
		t.Fatal(e)
	}
	ja, _ := JSON(a)
	jb, _ := JSON(b)
	if !bytes.Equal(ja, jb) {
		t.Fatal("build nondeterminism")
	}
	out := filepath.Join(root, "out")
	if e = WriteIndex(context.Background(), out, a); e != nil {
		t.Fatal(e)
	}
	files, _ := os.ReadDir(out)
	if len(files) != 2 {
		t.Fatal(files)
	}
	js, _ := os.ReadFile(filepath.Join(out, "index.js"))
	if bytes.Contains(bytes.ToLower(js), []byte("</script")) || !bytes.Contains(js, []byte(`<\/ScRiPt>`)) {
		t.Fatal(string(js))
	}
	body := bytes.TrimSuffix(bytes.TrimPrefix(js, []byte("window.MEMEVAL_RESULTS = ")), []byte(";\n"))
	var obj any
	if e = decode(body, &obj); e != nil {
		t.Fatal(e)
	}
	plain, _ := JSON(obj)
	if !bytes.Equal(plain, ja) {
		t.Fatal("JS and JSON differ")
	}
	write(t, filepath.Join(root, "suts/fixture/UPSTREAM.json"), []byte(`{}`))
	if _, e = BuildIndex(o, filepath.Join(root, "reports")); e == nil {
		t.Fatal("invalid upstream ignored")
	}
	write(t, filepath.Join(root, "suts/fixture/UPSTREAM.json"), []byte(`{"project":"X","repo":"https://github.com/a/b","package":"p","version":"1","license":"MIT"}`))
	c, e := LoadCard(filepath.Join(root, "reports/a"), o)
	if e != nil {
		t.Fatal(e)
	}
	if c["subject"].(map[string]any)["upstream"] == nil {
		t.Fatal("upstream missing")
	}
}

func TestCardNumericLexemesAndProtocolHeadlines(t *testing.T) {
	raw := []byte(`{"run_id":"numeric","mode":"fixed_reader","summary":{"metrics":{"token_f1":{"mean":0.12345678901234567890123456789,"n":2}}},"wall_seconds":1.2300e-2}`)
	c, e := BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw, Primary: "token_f1"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := JSON(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"0.12345678901234567890123456789", "1.2300e-2"} {
		if !bytes.Contains(b, []byte(v)) {
			t.Fatalf("numeric precision lost: %s", b)
		}
	}
	if at(c, "results", "headline").([]string)[0] != "token_f1" {
		t.Fatal(c)
	}
	raw = []byte(`{"run_id":"typed","mode":"retrieval","protocol":{"benchmark":{"variant":"typed_context"}},"summary":{}}`)
	c, e = BuildCard(&result.Report{}, nil, CardOptions{ReportJSON: raw})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(at(c, "results", "headline").([]string), ",") != "precision,recall" {
		t.Fatal(c)
	}
}
