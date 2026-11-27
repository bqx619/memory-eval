package jsonrpc

import "strings"

// CredentialMatch describes the same heuristic credential spans used by Redact.
// Streaming diagnostic writers use these boundaries without duplicating its regex.
type CredentialMatch struct {
	Start, End int
	Kind       string
}

func CredentialMatches(s string) []CredentialMatch {
	var matches []CredentialMatch
	for _, span := range tokenPattern.FindAllStringIndex(s, -1) {
		kind := "assignment"
		value := strings.ToLower(s[span[0]:span[1]])
		if strings.HasPrefix(value, "bearer") {
			kind = "bearer"
		}
		if strings.HasPrefix(value, "sk-") {
			kind = "sk"
		}
		matches = append(matches, CredentialMatch{span[0], span[1], kind})
	}
	return matches
}

// CredentialContinues matches the corresponding value character class in Redact.
func CredentialContinues(kind string, c byte) bool {
	if kind == "sk" {
		return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
	}
	if c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == '"' || c == '\'' || c == '\\' {
		return false
	}
	return c != ',' && c != ';'
}
