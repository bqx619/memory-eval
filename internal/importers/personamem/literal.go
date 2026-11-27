package personamem

import "memeval/internal/pytext"

// ParseOptions accepts only a list of string literals. It never executes Python.
func ParseOptions(s string) ([]string, error) { return pytext.StringList(s) }
