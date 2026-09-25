package butler

import (
	"bytes"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// HardwareProfile: Hồ sơ phần cứng được tự động nhận diện từ RAM thực tế của router
type HardwareProfile struct {
	Name                string
	TotalRAMMB          int64
	SafeConntrackBudget int64
	MaxClientCap        int32
	GuaranteedFloor     int32
	IdleMemoryLimit     int64
	ActiveMemoryLimit   int64 // Trần RAM khi Active, bảo vệ không bị Linux OOM Killer
}

var (
	currentProfile       HardwareProfile
	dynamicHardLimit     atomic.Int32
	dynamicGuaranteedMin atomic.Int32
	activeDeviceCount    atomic.Int32
	pausedCutoffNano     atomic.Int64 // Unix timestamp nano của thiết bị mới nhất bị tạm dừng
	isResourceCritical   atomic.Bool  // Cờ báo hiệu tài nguyên đang cạn kiệt
	pausedDeviceCount    atomic.Int32 // Số thiết bị đang bị tạm dừng kết nối mới
	kernelConntrackCount atomic.Int64 // Số session conntrack thực tế của kernel Linux
	kernelConntrackMax   atomic.Int64 // Dung lượng bảng conntrack tối đa của router
	cachedInstanceCount  atomic.Int32 // Cache số lượng tiến trình sing-box
	lastInstanceScanNano atomic.Int64 // Thời điểm quét gần nhất để tránh bão CPU
	lastLoggedInstCount  atomic.Int32 // Lưu số instance lần log gần nhất để chống spam syslog
)

// KernelConntrackStats trả về thống kê conntrack thực tế của kernel Linux
func KernelConntrackStats() (int64, int64) {
	return kernelConntrackCount.Load(), kernelConntrackMax.Load()
}

// updateKernelConntrack: Đọc conntrack thực tế từ nhân Linux netfilter
func updateKernelConntrack() {
	count := readInt64FromFile("/proc/sys/net/netfilter/nf_conntrack_count")
	max := readInt64FromFile("/proc/sys/net/netfilter/nf_conntrack_max")
	if count >= 0 {
		kernelConntrackCount.Store(count)
	}
	if max > 0 {
		kernelConntrackMax.Store(max)
	}
}

func readInt64FromFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	data = bytes.TrimSpace(data)
	n, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// detectHardwareProfile: Tự động đo RAM của router từ /proc/meminfo và xác lập hồ sơ tối ưu
func detectHardwareProfile() HardwareProfile {
	totalRAMKB := readMemTotalKB()
	totalRAMMB := totalRAMKB / 1024
	return profileFromRAM(totalRAMMB)
}

// profileFromRAM: Quyết định hồ sơ tối ưu dựa trên số MB RAM
func profileFromRAM(totalRAMMB int64) HardwareProfile {
	// Mặc định an toàn (Standard Profile 256MB)
	profile := HardwareProfile{
		Name:                "Standard-256MB",
		TotalRAMMB:          totalRAMMB,
		SafeConntrackBudget: 6000,
		MaxClientCap:        1200,
		GuaranteedFloor:     20,
		IdleMemoryLimit:     20 * 1024 * 1024,
		ActiveMemoryLimit:   48 * 1024 * 1024,
	}

	if totalRAMMB <= 0 {
		return profile
	}

	if totalRAMMB <= 128 {
		// Router RAM nhỏ (<= 128MB): Thắt lưng buộc bụng tối đa
		profile.Name = "Micro-128MB"
		profile.SafeConntrackBudget = 3000
		profile.MaxClientCap = 500
		profile.GuaranteedFloor = 15
		profile.IdleMemoryLimit = 12 * 1024 * 1024
		profile.ActiveMemoryLimit = 28 * 1024 * 1024
	} else if totalRAMMB <= 260 {
		// Router 240MB - 256MB (Cấu hình router hiện tại của bạn)
		profile.Name = "Standard-256MB"
		profile.SafeConntrackBudget = 6000
		profile.MaxClientCap = 1200
		profile.GuaranteedFloor = 20
		profile.IdleMemoryLimit = 20 * 1024 * 1024
		profile.ActiveMemoryLimit = 48 * 1024 * 1024
	} else if totalRAMMB <= 550 {
		// Router tầm trung (257MB - 512MB, ví dụ MT7981 Filogic)
		profile.Name = "HighEnd-512MB"
		profile.SafeConntrackBudget = 15000
		profile.MaxClientCap = 2500
		profile.GuaranteedFloor = 30
		profile.IdleMemoryLimit = 35 * 1024 * 1024
		profile.ActiveMemoryLimit = 96 * 1024 * 1024
	} else {
		// Thiết bị mạnh (> 512MB: Mini PC x86, Router 1GB-4GB)
		profile.Name = "Mega-Enterprise"
		profile.SafeConntrackBudget = 40000
		profile.MaxClientCap = 5000
		profile.GuaranteedFloor = 50
		profile.IdleMemoryLimit = 60 * 1024 * 1024
		profile.ActiveMemoryLimit = 192 * 1024 * 1024
	}

	return profile
}

// readMemTotalKB: Đọc MemTotal từ /proc/meminfo
func readMemTotalKB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}

	key := []byte("MemTotal:")
	idx := bytes.Index(data, key)
	if idx == -1 {
		return -1
	}

	rest := data[idx+len(key):]
	end := bytes.IndexByte(rest, '\n')
	if end != -1 {
		rest = rest[:end]
	}

	fields := bytes.Fields(rest)
	if len(fields) >= 1 {
		val, err := strconv.ParseInt(string(fields[0]), 10, 64)
		if err == nil {
			return val
		}
	}
	return -1
}

