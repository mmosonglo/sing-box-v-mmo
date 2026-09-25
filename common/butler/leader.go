package butler

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	isLeader    atomic.Bool
	leaderMutex sync.Mutex
	lockFile    *os.File
)

const LockFilePath = "/tmp/sing-box-butler.lock"

// startLeaderElection: Tranh chấp quyền Quản Gia Trưởng bằng cơ chế POSIX flock không khóa
func startLeaderElection() {
	go electionLoop()
}

func electionLoop() {
	for {
		if tryAcquireLeadership() {
			isLeader.Store(true)
			// Trở thành Quản Gia Trưởng: Kích hoạt các nhiệm vụ điều hành tối cao
			runLeaderTasks()
			// Nếu thoát khỏi runLeaderTasks (ví dụ bị mất lock hoặc file descriptor đóng)
			isLeader.Store(false)
			leaderMutex.Lock()
			if lockFile != nil {
				_ = lockFile.Close()
				lockFile = nil
			}
			leaderMutex.Unlock()
		}
		// Nếu là Worker: Thử lại sau mỗi 5 giây để sẵn sàng kế nhiệm nếu Quản Gia Trưởng bị tắt
		time.Sleep(5 * time.Second)
	}
}

// runLeaderTasks: Vòng lặp điều hành duy trì khóa của Quản Gia Trưởng
func runLeaderTasks() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for isLeader.Load() {
		<-ticker.C
	}
}

// countActiveDevices: Đếm số lượng máy LAN có kết nối hoặc hoạt động trong khoảng thời gian threshold
func countActiveDevices(threshold time.Duration) int32 {
	now := time.Now().UnixNano()
	thresholdNano := threshold.Nanoseconds()
	var count int32

	for i := 0; i < NumSlots; i++ {
		slot := &clientSlots[i]
		if slot.ActiveConns.Load() > 0 {
			count++
		} else if slot.IsActive.Load() {
			last := slot.LastActive.Load()
			if (now - last) <= thresholdNano {
				count++
			} else {
				slot.IsActive.Store(false)
			}
		}
	}

	if count <= 0 {
		count = 1
	}
	return count
}

// IsLeader trả về true nếu tiến trình hiện tại là Quản Gia Trưởng
func IsLeader() bool {
	return isLeader.Load()
}

// CleanupOnShutdown: Dọn dẹp an toàn khi tiến trình thoát
// 1. Nhả khóa flock và đóng FD an toàn qua leaderMutex (loại trừ data race 100%).
// 2. Không xóa thô bạo file JSON/Lock dùng chung để tránh race condition khi Passwall2 restart dịch vụ.
//    (Hệ thống Web LuCI đã có cơ chế Liveness Threshold 18s tự động reset UI khi tiến trình dừng).
func CleanupOnShutdown() {
	leaderMutex.Lock()
	defer leaderMutex.Unlock()

	if isLeader.Load() {
		isLeader.Store(false)
		if lockFile != nil {
			_ = lockFile.Close()
			lockFile = nil
		}
	}
}
