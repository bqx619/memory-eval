package longmemeval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const OfficialModel = "gpt-4o-2024-08-06"

func Templates() []string { return []string{General, Temporal, Update, Preference, Abstention} }
func TemplateSHA256() string {
	b, _ := json.Marshal(Templates())
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Template(task string, abstention bool) (string, error) {
	template := ""
	if abstention {
		template = Abstention
	} else {
		switch task {
		case "single-session-user", "single-session-assistant", "multi-session":
			template = General
		case "temporal-reasoning":
			template = Temporal
		case "knowledge-update":
			template = Update
		case "single-session-preference":
			template = Preference
		default:
			return "", fmt.Errorf("unsupported LongMemEval question type")
		}
	}
	return template, nil
}
