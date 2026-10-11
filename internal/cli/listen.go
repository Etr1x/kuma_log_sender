package cli

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

// Listen — простой приёмник, изображающий коннектор коллектора. Нужен, чтобы без
// KUMA убедиться, что события уходят и как именно они выглядят «на проводе»:
// каждое принятое событие печатается с экранированием управляющих символов.
func Listen(args []string) int {
	fs := newFlags("listen", `Тестовый приёмник: слушает порт и печатает всё, что пришло.

Примеры:
  kuma_log_sender listen -p udp -P 5144
  kuma_log_sender listen -p tcp -P 5140 -d "\n"
  kuma_log_sender listen -p http -P 5365 -tls -d "\0"

Параметры:
`)
	var proto, addr, delim string
	var port int
	var useTLS bool
	for _, n := range []string{"p", "proto"} {
		fs.StringVar(&proto, n, "udp", "протокол: tcp, udp или http")
	}
	for _, n := range []string{"P", "port"} {
		fs.IntVar(&port, n, 0, "порт")
	}
	for _, n := range []string{"a", "addr"} {
		fs.StringVar(&addr, n, "127.0.0.1", "адрес интерфейса; 0.0.0.0 — принимать с других машин")
	}
	for _, n := range []string{"d", "delimiter"} {
		fs.StringVar(&delim, n, "", `разделитель событий, как в коннекторе: \n, \t, \0 или пусто (для tcp по умолчанию \n)`)
	}
	fs.BoolVar(&useTLS, "tls", false, "включить TLS с самоподписанным сертификатом (tcp и http)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "Ошибка: "+format+"\n", args...)
		return 2
	}
	if port < 1 || port > 65535 {
		return fail("укажите порт: -P 5144")
	}
	p := config.Protocol(proto)
	d, err := config.ParseDelimiter(delim)
	if err != nil {
		return fail("%v", err)
	}
	if delim == "" {
		d = config.DefaultDelimiter(p)
	}
	listenAddr := net.JoinHostPort(addr, strconv.Itoa(port))
	var tlsCfg *tls.Config
	if useTLS {
		if p == config.UDP {
			return fail("TLS для udp не поддерживается")
		}
		cert, err := selfSigned()
		if err != nil {
			return fail("сертификат: %v", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	out := &printer{}
	what := proto
	if useTLS {
		what += "+tls"
	}
	switch p {
	case config.UDP:
		pc, err := net.ListenPacket("udp", listenAddr)
		if err != nil {
			return fail("%v", err)
		}
		defer pc.Close()
		go func() {
			buf := make([]byte, 65536)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				out.events("udp", from.String(), buf[:n], d.Bytes(), true)
			}
		}()
	case config.TCP:
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return fail("%v", err)
		}
		if tlsCfg != nil {
			ln = tls.NewListener(ln, tlsCfg)
		}
		defer ln.Close()
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go serveTCP(conn, d.Bytes(), out)
			}
		}()
	case config.HTTP:
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return fail("%v", err)
		}
		if tlsCfg != nil {
			ln = tls.NewListener(ln, tlsCfg)
		}
		defer ln.Close()
		srv := &http.Server{
			ErrorLog: nil,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				out.line("%s %s %s (%s)", r.RemoteAddr, r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				if r.URL.Path != config.DefaultHTTPPath {
					out.line("  путь не %s — настоящий коннектор KUMA такое событие не примет", config.DefaultHTTPPath)
				}
				out.events("http", r.RemoteAddr, body, d.Bytes(), true)
			}),
		}
		go srv.Serve(ln)
	default:
		return fail("неизвестный протокол %q (допустимо: tcp, udp, http)", proto)
	}

	fmt.Printf("Слушаю %s://%s, разделитель: %s. Остановка — Ctrl+C.\n", what, listenAddr, d.Label())
	waitInterrupt()
	fmt.Printf("\nПринято событий: %d\n", out.count)
	return 0
}

// printer сериализует вывод из нескольких соединений.
type printer struct {
	mu    sync.Mutex
	count int
}

func (p *printer) line(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Printf(stamp()+" "+format+"\n", args...)
}

// events печатает данные, разбивая их по разделителю, как это сделал бы коннектор.
// final=false означает, что хвост без разделителя ещё может быть дополнен.
func (p *printer) events(proto, from string, data, delim []byte, final bool) (rest []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := [][]byte{data}
	if len(delim) > 0 {
		parts = bytes.Split(data, delim)
		last := parts[len(parts)-1]
		parts = parts[:len(parts)-1]
		if len(last) > 0 {
			if !final {
				rest = append([]byte(nil), last...)
			} else {
				parts = append(parts, last)
				defer fmt.Printf("%s   └ у последнего события нет разделителя в конце\n", stamp())
			}
		}
	}
	for _, ev := range parts {
		if len(ev) == 0 {
			continue
		}
		p.count++
		fmt.Printf("%s #%d %s %s %d Б: %s\n", stamp(), p.count, proto, from, len(ev), strconv.QuoteToGraphic(string(ev)))
	}
	return rest
}

func serveTCP(conn net.Conn, delim []byte, out *printer) {
	defer conn.Close()
	from := conn.RemoteAddr().String()
	out.line("%s подключился", from)
	r := bufio.NewReader(conn)
	buf := make([]byte, 64*1024)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = out.events("tcp", from, append(pending, buf[:n]...), delim, len(delim) == 0)
		}
		if err != nil {
			if len(pending) > 0 {
				out.events("tcp", from, pending, delim, true)
			}
			if err != io.EOF {
				out.line("%s: %v", from, err)
			}
			out.line("%s отключился", from)
			return
		}
	}
}

// selfSigned выпускает сертификат на лету — как самоподписанный сертификат коллектора.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kuma_log_sender test listener"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
