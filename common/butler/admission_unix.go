//go:build !windows

package butler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	runtimeDebug "runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var portSuffixRegex = regexp.MustCompile(`_(\d+)\.json$`)

// resolvePasswallClientInfo tự động tìm MAC hoặc IP của client từ cấu trúc file của Passwall2
func resolvePasswallClientInfo() string {
	// 1. Lấy đường dẫn file config từ tham số dòng lệnh (-c hoặc --config)
	var configPath string
	for i, arg := range os.Args {
		if (arg == "-c" || arg == "--config") && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			break
		}
	}
	if configPath == "" {
		return ""
	}

	// 2. Trích xuất cổng redir ở cuối tên file (vd: vmmo_usa20_TCP_UDP_DNS_11201.json -> 11201)
	base := filepath.Base(configPath)
	matches := portSuffixRegex.FindStringSubmatch(base)
	if len(matches) < 2 {
		return ""
	}
	redirPort := matches[1]

	// 3. Tra cứu RULE_ID trong /tmp/etc/passwall2/var
	varFile, err := os.Open("/tmp/etc/passwall2/var")
	if err != nil {
		return ""
	}
	defer varFile.Close()

	var ruleID string
	targetVal := `="` + redirPort + `"`
	scanner := bufio.NewScanner(varFile)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ACL_") && strings.HasSuffix(line, targetVal) {
			// Format: ACL_cfg1f30ec_redir_port="11201"
			parts := strings.Split(line, "_")
			if len(parts) >= 2 {
				ruleID = parts[1]
				break
			}
		}
	}

	if ruleID == "" {
		return ""
	}

	// 4. Đọc MAC hoặc IP từ /tmp/etc/passwall2/acl/<RULE_ID>/source_list
	sourcePath := filepath.Join("/tmp/etc/passwall2/acl", ruleID, "source_list")
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return ""
	}

	content := strings.TrimSpace(string(data))
	// Chuẩn hóa: bỏ tiền tố "mac:" hoặc "ip:" để LuCI nhận diện sạch sẽ
	content = strings.TrimPrefix(content, "mac:")
	content = strings.TrimPrefix(content, "ip:")
	return content
}

const (
	StartupLockPath       = "/tmp/sing-box-startup.lock"
	EmergencyReserveRAMKB = 8 * 1024                // Lằn ranh đỏ: Luôn giữ 8MB RAM cho Linux Kernel SoftIRQ & OS
	DefaultEstimatedRAMKB = 5 * 1024                // Ước tính ban đầu 5MB RAM cho 1 tiến trình mới
	InitialWaitDuration   = 10 * time.Second        // Chờ nhanh 10 giây ban đầu: nếu RAM phục hồi kịp thì cất cánh ngay
	InitialRetryInterval  = 500 * time.Millisecond  // Trong 10s đầu: kiểm tra lại mỗi 500ms
	StandbySleepInterval  = 2000 * time.Millisecond // Sau 10s: đưa vào hàng đợi ngủ đông và kiểm tra mỗi 2s
)

