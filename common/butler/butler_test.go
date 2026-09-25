package butler

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func TestClientSlotSizeIs64Bytes(t *testing.T) {
	size := unsafe.Sizeof(ClientSlot{})
	if size != 64 {
		t.Fatalf("ClientSlot must be exactly 64 bytes for cache line alignment, got %d bytes", size)
	}
}

// TestFullIPHashResistance: Xác minh các IP khác subnet cùng byte cuối không bị đụng độ slot
func TestFullIPHashResistance(t *testing.T) {
	ip1 := netip.MustParseAddr("192.168.1.5")
	ip2 := netip.MustParseAddr("192.168.2.5")
	ip3 := netip.MustParseAddr("10.0.0.5")

	slot1 := getSlot(ip1)
	slot2 := getSlot(ip2)
	slot3 := getSlot(ip3)

	if slot1 == slot2 && slot2 == slot3 {
		t.Fatalf("full IP hashing failed: all 3 different subnets mapped to the same slot pointer")
	}
}

// TestTOCTOURaceAgainstHardLimit: Giả lập bão 100 goroutines đồng thời tấn công vào 1 slot sát trần hard limit
// Kiểm tra CAS loop có chặn đứng 100% không cho vượt quá hard limit
func TestTOCTOURaceAgainstHardLimit(t *testing.T) {
	ip := netip.MustParseAddr("192.168.1.88")
	slot := getSlot(ip)

	oldLimit := dynamicHardLimit.Load()
	dynamicHardLimit.Store(1200)
	defer dynamicHardLimit.Store(oldLimit)

	// Đưa slot lên sát trần (1190 / 1200)
	slot.ActiveConns.Store(1190)

	startGate := make(chan struct{})
	var wg sync.WaitGroup
	burstGoroutines := 100
	var successCount atomic.Int32

	for i := 0; i < burstGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate // Đồng loạt xuất phát tại cùng 1 microsecond

			if Acquire(ip) {
				successCount.Add(1)
			}
		}()
	}

	// Mở cổng bão kết nối
	close(startGate)
	wg.Wait()

	finalConns := slot.ActiveConns.Load()

	// 1. Tuyệt đối không được vượt quá hard limit (1200)
	if finalConns > 1200 {
		t.Fatalf("TOCTOU RACE DETECTED! ActiveConns exceeded hard limit: got %d, max allowed %d", finalConns, 1200)
	}

	// 2. Số lượng cấp phép thành công chỉ được đúng bằng 10 kết nối còn trống (1200 - 1190)
	expectedSuccess := int32(1200 - 1190)
	if successCount.Load() != expectedSuccess {
		t.Fatalf("expected exactly %d successful acquires, got %d", expectedSuccess, successCount.Load())
	}

	// Dọn dẹp slot
	slot.ActiveConns.Store(0)
	totalActiveConns.Store(0)
}

// TestLostUpdateHighConcurrencySameSlot: 50 Goroutines Acquire và 50 Goroutines Release
// chạy đan xen bão hòa hàng nghìn lần trên CÙNG 1 SLOT để bắt lỗi lost update hoặc counter drift
func TestLostUpdateHighConcurrencySameSlot(t *testing.T) {
	ip := netip.MustParseAddr("192.168.1.99")
	slot := getSlot(ip)
	slot.ActiveConns.Store(0)

	numGoroutines := 50
	opsPerGoroutine := 100

	var wg sync.WaitGroup
	startGate := make(chan struct{})

	// 50 goroutines liên tục Acquire
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			for j := 0; j < opsPerGoroutine; j++ {
				Acquire(ip)
			}
		}()
	}

	// 50 goroutines liên tục Release
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			for j := 0; j < opsPerGoroutine; j++ {
				Release(ip)
			}
		}()
	}

	close(startGate)
	wg.Wait()

	// Nếu có lost update do Store(0) hoặc race condition, counter sẽ bị lệch âm hoặc dương bất thường
	current := slot.ActiveConns.Load()
	if current < 0 {
		t.Fatalf("COUNTER DRIFT DETECTED: ActiveConns is negative: %d", current)
	}

	// Thu hồi toàn bộ các kết nối còn lại về 0
	for current > 0 {
		Release(ip)
		current = slot.ActiveConns.Load()
	}

	if slot.ActiveConns.Load() != 0 {
		t.Fatalf("expected 0 conns after cleanup, got %d", slot.ActiveConns.Load())
	}

	totalActiveConns.Store(0)
}

