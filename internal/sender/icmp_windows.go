package sender

import (
	"net"
	"syscall"
	"unsafe"
)

// enableICMPErrors включает SIO_UDP_CONNRESET, который Go по умолчанию выключает:
// без него Windows не сообщает сокету об ICMP port unreachable.
func enableICMPErrors(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	raw.Control(func(fd uintptr) {
		flag := uint32(1)
		var ret uint32
		syscall.WSAIoctl(syscall.Handle(fd), syscall.SIO_UDP_CONNRESET,
			(*byte)(unsafe.Pointer(&flag)), uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
	})
}
