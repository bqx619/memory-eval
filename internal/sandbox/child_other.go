//go:build !darwin && !linux

package sandbox

import (
	"context"
	"fmt"
)

func start(context.Context, Spec, string, []string) (Process, error) {
	return nil, fmt.Errorf("unsupported OS")
}
func Alive(int) bool { return false }
