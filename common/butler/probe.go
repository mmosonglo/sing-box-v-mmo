package butler

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/common/urltest"
	N "github.com/sagernet/sing/common/network"
)

// StartHealthProbe khởi chạy worker kiểm tra sức khỏe proxy ngầm:
// - Cực nhẹ: sử dụng HTTP 204 (không giải mã payload, không tải file).
// - Chu kỳ: 30 giây / lần (timeout 3 giây).
// - Nếu chết: ghi nhận vào danh sách dead proxy qua RecordDeadProxyState để báo lên LuCI đổi viền đỏ.
// - Nếu sống lại: tự động gỡ bỏ khỏi danh sách.
func StartHealthProbe(ctx context.Context, detour N.Dialer, nodeTag string) {
	if detour == nil {
		return
	}

	go func() {
		pid := os.Getpid()
		clientInfo := ResolvePasswallClientInfo()

		// Đợi 5 giây đầu sau khi khởi động để kết nối VPN/proxy ổn định
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Kiểm tra lần đầu
		checkOnce := func() {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			latencyMs, err := urltest.URLTest(probeCtx, "http://cp.cloudflare.com/generate_204", detour)
			if err != nil {
				// Proxy chết / Timeout
				RecordDeadProxyState(pid, clientInfo, nodeTag, true, 0, err.Error())
			} else {
				// Proxy sống bình thường
				RecordDeadProxyState(pid, clientInfo, nodeTag, false, int(latencyMs), "")
			}
		}

		checkOnce()

		for {
			select {
			case <-ctx.Done():
				RemoveDeadProxyItem(pid)
				return
			case <-ticker.C:
				checkOnce()
			}
		}
	}()
}
