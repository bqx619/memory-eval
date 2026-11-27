package reader

import "memeval/internal/llm"

// EmptyResponseError is terminal for a reader attempt, not a transport retry.
// The runner excludes this answer from scoring while retaining response usage.
type EmptyResponseError struct{ Response llm.Response }

func (e *EmptyResponseError) Error() string { return "reader returned no content" }
