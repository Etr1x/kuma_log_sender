package sender

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

var testOpt = Options{Timeout: 2 * time.Second}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return host, port
}

// tcpSink принимает соединения и складывает всё прочитанное по каждому из них.
type tcpSink struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []*strings.Builder
	// closeAfter > 0: закрыть соединение со стороны сервера, прочитав столько байт.
	closeAfter int
}

func newTCPSink(t *testing.T, ln net.Listener) *tcpSink {
	s := &tcpSink{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			b := &strings.Builder{}
			s.conns = append(s.conns, b)
			limit := s.closeAfter
			s.mu.Unlock()
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				read := 0
				for {
					n, err := c.Read(buf)
					s.mu.Lock()
					b.Write(buf[:n])
					s.mu.Unlock()
					read += n
					if err != nil || (limit > 0 && read >= limit) {
						return
					}
				}
			}()
		}
	}()
	return s
}

func (s *tcpSink) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.conns))
	for i, b := range s.conns {
		out[i] = b.String()
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTCPOneConnectionWithDelimiter(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	sink := newTCPSink(t, ln)
	host, port := hostPort(t, ln.Addr().String())
	c := config.Collector{Name: "t", Protocol: config.TCP, Host: host, Port: port, Delimiter: config.DelimLF}

	s, err := New(c, "", testOpt)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range []string{"один", "two", "3"} {
		res, err := s.Send(context.Background(), Frame([]byte(ev), c.Delimiter.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if (i == 0) != (res.Note != "") {
			t.Errorf("отправка %d: сообщение о соединении ожидается только при первой: %q", i, res.Note)
		}
	}
	s.Close()
	waitFor(t, "данные на сервере", func() bool {
		g := sink.got()
		return len(g) == 1 && g[0] == "один\ntwo\n3\n"
	})
}

func TestTCPReconnectsWhenServerCloses(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	sink := newTCPSink(t, ln)
	sink.closeAfter = 1
	host, port := hostPort(t, ln.Addr().String())
	c := config.Collector{Name: "t", Protocol: config.TCP, Host: host, Port: port, Delimiter: config.DelimNUL}

	s, _ := New(c, "", testOpt)
	defer s.Close()
	if _, err := s.Send(context.Background(), []byte("a\x00")); err != nil {
		t.Fatal(err)
	}
	// Сервер закрывает соединение после первого события; ждём, пока клиент это заметит.
	ts := s.(*tcpSender)
	waitFor(t, "обнаружение закрытия", func() bool { return ts.cur.dead.Load() })
	res, err := s.Send(context.Background(), []byte("b\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Note, "закрыл") {
		t.Errorf("в пояснении должно быть сказано о переподключении: %q", res.Note)
	}
	waitFor(t, "второе соединение", func() bool {
		g := sink.got()
		return len(g) == 2 && g[0] == "a\x00" && g[1] == "b\x00"
	})
}

func TestTCPRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port := hostPort(t, ln.Addr().String())
	ln.Close()
	s, _ := New(config.Collector{Name: "t", Protocol: config.TCP, Host: host, Port: port}, "", testOpt)
	defer s.Close()
	_, err := s.Send(context.Background(), []byte("x\n"))
	if err == nil {
		t.Fatal("ожидалась ошибка подключения")
	}
	if hint := Explain(err, config.TCP); !strings.Contains(hint, "порт закрыт") {
		t.Errorf("подсказка для %v: %q", err, hint)
	}
}

func TestTCPWithTLS(t *testing.T) {
	// Сертификат берём у httptest — он самоподписанный, как обычно и бывает у коллекторов.
	hs := httptest.NewUnstartedServer(nil)
	hs.StartTLS()
	cert := hs.TLS.Certificates[0]
	hs.Close()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	sink := newTCPSink(t, ln)
	host, port := hostPort(t, ln.Addr().String())
	c := config.Collector{Name: "t", Protocol: config.TCP, Host: host, Port: port, Delimiter: config.DelimLF, TLS: true}

	s, _ := New(c, "", testOpt)
	res, err := s.Send(context.Background(), []byte("secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Note, "TLS 1.3") {
		t.Errorf("ожидалось упоминание TLS в %q", res.Note)
	}
	s.Close()
	waitFor(t, "данные по TLS", func() bool { g := sink.got(); return len(g) == 1 && g[0] == "secret\n" })

	// С включённой проверкой самоподписанный сертификат должен отвергаться с понятной подсказкой.
	c.TLSVerify = true
	s, _ = New(c, "", testOpt)
	defer s.Close()
	_, err = s.Send(context.Background(), []byte("x\n"))
	if err == nil || !strings.Contains(Explain(err, config.TCP), "сертификат") {
		t.Errorf("проверка сертификата: %v → %q", err, Explain(err, config.TCP))
	}
}

func TestTLSClientAgainstPlainServer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Обычный текстовый сервис: отвечает не по TLS.
			go func() {
				c.Write([]byte("220 hello, plain text here\r\n"))
				time.Sleep(200 * time.Millisecond)
				c.Close()
			}()
		}
	}()
	host, port := hostPort(t, ln.Addr().String())
	s, _ := New(config.Collector{Name: "t", Protocol: config.TCP, Host: host, Port: port, TLS: true}, "", testOpt)
	defer s.Close()
	_, err := s.Send(context.Background(), []byte("x\n"))
	if err == nil || !strings.Contains(Explain(err, config.TCP), "отключите TLS") {
		t.Errorf("TLS к серверу без TLS: %v → %q", err, Explain(err, config.TCP))
	}
}

func TestUDPDatagramPerEvent(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	host, port := hostPort(t, pc.LocalAddr().String())
	c := config.Collector{Name: "u", Protocol: config.UDP, Host: host, Port: port, Delimiter: config.DelimNUL}

	s, _ := New(c, "", testOpt)
	for _, ev := range []string{"first", "второе\nсобытие"} {
		if _, err := s.Send(context.Background(), Frame([]byte(ev), c.Delimiter.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 65536)
	for _, want := range []string{"first\x00", "второе\nсобытие\x00"} {
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatalf("датаграмма: %q, %v; ожидали %q", buf[:n], err, want)
		}
	}
	s.Close()

	big := make([]byte, MaxUDPPayload+1)
	s, _ = New(c, "", testOpt)
	defer s.Close()
	if _, err := s.Send(context.Background(), big); err == nil || !strings.Contains(Explain(err, config.UDP), "UDP не передаст") {
		t.Errorf("слишком большое событие: %v", err)
	}
}

// На закрытый UDP-порт локальная машина отвечает ICMP port unreachable —
// утилита должна сообщить об этом, а не молча считать событие доставленным.
func TestUDPPortUnreachableIsReported(t *testing.T) {
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	host, port := hostPort(t, pc.LocalAddr().String())
	pc.Close()

	var mu sync.Mutex
	var warnings []string
	opt := Options{Timeout: 2 * time.Second, Notify: func(warn bool, msg string) {
		mu.Lock()
		defer mu.Unlock()
		if warn {
			warnings = append(warnings, msg)
		}
	}}
	s, _ := New(config.Collector{Name: "u", Protocol: config.UDP, Host: host, Port: port}, "", opt)
	_, err := s.Send(context.Background(), []byte("x"))
	s.Close()

	mu.Lock()
	defer mu.Unlock()
	if err == nil && len(warnings) == 0 {
		t.Skip("ОС не вернула ICMP-ошибку для UDP (зависит от настроек файрвола)")
	}
	t.Logf("err=%v warnings=%q", err, warnings)
	if len(warnings) > 0 && !strings.Contains(warnings[0], "никто не слушает") {
		t.Errorf("непонятное предупреждение: %q", warnings[0])
	}
}

type httpSink struct {
	mu     sync.Mutex
	bodies []string
	paths  []string
	ctype  string
}

func (h *httpSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(body))
	h.paths = append(h.paths, r.Method+" "+r.URL.Path)
	h.ctype = r.Header.Get("Content-Type")
	h.mu.Unlock()
	if r.URL.Path != "/input" {
		http.NotFound(w, r)
	}
}

func TestHTTP(t *testing.T) {
	sink := &httpSink{}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	host, port := hostPort(t, srv.Listener.Addr().String())
	c := config.Collector{Name: "h", Protocol: config.HTTP, Host: host, Port: port, Delimiter: config.DelimNUL}
	c.Normalize()

	s, _ := New(c, "", testOpt)
	defer s.Close()
	res, err := s.Send(context.Background(), Frame([]byte("<Event>\n</Event>"), c.Delimiter.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "HTTP 200 OK" || res.Bytes != len("<Event>\n</Event>")+1 {
		t.Errorf("результат: %+v", res)
	}
	if sink.bodies[0] != "<Event>\n</Event>\x00" || sink.paths[0] != "POST /input" || sink.ctype != "text/plain" {
		t.Errorf("сервер получил %q %q %q", sink.bodies, sink.paths, sink.ctype)
	}

	c.Path = "/wrong"
	s2, _ := New(c, "", testOpt)
	defer s2.Close()
	_, err = s2.Send(context.Background(), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(Explain(err, config.HTTP), "/input") {
		t.Errorf("неверный путь: %v → %q", err, Explain(err, config.HTTP))
	}
}

func TestHTTPS(t *testing.T) {
	sink := &httpSink{}
	srv := httptest.NewTLSServer(sink)
	defer srv.Close()
	host, port := hostPort(t, srv.Listener.Addr().String())
	c := config.Collector{Name: "h", Protocol: config.HTTP, Host: host, Port: port, TLS: true}
	c.Normalize()

	s, _ := New(c, "", testOpt)
	defer s.Close()
	if _, err := s.Send(context.Background(), []byte("по https")); err != nil {
		t.Fatal(err)
	}
	if sink.bodies[0] != "по https" {
		t.Errorf("тело: %q", sink.bodies)
	}

	// Несовпадение режима TLS — самая частая ошибка настройки; подсказка должна вести к ней.
	c.TLS = false
	plain, _ := New(c, "", testOpt)
	defer plain.Close()
	_, err := plain.Send(context.Background(), []byte("x"))
	if err == nil || !strings.Contains(Explain(err, config.HTTP), "включите TLS") {
		t.Errorf("http → https-сервер: %v → %q", err, Explain(err, config.HTTP))
	}
}

func TestHTTPSClientAgainstPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(&httpSink{})
	defer srv.Close()
	host, port := hostPort(t, srv.Listener.Addr().String())
	c := config.Collector{Name: "h", Protocol: config.HTTP, Host: host, Port: port, TLS: true}
	c.Normalize()
	s, _ := New(c, "", testOpt)
	defer s.Close()
	_, err := s.Send(context.Background(), []byte("x"))
	if err == nil || !strings.Contains(Explain(err, config.HTTP), "отключите TLS") {
		t.Errorf("https → http-сервер: %v → %q", err, Explain(err, config.HTTP))
	}
}

func TestDefaultHostAndMissingHost(t *testing.T) {
	if _, err := New(config.Collector{Name: "x", Protocol: config.UDP, Port: 1}, "", testOpt); err == nil {
		t.Error("без хоста отправитель создаваться не должен")
	}
	s, err := New(config.Collector{Name: "x", Protocol: config.UDP, Port: 514}, "kuma.example.local", testOpt)
	if err != nil || s.(*udpSender).addr != "kuma.example.local:514" {
		t.Errorf("хост по умолчанию: %v", err)
	}
}
