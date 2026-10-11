package sender

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

// tcpSender держит одно соединение на всю задачу и сам переподключается,
// если сервер его закрыл.
type tcpSender struct {
	addr   string
	tlsCfg *tls.Config // nil — без TLS
	opt    Options
	cur    *tcpConn
}

// tcpConn — соединение с фоновым чтением. По TCP успешная запись в сокет,
// уже закрытый удалённой стороной, не возвращает ошибку, и событие молча
// теряется. Постоянное чтение позволяет узнать о закрытии до следующей записи.
type tcpConn struct {
	conn    net.Conn
	dead    atomic.Bool // чтение завершилось: соединение закрыто сервером
	closing atomic.Bool // закрываем сами
	done    chan struct{}
}

func (c *tcpConn) watch() {
	defer close(c.done)
	io.Copy(io.Discard, c.conn)
	if !c.closing.Load() {
		c.dead.Store(true)
	}
}

func (c *tcpConn) close() {
	c.closing.Store(true)
	c.conn.Close()
	<-c.done
}

func (s *tcpSender) connect(ctx context.Context) (string, error) {
	start := time.Now()
	d := net.Dialer{Timeout: s.opt.Timeout}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return "", err
	}
	what := "TCP"
	if s.tlsCfg != nil {
		tc := tls.Client(conn, s.tlsCfg)
		hctx, cancel := context.WithTimeout(ctx, s.opt.Timeout)
		err := tc.HandshakeContext(hctx)
		cancel()
		if err != nil {
			conn.Close()
			return "", fmt.Errorf("TLS-рукопожатие с %s: %w", s.addr, err)
		}
		conn = tc
		what = "TLS " + tlsVersion(tc.ConnectionState().Version)
	}
	c := &tcpConn{conn: conn, done: make(chan struct{})}
	go c.watch()
	s.cur = c
	return fmt.Sprintf("%s-соединение с %s установлено за %s", what, conn.RemoteAddr(), FormatTook(time.Since(start))), nil
}

func (s *tcpSender) drop() {
	if s.cur != nil {
		s.cur.close()
		s.cur = nil
	}
}

func (s *tcpSender) Send(ctx context.Context, payload []byte) (Result, error) {
	start := time.Now()
	var detail string
	for attempt := 0; ; attempt++ {
		if s.cur != nil && s.cur.dead.Load() {
			detail += "сервер закрыл прежнее соединение; "
			s.drop()
		}
		if s.cur == nil {
			d, err := s.connect(ctx)
			if err != nil {
				return Result{Took: time.Since(start)}, err
			}
			detail += d
		}
		s.cur.conn.SetWriteDeadline(time.Now().Add(s.opt.Timeout))
		n, err := s.cur.conn.Write(payload)
		if err == nil {
			return Result{Bytes: n, Took: time.Since(start), Note: detail}, nil
		}
		s.drop()
		// Повторяем один раз и только если не ушло ни байта, иначе событие задвоится.
		if attempt > 0 || n > 0 || ctx.Err() != nil {
			if n > 0 {
				err = fmt.Errorf("записано только %d из %d байт: %w", n, len(payload), err)
			}
			return Result{Bytes: n, Took: time.Since(start)}, err
		}
		detail += fmt.Sprintf("запись не удалась (%v), переподключение; ", err)
	}
}

func (s *tcpSender) Close() error {
	s.drop()
	return nil
}

func tlsVersion(v uint16) string {
	switch v {
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}
