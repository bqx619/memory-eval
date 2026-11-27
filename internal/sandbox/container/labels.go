package container

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
)

func nonce() string {
	var v [8]byte
	if _, e := rand.Read(v[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(v[:])
}
func (b *Backend) labels(ep, env, role string) map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if env == "" {
		env = "probe"
	}
	return map[string]string{"memeval.run": b.runID, "memeval.episode": ep, "memeval.env": env, "memeval.role": role, "memeval.owner_pid": strconv.Itoa(os.Getpid()), "memeval.owner_start": b.ownerStart}
}
func labelArgs(m map[string]string) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a := []string{}
	for _, k := range keys {
		a = append(a, "--label", k+"="+m[k])
	}
	return a
}