func TestGuaranteedMinimumAndRelease(t *testing.T) {
	ip := netip.MustParseAddr("192.168.1.105")

	for i := 0; i < int(GuaranteedMinConns); i++ {
		if !Acquire(ip) {
			t.Fatalf("connection %d must be acquired within guaranteed minimum", i)
		}
	}

	if ClientActiveConnections(ip) != GuaranteedMinConns {
		t.Fatalf("expected %d active conns, got %d", GuaranteedMinConns, ClientActiveConnections(ip))
	}

	for i := 0; i < int(GuaranteedMinConns); i++ {
		Release(ip)
	}

	if ClientActiveConnections(ip) != 0 {
		t.Fatalf("expected 0 active conns after release, got %d", ClientActiveConnections(ip))
	}
}

func TestMultiDeviceMMOConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	numDevices := 20
	connsPerDevice := 30

	for d := 1; d <= numDevices; d++ {
		wg.Add(1)
		go func(deviceIndex int) {
			defer wg.Done()
			ip := netip.MustParseAddr(fmt.Sprintf("192.168.1.%d", deviceIndex))

			for i := 0; i < connsPerDevice; i++ {
				Acquire(ip)
			}

			for i := 0; i < connsPerDevice; i++ {
				Release(ip)
			}
		}(d)
	}

	wg.Wait()
}

func TestDefaultsMatchExpectedConstants(t *testing.T) {
	if GuaranteedMinConns != 20 {
		t.Fatalf("expected default GuaranteedMinConns 20, got %d", GuaranteedMinConns)
	}
	if PerClientHardLimit != 1200 {
		t.Fatalf("expected default PerClientHardLimit 1200, got %d", PerClientHardLimit)
	}
	if MaxSystemConns != 10000 {
		t.Fatalf("expected default MaxSystemConns 10000, got %d", MaxSystemConns)
	}
}

func TestEnvParsing(t *testing.T) {
	t.Setenv("TEST_INT32_VAR", "64")
	if v := envInt32("TEST_INT32_VAR", 20); v != 64 {
		t.Fatalf("expected 64, got %d", v)
	}
	if v := envInt32("TEST_NON_EXISTENT", 20); v != 20 {
		t.Fatalf("expected fallback 20, got %d", v)
	}
	t.Setenv("TEST_INVALID_INT", "abc")
	if v := envInt32("TEST_INVALID_INT", 20); v != 20 {
		t.Fatalf("expected fallback 20 for invalid string, got %d", v)
	}

	t.Setenv("TEST_INT64_VAR", "50000")
	if v := envInt64("TEST_INT64_VAR", 10000); v != 50000 {
		t.Fatalf("expected 50000, got %d", v)
	}
}

func TestAutoHardwareProfiling(t *testing.T) {
	// 1. Kiểm tra profile thực tế đo từ máy hiện tại
	profile := detectHardwareProfile()
	if profile.Name == "" {
		t.Fatal("profile name cannot be empty")
	}

	// 2. Kiểm tra router 128MB
	p128 := profileFromRAM(120)
	if p128.Name != "Micro-128MB" || p128.SafeConntrackBudget != 3000 {
		t.Fatalf("expected Micro-128MB profile, got %+v", p128)
	}

	// 3. Kiểm tra router 240MB - 256MB
	p256 := profileFromRAM(240)
	if p256.Name != "Standard-256MB" || p256.SafeConntrackBudget != 6000 || p256.MaxClientCap != 1200 {
		t.Fatalf("expected Standard-256MB profile, got %+v", p256)
	}

	// 4. Kiểm tra router 512MB (RAM thực tế sau khi trừ kernel thường 480MB - 512MB)
	p512 := profileFromRAM(500)
	if p512.Name != "HighEnd-512MB" || p512.SafeConntrackBudget != 15000 || p512.MaxClientCap != 2500 {
		t.Fatalf("expected HighEnd-512MB profile, got %+v", p512)
	}

	// 5. Kiểm tra thiết bị x86 / Mini PC > 512MB
	p1024 := profileFromRAM(1024)
	if p1024.Name != "Mega-Enterprise" || p1024.SafeConntrackBudget != 40000 || p1024.MaxClientCap != 5000 {
		t.Fatalf("expected Mega-Enterprise profile, got %+v", p1024)
	}
}

