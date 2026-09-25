package butler

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type RejectionRecord struct {
	Timestamp string `json:"timestamp"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
}

var (
	rejectionsLock sync.Mutex
	rejectionsList []RejectionRecord
)

const (
	maxRejections         = 10
	rejectionsLogFilePath = "/tmp/sing-box-rejections.log"
	maxLogFileSize        = 32 * 1024 // 32KB trần an toàn tuyệt đối cho tmpfs RAM
)

// RecordRejection lưu vết từ chối do thiếu RAM:
// 1. Cập nhật in-memory ring-buffer.
// 2. Tự động xoay vòng (logrotate/truncate) nếu file vượt 32KB để bảo vệ RAM router.
// 3. Ghi append 1 dòng JSON vào file chia sẻ IPC để Leader đọc và đẩy lên bảng ACL LuCI.
func RecordRejection(reason, detail string) {
	rejectionsLock.Lock()
	defer rejectionsLock.Unlock()

	rec := RejectionRecord{
		Timestamp: time.Now().Format("15:04:05"),
		Reason:    reason,
		Detail:    detail,
	}

	rejectionsList = append(rejectionsList, rec)
	if len(rejectionsList) > maxRejections {
		rejectionsList = rejectionsList[len(rejectionsList)-maxRejections:]
	}

	// Ghi an toàn có giới hạn kích thước ra file chia sẻ IPC
	if line, err := json.Marshal(rec); err == nil {
		if fi, err := os.Stat(rejectionsLogFilePath); err == nil && fi.Size() > maxLogFileSize {
			truncateRejectionsLogFile()
		}
		if f, err := os.OpenFile(rejectionsLogFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
}

func truncateRejectionsLogFile() {
	data, err := os.ReadFile(rejectionsLogFilePath)
	if err != nil {
		return
	}
	lines := splitLines(data)
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	tmpPath := rejectionsLogFilePath + ".tmp"
	var out []byte
	for _, l := range lines {
		if len(l) > 0 {
			out = append(out, l...)
			out = append(out, '\n')
		}
	}
	if err := os.WriteFile(tmpPath, out, 0o644); err == nil {
		_ = os.Rename(tmpPath, rejectionsLogFilePath)
	}
}

// GetRejections trả về bản sao danh sách từ chối gần nhất (kết hợp cả file chia sẻ /tmp/sing-box-rejections.log)
func GetRejections() []RejectionRecord {
	rejectionsLock.Lock()
	defer rejectionsLock.Unlock()

	// Nếu là Leader, đọc thêm các rejection từ file chia sẻ do các tiến trình worker bị exit ghi lại
	if data, err := os.ReadFile(rejectionsLogFilePath); err == nil {
		lines := splitLines(data)
		var fileRecs []RejectionRecord
		for _, l := range lines {
			if len(l) == 0 {
				continue
			}
			var r RejectionRecord
			if err := json.Unmarshal(l, &r); err == nil {
				fileRecs = append(fileRecs, r)
			}
		}
		if len(fileRecs) > 0 {
			if len(fileRecs) > maxRejections {
				fileRecs = fileRecs[len(fileRecs)-maxRejections:]
			}
			return fileRecs
		}
	}

	res := make([]RejectionRecord, len(rejectionsList))
	copy(res, rejectionsList)
	return res
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