// AcquireStartupGate: Trạm kiểm soát cất cánh tuần tự & Dự báo an toàn RAM (v-mmo Standby & Smart Adaptive Controller)
// 1. Kiểm tra RAM: Nếu thiếu RAM, chờ thêm 10s (mỗi 500ms thử lại).
// 2. Nếu sau 10s vẫn chưa lên được: Đưa vào hàng chờ ngủ đông (Standby Queue) và kiểm tra định kỳ mỗi 2s.
// 3. Khi router đủ RAM an toàn -> Tự động thức tỉnh, rời hàng chờ và nạp cấu hình cất cánh!
// 4. Nhịp cất cánh thông minh (Smart Adaptive Cooldown):
//    - RAM dồi dào (>60MB) & ZRAM rảnh: 100ms
//    - RAM an toàn (30-60MB): 500ms
//    - RAM căng (15-30MB): 2s
//    - RAM sát nút (<15MB): 4s
func AcquireStartupGate(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(StartupLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, nil
	}

	pid := os.Getpid()

	// 1. Xếp hàng tuần tự bằng khóa POSIX flock độc quyền
	// Các tiến trình vào sau sẽ ngủ nhẹ ở tầng OS (CPU 0%) đợi đến lượt mình
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}, nil
	}

	// 2. Tự khảo sát RAM các anh em sing-box đang chạy để tính mức RAM trung bình
	estimatedCostKB, _ := surveySingBoxMemoryCost()

	// 3. Kiểm tra và Dự báo RAM khả dụng
	startTime := time.Now()
	inStandby := false
	for {
		availKB, _, _, _ := readMemAndSwapKB()
		if availKB <= 0 {
			// Không đọc được /proc/meminfo -> cho phép chạy an toàn
			break
		}

		predictedRemainingKB := availKB - estimatedCostKB
		if predictedRemainingKB >= EmergencyReserveRAMKB {
			// Đủ RAM an toàn -> Nếu trước đó đang trong hàng đợi thì báo thức tỉnh
			if inStandby {
				clientInfo := resolvePasswallClientInfo()
				RecordStandbyState(pid, clientInfo, "", "waking", "Đủ RAM, tiến trình đang thức tỉnh và nạp cấu hình...")
			}
			break
		}

		// Thiếu RAM:
		// Trong 10 giây đầu: chờ nhanh nhường CPU, chưa vội đưa vào bảng hàng đợi ngủ đông
		// Quá 10 giây: chính thức đưa vào hàng đợi ngủ đông (Standby Queue)
		sleepDuration := InitialRetryInterval
		if time.Since(startTime) >= InitialWaitDuration {
			inStandby = true
			clientInfo := resolvePasswallClientInfo()
			detailMsg := fmt.Sprintf("RAM khả dụng còn %dMB, cần %dMB (+8MB đệm). Đang ngủ chờ RAM...", availKB/1024, estimatedCostKB/1024)
			RecordStandbyState(pid, clientInfo, "", "waiting", detailMsg)
			sleepDuration = StandbySleepInterval
		}

		// KHỬ HEAD-OF-LINE BLOCKING: Nhả khóa trước khi ngủ để các tiến trình khác hoặc OS không bị nghẽn
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

		// Thu hồi Heap tối đa về OS trước khi ngủ để tiết kiệm từng byte RAM
		runtime.GC()
		runtimeDebug.FreeOSMemory()

		select {
		case <-ctx.Done():
			RemoveStandbyItem(pid)
			f.Close()
			return func() {}, ctx.Err()
		case <-time.After(sleepDuration):
			// Thức dậy kiểm tra lại RAM
		}

		// Lấy lại khóa độc quyền để kiểm tra lại
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			RemoveStandbyItem(pid)
			f.Close()
			return func() {}, nil
		}
	}

	// 4. Trả về hàm unlockGate để dọn rác và giãn cách thông minh trước khi nhả khóa cho node kế tiếp
	unlocked := false
	unlockGate := func() {
		if unlocked {
			return
		}
		unlocked = true

		// Đã cất cánh thành công -> gỡ khỏi hàng chờ thức tỉnh
		RemoveStandbyItem(pid)

		// Thu dọn ngay rác bộ nhớ phát sinh trong quá trình nạp JSON / biên dịch routing
		runtime.GC()
		runtimeDebug.FreeOSMemory()

		// Cho phép bộ đếm instance được làm mới
		cachedInstanceCount.Store(0)

		// Giãn cách nhịp thở thông minh (Smart Adaptive Cooldown):
		// - Còn nhiều RAM (>60MB) & ZRAM thoải mái: Lướt nhanh (100ms) để cất cánh tức thì.
		// - RAM trung bình (30MB - 60MB): Giãn nhẹ (500ms).
		// - RAM bắt đầu eo hẹp (<30MB) hoặc ZRAM thấp: Delay (2s - 4s) cho ZRAM nén các trang tĩnh vào swap trước khi cho node sau vào.
		curAvailKB, curSwapFreeKB, _, _ := readMemAndSwapKB()
		var cooldown time.Duration
		switch {
		case curAvailKB <= 0:
			cooldown = 200 * time.Millisecond
		case curAvailKB >= 60*1024 && curSwapFreeKB >= 100*1024:
			// Dồi dào RAM: Cất cánh siêu tốc
			cooldown = 100 * time.Millisecond
		case curAvailKB >= 30*1024:
			// RAM an toàn: Nhịp thở nhẹ nhàng
			cooldown = 500 * time.Millisecond
		case curAvailKB >= 15*1024:
			// RAM bắt đầu căng: Đợi 2 giây để Linux nén ZRAM & cập nhật meminfo
			cooldown = 2 * time.Second
		default:
			// RAM sát lằn ranh đỏ (<15MB): Đợi tối đa 4 giây để bảo vệ router
			cooldown = 4 * time.Second
		}
		time.Sleep(cooldown)

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
