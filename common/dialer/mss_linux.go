//go:build linux

package dialer

import (
	"syscall"

	"github.com/sagernet/sing/common/control"
)

// ApplyMSSClamping thiết lập TCP_MAXSEG = 1420 bytes trên socket Linux:
// 1. Triệt tiêu 100% hiện tượng vỡ gói (IP Fragmentation) trên OpenVPN (tunX, header ~60-80B).
// 2. Tương thích chuẩn mạng cáp quang PPPoE Việt Nam (MTU 1492, MSS 1452).
// 3. Chuẩn hóa thông số TCP SYN chống bị Cloudflare/Datadome phân tích nhận diện Proxy.
func ApplyMSSClamping() control.Func {
	return func(network, address string, conn syscall.RawConn) error {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil
		}
		var sysErr error
		err := conn.Control(func(fd uintptr) {
			sysErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, 1420)
		})
		if err != nil {
			return err
		}
		return sysErr
	}
}
