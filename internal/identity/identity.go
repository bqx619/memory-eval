package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Identity struct {
	seed   [32]byte
	sample string
}

func Seed(value string) (string, error) {
	if value == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("run-seed must be 32 bytes encoded as 64 hex characters")
	}
	return hex.EncodeToString(b), nil
}
func New(seed, sample string) (Identity, error) {
	s, err := Seed(seed)
	if err != nil {
		return Identity{}, err
	}
	b, _ := hex.DecodeString(s)
	x := Identity{sample: sample}
	copy(x.seed[:], b)
	return x, nil
}
func SampleID(caseID string, repeat int) string { return fmt.Sprintf("%s#%d", caseID, repeat) }
func (x Identity) Anon(kind, original string) string {
	h := hmac.New(sha256.New, x.seed[:])
	h.Write([]byte(x.sample + "\x00" + kind + "\x00" + original))
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func (x Identity) Namespace() string { return "paeval_" + x.Anon("namespace", "") }
