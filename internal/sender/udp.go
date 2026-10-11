package sender

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

// MaxUDPPayload — максимальный размер данных в одной UDP-датаграмме (IPv4).
const MaxUDPPayload = 65507

// icmpGrace — сколько ждать ICMP-ответа после последней датаграммы перед
// закрытием сокета: иначе при разовой отправке «порт недоступен» не успеет прийти.
const icmpGrace = 150 * time.Millisecond

// udpSender шлёт каждое событие отдельной датаграммой через «подключённый»
// сокет. UDP не подтверждает доставку, но подключённый сокет позволяет получить
// хотя бы ICMP port unreachable, если на той стороне никто не слушает порт.
type udpSender struct {
	addr     string
	opt      Options
	conn     *net.UDPConn
	closing  atomic.Bool
	refused  atomic.Bool
	done     chan struct{}
	lastSend time.Time
}

func (s *udpSender) connect(ctx context.Context) (string, error) {
	d := net.Dialer{Timeout: s.opt.Timeout}
	conn, err := d.DialContext(ctx, "udp", s.addr)
	if err != nil {
		return "", err
	}
	s.conn = conn.(*net.UDPConn)
	enableICMPErrors(s.conn)
	s.done = make(chan struct{})
	go s.watch()
	return fmt.Sprintf("UDP-сокет %s → %s", conn.LocalAddr(), conn.RemoteAddr()), nil
}

// watch читает сокет: коллектор ничего не отвечает, поэтому единственное, что
// сюда может прийти, — ошибка, порождённая ICMP-сообщением.
func (s *udpSender) watch() {
	defer close(s.done)
	buf := make([]byte, 2048)
	for i := 0; i < 1000; i++ {
		_, err := s.conn.Read(buf)
		if s.closing.Load() {
			return
		}
		if err == nil {
			continue
		}
		if !s.refused.Swap(true) {
			s.opt.notify(true, "UDP %s: %s", s.addr, Describe(err, config.UDP))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *udpSender) Send(ctx context.Context, payload []byte) (Result, error) {
	start := time.Now()
	if len(payload) > MaxUDPPayload {
		return Result{}, fmt.Errorf("событие %d байт не помещается в UDP-датаграмму (максимум %d): %w",
			len(payload), MaxUDPPayload, errTooLong)
	}
	var detail string
	if s.conn == nil {
		d, err := s.connect(ctx)
		if err != nil {
			return Result{Took: time.Since(start)}, err
		}
		detail = d
	}
	s.conn.SetWriteDeadline(time.Now().Add(s.opt.Timeout))
	n, err := s.conn.Write(payload)
	s.lastSend = time.Now()
	if err != nil {
		return Result{Took: time.Since(start)}, err
	}
	return Result{Bytes: n, Took: time.Since(start), Note: detail}, nil
}

func (s *udpSender) Close() error {
	if s.conn == nil {
		return nil
	}
	if wait := icmpGrace - time.Since(s.lastSend); wait > 0 && !s.refused.Load() {
		time.Sleep(wait)
	}
	s.closing.Store(true)
	err := s.conn.Close()
	<-s.done
	s.conn = nil
	return err
}
