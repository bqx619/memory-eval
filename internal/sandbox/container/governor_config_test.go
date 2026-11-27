package container

import (
	"memeval/internal/config/dotenv"
	"strconv"
	"testing"
)

func TestGovernorConcurrencyPrecedenceAndDisclosure(t *testing.T) {
	for _, key := range []string{"MEMEVAL_SUT_LLM_RPM", "MEMEVAL_SUT_LLM_BUDGET_TOKENS", "MEMEVAL_SUT_LLM_MAX_PAUSE"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name, value, source string
		declared            *int
		want                int
		wantSource          string
	}{
		{"default", "", "", nil, 8, "default"},
		{"manifest", "", "", new(32), 32, "manifest"},
		{"exported-default", "8", "default", new(32), 32, "manifest"},
		{"exported-default-no-declaration", "8", "default", nil, 8, "default"},
		{"flag-over-manifest", "16", "flag", new(32), 16, "flag"},
		{"explicit-default-flag", "8", "flag", new(32), 8, "flag"},
		{"env-over-manifest", "12", "env", new(32), 12, "env"},
		{"explicit-default-env", "8", "env", new(32), 8, "env"},
		{"file-over-manifest", "24", ".env", new(32), 24, "env"},
		{"process-env-without-settings", "10", "", new(32), 10, "env"},
	} {
		for _, parallel := range []int{1, 4, 16} {
			t.Run(tc.name+"/parallel-"+strconv.Itoa(parallel), func(t *testing.T) {
				t.Setenv("MEMEVAL_SUT_LLM_CONCURRENCY", tc.value)
				ctx := t.Context()
				if tc.source != "" {
					ctx = dotenv.With(ctx, &dotenv.Settings{Sources: map[string]string{"MEMEVAL_SUT_LLM_CONCURRENCY": tc.source}})
				}
				want, perEpisode := tc.want, 8
				if tc.declared != nil {
					perEpisode = *tc.declared
				}
				if tc.wantSource == "manifest" || tc.wantSource == "default" {
					want *= parallel
				}
				c, err := GovernorConfig(ctx, tc.declared, parallel)
				if err != nil || c.Concurrency != want || c.ConcurrencySource != tc.wantSource || c.ConcurrencyPerEpisode != perEpisode || c.Parallel != parallel {
					t.Fatal(c, err)
				}
				b := New(Config{})
				if err = b.ConfigureGovernor(t.TempDir(), c); err != nil {
					t.Fatal(err)
				}
				b.gov = &runGovernor{dir: t.TempDir(), episode: &episode{config: runConfig{Command: []string{"/memeval", "__container-governor", "--listen", "0.0.0.0:8317"}}}}
				d := b.governorDisclosure()
				if d["concurrency"] != want || d["concurrency_source"] != tc.wantSource || d["concurrency_per_episode"] != perEpisode || d["parallel"] != parallel {
					t.Fatal("incorrect disclosure", d)
				}
			})
		}
	}
}

func TestGovernorConcurrencyScalingValidation(t *testing.T) {
	for _, key := range []string{"MEMEVAL_SUT_LLM_CONCURRENCY", "MEMEVAL_SUT_LLM_RPM", "MEMEVAL_SUT_LLM_BUDGET_TOKENS", "MEMEVAL_SUT_LLM_MAX_PAUSE"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name     string
		declared *int
		parallel int
	}{
		{"zero parallel", nil, 0}, {"negative parallel", nil, -1}, {"too many workers", nil, 17},
		{"zero fanout", new(0), 4}, {"negative fanout", new(-1), 4},
		{"overflow", new(int(^uint(0) >> 1)), 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := GovernorConfig(t.Context(), tc.declared, tc.parallel); err == nil {
				t.Fatal("invalid scaled concurrency accepted")
			}
		})
	}
}
