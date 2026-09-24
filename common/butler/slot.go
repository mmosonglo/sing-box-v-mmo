package butler

import (
	"net/netip"
	"sync/atomic"
)

const (
	// NumSlots: 1024 slots (1024 * 64B = 64KB RAM tĩnh).
	// Phân tán đều và triệt tiêu đụng độ khi có nhiều VLAN, subnet (/24, /16) hoặc IPv6.
	NumSlots = 1024
	SlotMask = NumSlots - 1
)

// ClientSlot đại diện cho 1 thiết bị LAN (được căn chỉnh đúng 64 bytes để chống False Sharing giữa các CPU Core)
type ClientSlot struct {
	TotalConns  atomic.Uint64 // 8 bytes: Offset 0..7 (Căn chỉnh 8-byte tự nhiên trên 32-bit & 64-bit)
	LastActive  atomic.Int64  // 8 bytes: Offset 8..15
	FirstSeen   atomic.Int64  // 8 bytes: Offset 16..23 (Thời điểm máy bắt đầu kết nối vào mạng)
	ActiveConns atomic.Int32  // 4 bytes: Offset 24..27
	IsActive    atomic.Bool   // 4 bytes: Offset 28..31 (atomic.Bool trong Go chứa v uint32)
	_pad        [32]byte      // 32 bytes: Offset 32..63 -> ĐÚNG 64 BYTES CHUẨN CACHE LINE (8+8+8+4+4+32 = 64B)
}

// clientSlots: Bảng tra cứu trực tiếp 1024 slot cố định (Zero Garbage Collection)
var clientSlots [NumSlots]ClientSlot

// hashAddr: Băm FNV-1a 32-bit toàn bộ địa chỉ IP (thực sự Zero Heap Allocation - dùng As4()/As16()
// trả về mảng cố định trên stack, khác với AsSlice() vốn heap-allocate slice mới mỗi lần gọi)
func hashAddr(addr netip.Addr) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	if addr.Is4() {
		b := addr.As4()
		for _, v := range b {
			hash = (hash ^ uint32(v)) * prime32
		}
		return hash
	}
	b := addr.As16()
	for _, v := range b {
		hash = (hash ^ uint32(v)) * prime32
	}
	return hash
}

// getSlot tra cứu slot O(1) lock-free bằng băm toàn bộ địa chỉ IP
func getSlot(addr netip.Addr) *ClientSlot {
	if !addr.IsValid() {
		return &clientSlots[0]
	}
	idx := hashAddr(addr) & SlotMask
	return &clientSlots[idx]
}