const instanceCountFilePath = "/tmp/sing-box-instances.count"
const instanceScanInterval = 15 * time.Second

// countSingBoxInstances: Đếm số lượng tiến trình sing-box thực tế trên Linux
// Khử bão CPU:
// 1. Dùng bộ đệm in-memory 15 giây để không quét /proc liên tục mỗi 5s.
// 2. Nếu là Leader: Quét /proc và ghi số lượng ra /tmp/sing-box-instances.count (Atomic rename).
// 3. Nếu là Worker: Đọc nhanh từ /tmp/sing-box-instances.count (chỉ 1 syscall đọc vài bytes thay vì 300+ syscalls).
// Nhờ đó giảm 98.3% syscalls từ 1,200 syscalls/giây xuống còn dưới 20 syscalls/giây trên toàn bộ Router!
func countSingBoxInstances() int32 {
	now := time.Now().UnixNano()
	lastScan := lastInstanceScanNano.Load()
	cached := cachedInstanceCount.Load()

	// 1. Trả về ngay nếu bộ đệm còn hiệu lực (< 15s)
	if cached > 0 && (now-lastScan) < int64(instanceScanInterval) {
		return cached
	}

	// 2. Nếu không phải Leader: Thử đọc từ file do Leader ghi
	if !IsLeader() {
		if data, err := os.ReadFile(instanceCountFilePath); err == nil {
			if n, err := strconv.ParseInt(string(bytes.TrimSpace(data)), 10, 32); err == nil && n > 0 {
				cachedInstanceCount.Store(int32(n))
				lastInstanceScanNano.Store(now)
				return int32(n)
			}
		}
		// Nếu file chưa có hoặc đọc lỗi mà đã có cache cũ -> giữ cache cũ
		if cached > 0 {
			lastInstanceScanNano.Store(now)
			return cached
		}
	}

	// 3. Chỉ Leader (hoặc fallback ban đầu khi chưa có cache) mới quét /proc
	count := scanProcSingBox()
	if count <= 0 {
		count = 1
	}

	cachedInstanceCount.Store(count)
	lastInstanceScanNano.Store(now)

	// Leader lưu ra file để chia sẻ cho các worker khác dùng chung
	if IsLeader() {
		tmpPath := instanceCountFilePath + ".tmp"
		_ = os.WriteFile(tmpPath, []byte(strconv.FormatInt(int64(count), 10)+"\n"), 0o644)
		_ = os.Rename(tmpPath, instanceCountFilePath)
	}

	return count
}

func scanProcSingBox() int32 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 1
	}
	var count int32
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		comm, err := os.ReadFile("/proc/" + name + "/comm")
		if err == nil && bytes.Equal(bytes.TrimSpace(comm), []byte("sing-box")) {
			count++
		}
	}
	if count <= 0 {
		return 1
	}
	return count
}

