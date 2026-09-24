package butler

import (
	"os"
	"strconv"
)

// Các biến ngưỡng của Governor - MẶC ĐỊNH giữ nguyên giá trị chuẩn an toàn,
// nhưng có thể ghi đè linh hoạt bằng biến môi trường để phù hợp từng môi trường
// (nhà ít thiết bị nới rộng hơn, nhà đông thiết bị siết chặt hơn) mà không cần build lại binary.
var (
	envMaxSystemConns  int64 = envInt64("BUTLER_MAX_SYSTEM_CONNS", 0)
	// GuaranteedMinConns: ghi đè bằng BUTLER_GUARANTEED_MIN (mặc định 20)
	GuaranteedMinConns int32 = envInt32("BUTLER_GUARANTEED_MIN", 20)

	// PerClientHardLimit: ghi đè bằng BUTLER_HARD_LIMIT (mặc định 1200)
	PerClientHardLimit int32 = envInt32("BUTLER_HARD_LIMIT", 1200)

	// MaxSystemConns: ghi đè bằng BUTLER_MAX_SYSTEM_CONNS (mặc định 10000)
	MaxSystemConns int64 = envInt64("BUTLER_MAX_SYSTEM_CONNS", 10000)
)

// GetEffectiveMaxSystemConns trả về trần tổng kết nối an toàn (Zero Allocation on Hot Path)
func GetEffectiveMaxSystemConns() int64 {
	if envMaxSystemConns > 0 {
		return envMaxSystemConns
	}
	if currentProfile.SafeConntrackBudget > 0 {
		return currentProfile.SafeConntrackBudget
	}
	return MaxSystemConns
}

func envInt32(key string, fallback int32) int32 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n <= 0 {
		return fallback
	}
	return int32(n)
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
