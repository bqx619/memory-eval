package container

import (
	"context"
	"fmt"
	"io"
	"memeval/internal/procexec"
)

// Image operations share the explicit context, minimal CLI environment and
// credential redactor used by episode operations. Build output is never argv.
func (b *Backend) ImageCommand(ctx context.Context, args ...string) ([]byte, error) {
	return b.call(ctx, args...)
}
func (b *Backend) Build(ctx context.Context, log io.Writer, args ...string) error {
	cmd := b.command(ctx, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := procexec.Run(cmd); err != nil {
		return fmt.Errorf("container image build failed: %w (see build.log)", err)
	}
	return nil
}
func (b *Backend) Platform() string { return b.cfg.Platform }
