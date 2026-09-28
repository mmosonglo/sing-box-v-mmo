package butler

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/common/urltest"
	N "github.com/sagernet/sing/common/network"
)

// StartHealthProbe khởi chạy worker kiểm tra sức khỏe proxy ngầm:
// 1. Chờ tiến trình khởi động hoàn chỉnh và kết nối mạng/VPN thực sự ổn định:
//    - Đợi tối thiểu 15 giây.
//    - Cộng thêm jitter so-le theo PID (pid % 10 giây) để các tiến trình không kiểm tra cùng một lúc.
// 2. Tối ưu Zero-Overhead:
//    - Sử dụng HTTP 204 (Plain HTTP cp.cloudflare.com) không handshake TLS, không payload.
//    - Khi proxy bình thường: kiểm tra thư thả mỗi 60 giây, hoàn toàn ZERO DISK I/O (không ghi đĩa).
//    - Khi proxy chết: kiểm tra lại mỗi 15 giây để khi mạng có lại thì gỡ viền đỏ ngay lập tức.
func StartHealthProbe(ctx context.Context, detour N.Dialer, nodeTag string) {
	if detour == nil {
		return
	}

	go func() {
		pid := os.Getpid()
		clientInfo := ResolvePasswallClientInfo()

		// 1. Đợi tiến trình khởi động hoàn chỉnh, ổn định định tuyến và kết nối
		// Cơ chế Jittering: 15 giây cơ bản + (PID % 10) giây để 20 node tản đều thời gian, không gây CPU Spike
		initialWarmup := 15*time.Second + time.Duration(pid%10)*time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialWarmup):
		}

		isCurrentlyDead := false

		checkOnce := func() {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			latencyMs, err := urltest.URLTest(probeCtx, "http://cp.cloudflare.com/generate_204", detour)
			if err != nil {
				// Proxy chết / Timeout
				isCurrentlyDead = true
				RecordDeadProxyState(pid, clientInfo, nodeTag, true, 0, err.Error())
			} else {
				// Proxy sống bình thường
				isCurrentlyDead = false
				RecordDeadProxyState(pid, clientInfo, nodeTag, false, int(latencyMs), "")
			}
		}

		// Lượt kiểm tra đầu tiên sau khi đã ổn định hoàn toàn
		checkOnce()

		for {
			// Chu kỳ thông minh: nếu đang chết thì check sau 15s để hồi phục nhanh; nếu đang sống tốt thì 60s/lần cực nhẹ
			nextInterval := 60 * time.Second
			if isCurrentlyDead {
				nextInterval = 15 * time.Second
			}

			select {
			case <-ctx.Done():
				RemoveDeadProxyItem(pid)
				return
			case <-time.After(nextInterval):
				checkOnce()
			}
		}
	}()
}