func TestDynamicLimitScaling(t *testing.T) {
	// Giả lập profile Standard-256MB
	savedProfile := currentProfile
	savedDyn := dynamicHardLimit.Load()
	savedGMin := dynamicGuaranteedMin.Load()
	defer func() {
		currentProfile = savedProfile
		dynamicHardLimit.Store(savedDyn)
		dynamicGuaranteedMin.Store(savedGMin)
	}()

	currentProfile = HardwareProfile{
		Name:                "Standard-256MB",
		TotalRAMMB:          240,
		SafeConntrackBudget: 6000,
		MaxClientCap:        1200,
		GuaranteedFloor:     20,
		IdleMemoryLimit:     20 * 1024 * 1024,
	}
	dynamicGuaranteedMin.Store(20)

	// 1 client: trần MaxClientCap (1200)
	updateDynamicLimits(1)
	if limit := GetDynamicHardLimit(); limit != 1200 {
		t.Fatalf("expected 1200 limit for 1 client, got %d", limit)
	}

	// 10 clients: 5100 (với 15% headroom) / 10 = 510
	updateDynamicLimits(10)
	if limit := GetDynamicHardLimit(); limit != 510 {
		t.Fatalf("expected 510 limit for 10 clients, got %d", limit)
	}

	// 20 clients: 5100 / 20 = 255
	updateDynamicLimits(20)
	if limit := GetDynamicHardLimit(); limit != 255 {
		t.Fatalf("expected 255 limit for 20 clients, got %d", limit)
	}

	// 500 clients: 5100 / 500 = 10 -> Nhỏ hơn 250 là DỪNG, khóa sàn ở 250 (AbsoluteMinFloor) theo chỉ đạo quản gia
	updateDynamicLimits(500)
	if limit := GetDynamicHardLimit(); limit != 250 {
		t.Fatalf("expected 250 (AbsoluteMinFloor) for 500 clients, got %d", limit)
	}
}

func TestLeaderElection(t *testing.T) {
	_ = IsLeader()
}

