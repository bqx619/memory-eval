package locomo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"memeval/internal/pytext"
	"strings"
	"unicode/utf8"
)

const TemporalSuffix = " Use DATE of CONVERSATION to answer with an approximate date."
const Cat5Suffix = " Select the correct answer: (a) {} (b) {}. "
const NotMentioned = "Not mentioned in the conversation"
const QuestionProtocol = "locomo-official-cat2-cat5-v1"
const OptionRNG = "HMAC-SHA256(run_seed, locomo-cat5-order-v1\\0sample_id) -> ChaCha8.Float64 < 0.5"

type QuestionOptions struct {
	AdversarialAnswer *string
	RunSeed, SampleID string
}

// QuestionAudit is evaluator-only. It is never passed to suite.PublicRequest.
type QuestionAudit struct {
	Protocol      string            `json:"protocol"`
	SuffixApplied bool              `json:"suffix_applied"`
	OptionOrder   []string          `json:"option_order,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
	RawResponse   *string           `json:"raw_response,omitempty"`
	MappedOption  string            `json:"mapped_option,omitempty"`
}

// The per-sample stream is independent of traversal order, filtering and limits.
// It preserves upstream's uniform < 0.5 branch, not Python's PRNG bit sequence.
func cat5Draw(seed, sample string) (float64, error) {
	key, err := hex.DecodeString(seed)
	if err != nil || len(key) != 32 || sample == "" {
		return 0, fmt.Errorf("LoCoMo cat5 requires run_seed (32-byte hex) and sample_id")
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("locomo-cat5-order-v1\x00" + sample))
	var stream [32]byte
	copy(stream[:], h.Sum(nil))
	return rand.New(rand.NewChaCha8(stream)).Float64(), nil
}

func prepareQuestion(question string, category int, opt QuestionOptions) (string, QuestionAudit, []string, error) {
	audit := QuestionAudit{Protocol: QuestionProtocol}
	switch category {
	case 2:
		audit.SuffixApplied = true
		question += TemporalSuffix
	case 5:
		if opt.AdversarialAnswer == nil {
			return question, audit, []string{"LoCoMo cat5 private adversarial answer unavailable: official suffix and letter mapping omitted; fidelity partial; reimport with converter v2"}, nil
		}
		draw, err := cat5Draw(opt.RunSeed, opt.SampleID)
		if err != nil {
			return "", audit, nil, err
		}
		a, b := NotMentioned, *opt.AdversarialAnswer
		audit.OptionOrder = []string{"not_mentioned", "adversarial_answer"}
		if draw >= 0.5 {
			a, b = b, a
			audit.OptionOrder[0], audit.OptionOrder[1] = audit.OptionOrder[1], audit.OptionOrder[0]
		}
		audit.Options = map[string]string{"a": a, "b": b}
		audit.SuffixApplied = true
		parts := strings.Split(Cat5Suffix, "{}")
		question += parts[0] + a + parts[1] + b + parts[2]
	}
	return question, audit, nil, nil
}

// ProcessAnswer mirrors gpt_utils.get_cat_5_answer followed by the single-QA
// caller's strip(). In particular c/xyz map to b: this upstream behavior is
// intentional parity, not a stricter choice parser. No choices means legacy QA.
func ProcessAnswer(raw string, options map[string]string) (string, string) {
	if len(options) != 2 {
		return pytext.Strip(raw), ""
	}
	p := pytext.Lower(pytext.Strip(raw))
	key := ""
	switch utf8.RuneCountInString(p) {
	case 1:
		key = "b"
		if strings.Contains(p, "a") {
			key = "a"
		}
	case 3:
		key = "b"
		if strings.Contains(p, "(a)") {
			key = "a"
		}
	}
	if key != "" {
		return pytext.Strip(options[key]), key
	}
	return p, ""
}
