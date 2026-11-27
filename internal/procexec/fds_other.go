//go:build !darwin && !linux

package procexec

import "fmt"

const Policy = "unsupported"

func cloexecAll() error               { return fmt.Errorf("fd hygiene: unsupported OS") }
func Snapshot() ([]Descriptor, error) { return nil, cloexecAll() }
