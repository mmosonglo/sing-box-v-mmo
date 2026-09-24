//go:build !windows

package butler

// logToSyslog: Silent mode (no logging to syslog)
func logToSyslog(msg string) {
}
