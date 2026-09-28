package butler

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type RejectionRecord struct {
	Timestamp string `json:"timestamp"`
	Client    string `json:"client,omitempty"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
}

type StandbyItem struct {
	Timestamp string `json:"timestamp"`
	PID       int    `json:"pid"`
	Client    string `json:"client,omitempty"` // Địa chỉ MAC hoặc IP của máy client từ Passwall2
	Node      string `json:"node,omitempty"`
	State     string `json:"state"` // "waiting" (đang ngủ đợi RAM), "waking" (đang thức tỉnh)
	Detail    string `json:"detail"`
}

type DeadProxyItem struct {
	Timestamp string `json:"timestamp"`
	PID       int    `json:"pid"`
	Client    string `json:"client,omitempty"` // Địa chỉ MAC hoặc IP bị ảnh hưởng
	Node      string `json:"node,omitempty"`
	LatencyMs int    `json:"latency_ms"`
	Error     string `json:"error"`
}

var (
	rejectionsLock sync.Mutex
	rejectionsList []RejectionRecord

	standbyLock sync.Mutex
	standbyList []StandbyItem

	deadProxyLock sync.Mutex
	deadProxyList []DeadProxyItem
)

const (
	maxRejections         = 10
	rejectionsLogFilePath = "/tmp/sing-box-rejections.log"
	maxLogFileSize        = 32 * 1024 // 32KB trần an toàn tuyệt đối cho tmpfs RAM
	standbyQueueFilePath  = "/tmp/sing-box-standby.json"
	deadProxiesFilePath   = "/tmp/sing-box-dead.json"
)

// RecordStandbyState cập nhật trạng thái của tiến trình trong hàng chờ thức tỉnh
func RecordStandbyState(pid int, client, node, state, detail string) {
	standbyLock.Lock()
	defer standbyLock.Unlock()

	// Cập nhật in-memory
	nowStr := time.Now().Format("15:04:05")
	found := false
	for i := range standbyList {
		if standbyList[i].PID == pid {
			if state == "removed" {
				standbyList = append(standbyList[:i], standbyList[i+1:]...)
			} else {
				standbyList[i].Timestamp = nowStr
				if client != "" {
					standbyList[i].Client = client
				}
				standbyList[i].State = state
				standbyList[i].Detail = detail
			}
			found = true
			break
		}
	}
	if !found && state != "removed" {
		standbyList = append(standbyList, StandbyItem{
			Timestamp: nowStr,
			PID:       pid,
			Client:    client,
			Node:      node,
			State:     state,
			Detail:    detail,
		})
	}

	// Đọc và đồng bộ IPC file
	syncStandbyFile(pid, client, node, state, detail)
}

func syncStandbyFile(pid int, client, node, state, detail string) {
	var current []StandbyItem
	if data, err := os.ReadFile(standbyQueueFilePath); err == nil {
		_ = json.Unmarshal(data, &current)
	}

	found := false
	nowStr := time.Now().Format("15:04:05")
	for i := range current {
		if current[i].PID == pid {
			if state == "removed" {
				current = append(current[:i], current[i+1:]...)
			} else {
				current[i].Timestamp = nowStr
				if client != "" {
					current[i].Client = client
				}
				current[i].State = state
				current[i].Detail = detail
			}
			found = true
			break
		}
	}
	if !found && state != "removed" {
		current = append(current, StandbyItem{
			Timestamp: nowStr,
			PID:       pid,
			Client:    client,
			Node:      node,
			State:     state,
			Detail:    detail,
		})
	}

	if len(current) == 0 {
		_ = os.Remove(standbyQueueFilePath)
	} else {
		if data, err := json.Marshal(current); err == nil {
			_ = os.WriteFile(standbyQueueFilePath+".tmp", data, 0o644)
			_ = os.Rename(standbyQueueFilePath+".tmp", standbyQueueFilePath)
		}
	}
}

// GetStandbyQueue trả về danh sách các node/tiến trình đang ngủ chờ RAM
func GetStandbyQueue() []StandbyItem {
	standbyLock.Lock()
	defer standbyLock.Unlock()

	if data, err := os.ReadFile(standbyQueueFilePath); err == nil {
		var list []StandbyItem
		if err := json.Unmarshal(data, &list); err == nil && len(list) > 0 {
			return list
		}
	}

	res := make([]StandbyItem, len(standbyList))
	copy(res, standbyList)
	return res
}

// RecordRejection lưu vết từ chối do thiếu RAM hoặc quá tải thiết bị:
// 1. Cập nhật in-memory ring-buffer.
// 2. Tự động xoay vòng (logrotate/truncate) nếu file vượt 32KB để bảo vệ RAM router.
// 3. Ghi append 1 dòng JSON vào file chia sẻ IPC để Leader đọc và đẩy lên bảng ACL LuCI.
func RecordRejection(client, reason, detail string) {
	rejectionsLock.Lock()
	defer rejectionsLock.Unlock()

	rec := RejectionRecord{
		Timestamp: time.Now().Format("15:04:05"),
		Client:    client,
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

// RemoveStandbyItem xóa tiến trình khỏi danh sách chờ khi tiến trình thoát hoặc thức dậy thành công
func RemoveStandbyItem(pid int) {
	RecordStandbyState(pid, "", "", "removed", "")
}

// RecordDeadProxyState ghi nhận hoặc gỡ bỏ trạng thái proxy bị chết/mất mạng
func RecordDeadProxyState(pid int, client, node string, isDead bool, latencyMs int, errMsg string) {
	deadProxyLock.Lock()
	defer deadProxyLock.Unlock()

	nowStr := time.Now().Format("15:04:05")
	found := false
	stateChanged := false
	for i := range deadProxyList {
		if deadProxyList[i].PID == pid {
			if !isDead {
				deadProxyList = append(deadProxyList[:i], deadProxyList[i+1:]...)
				stateChanged = true
			} else {
				deadProxyList[i].Timestamp = nowStr
				if client != "" {
					deadProxyList[i].Client = client
				}
				deadProxyList[i].LatencyMs = latencyMs
				deadProxyList[i].Error = errMsg
				stateChanged = true
			}
			found = true
			break
		}
	}
	if !found && isDead {
		deadProxyList = append(deadProxyList, DeadProxyItem{
			Timestamp: nowStr,
			PID:       pid,
			Client:    client,
			Node:      node,
			LatencyMs: latencyMs,
			Error:     errMsg,
		})
		stateChanged = true
	}

	// Chỉ đồng bộ ra file khi có sự thay đổi trạng thái (Zero Disk I/O khi ổn định)
	if stateChanged {
		syncDeadProxiesFile(pid, client, node, isDead, latencyMs, errMsg)
	}
}

func syncDeadProxiesFile(pid int, client, node string, isDead bool, latencyMs int, errMsg string) {
	var current []DeadProxyItem
	if data, err := os.ReadFile(deadProxiesFilePath); err == nil {
		_ = json.Unmarshal(data, &current)
	}

	found := false
	nowStr := time.Now().Format("15:04:05")
	for i := range current {
		if current[i].PID == pid {
			if !isDead {
				current = append(current[:i], current[i+1:]...)
			} else {
				current[i].Timestamp = nowStr
				if client != "" {
					current[i].Client = client
				}
				current[i].LatencyMs = latencyMs
				current[i].Error = errMsg
			}
			found = true
			break
		}
	}
	if !found && isDead {
		current = append(current, DeadProxyItem{
			Timestamp: nowStr,
			PID:       pid,
			Client:    client,
			Node:      node,
			LatencyMs: latencyMs,
			Error:     errMsg,
		})
	}

	if len(current) == 0 {
		_ = os.Remove(deadProxiesFilePath)
	} else {
		if data, err := json.Marshal(current); err == nil {
			_ = os.WriteFile(deadProxiesFilePath+".tmp", data, 0o644)
			_ = os.Rename(deadProxiesFilePath+".tmp", deadProxiesFilePath)
		}
	}
}

// GetDeadProxies trả về danh sách các proxy/node đang bị đứt mạng hoặc timeout
func GetDeadProxies() []DeadProxyItem {
	deadProxyLock.Lock()
	defer deadProxyLock.Unlock()

	if data, err := os.ReadFile(deadProxiesFilePath); err == nil {
		var list []DeadProxyItem
		if err := json.Unmarshal(data, &list); err == nil && len(list) > 0 {
			return list
		}
	}

	res := make([]DeadProxyItem, len(deadProxyList))
	copy(res, deadProxyList)
	return res
}

// RemoveDeadProxyItem dọn dẹp trạng thái dead proxy khi tiến trình thoát
func RemoveDeadProxyItem(pid int) {
	RecordDeadProxyState(pid, "", "", false, 0, "")
}