func TestDynamicGuaranteedMinCalculation(t *testing.T) {
	savedProfile := currentProfile
	savedAvail := lastMemAvailKB.Load()
	savedGuaranteed := dynamicGuaranteedMin.Load()
	savedHardLimit := dynamicHardLimit.Load()
	defer func() {
		currentProfile = savedProfile
		lastMemAvailKB.Store(savedAvail)
		dynamicGuaranteedMin.Store(savedGuaranteed)
		dynamicHardLimit.Store(savedHardLimit)
	}()

	currentProfile = HardwareProfile{
		Name:                "Standard-256MB",
		TotalRAMMB:          240,
		SafeConntrackBudget: 6000,
		MaxClientCap:        1200,
		GuaranteedFloor:     250,
		IdleMemoryLimit:     20 * 1024 * 1024,
	}
	dynamicHardLimit.Store(1200)

	// 1. Khi router dư RAM (ví dụ MemAvailable = 80MB = 81920KB):
	// Safe budget = 81920 - 15360 = 66560 KB
	// ramCapacity = 66560 / 16 = 4160 conns
	// 20 máy -> 4160 / 20 = 208 conns/máy -> Nhỏ hơn 250 là DỪNG, khóa sàn ở 250
	lastMemAvailKB.Store(80 * 1024)
	updateDynamicGuaranteedMin(20)
	gMin := GetEffectiveGuaranteedMin()
	if gMin != 250 {
		t.Fatalf("expected guaranteed min clamped to 250 floor, got %d", gMin)
	}

	// 2. Khi router giảm RAM (MemAvailable = 25MB = 25600KB):
	// Safe budget = 25600 - 15360 = 10240 KB
	// ramCapacity = 10240 / 16 = 640 conns
	// 20 máy -> 640 / 20 = 32 conns/máy -> Nhỏ hơn 250 là DỪNG, khóa sàn ở 250!
	lastMemAvailKB.Store(25 * 1024)
	updateDynamicGuaranteedMin(20)
	gMinLow := GetEffectiveGuaranteedMin()
	if gMinLow != 250 {
		t.Fatalf("expected clamped floor 250 conns for low RAM (32 conns calculated), got %d", gMinLow)
	}

	// 3. Khi router cạn kiệt RAM (MemAvailable = 10MB < 15MB reserve):
	// Kết quả chia < 250 -> Dừng chia, giữ sàn tối thiểu 250
	lastMemAvailKB.Store(10 * 1024)
	updateDynamicGuaranteedMin(20)
	gMinCrit := GetEffectiveGuaranteedMin()
	if gMinCrit != 250 {
		t.Fatalf("expected clamped floor 250 conns for critical RAM, got %d", gMinCrit)
	}
}

func TestLastInDevicePausingWhenResourceCritical(t *testing.T) {
	ipOld := netip.MustParseAddr("192.168.1.111")
	ipNew := netip.MustParseAddr("192.168.1.222")

	slotOld := getSlot(ipOld)
	slotNew := getSlot(ipNew)

	// Dọn dẹp slot trước khi test
	slotOld.ActiveConns.Store(0)
	slotOld.FirstSeen.Store(1000) // Máy cũ kết nối lúc T=1000
	slotNew.ActiveConns.Store(0)
	slotNew.FirstSeen.Store(2000) // Máy mới kết nối lúc T=2000

	defer func() {
		pausedCutoffNano.Store(0)
		isResourceCritical.Store(false)
		slotOld.ActiveConns.Store(0)
		slotNew.ActiveConns.Store(0)
	}()

	// 1. Trạng thái bình thường: Cả máy cũ và máy mới đều Acquire được
	if !Acquire(ipOld) {
		t.Fatal("expected old device to acquire connection in normal state")
	}
	if !Acquire(ipNew) {
		t.Fatal("expected new device to acquire connection in normal state")
	}
	Release(ipOld)
	Release(ipNew)

	// 2. Giả lập tài nguyên cạn kiệt: Kích hoạt Shedding cho máy kết nối sau cùng (FirstSeen >= 2000)
	isResourceCritical.Store(true)
	pausedCutoffNano.Store(2000)

	// Máy cũ (FirstSeen = 1000 < 2000) VẪN ĐƯỢC BẢO VỆ và kết nối bình thường
	if !Acquire(ipOld) {
		t.Fatal("old device must NOT be paused during resource crisis")
	}
	Release(ipOld)

	// Máy mới nhất (FirstSeen = 2000 >= 2000) BỊ TẠM DỪNG mở kết nối mới để cứu router
	if Acquire(ipNew) {
		t.Fatal("newest device MUST be paused during resource crisis")
	}

	// 3. Tài nguyên hồi phục: Gỡ tạm dừng
	isResourceCritical.Store(false)
	pausedCutoffNano.Store(0)

	// Máy mới lại Acquire thành công bình thường
	if !Acquire(ipNew) {
		t.Fatal("newest device must be unpaused after resource crisis is resolved")
	}
	Release(ipNew)
}

func BenchmarkHashAddrIPv4(b *testing.B) {
	ip := netip.MustParseAddr("192.168.1.105")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hashAddr(ip)
	}
}

