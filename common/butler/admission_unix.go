//go:build !windows

package butler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	runtimeDebug "runtime/debug"
	"strconv"
	"syscall"
	"time"
)

const (
	StartupLockPath       = "/tmp/sing-box-startup.lock"
	EmergencyReserveRAMKB = 8 * 1024       // Lằn ranh đỏ: Luôn giữ 8MB RAM cho Linux Kernel SoftIRQ & OS
	DefaultEstimatedRAMKB = 5 * 1024       // Ước tính ban đầu 5MB RAM cho 1 tiến trình mới
	MaxQueueWaitDuration    = 10 * time.Second // Hàng chờ "Ok đợi chút" tối đa 10 giây
	QueueRetryInterval      = 500 * time.Millisecond
	StartupCooldownInterval = 1 * time.Second  // Giãn cách nhịp thở 1 giây cho Linux nén ZRAM & cập nhật meminfo chuẩn xác
)

// AcquireStartupGate: Trạm kiểm soát cất cánh tuần tự & Dự báo an toàn RAM
// Đảm bảo:
// 1. Chỉ 1 tiến trình khởi tạo cấu hình tại một thời điểm (chống cú sốc đầy RAM khi Passwall2 gửi 20 node cùng lúc).
// 2. Tự đo lường mức RAM trung bình các tiến trình sing-box khác đang dùng để làm căn cứ dự báo.
// 3. Nếu RAM an toàn (>= 8MB đệm) -> Cấp phép ngay. Nếu thiếu RAM -> Đưa vào hàng chờ "Ok đợi chút" tối đa 10s.
func AcquireStartupGate(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(StartupLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, nil
	}

	// 1. Xếp hàng tuần tự bằng khóa POSIX flock độc quyền
	// Các tiến trình vào sau sẽ ngủ nhẹ ở tầng OS (CPU 0%) đợi đến lượt mình
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}, nil
	}

	// 2. Tự khảo sát RAM các anh em sing-box đang chạy để tính mức RAM trung bình
	estimatedCostKB, _ := surveySingBoxMemoryCost()

	// 3. Kiểm tra và Dự báo RAM khả dụng (có hàng chờ "Ok đợi chút" nếu RAM sát nút)
	startTime := time.Now()
	for {
		availKB, _, _, _ := readMemAndSwapKB()
		if availKB <= 0 {
			// Không đọc được /proc/meminfo -> cho phép chạy an toàn
			break
		}

		predictedRemainingKB := availKB - estimatedCostKB
		if predictedRemainingKB >= EmergencyReserveRAMKB {
			// Đủ RAM an toàn -> Cấp phép cất cánh!
			break
		}

		// Thiếu RAM: Kích hoạt hàng chờ "Ok đợi chút"
		// Kiểm tra thời gian hết hạn hàng chờ
		if time.Since(startTime) >= MaxQueueWaitDuration {
			// Đã đợi 10s mà RAM vẫn cạn kiệt -> Từ chối để bảo vệ Router không bị OOM Killer sập máy
			RecordRejection("Hết RAM khởi động", fmt.Sprintf("RAM khả dụng chỉ còn %dMB, cần %dMB", availKB/1024, estimatedCostKB/1024))
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
			return func() {}, errors.New("insufficient memory: startup aborted to prevent router OOM crash")
		}

		select {
		case <-ctx.Done():
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
			return func() {}, ctx.Err()
		case <-time.After(QueueRetryInterval):
			// Đợi 500ms để Linux đẩy bớt trang tĩnh vào ZRAM hoặc các tiến trình khác dọn dẹp
		}
	}

	// 4. Trả về hàm unlockGate để nhả trạm kiểm soát sau khi nạp xong
	unlocked := false
	unlockGate := func() {
		if unlocked {
			return
		}
		unlocked = true

		// Thu dọn ngay rác bộ nhớ phát sinh trong quá trình nạp JSON / biên dịch routing
		runtime.GC()
		runtimeDebug.FreeOSMemory()

		// Cho phép bộ đếm instance được làm mới
		cachedInstanceCount.Store(0)

		// Giãn cách nhịp thở 1 giây: Cho Linux Kernel đẩy heap vào ZRAM và cập nhật meminfo chuẩn xác
		time.Sleep(StartupCooldownInterval)

		// Mở khóa để tiến trình kế tiếp trong hàng chờ được cất cánh
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}

	return unlockGate, nil
}

// surveySingBoxMemoryCost: Quét /proc/*/statm để tự học mức RAM trung bình của các tiến trình sing-box
func surveySingBoxMemoryCost() (int64, int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return DefaultEstimatedRAMKB, 0
	}

	var totalRSSKB int64
	var count int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}

		pid, err := strconv.Atoi(name)
		if err != nil || pid == os.Getpid() {
			continue
		}

		comm, err := os.ReadFile("/proc/" + name + "/comm")
		if err != nil || !bytes.Equal(bytes.TrimSpace(comm), []byte("sing-box")) {
			continue
		}

		// Đọc resident pages từ /proc/<pid>/statm
		statmData, err := os.ReadFile("/proc/" + name + "/statm")
		if err == nil {
			fields := bytes.Fields(statmData)
			if len(fields) >= 2 {
				pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
				if err == nil && pages > 0 {
					// 1 page = 4KB trên hầu hết Linux ARM/MIPS/x86
					rssKB := pages * 4
					totalRSSKB += rssKB
					count++
				}
			}
		}
	}

	if count <= 0 {
		return DefaultEstimatedRAMKB, 0
	}

	avgCostKB := totalRSSKB / int64(count)
	// Đảm bảo cận dưới an toàn tối thiểu 3.5MB
	if avgCostKB < 3500 {
		avgCostKB = 3500
	}

	return avgCostKB, count
}
