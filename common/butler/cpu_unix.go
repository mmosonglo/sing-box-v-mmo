//go:build !windows

package butler

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

var (
	lastCPUTotal atomic.Uint64
	lastCPUIdle  atomic.Uint64
)

// readCPUStats: Đọc mức sử dụng CPU (%), Load Average và Nhiệt độ CPU
func readCPUStats() (usagePercent int, loadAvg string, tempC int) {
	// 1. Đọc Load Average từ /proc/loadavg
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			loadAvg = fields[0] + " " + fields[1] + " " + fields[2]
		}
	}

	// 2. Đọc CPU % từ /proc/stat
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if bytes.HasPrefix(line, []byte("cpu ")) {
				fields := strings.Fields(string(line))
				// fields: cpu user nice system idle iowait irq softirq steal guest guest_nice
				if len(fields) >= 5 {
					var total uint64
					var idle uint64
					for i := 1; i < len(fields); i++ {
						val, _ := strconv.ParseUint(fields[i], 10, 64)
						total += val
						if i == 4 || i == 5 { // idle, iowait
							idle += val
						}
					}

					prevTotal := lastCPUTotal.Swap(total)
					prevIdle := lastCPUIdle.Swap(idle)

					if prevTotal > 0 && total > prevTotal {
						diffTotal := total - prevTotal
						diffIdle := idle - prevIdle
						if diffTotal > diffIdle {
							usagePercent = int((diffTotal - diffIdle) * 100 / diffTotal)
							if usagePercent > 100 {
								usagePercent = 100
							}
						}
					}
				}
				break
			}
		}
	}

	// 3. Đọc Nhiệt độ CPU (°C) nếu phần cứng hỗ trợ
	tempFiles := []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/hwmon/hwmon0/temp1_input",
	}
	for _, tf := range tempFiles {
		if data, err := os.ReadFile(tf); err == nil {
			tVal, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			if err == nil && tVal > 0 {
				if tVal > 1000 {
					tempC = int(tVal / 1000)
				} else {
					tempC = int(tVal)
				}
				break
			}
		}
	}

	return usagePercent, loadAvg, tempC
}
