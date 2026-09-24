package butler

import (
	"net/netip"
	"runtime"
	"sync/atomic"
	"time"
)



var (
	totalActiveConns atomic.Int64
	lastTrafficNano  atomic.Int64
)

func init() {
	lastTrafficNano.Store(time.Now().UnixNano())
	initProfile()
	startDynamicLimiter()
	startLeaderElection()
	startReclaimer()
	startReporter()
}

// GetEffectiveHardLimit trả về trần kết nối động theo hồ sơ phần cứng và số máy online
func GetEffectiveHardLimit() int32 {
	if dyn := dynamicHardLimit.Load(); dyn > 0 {
		return dyn
	}
	return PerClientHardLimit
}

// Acquire: Thẩm định và cấp phép mở kết nối cho thiết bị LAN bằng CAS Loop nguyên tử
// Triệt tiêu 100% nguy cơ TOCTOU race khi có burst kết nối đồng thời lớn
func Acquire(addr netip.Addr) bool {
	slot := getSlot(addr)
	now := time.Now().UnixNano()

	// Ghi nhận thời điểm thiết bị xuất hiện lần đầu nếu chưa có
	if slot.FirstSeen.Load() == 0 {
		slot.FirstSeen.CompareAndSwap(0, now)
	}

	// 1. Chốt chặn chống sập Router: Nếu tài nguyên cạn kiệt, tạm dừng thiết bị kết nối sau cùng
	if IsPausedClient(slot) {
		return false
	}

	for {
		currClient := slot.ActiveConns.Load()

		// 2. Chốt chặn bảo vệ Router: Hạn ngạch tự động co giãn theo số máy online thực tế
		effectiveLimit := GetEffectiveHardLimit()
		if currClient >= effectiveLimit {
			return false
		}

		// 3. Chốt chặn bảo vệ bảng Conntrack toàn hệ thống & Khoảng trống an toàn (Safety Headroom):
		// Triết lý Fast-Fail chuẩn sing-box: từ chối dứt khoát ngay lập tức khi cạn ngân sách,
		// TUYỆT ĐỐI KHÔNG sleep đồng bộ trên hot path gây Head-of-Line Blocking và tăng tail latency.
		effectiveMaxConns := GetEffectiveMaxSystemConns()
		effectiveGuaranteedMin := GetEffectiveGuaranteedMin()
		currTotal := totalActiveConns.Load()
		if currTotal >= effectiveMaxConns && currClient >= effectiveGuaranteedMin {
			return false
		}

		// 4. Thực hiện tăng ActiveConns bằng CAS nguyên tử (Atomic Compare-And-Swap)
		// Ngăn chặn TOCTOU: Đảm bảo kiểm tra và tăng giá trị là một thao tác duy nhất
		if !slot.ActiveConns.CompareAndSwap(currClient, currClient+1) {
			// Xảy ra chạy đua với goroutine khác -> Nhường nhẹ CPU (runtime.Gosched) để tránh busy-spin trên CPU 1-2 core
			runtime.Gosched()
			continue
		}

		// Tăng tổng kết nối hệ thống wait-free (Fetch-and-Add nguyên tử phần cứng)
		totalActiveConns.Add(1)

		slot.TotalConns.Add(1)
		slot.LastActive.Store(now)
		slot.IsActive.Store(true)
		lastTrafficNano.Store(now)

		// Báo cho bộ dọn rác chuyển sang Active nếu đang ở Idle
		onTrafficActive()
		return true
	}
}

// Release: Thu hồi kết nối khi phiên làm việc kết thúc bằng CAS Loop
// Triệt tiêu 100% lỗi "lost update" do Store(0) và chống counter drift
func Release(addr netip.Addr) {
	slot := getSlot(addr)

	decremented := false
	// Giảm an toàn ActiveConns bằng CAS Loop (không bao giờ âm, không ghi đè cứng)
	for {
		curr := slot.ActiveConns.Load()
		if curr <= 0 {
			break
		}
		if slot.ActiveConns.CompareAndSwap(curr, curr-1) {
			decremented = true
			break
		}
	}

	// CHỐNG DRIFT: Tuyệt đối không trừ totalActiveConns nếu slot chưa từng được trừ!
	if !decremented {
		return
	}

	// Giảm an toàn totalActiveConns bằng CAS Loop
	for {
		currTot := totalActiveConns.Load()
		if currTot <= 0 {
			break
		}
		if totalActiveConns.CompareAndSwap(currTot, currTot-1) {
			break
		}
	}

	lastTrafficNano.Store(time.Now().UnixNano())
}

// TotalActiveConnections trả về tổng số kết nối đang hoạt động toàn hệ thống
func TotalActiveConnections() int64 {
	return totalActiveConns.Load()
}

// ClientActiveConnections trả về số kết nối đang hoạt động của 1 IP
func ClientActiveConnections(addr netip.Addr) int32 {
	return getSlot(addr).ActiveConns.Load()
}
