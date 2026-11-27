package procexec

import "fmt"

// Match os/exec.Output's bounded first/last 32 KiB stderr capture.
type errorOutput struct {
	first, last []byte
	skipped     int64
}

func (w *errorOutput) Write(p []byte) (int, error) {
	n := len(p)
	const cap = 32 << 10
	keep := min(len(p), cap-len(w.first))
	w.first = append(w.first, p[:keep]...)
	p = p[keep:]
	if len(p) >= cap {
		w.skipped += int64(len(w.last) + len(p) - cap)
		w.last = append(w.last[:0], p[len(p)-cap:]...)
	} else {
		drop := max(0, len(w.last)+len(p)-cap)
		w.skipped += int64(drop)
		w.last = append(w.last[:copy(w.last, w.last[drop:])], p...)
	}
	return n, nil
}

func (w *errorOutput) Bytes() []byte {
	b := append([]byte(nil), w.first...)
	if w.skipped != 0 {
		b = fmt.Appendf(b, "\n... omitting %d bytes ...\n", w.skipped)
	}
	return append(b, w.last...)
}
