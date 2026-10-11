//go:build !windows

package sender

import "net"

// На Unix подключённый UDP-сокет получает ICMP-ошибки без дополнительных настроек.
func enableICMPErrors(*net.UDPConn) {}
