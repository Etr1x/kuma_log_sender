//go:build !windows

package sender

import "syscall"

var (
	errnoRefused     = []syscall.Errno{syscall.ECONNREFUSED}
	errnoReset       = []syscall.Errno{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
	errnoUnreachable = []syscall.Errno{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN}
	errnoTimeout     = []syscall.Errno{syscall.ETIMEDOUT}
	errnoMsgSize     = []syscall.Errno{syscall.EMSGSIZE}
	errnoDenied      = []syscall.Errno{syscall.EACCES, syscall.EPERM}
)
