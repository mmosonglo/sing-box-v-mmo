//go:build windows

package butler

// tryAcquireLeadership: Trên Windows (môi trường dev/test), mặc định là Leader
func tryAcquireLeadership() bool {
	return true
}
