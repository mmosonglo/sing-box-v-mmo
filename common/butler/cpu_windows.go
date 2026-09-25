//go:build windows

package butler

func readCPUStats() (usagePercent int, loadAvg string, tempC int) {
	return 0, "0.00 0.00 0.00", 0
}
