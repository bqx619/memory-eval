// Package governor implements the run-scoped, single-target SUT HTTP gateway.
// It never records request/response bodies, credentials, or URL query strings.
package governor

import (
	"context"
	"time"
)

const BodyLimit = 16 << 20
const MeterFile = "attempts.jsonl"

type Config struct {
	ConsumedTokens            int64 // Observed spend retained from earlier committed generations.
	Target                    string
	Concurrency, RPM          int
	ConcurrencySource         string // Host disclosure: flag, env, manifest, or default.
	ConcurrencyPerEpisode     int    // Declared/default fan-out, before a global override.
	Parallel                  int    // Configured episode parallelism, not active worker count.
	BudgetTokens              int64
	UpstreamTimeout, MaxPause time.Duration
	MeterDir                  string
}

func Defaults() Config {
	return Config{Concurrency: 8, ConcurrencySource: "default", ConcurrencyPerEpisode: 8, Parallel: 1, UpstreamTimeout: 180 * time.Second, MaxPause: 30 * time.Minute}
}

// Hooks are for deterministic tests. Production uses real time and full jitter.
type Hooks struct {
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
	Jitter func(time.Duration) time.Duration
}
type Usage struct {
	Prompt        int64 `json:"prompt_tokens"`
	Completion    int64 `json:"completion_tokens"`
	Total         int64 `json:"total_tokens"`
	PromptDetails struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		Reasoning int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}
type Attempt struct {
	Time        string  `json:"t"`
	MS          float64 `json:"ms"`
	EpisodeIP   string  `json:"episode_ip"`
	Path        string  `json:"path"`
	Model       string  `json:"model"`
	Status      int     `json:"status"`
	Attempt     int     `json:"attempt"`
	Prompt      *int64  `json:"prompt_tokens"`
	Cached      *int64  `json:"cached_tokens"`
	Completion  *int64  `json:"completion_tokens"`
	Reasoning   *int64  `json:"reasoning_tokens"`
	Usage       *Usage  `json:"usage"`
	RetryAfter  float64 `json:"retry_after"`
	QueueWaitMS float64 `json:"queue_wait_ms"`
	Error       string  `json:"error,omitempty"`
	// Absence preserves the interpretation of historical, upstream-only rows.
	NotForwarded bool `json:"not_forwarded,omitempty"`
	halfProbe    bool
}
type CircuitEvent struct {
	At       string  `json:"at"`
	State    string  `json:"state"`
	Reason   string  `json:"reason"`
	Cooldown float64 `json:"cooldown_s"`
}
type EpisodeBilling struct {
	NS         string `json:"ns"`
	Calls      int64  `json:"calls"`
	Prompt     int64  `json:"prompt_tokens"`
	Completion int64  `json:"completion_tokens"`
}
type Billing struct {
	Calls             int64            `json:"calls"`
	Prompt            int64            `json:"prompt_tokens"`
	Cached            int64            `json:"cached_tokens"`
	Completion        int64            `json:"completion_tokens"`
	Reasoning         int64            `json:"reasoning_tokens"`
	Retries           int64            `json:"retries"`
	RateLimited       int64            `json:"rate_limited_responses"`
	CircuitOpens      int64            `json:"circuit_opens"`
	Unmetered         int64            `json:"unmetered_calls"`
	ClientDisconnects int64            `json:"client_disconnects"`
	QueueP95          float64          `json:"queue_wait_p95_ms"`
	ByEpisode         []EpisodeBilling `json:"by_episode"`
}
type Status struct {
	State      string         `json:"state"`
	Reason     string         `json:"reason"`
	RetryAfter float64        `json:"retry_after_s"`
	Billing    Billing        `json:"billing"`
	Events     []CircuitEvent `json:"circuit_events"`
}

func (s Status) Stopped() bool { return s.State == "exhausted" || s.State == "budget" }

type Attribution struct {
	IP, NS, Container string
	From, Until       time.Time
}

const clientDisconnected = "client disconnected"

// add is shared by live status and ledger replay, including older rows that
// predate not_forwarded. A disconnect can carry already-observed usage.
func (b *Billing) add(a Attempt) {
	if !a.NotForwarded {
		b.Calls++
	}
	disconnected := a.Error == clientDisconnected
	if disconnected {
		b.ClientDisconnects++
	}
	if !a.NotForwarded && !disconnected {
		if a.Attempt > 1 {
			b.Retries++
		}
		if a.Status == 429 {
			b.RateLimited++
		}
		if a.Usage == nil {
			b.Unmetered++
		}
	}
	if a.Usage != nil {
		b.Prompt += a.Usage.Prompt
		b.Cached += a.Usage.PromptDetails.Cached
		b.Completion += a.Usage.Completion
		b.Reasoning += a.Usage.CompletionDetails.Reasoning
	}
}
