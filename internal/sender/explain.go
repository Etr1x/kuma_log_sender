package sender

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

var errTooLong = errors.New("слишком длинное событие")

// errnoIs сообщает, есть ли в цепочке ошибок системный код из списка.
func errnoIs(err error, codes ...syscall.Errno) bool {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return false
	}
	for _, c := range codes {
		if en == c {
			return true
		}
	}
	return false
}

// Explain возвращает короткую подсказку о вероятной причине ошибки отправки —
// то, что стоит проверить в первую очередь. Пустая строка, если сказать нечего.
func Explain(err error, proto config.Protocol) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	text := err.Error()

	var hs *HTTPStatusError
	if errors.As(err, &hs) {
		switch {
		case strings.Contains(hs.Body, "HTTPS server"):
			return "коллектор ждёт HTTPS: включите TLS в параметрах коллектора"
		case hs.Code == 404:
			return "неверный путь: у http-коннектора KUMA это обычно /input"
		case hs.Code == 405:
			return "метод POST не принят: проверьте путь и что порт принадлежит http-коннектору"
		case hs.Code == 401 || hs.Code == 403:
			return "доступ запрещён на стороне сервера"
		case hs.Code >= 300 && hs.Code < 400:
			return "сервер перенаправляет запрос: проверьте адрес, порт и путь"
		case hs.Code >= 500:
			return "ошибка на стороне коллектора: смотрите его журнал"
		}
		return "коллектор не принял событие"
	}

	if errors.Is(err, errTooLong) || errnoIs(err, errnoMsgSize...) {
		return "UDP не передаст такое событие: используйте tcp- или http-коллектор"
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "имя хоста не разрешается в адрес: проверьте имя и доступность DNS"
	}

	var rh tls.RecordHeaderError
	if errors.As(err, &rh) || strings.Contains(text, "server gave HTTP response to HTTPS client") {
		return "сервер отвечает без TLS: отключите TLS в параметрах коллектора"
	}
	var certErr *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if errors.As(err, &certErr) || errors.As(err, &unknownCA) || errors.As(err, &hostErr) {
		return "сертификат сервера не прошёл проверку: отключите «Проверять сертификат» или добавьте УЦ в доверенные"
	}

	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errnoIs(err, errnoTimeout...) || (errors.As(err, &ne) && ne.Timeout()) {
		return "нет ответа за отведённое время: хост недоступен, порт фильтруется файрволом или указан не тот адрес"
	}

	switch {
	case errnoIs(err, errnoRefused...):
		if proto == config.UDP {
			return "пришёл ICMP port unreachable: на хосте никто не слушает этот UDP-порт"
		}
		return "порт закрыт: коллектор не запущен, слушает другой порт или подключение отклоняет файрвол"
	case errnoIs(err, errnoReset...):
		if proto == config.UDP {
			return "пришёл ICMP-отказ: на хосте никто не слушает этот UDP-порт или его закрывает файрвол"
		}
		return "соединение разорвано удалённой стороной: проверьте, совпадает ли режим TLS с настройкой коннектора"
	case errnoIs(err, errnoUnreachable...):
		return "нет маршрута до хоста: проверьте адрес, VPN и маршрутизацию"
	case errnoIs(err, errnoDenied...):
		return "отправку запретила локальная система: проверьте файрвол на этой машине"
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(text, "malformed HTTP response") {
		if proto == config.HTTP {
			return "сервер не ответил по HTTP: возможно, он ждёт TLS или порт принадлежит не http-коннектору"
		}
		return "сервер закрыл соединение: проверьте, совпадает ли режим TLS с настройкой коннектора"
	}
	if strings.Contains(text, "tls:") {
		return "ошибка TLS: проверьте режим TLS коннектора (KUMA поддерживает TLS 1.2 и 1.3)"
	}
	return ""
}

// Describe склеивает текст ошибки с подсказкой.
func Describe(err error, proto config.Protocol) string {
	if err == nil {
		return ""
	}
	if hint := Explain(err, proto); hint != "" {
		return err.Error() + " — " + hint
	}
	return err.Error()
}
