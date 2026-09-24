//go:build windows

package butler

// logToSyslog: No-op trên môi trường Windows
func logToSyslog(msg string) {
}
