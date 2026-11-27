package octop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/config/dotenv"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/sandbox/container"
	"memeval/internal/scoring"
	"memeval/internal/suite"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// imagePrerequisites never builds an image. Each missing prerequisite is a skip
// reason; protocol violations after all three pass remain test failures.
func imagePrerequisites(ctx context.Context, enabled string, version func(context.Context) error, installed func() error) error {
	if enabled != "1" {
		return fmt.Errorf("MEMEVAL_OCTOP_IMAGE_TEST=1 required (container contracts default off)")
	}
	if err := version(ctx); err != nil {
		return fmt.Errorf("Docker context unavailable: %w", err)
	}
	if err := installed(); err != nil {
		return fmt.Errorf("octop-memory environment not installed or CheckEnv failed; run memeval env install suts/octop-memory/manifest.json: %w", err)
	}
	return nil
}

func installed(t *testing.T) *Adapter {
	t.Helper()
	var m *sut.Manifest
	var l *sut.Lock
	var b *container.Backend
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err := imagePrerequisites(ctx, dotenv.Value("MEMEVAL_OCTOP_IMAGE_TEST"), func(ctx context.Context) error {
		cfg := container.DefaultConfig()
		b = container.New(cfg)
		_, err := b.ImageCommand(ctx, "version", "--format", "json")
		if err != nil {
			return fmt.Errorf("context %q: %w", cfg.Context, err)
		}
		return nil
	}, func() (err error) {
		m, err = sut.Load("../../../suts/octop-memory/manifest.json")
		if err == nil {
			l, err = sut.CheckLock(m) // Includes CheckEnv: lock, inventory and installed image.
		}
		if err == nil {
			// Capabilities uses the probe image too. Require the install/caps
			// prerequisite instead of letting this test build a missing image.
			var sha string
			sha, err = b.CurrentProbeSHA()
			if err == nil {
				_, err = b.ImageCommand(ctx, "image", "inspect", container.DefaultConfig().ProbeRepository+":"+sha[:12])
				if err != nil {
					err = fmt.Errorf("installed probe image missing; run memeval sandbox caps before integration tests: %w", err)
				}
			}
		}
		return err
	})
	if err != nil {
		t.Skip(err)
	}
	a := New(&sut.Adapter{Manifest: m, Lock: l, Backend: b})
	t.Cleanup(func() {
		if err := errors.Join(a.Close(context.Background()), b.FinishRun(context.Background())); err != nil {
			t.Error(err)
		}
	})
	if err = a.Reset(t.Context(), "contract"); err != nil {
		t.Fatal(err)
	}
	return a
}

