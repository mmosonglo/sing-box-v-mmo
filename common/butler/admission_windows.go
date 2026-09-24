//go:build windows

package butler

import (
	"context"
)

// AcquireStartupGate: No-op trên môi trường Windows
func AcquireStartupGate(ctx context.Context) (func(), error) {
	return func() {}, nil
}
