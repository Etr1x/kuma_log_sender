package sender

import "syscall"

// Коды Winsock. Сравниваем числа, а не текст: на русской Windows сообщения
// об ошибках локализованы.
var (
	errnoRefused     = []syscall.Errno{10061}        // WSAECONNREFUSED
	errnoReset       = []syscall.Errno{10054, 10053} // WSAECONNRESET, WSAECONNABORTED
	errnoUnreachable = []syscall.Errno{10051, 10065, 10050}
	errnoTimeout     = []syscall.Errno{10060} // WSAETIMEDOUT
	errnoMsgSize     = []syscall.Errno{10040} // WSAEMSGSIZE
	errnoDenied      = []syscall.Errno{10013} // WSAEACCES
)
