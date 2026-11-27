//go:build !linux

package container

import (
	"fmt"
	"io"
)

func Probe([]string, io.Writer) error { return fmt.Errorf("container probe: linux only") }
