package butler

import (
	"bytes"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	IdleMemoryLimit     int64 = 20 * 1024 * 1024 // 20MB trần an toàn khi nhàn rỗi
	ActiveMemoryLimit   int64 = -1               // Không giới hạn khi có tải (cần bao nhiêu lấy bấy nhiêu)
	IdleGCPercent       int   = 20               // Thu hồi sâu khi nhàn rỗi
	WarmGCPercent       int   = 50               // Thu hồi nhẹ trong thời gian chờ
	ActiveGCPercent     int   = 100              // GC tiêu chuẩn khi có lưu lượng để CPU thấp nhất
	IdleCooldownSeconds int64 = 45               // Chờ 45 giây trước khi vào Deep Idle

	// MinEmergencyInterval: Khoảng nghỉ cưỡng bức tối thiểu 60 giây giữa 2 lần dọn dẹp khẩn cấp
	// Chống dọn dẹp dồn dập làm CPU router bị bão (CPU Spike / Thrashing)
	MinEmergencyInterval = 60 * time.Second
)

var (
	isTrafficActive atomic.Bool
	isDeepCleaned   atomic.Bool
	emergencyCount  atomic.Uint64
	lastMemAvailKB  atomic.Int64
	lastSwapFreeKB  atomic.Int64
	lastEmergencyAt atomic.Int64 // Unix timestamp nano của lần dọn khẩn cấp cuối
)

func onTrafficActive() {
	wasDeep := isDeepCleaned.Swap(false)
	if !isTrafficActive.Swap(true) || wasDeep {
		activeLimit := currentProfile.ActiveMemoryLimit
		if activeLimit <= 0 {
			activeLimit = 48 * 1024 * 1024
		}
		debug.SetMemoryLimit(activeLimit)
		debug.SetGCPercent(ActiveGCPercent)
	}
}

func startReclaimer() {
	// Khởi tạo trần an toàn lúc ban đầu theo hồ sơ phần cứng đo được
	limit := currentProfile.IdleMemoryLimit
	if limit <= 0 {
		limit = IdleMemoryLimit
	}
	debug.SetGCPercent(WarmGCPercent)
	debug.SetMemoryLimit(limit)

	// Dọn rác khởi tạo 1 lần duy nhất sau 3 giây nạp cấu hình (không dồn dập)
	time.AfterFunc(3*time.Second, func() {
		if totalActiveConns.Load() == 0 {
			runtime.GC()
			debug.FreeOSMemory()
		}
	})

	go reclaimerLoop()
}

func reclaimerLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Kiểm tra nhịp nhàng mỗi 15 giây (tránh đọc file /proc/meminfo quá dồn dập)
	safetyTicker := time.NewTicker(15 * time.Second)
	defer safetyTicker.Stop()

	// Định kỳ 30 giây hòa giải và hiệu chuẩn số đếm kết nối (Zero Drift Guarantee)
	reconcileTicker := time.NewTicker(30 * time.Second)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ticker.C:
			handleIdleTransitions()
		case <-safetyTicker.C:
			checkMemoryAndZramHarmony()
		case <-reconcileTicker.C:
			reconcileConnectionCounters()
		}
	}
}

// reconcileConnectionCounters: Quét nhẹ 1024 slot O(1) để hiệu chuẩn totalActiveConns chính xác tuyệt đối
func reconcileConnectionCounters() {
	var total int64
	for i := 0; i < NumSlots; i++ {
		total += int64(clientSlots[i].ActiveConns.Load())
	}
	totalActiveConns.Store(total)
}

