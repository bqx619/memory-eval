package selftest

import "memeval/internal/procexec"

func openFDs() ([]int, []procexec.Descriptor, error) {
	all, err := procexec.Snapshot()
	fds := []int{}
	for _, fd := range all {
		// FD_CLOEXEC descriptors cannot survive exec. Go can create its own
		// poller before main (KQUEUE on Darwin, epoll/eventfd on Linux).
		// Keep every observation in the report; never exempt an fd by number
		// or type, and never sanitize in the child before this snapshot.
		if !fd.CloseOnExec {
			fds = append(fds, fd.FD)
		}
	}
	return fds, all, err
}