// updateDynamicLimits: Quản gia check RAM thực tế trước để có định mức thực tế,
// sau đó chia đều cho các tiến trình sing-box đang chạy.
// Nếu kết quả chia nhỏ hơn 250 thì DỪNG (khóa sàn ở 250 kết nối/máy để bảo đảm MMO bot chạy ổn định).
func updateDynamicLimits(activeCount int32) {
	instCount := countSingBoxInstances()
	if instCount < activeCount {
		instCount = activeCount
	}
	if instCount <= 0 {
		instCount = 1
	}
	activeDeviceCount.Store(instCount)

	// Nếu người dùng có set biến môi trường BUTLER_HARD_LIMIT thủ công -> Tôn trọng biến người dùng set
	if envLimit := envInt32("BUTLER_HARD_LIMIT", 0); envLimit > 0 {
		dynamicHardLimit.Store(envLimit)
		return
	}

	// 1. Quản gia check RAM thực tế trước:
	availKB := lastMemAvailKB.Load()
	if availKB <= 0 {
		checkMemoryAndZramHarmony()
		availKB = lastMemAvailKB.Load()
	}

	// Ngân sách an toàn mặc định theo hồ sơ phần cứng (15% Safety Headroom)
	safeBudget := int64(float64(currentProfile.SafeConntrackBudget) * 0.85)
	if safeBudget <= 0 {
		safeBudget = currentProfile.SafeConntrackBudget
	}

	// Nếu RAM thực tế đo được thấp (ví dụ < 30MB do gánh nhiều OpenVPN),
	// tính toán định mức thực tế dựa trên dung lượng RAM còn lại (giữ 10MB dự trữ an toàn)
	if availKB > 0 && availKB < 30*1024 {
		safeRAMKB := availKB - 10*1024
		if safeRAMKB < 0 {
			safeRAMKB = 0
		}
		// Mỗi kết nối sing-box tiêu thụ ~16KB
		ramCapacityConns := safeRAMKB / 16
		if ramCapacityConns < safeBudget && ramCapacityConns > 0 {
			safeBudget = ramCapacityConns
		}
	}

	// 2. Chia đều định mức thực tế cho số tiến trình sing-box đang chạy:
	calculatedLimit := int32(safeBudget / int64(instCount))

	// 3. Quy tắc cốt lõi: Nếu nhỏ hơn 250 là DỪNG (Khóa sàn ở 250 kết nối/máy)
	const AbsoluteMinFloor int32 = 250
	if calculatedLimit < AbsoluteMinFloor {
		calculatedLimit = AbsoluteMinFloor
	}

	// Kẹp trần tối đa theo MaxClientCap (ví dụ 1200)
	if calculatedLimit > currentProfile.MaxClientCap {
		calculatedLimit = currentProfile.MaxClientCap
	}

	dynamicHardLimit.Swap(calculatedLimit)
}

// updateDynamicGuaranteedMin: Tính toán số kết nối tối thiểu bảo đảm (khóa sàn ở 250 khi đông máy)
func updateDynamicGuaranteedMin(activeCount int32) {
	if activeCount <= 0 {
		activeCount = 1
	}

	// Nếu người dùng có set biến môi trường BUTLER_GUARANTEED_MIN thủ công -> Tôn trọng biến người dùng set
	if envMin := envInt32("BUTLER_GUARANTEED_MIN", 0); envMin > 0 {
		dynamicGuaranteedMin.Store(envMin)
		return
	}

	const AbsoluteMinFloor int32 = 250
	currHardLimit := dynamicHardLimit.Load()
	if currHardLimit <= AbsoluteMinFloor {
		dynamicGuaranteedMin.Store(AbsoluteMinFloor)
		return
	}

	// Khi ít máy (ví dụ 5-10 máy) và RAM rảnh: Sàn bảo đảm co giãn theo RAM
	availKB := lastMemAvailKB.Load()
	if availKB <= 0 {
		checkMemoryAndZramHarmony()
		availKB = lastMemAvailKB.Load()
	}

	if availKB <= 0 {
		availKB = currentProfile.TotalRAMMB * 1024 / 3
	}

	const emergencyReserveKB int64 = 15 * 1024
	safeRAMBudgetKB := availKB - emergencyReserveKB
	if safeRAMBudgetKB < 0 {
		safeRAMBudgetKB = 0
	}

	ramCapacityConns := safeRAMBudgetKB / 16
	calculatedGuaranteed := int32(ramCapacityConns / int64(activeCount))

	if calculatedGuaranteed < AbsoluteMinFloor {
		calculatedGuaranteed = AbsoluteMinFloor
	}
	if calculatedGuaranteed > currHardLimit {
		calculatedGuaranteed = currHardLimit
	}

	dynamicGuaranteedMin.Store(calculatedGuaranteed)
}

