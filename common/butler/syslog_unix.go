//go:build !windows

package butler

import (
	"fmt"
	"net"
	"time"
)

// logToSyslog: Ghi nhật ký trực tiếp vào hệ thống OpenWrt (/dev/log) với định danh v-mmo
func logToSyslog(msg string) {
	conn, err := net.Dial("unixgram", "/dev/log")
	if err != nil {
		conn, err = net.Dial("unixgram", "/var/run/log")
		if err != nil {
			return
		}
	}
	defer conn.Close()

	// Thiết lập Write Deadline 50ms ngăn chặn treo toàn bộ hệ thống
	_ = conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))

	// Facility DAEMON (3 * 8 = 24), Severity INFO (6) => PRI = 30
	line := fmt.Sprintf("<30>v-mmo: %s\n", msg)
	_, _ = conn.Write([]byte(line))
}