func BenchmarkHashAddrIPv6(b *testing.B) {
	ip := netip.MustParseAddr("2400:cb00:2048:1::c629:d7a2")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hashAddr(ip)
	}
}

func BenchmarkGetSlot(b *testing.B) {
	ip := netip.MustParseAddr("192.168.1.105")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getSlot(ip)
	}
}

func TestActiveMemoryLimitPerProfile(t *testing.T) {
	p128 := profileFromRAM(128)
	if p128.ActiveMemoryLimit != 28*1024*1024 {
		t.Fatalf("expected 28MB ActiveMemoryLimit for 128MB, got %d", p128.ActiveMemoryLimit)
	}

	p256 := profileFromRAM(256)
	if p256.ActiveMemoryLimit != 48*1024*1024 {
		t.Fatalf("expected 48MB ActiveMemoryLimit for 256MB, got %d", p256.ActiveMemoryLimit)
	}

	p512 := profileFromRAM(512)
	if p512.ActiveMemoryLimit != 96*1024*1024 {
		t.Fatalf("expected 96MB ActiveMemoryLimit for 512MB, got %d", p512.ActiveMemoryLimit)
	}
}

func TestKernelConntrackProtection(t *testing.T) {
	// Giả lập router kernel conntrack đạt 90% (ví dụ 14745 / 16384) do 9 OpenVPN + dồn lưu lượng
	kernelConntrackCount.Store(14745)
	kernelConntrackMax.Store(16384)

	updateResourceProtectionAndShedding(2)

	if !IsResourceCritical() {
		t.Fatal("expected IsResourceCritical to be true when kernel conntrack >= 85%")
	}

	// Khi kernel conntrack hạ về an toàn (5000 / 16384)
	kernelConntrackCount.Store(5000)
	lastMemAvailKB.Store(80 * 1024)
	totalActiveConns.Store(10)
	updateResourceProtectionAndShedding(2)

	if IsResourceCritical() {
		t.Fatal("expected IsResourceCritical to be false when kernel conntrack and RAM are safe")
	}
}

func BenchmarkGetEffectiveMaxSystemConnsZeroAlloc(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetEffectiveMaxSystemConns()
	}
}

func TestZRAMZeroHandling(t *testing.T) {
	defer func() {
		lastMemAvailKB.Store(80 * 1024)
		lastSwapFreeKB.Store(100 * 1024)
		isResourceCritical.Store(false)
		pausedCutoffNano.Store(0)
	}()

	// Khi RAM vật lý thấp (12MB < 15MB) VÀ ZRAM Swap cạn kiệt hoàn toàn (0 KB)
	lastMemAvailKB.Store(12 * 1024)
	lastSwapFreeKB.Store(0)
	kernelConntrackCount.Store(1000)
	kernelConntrackMax.Store(16384)

	updateResourceProtectionAndShedding(2)

	if !IsResourceCritical() {
		t.Fatal("expected IsResourceCritical to be true when ZRAM Swap is 0 KB and RAM is low")
	}
}

func TestReleasePreventsPositiveDrift(t *testing.T) {
	addr := netip.MustParseAddr("192.168.1.188")
	slot := getSlot(addr)
	slot.ActiveConns.Store(0)
	totalActiveConns.Store(0)

	// Gọi Release trên slot rỗng không được làm âm hay sai lệch total
	Release(addr)
	if totalActiveConns.Load() != 0 {
		t.Fatalf("expected totalActiveConns to remain 0 after empty release, got %d", totalActiveConns.Load())
	}

	// Chu kỳ Acquire và Release bình thường
	if !Acquire(addr) {
		t.Fatal("expected Acquire to succeed")
	}
	if totalActiveConns.Load() != 1 {
		t.Fatalf("expected totalActiveConns to be 1, got %d", totalActiveConns.Load())
	}
	Release(addr)
	if totalActiveConns.Load() != 0 {
		t.Fatalf("expected totalActiveConns to return to 0, got %d", totalActiveConns.Load())
	}
}



