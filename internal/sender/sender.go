// Package sender доставляет байты события на коллектор KUMA по tcp, udp или http.
// Всё реализовано стандартной библиотекой Go — внешние ncat/curl/libssl не нужны.
package sender

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

// Options — параметры отправки, общие для всех протоколов.
type Options struct {
	Timeout time.Duration
	// Notify получает сообщения, не привязанные к конкретной отправке: сервер
	// закрыл соединение, пришла ICMP-ошибка на UDP-датаграмму и т. п.
	// Вызывается из фоновой горутины. Может быть nil.
	Notify func(warn bool, msg string)
}

func (o Options) notify(warn bool, format string, args ...any) {
	if o.Notify != nil {
		o.Notify(warn, fmt.Sprintf(format, args...))
	}
}

// Result описывает успешную отправку.
type Result struct {
	Bytes  int
	Took   time.Duration
	Note   string // событие транспорта для отдельной строки журнала: «соединение установлено…»
	Status string // ответ сервера, если он есть: «HTTP 200 OK»
}

// Sender отправляет готовые к передаче данные (событие вместе с разделителем).
// Не предназначен для одновременного использования из нескольких горутин.
type Sender interface {
	Send(ctx context.Context, payload []byte) (Result, error)
	Close() error
}

// New создаёт отправителя под протокол коллектора. Соединение устанавливается
// лениво, при первой отправке.
func New(c config.Collector, defaultHost string, opt Options) (Sender, error) {
	if opt.Timeout <= 0 {
		opt.Timeout = config.DefaultTimeoutSec * time.Second
	}
	addr, err := c.Addr(defaultHost)
	if err != nil {
		return nil, err
	}
	var tlsCfg *tls.Config
	if c.TLS {
		tlsCfg = &tls.Config{
			ServerName:         c.ResolveHost(defaultHost),
			InsecureSkipVerify: !c.TLSVerify, // у коллекторов KUMA обычно самоподписанные сертификаты
			MinVersion:         tls.VersionTLS12,
		}
	}
	switch c.Protocol {
	case config.TCP:
		return &tcpSender{addr: addr, tlsCfg: tlsCfg, opt: opt}, nil
	case config.UDP:
		return &udpSender{addr: addr, opt: opt}, nil
	case config.HTTP:
		return newHTTPSender(c, addr, tlsCfg, opt), nil
	}
	return nil, fmt.Errorf("неизвестный протокол %q", c.Protocol)
}

// Frame склеивает событие с разделителем коллектора.
func Frame(event, delim []byte) []byte {
	out := make([]byte, 0, len(event)+len(delim))
	out = append(out, event...)
	return append(out, delim...)
}

// FormatTook выводит длительность отправки в миллисекундах.
func FormatTook(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 мс"
	}
	return fmt.Sprintf("%d мс", d.Milliseconds())
}
