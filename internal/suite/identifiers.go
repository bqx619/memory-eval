package suite

// identifierMatcher preserves literal substring matching (including UTF-8),
// sharing identifier prefixes instead of scanning every identifier per field.
// It is rebuilt for each episode and holds no event content.
type identifierMatcher struct {
	next map[byte]*identifierMatcher
	end  bool
}

func newIdentifierMatcher(values map[string]bool) *identifierMatcher {
	root := &identifierMatcher{}
	for value := range values {
		if value == "" {
			continue
		}
		n := root
		for i := 0; i < len(value); i++ {
			if n.next == nil {
				n.next = make(map[byte]*identifierMatcher)
			}
			if n.next[value[i]] == nil {
				n.next[value[i]] = &identifierMatcher{}
			}
			n = n.next[value[i]]
		}
		n.end = true
	}
	return root
}

func (root *identifierMatcher) contains(value string) bool {
	for i := 0; i < len(value); i++ {
		n := root
		for j := i; j < len(value); j++ {
			n = n.next[value[j]]
			if n == nil {
				break
			}
			if n.end {
				return true
			}
		}
	}
	return false
}