// Candidate/page fixtures use the installed SDK only inside the running SUT
// container, against its episode database. No host Python or SDK is executed.
func seedContainer(t *testing.T, a *Adapter, ns, kind string) {
	t.Helper()
	code, err := os.ReadFile("testdata/seed.py")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := a.Handle.EffectiveConfig()["container_name"].(string)
	backend, ok := a.Backend.(*container.Backend)
	if !ok || name == "" {
		t.Fatal("fixture seeding requires a running container")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if output, err := backend.ImageCommand(ctx, "exec", name, filepath.Join(a.Lock.VenvRealPath, "bin/python"), "-I", "-c", string(code), filepath.Join(a.Handle.Dir(), "work/memory.sqlite"), ns, kind); err != nil {
		t.Fatalf("container seed: %s %v", output, err)
	}
}

func events() []protocol.PublicEvent {
	return []protocol.PublicEvent{
		{ID: "evt-a", Sequence: 1, Role: "user", Content: "quartzviolet telescope uses a cobalt lens", Timestamp: "2023-05-08T13:56:00Z", SessionID: "session-contract"},
		{ID: "evt-b", Sequence: 2, Role: "assistant", Content: "amberfalcon observatory opens on Tuesday", Timestamp: "2023-05-08T13:57:00Z", SessionID: "session-contract"},
		{ID: "evt-c", Sequence: 3, Role: "fact", Content: "ok", Timestamp: "2023-05-08T13:58:00Z", SessionID: "session-contract"},
	}
}
func ingest(t *testing.T, a *Adapter) {
	t.Helper()
	h, err := a.Ingest(context.Background(), "contract", events())
	if err != nil || h.Accepted != 3 || h.Filtered != 0 {
		t.Fatal(h, err)
	}
	r, err := a.Ready(context.Background(), "contract", h, time.Time{})
	if err != nil || !r.Visible {
		t.Fatal(r, err)
	}
}
func query(t *testing.T, a *Adapter, q string) protocol.RetrieveResponse {
	t.Helper()
	r, err := a.Retrieve(context.Background(), "contract", protocol.RetrieveRequest{Query: q, Budget: protocol.Budget{TopK: 10, MaxContextChars: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func seed(t *testing.T, a *Adapter, kind string) {
	t.Helper()
	seedContainer(t, a, "contract", kind)
	if kind != "page" {
		var p struct {
			Promoted int `json:"promoted"`
		}
		if err := a.Call(context.Background(), "promote_candidate", map[string]any{"candidate_id": "candidate-contract"}, &p); err != nil || p.Promoted != 1 {
			t.Fatal(p, err)
		}
	}
}
func known() map[string]suite.Event {
	r := map[string]suite.Event{}
	for _, e := range events() {
		r[e.ID] = suite.Event{SourceID: e.ID}
	}
	return r
}
func TestBridgeRawFullTextAndTime(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	d, err := a.CallDocument(context.Background(), "raw/2023-05-08/evt-a.md")
	if err != nil {
		t.Fatal(err)
	}
	r := query(t, a, "quartzviolet")
	if len(r.Items) != 1 || !reflect.DeepEqual(r.Items[0].SourceIDs, []string{"evt-a"}) || r.Items[0].Text != d || !strings.Contains(d, "timestamp: 2023-05-08") {
		t.Fatalf("raw mismatch: %+v %q", r, d)
	}
	if _, _, err = runner.ValidateResponse(r, known(), protocol.Budget{TopK: 10, MaxContextChars: 100000}, false); err != nil {
		t.Fatal(err)
	}
}

// Test helper deliberately uses the same real memory_get RPC as the adapter.
func (a *Adapter) CallDocument(ctx context.Context, path string) (string, error) {
	var d document
	err := a.Call(ctx, "memory_get", map[string]any{"path": path}, &d)
	if d.Excerpt == nil {
		return "", err
	}
	return *d.Excerpt, err
}
func TestBridgeAtomAndMergedProvenance(t *testing.T) {
	for _, kind := range []string{"atom", "merged"} {
		t.Run(kind, func(t *testing.T) {
			a := installed(t)
			ingest(t, a)
			seed(t, a, kind)
			r := query(t, a, "quartzviolet")
			found := false
			for _, item := range r.Items {
				if item.ID == "evt-a" {
					continue
				}
				found = true
				want := []string{"evt-a"}
				if kind == "merged" {
					want = append(want, "evt-b")
				}
				if !reflect.DeepEqual(item.SourceIDs, want) {
					t.Fatalf("atom provenance: %+v", item)
				}
			}
			if !found {
				t.Fatal("no actual atom hit")
			}
			if _, _, err := runner.ValidateResponse(r, known(), protocol.Budget{TopK: 10, MaxContextChars: 100000}, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestBridgePageHeadlineUnsupported(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	seed(t, a, "page")
	var search map[string]any
	if err := a.Call(context.Background(), "memory_search", map[string]any{"query": "Zirconia", "maxResults": 10, "corpus": "memory"}, &search); err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(search)
	t.Logf("real page search: %s", wire)
	r := query(t, a, "Zirconia")
	found := false
	for _, item := range r.Items {
		if item.ID == "page-contract" {
			found = true
			if item.Status != "unsupported_item" || len(item.SourceIDs) != 0 {
				t.Fatal(item)
			}
		}
	}
	if !found || a.Diagnostics()["untraceable_hits"] < 1 {
		t.Fatal("page headline fixture did not exercise untraceable hit", r)
	}
	if _, _, err := runner.ValidateResponse(r, known(), protocol.Budget{TopK: 10, MaxContextChars: 100000}, false); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatal(err)
	}
}
func TestBridgeEmptyExpected(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	r := query(t, a, "zzqxywunmatchedtoken")
	if r.Items == nil || len(r.Items) != 0 {
		t.Fatal(r)
	}
	m, err := scoring.Retrieval([]string{}, []string{}, true, 10)
	if err != nil || m.EmptyExpected == nil || !*m.EmptyExpected || *m.Values["empty_result_correct"] != 1 {
		t.Fatal(m, err)
	}
}
func TestBridgeSkippedCaptureAndVisibility(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	h, err := a.Ingest(context.Background(), "contract", []protocol.PublicEvent{{ID: "echo", Content: "## Memory Recall injected", Role: "user"}})
	var failure *adapter.IngestError
	if !errors.As(err, &failure) || h.Filtered != 1 || a.Diagnostics()["skipped_recall_marker"] != 1 {
		t.Fatal(h, err, a.Diagnostics())
	}
	// A manual RPC write not accepted by this adapter must fail the ready barrier.
	if err = a.Call(context.Background(), "capture", map[string]any{"events": []map[string]any{{"id": "foreign", "event_type": "user_message", "content": "ok"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ready(context.Background(), "contract", h, time.Time{}); !errors.As(err, &failure) {
		t.Fatal(err)
	}
}
func TestBridgeResetAndGlobalPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stamp := func() map[string]int64 {
		m := map[string]int64{}
		for _, d := range []string{".octopmemory", ".octop-memory"} {
			root := filepath.Join(home, d)
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				m[path] = info.ModTime().UnixNano()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return m
	}
	before := stamp()
	a := installed(t)
	ingest(t, a)
	old := a.Handle.Dir()
	if err = a.Reset(context.Background(), "contract"); err != nil {
		t.Fatal(err)
	}
	if a.Handle.Dir() == old {
		t.Fatal("Reset reused episode")
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old episode retained", err)
	}
	if len(query(t, a, "quartzviolet").Items) != 0 {
		t.Fatal("state leaked across Reset")
	}
	if err = a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, stamp()) {
		t.Fatal("real global paths changed")
	}
}

func TestBridgeForeignCitationIsContractError(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	// create_atom creates a NEW manual raw event in 1.0.1; it is not in the
	// evaluator's accepted prefix and must never become a fabricated citation.
	if err := a.Call(context.Background(), "create_atom", map[string]any{"assertion": "foreignnebula calibration", "entity_name": "Foreign"}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := a.Retrieve(context.Background(), "contract", protocol.RetrieveRequest{Query: "foreignnebula", Budget: protocol.Budget{TopK: 10, MaxContextChars: 10000}})
	var contract *adapter.ContractError
	if !errors.As(err, &contract) {
		t.Fatal("foreign citation accepted", err)
	}
}

type seededRunner struct {
	*Adapter
	t    *testing.T
	page bool
}

func (a *seededRunner) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	h, err := a.Adapter.Ingest(ctx, ns, events)
	if err == nil && a.page {
		seedContainer(a.t, a.Adapter, ns, "page")
	}
	return h, err
}
func TestBridgeRunnerFailureAccounting(t *testing.T) {
	for _, page := range []bool{false, true} {
		t.Run(map[bool]string{false: "ingest_failed", true: "unsupported_page"}[page], func(t *testing.T) {
			a := installed(t)
			content := "## Memory Recall echo"
			if page {
				content = "source text"
			}
			s := &suite.Suite{SchemaVersion: "2.0", SuiteID: "contract", Provenance: map[string]any{"source_url": "test://contract"}, Episodes: []suite.Episode{{ID: "ep", Events: []suite.Event{{SourceID: "source", Owner: "user", Role: "user", Content: content}}, Cases: []suite.Case{{ID: "case", Kind: "retrieval", User: "user", Query: "Zirconia", Cutoff: 1, Gold: suite.Gold{EvidenceIDs: []string{"source"}, Exhaustive: true}}}}}}
			r, err := runner.Run(context.Background(), s, &seededRunner{Adapter: a, t: t, page: page}, runner.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if page {
				if r.Cases[0].Status != result.Unsupported || r.Summary.Unsupported != 1 || r.Summary.UntraceableHits != 1 {
					t.Fatal(r.Cases, r.Summary)
				}
			} else {
				if r.Cases[0].Status != result.IngestFailed || r.Summary.ExecutionErrors != 1 || r.Cases[0].Diagnostics["skipped_recall_marker"] != 1 {
					t.Fatal(r.Cases, r.Summary)
				}
			}
		})
	}
}

func TestBlankQuery(t *testing.T) {
	a := installed(t)
	ctx := context.Background()
	if err := a.Reset(ctx, "blank-query"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", " \n\t"} {
		r, err := a.Retrieve(ctx, "blank-query", protocol.RetrieveRequest{Query: query, Budget: protocol.Budget{TopK: 10, MaxContextChars: 10000}})
		if err != nil || r.Items == nil || len(r.Items) != 0 {
			t.Fatal(r, err)
		}
	}
}
