//go:build !linux

package dialer

import (
	"syscall"

	"github.com/sagernet/sing/common/control"
)

func ApplyMSSClamping() control.Func {
	return func(network, address string, conn syscall.RawConn) error {
		return nil
	}
}