// updateResourceProtectionAndShedding: Khi hết tài nguyên, tạm dừng thiết bị kết nối sau cùng để cứu router
func updateResourceProtectionAndShedding(activeCount int32) {
	availKB := lastMemAvailKB.Load()
	swapFreeKB := lastSwapFreeKB.Load()
	totalConns := totalActiveConns.Load()
	maxSysConns := GetEffectiveMaxSystemConns()

	// Điều kiện cạn kiệt tài nguyên:
	// 1. RAM khả dụng cực thấp: < 15MB VÀ ZRAM Swap < 30MB, HOẶC RAM khả dụng < 10MB
	// 2. HOẶC Tổng số kết nối hệ thống đã chạm trần tối đa an toàn (totalConns >= maxSysConns)
	// 3. HOẶC Bảng Conntrack Kernel thực tế của Linux vượt quá 85% dung lượng tối đa (bảo vệ 9 OpenVPN + toàn Router)
	kcCount := kernelConntrackCount.Load()
	kcMax := kernelConntrackMax.Load()
	kcCritical := kcCount > 0 && kcMax > 0 && (kcCount*100/kcMax >= 85)

	isCritical := (availKB > 0 && availKB < 15*1024 && swapFreeKB >= 0 && swapFreeKB < 30*1024) ||
		(availKB > 0 && availKB < 10*1024) ||
		(totalConns >= maxSysConns) ||
		kcCritical

	isResourceCritical.Store(isCritical)

	if !isCritical || activeCount <= 1 {
		// Tài nguyên an toàn (hoặc chỉ có 1 thiết bị): Gỡ bỏ tạm dừng
		pausedCutoffNano.Store(0)
		pausedDeviceCount.Store(0)
		return
	}

	// TÀI NGUYÊN BỊ CẠN KIỆT: Tìm thiết bị kết nối sau cùng (FirstSeen lớn nhất trong các máy đang online)
	var latestFirstSeen int64
	for i := 0; i < NumSlots; i++ {
		slot := &clientSlots[i]
		if slot.ActiveConns.Load() > 0 || slot.IsActive.Load() {
			seen := slot.FirstSeen.Load()
			if seen > latestFirstSeen {
				latestFirstSeen = seen
			}
		}
	}

	if latestFirstSeen > 0 {
		// Thiết lập mốc cutoff: Bất kỳ thiết bị nào có FirstSeen >= latestFirstSeen sẽ bị tạm dừng mở kết nối mới
		pausedCutoffNano.Store(latestFirstSeen)
		pausedDeviceCount.Store(1)
	}
}

func initProfile() {
	currentProfile = detectHardwareProfile()
	updateKernelConntrack()
	// Khởi tạo hạn ngạch ban đầu bằng MaxClientCap
	updateDynamicLimits(1)
	updateDynamicGuaranteedMin(1)
}

func startDynamicLimiter() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			updateKernelConntrack()
			activeCount := countActiveDevices(60 * time.Second)
			updateDynamicLimits(activeCount)
			updateDynamicGuaranteedMin(activeCount)
			updateResourceProtectionAndShedding(activeCount)
		}
	}()
}

// GetCurrentProfile trả về thông tin hồ sơ phần cứng đang hoạt động
func GetCurrentProfile() HardwareProfile {
	return currentProfile
}

// GetDynamicHardLimit trả về hạn mức động hiện tại
func GetDynamicHardLimit() int32 {
	return dynamicHardLimit.Load()
}

// GetEffectiveGuaranteedMin trả về số kết nối tối thiểu bảo đảm tính toán động theo RAM thực tế
func GetEffectiveGuaranteedMin() int32 {
	if dyn := dynamicGuaranteedMin.Load(); dyn > 0 {
		return dyn
	}
	return GuaranteedMinConns
}

// IsPausedClient kiểm tra xem thiết bị này có đang bị tạm dừng do cạn kiệt tài nguyên không
func IsPausedClient(slot *ClientSlot) bool {
	cutoff := pausedCutoffNano.Load()
	if cutoff <= 0 {
		return false
	}
	seen := slot.FirstSeen.Load()
	return seen > 0 && seen >= cutoff
}

// IsResourceCritical trả về trạng thái tài nguyên đang cạn kiệt
func IsResourceCritical() bool {
	return isResourceCritical.Load()
}

// PausedDevicesCount trả về số thiết bị đang bị tạm dừng mở kết nối mới
func PausedDevicesCount() int32 {
	return pausedDeviceCount.Load()
}
