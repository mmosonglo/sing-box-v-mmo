package butler

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

type ClientReport struct {
	IP          string `json:"ip"`
	ActiveConns int32  `json:"active_conns"`
	TotalConns  uint64 `json:"total_conns"`
}

type ButlerStatus struct {
	HardwareProfile        string         `json:"hardware_profile"`
	DynamicLimitPerDevice  int32          `json:"dynamic_limit_per_device"`
	DynamicGuaranteedMin   int32          `json:"dynamic_guaranteed_min"`
	ResourceCritical       bool           `json:"resource_critical"`
	PausedDevices          int32          `json:"paused_devices"`
	IsLeader               bool           `json:"is_leader"`
	UptimeSeconds          int64          `json:"uptime_seconds"`
	TotalActiveConns       int64          `json:"total_active_conns"`
	ActiveClients          int            `json:"active_clients"`
	RouterMemAvailMB       int64          `json:"router_mem_avail_mb"`
	ZramSwapFreeMB         int64          `json:"zram_swap_free_mb"`
	KernelConntrackCount   int64          `json:"kernel_conntrack_count"`
	KernelConntrackMax     int64          `json:"kernel_conntrack_max"`
	MemoryState            string         `json:"memory_state"`
	EmergencyTrims         uint64         `json:"emergency_trims"`
	TopClients             []ClientReport `json:"top_clients"`
}

var startTime = time.Now()

func startReporter() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if IsLeader() {
				writeStatusFile()
			}
		}
	}()
}

func writeStatusFile() {
	var clients []ClientReport

	// Quét NumSlots để thu thập danh sách client có kết nối
	for i := 0; i < NumSlots; i++ {
		slot := &clientSlots[i]
		active := slot.ActiveConns.Load()
		total := slot.TotalConns.Load()
		if active > 0 || total > 0 {
			clients = append(clients, ClientReport{
				IP:          fmt.Sprintf("LAN-Host.%d", i),
				ActiveConns: active,
				TotalConns:  total,
			})
		}
	}

	// Sắp xếp giảm dần theo số kết nối đang hoạt động
	sort.Slice(clients, func(i, j int) bool {
		return clients[i].ActiveConns > clients[j].ActiveConns
	})

	// Giới hạn Top 10 client để giữ file JSON luôn cực nhỏ (< 5KB), không ngốn RAM /tmp
	topCount := len(clients)
	if topCount > 10 {
		topCount = 10
	}
	topClients := clients[:topCount]

	memState := "active"
	if totalActiveConns.Load() == 0 {
		if isDeepCleaned.Load() {
			memState = "deep_idle"
		} else {
			memState = "warm_idle"
		}
	}

	status := ButlerStatus{
		HardwareProfile:       currentProfile.Name,
		DynamicLimitPerDevice: GetEffectiveHardLimit(),
		DynamicGuaranteedMin:  GetEffectiveGuaranteedMin(),
		ResourceCritical:      IsResourceCritical(),
		PausedDevices:         PausedDevicesCount(),
		IsLeader:              IsLeader(),
		UptimeSeconds:         int64(time.Since(startTime).Seconds()),
		TotalActiveConns:      totalActiveConns.Load(),
		ActiveClients:         len(clients),
		RouterMemAvailMB:      lastMemAvailKB.Load() / 1024,
		ZramSwapFreeMB:        lastSwapFreeKB.Load() / 1024,
		KernelConntrackCount:  kernelConntrackCount.Load(),
		KernelConntrackMax:    kernelConntrackMax.Load(),
		MemoryState:           memState,
		EmergencyTrims:        emergencyCount.Load(),
		TopClients:            topClients,
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return
	}

	// Ghi an toàn bằng kỹ thuật Atomic Rename: ghi ra .tmp rồi rename đè lên
	tmpPath := "/tmp/sing-box-butler.json.tmp"
	targetPath := "/tmp/sing-box-butler.json"

	if err := os.WriteFile(tmpPath, data, 0o644); err == nil {
		_ = os.Rename(tmpPath, targetPath)
	}
}
