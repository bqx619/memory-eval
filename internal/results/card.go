// Package results extracts public result cards; it never exports case text or logs.
package results

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"memeval/internal/result"
	"memeval/internal/sut"
	"path/filepath"
	"strings"
)

type Upstream = sut.Upstream
type Card map[string]any

type CardOptions struct {
	RepoRoot, ReportPath string
	// ReportJSON preserves absent fields and the original numeric lexemes. CLI
	// callers always supply the exact bytes used for source.report_sha256.
	ReportJSON   []byte
	VariantNotes any
	Primary      string
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func object(v any) map[string]any {
	if c, ok := v.(Card); ok {
		return c
	}
	m, _ := v.(map[string]any)
	return m
}
func at(v any, keys ...string) any {
	for _, k := range keys {
		v = object(v)[k]
	}
	return v
}
func str(v any) string { s, _ := v.(string); return s }
func nonempty(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}
func pick(v any, keys ...string) map[string]any {
	m := map[string]any{}
	for _, k := range keys {
		m[k] = at(v, k)
	}
	return m
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

// JSON sorts every object key and preserves json.Number; no clock or git lookup.
func JSON(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var ordered any
	if e = decode(b, &ordered); e != nil {
		return nil, e
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	e = enc.Encode(ordered)
	return out.Bytes(), e
}

func BuildCard(r *result.Report, upstream *Upstream, o CardOptions) (Card, error) {
	if r == nil {
		return nil, fmt.Errorf("report required")
	}
	if upstream != nil {
		if e := upstream.Validate(); e != nil {
			return nil, e
		}
	}
	raw := o.ReportJSON
	if len(raw) == 0 {
		var e error
		raw, e = json.Marshal(r)
		if e != nil {
			return nil, e
		}
	}
	var src map[string]any
	if e := decode(raw, &src); e != nil {
		return nil, e
	}
	if str(src["run_id"]) == "" || object(src["summary"]) == nil {
		return nil, fmt.Errorf("result card requires run_id and summary")
	}
	summary, s := src["summary"], src["sut"]
	protocol, budget := src["protocol"], at(src, "protocol", "budget")
	binding := at(protocol, "benchmark")
	datasetProtocol := pick(binding, "variant", "ref", "sha256")
	datasetProtocol["id"] = at(binding, "protocol_id")
	categories := any(nil)
	if m := object(src["by_category"]); m != nil {
		categories = len(m)
	}
	by := any(nil)
	if m := object(src["by_category"]); m != nil {
		out := map[string]any{}
		for k, v := range m {
			out[k] = pick(v, "planned", "status_counts", "metrics")
		}
		by = out
	}
	timings := any(nil)
	if m := object(at(summary, "timings")); m != nil {
		out := map[string]any{}
		for k, v := range m {
			out[k] = pick(v, "p50", "p95")
		}
		timings = out
	}
	isolation := pick(src["isolation"], "tier", "backend", "network_mode", "network_enforced")
	isolation["run_clean"] = at(src, "isolation", "container", "residue", "run_clean")
	isolation["selftest_passed"] = selftestPassed(at(src, "isolation", "selftest"))
	repeats := src["repeats"]
	if object(repeats) != nil {
		repeats = at(repeats, "count")
	}
	llm := at(s, "llm")
	if at(src, "isolation", "network_mode") == "none" {
		llm = nil
	}
	path := any(nil)
	hash := any(nil)
	if o.ReportPath != "" {
		p, e := filepath.Abs(o.ReportPath)
		if e != nil {
			return nil, e
		}
		if o.RepoRoot != "" {
			root, e := filepath.Abs(o.RepoRoot)
			if e != nil {
				return nil, e
			}
			if rel, e := filepath.Rel(root, p); e == nil && filepath.IsLocal(rel) {
				p = rel
			}
		}
		path = filepath.ToSlash(p)
		hash = digest(raw)
	}
	notes := o.VariantNotes
	schema := src["suite_schema_version"]
	// No suite version, parallelism, commit, or isolated success is inferred from
	// the current executable, a legacy protocol version, or missing booleans.
	return Card{
		"card_version": 1, "id": src["run_id"],
		"subject": map[string]any{"name": at(s, "name"), "variant": at(s, "variant"), "variant_notes": notes, "upstream": upstream, "declared": at(s, "declared"), "llm": llm, "manifest_sha256": nonempty(at(s, "manifest_sha256")), "env_lock_sha256": nonempty(at(s, "env_lock_sha256")), "image_id": nonempty(at(s, "lock", "image", "id"))},
		"dataset": map[string]any{"suite_id": src["suite_id"], "suite_sha256": src["suite_sha256"], "schema_version": schema, "protocol": datasetProtocol, "cases_planned": at(summary, "planned"), "cases_with_gold": casesWithGold(src["cases"]), "categories": categories, "license": at(src, "source", "license")},
		"run":     map[string]any{"mode": src["mode"], "reset_policy": at(protocol, "reset_policy"), "top_k": at(budget, "top_k"), "max_context_chars": at(budget, "max_context_chars"), "run_seed": src["run_seed"], "limit": at(protocol, "case_limit"), "repeats": repeats, "parallel": src["parallel"], "resume_count": src["resume_count"], "started_at_utc": src["started_at_utc"], "wall_seconds": src["wall_seconds"], "isolation": isolation, "framework_version": src["framework_version"], "memeval_commit": nonempty(src["memeval_commit"])},
		"results": map[string]any{"status_counts": at(summary, "status_counts"), "scoring_coverage": at(summary, "scoring_coverage"), "execution_errors": at(summary, "execution_errors"), "invalid": at(summary, "invalid"), "unsupported": at(summary, "unsupported"), "headline": headline(src, o.Primary), "metrics": at(summary, "metrics"), "scores": uniqueScores(src["scores"], object(at(summary, "metrics")), ""), "by_category": by, "timings": timings},
		"cost": map[string]any{
			"sut":       scalarTotals(at(src, "billing", "sut"), "calls", "prompt_tokens", "cached_tokens", "completion_tokens", "retries", "rate_limited_responses", "reasoning_tokens", "unmetered_calls", "client_disconnects", "circuit_opens", "queue_wait_p95_ms"),
			"evaluator": scalarTotals(at(src, "billing", "evaluator")),
		}, "warnings": src["warnings"], "source": map[string]any{"report_path": path, "report_sha256": hash},
	}, nil
}

// Cards contain top-level numeric totals (including unknown/null totals), never
// per-episode/provider detail. An explicit key list restricts the SUT contract.
func scalarTotals(v any, keys ...string) any {
	m := object(v)
	if m == nil {
		return nil
	}
	if len(keys) == 0 {
		for key := range m {
			keys = append(keys, key)
		}
	}
	out := map[string]any{}
	for _, key := range keys {
		value, exists := m[key]
		if !exists {
			continue
		}
		switch value.(type) {
		case json.Number, nil:
			out[key] = value
		}
	}
	return out
}

// Treat {mean,n} as a metric leaf. Only a leading memory_only component is
// ignored in its dotted id; official and other namespaces remain distinct.
// Build new groups so the report stays intact; JSON sorts their keys on output.
func uniqueScores(v any, metrics map[string]any, path string) any {
	m := object(v)
	if m == nil {
		return v
	}
	if _, mean := m["mean"]; mean {
		if _, n := m["n"]; n {
			return v
		}
	}
	out := map[string]any{}
	for key, child := range m {
		id := key
		if path != "" {
			id = path + "." + key
		}
		if sameMetric(child, metrics[strings.TrimPrefix(id, "memory_only.")]) {
			continue
		}
		kept := uniqueScores(child, metrics, id)
		if group := object(kept); group != nil && len(group) == 0 {
			continue
		}
		out[key] = kept
	}
	return out
}

func sameMetric(a, b any) bool {
	x, y := object(a), object(b)
	if len(x) != 2 || len(y) != 2 {
		return false
	}
	for _, key := range []string{"mean", "n"} {
		av, aok := x[key]
		bv, bok := y[key]
		if !aok || !bok {
			return false
		}
		if av == nil && bv == nil {
			continue
		}
		an, aok := av.(json.Number)
		bn, bok := bv.(json.Number)
		if !aok || !bok {
			return false
		}
		// Compare exact numbers, accepting e.g. 1 and 1.0 without rounding a
		// distinct high-precision mean or count through float64.
		ar, aok := new(big.Rat).SetString(string(an))
		br, bok := new(big.Rat).SetString(string(bn))
		if !aok || !bok || ar.Cmp(br) != 0 {
			return false
		}
	}
	return true
}

// Shared with the browser builder: v2's non-null retrieval recall is the
// evidence-presence indicator. Count report rows, including repeats, verbatim.
func casesWithGold(v any) any {
	rows, ok := v.([]any)
	if !ok {
		return nil
	}
	n := 0
	for _, c := range rows {
		if at(c, "metrics", "recall") != nil {
			n++
		}
	}
	return n
}
func headline(src map[string]any, primary string) any {
	if src["mode"] == "fixed_reader" {
		if primary != "" {
			return []string{primary}
		}
		return nil
	}
	if src["mode"] == "typed_context" || at(src, "protocol", "benchmark", "variant") == "typed_context" {
		return []string{"precision", "recall"}
	}
	metrics := object(at(src, "summary", "metrics"))
	declared, _ := at(src, "protocol", "retrieval_metrics").([]any)
	for _, v := range declared {
		if v == "longmemeval_official" {
			// The protocol's unit and measured keys select the official @k family.
			preferred := fmt.Sprint(at(src, "protocol", "budget", "top_k"))
			for _, k := range []string{preferred, "5", "10", "50"} {
				id := "longmemeval_official.turn.recall_any@" + k
				if _, ok := metrics[id]; ok {
					return []string{id, "longmemeval_official.turn.ndcg_any@" + k}
				}
			}
		}
	}
	if src["mode"] == "retrieval" {
		if _, ok := metrics["recall"]; ok {
			return []string{"recall", "ndcg"}
		}
	}
	return nil
}

func selftestPassed(v any) any {
	if p, ok := at(v, "passed").(bool); ok {
		return p
	}
	checks := object(at(v, "checks"))
	if len(checks) == 0 {
		return nil
	}
	for _, c := range checks {
		p, ok := at(c, "passed").(bool)
		if !ok {
			return nil
		}
		if !p {
			return false
		}
	}
	return true
}