func handleIdleTransitions() {
	if totalActiveConns.Load() == 0 {
		idleDur := time.Since(time.Unix(0, lastTrafficNano.Load()))

		// Warm Idle sau 3 giây không có traffic: chỉ chỉnh GC, KHÔNG gọi runtime.GC() hay FreeOSMemory
		if isTrafficActive.Load() && idleDur >= 3*time.Second {
			isTrafficActive.Store(false)
			debug.SetGCPercent(WarmGCPercent)
		}

		// Deep Idle sau 45 giây liên tục không có kết nối nào:
		// Chạy ONE-SHOT DUY NHẤT 1 LẦN, đặt cờ isDeepCleaned để KHÔNG lặp lại dồn dập
		if idleDur >= time.Duration(IdleCooldownSeconds)*time.Second {
			if totalActiveConns.Load() == 0 && !isDeepCleaned.Swap(true) {
				if totalActiveConns.Load() > 0 {
					isDeepCleaned.Store(false)
					return
				}
				debug.SetGCPercent(IdleGCPercent)
				limit := currentProfile.IdleMemoryLimit
				if limit <= 0 {
					limit = IdleMemoryLimit
				}
				debug.SetMemoryLimit(limit)
				runtime.GC()
				debug.FreeOSMemory()
			}
		}
	}
}

// checkMemoryAndZramHarmony: Phối hợp nhịp nhàng giữa RAM vật lý và ZRAM Swap 238MB
// Không dọn dẹp thô bạo làm phá vỡ cơ chế nén tự nhiên của ZRAM
func checkMemoryAndZramHarmony() {
	availKB, swapFreeKB := readMemAndSwapKB()
	if availKB > 0 {
		lastMemAvailKB.Store(availKB)
	}
	if swapFreeKB >= 0 {
		lastSwapFreeKB.Store(swapFreeKB)
	}

	// NGUYÊN TẮC HÀI HÒA VỚI ZRAM:
	// 1. ZRAM được thiết kế để hấp thụ bộ nhớ tĩnh (anonymous pages) với tỷ lệ nén 3:1.
	//    Nếu MemAvailable giảm nhưng ZRAM vẫn còn nhiều (SwapFree > 50MB), để ZRAM làm việc tự nhiên.
	// 2. Chỉ kích hoạt Emergency Trim khi CẢ HAI điều kiện cùng xảy ra:
	//    - RAM vật lý khả dụng MemAvailable < 15MB (15,360 KB)
	//    - VÀ ZRAM Swap cũng đã cạn kiệt SwapFree < 30MB (30,720 KB)
	//    (Hoặc trường hợp cực đoan: MemAvailable < 8MB)
	isCritical := (availKB > 0 && availKB < 15*1024 && swapFreeKB >= 0 && swapFreeKB < 30*1024) ||
		(availKB > 0 && availKB < 8*1024)

	if !isCritical {
		return
	}

	// CHỐNG DỒN DẬP (Anti-Spike Rate Limiting):
	// Mặc định cách ít nhất 60s, nhưng nếu RAM cực kỳ nguy cấp (<10MB) thì rút ngắn xuống 15s để cứu router kịp thời
	minInterval := MinEmergencyInterval
	if availKB > 0 && availKB < 10*1024 {
		minInterval = 15 * time.Second
	}
	now := time.Now().UnixNano()
	lastAt := lastEmergencyAt.Load()
	if lastAt > 0 && time.Duration(now-lastAt) < minInterval {
		return
	}

	// Ghi nhận thời điểm và dọn dẹp có kiểm soát
	lastEmergencyAt.Store(now)
	emergencyCount.Add(1)

	// Dọn dẹp nhịp nhàng: GC nhẹ rồi trả trang nhớ về cho OS
	runtime.GC()
	debug.FreeOSMemory()
}

// readMemAndSwapKB: Đọc cả MemAvailable và SwapFree từ /proc/meminfo một lần duy nhất
func readMemAndSwapKB() (memAvailKB int64, swapFreeKB int64) {
	memAvailKB = -1
	swapFreeKB = -1

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}

	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("MemAvailable:")) {
			memAvailKB = parseKB(line)
		} else if bytes.HasPrefix(line, []byte("SwapFree:")) {
			swapFreeKB = parseKB(line)
		}
		if memAvailKB > 0 && swapFreeKB > 0 {
			break
		}
	}
	return
}

func parseKB(line []byte) int64 {
	fields := bytes.Fields(line)
	if len(fields) >= 2 {
		val, err := strconv.ParseInt(string(fields[1]), 10, 64)
		if err == nil {
			return val
		}
	}
	return -1
}
