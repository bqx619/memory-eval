//go:build !darwin && !linux

package container

import "fmt"

func fileOwner(string) (int, int, error) {
	return 0, 0, fmt.Errorf("bind mount owner verification unavailable")
}
