package sandbox

// CloseReaders is optional for backends that own attached stream descriptors.
// Call it only after stdout/stderr consumers have stopped.
func CloseReaders(p Process) {
	if c, ok := p.(interface{ CloseReaders() }); ok {
		c.CloseReaders()
	}
}
