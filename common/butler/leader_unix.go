//go:build !windows

package butler

import (
	"os"
	"syscall"
)

// tryAcquireLeadership: Thử lấy khóa độc quyền non-blocking bằng POSIX flock trên Linux/Unix
func tryAcquireLeadership() bool {
	f, err := os.OpenFile(LockFilePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Đã có tiến trình sing-box khác giữ quyền Quản Gia Trưởng
		f.Close()
		return false
	}

	leaderMutex.Lock()
	lockFile = f
	leaderMutex.Unlock()
	return true
}
