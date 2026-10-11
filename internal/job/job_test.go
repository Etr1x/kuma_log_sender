package job

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

func udpCollector(t *testing.T) (config.Collector, net.PacketConn) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	host, p, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(p)
	return config.Collector{Name: "test-udp", Protocol: config.UDP, Host: host, Port: port, Delimiter: config.DelimLF}, pc
}

func static(evs ...string) func() ([][]byte, []string, error) {
	return func() ([][]byte, []string, error) {
		out := make([][]byte, len(evs))
		for i, e := range evs {
			out[i] = []byte(e)
		}
		return out, nil, nil
	}
}

func read(t *testing.T, pc net.PacketConn) string {
	t.Helper()
	buf := make([]byte, 65536)
	pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("датаграмма не пришла: %v", err)
	}
	return string(buf[:n])
}

func journalText(j *journal.Journal) string {
	var b strings.Builder
	for _, e := range j.Snapshot() {
		b.WriteString(e.Line() + "\n")
	}
	return b.String()
}

func TestPlaysEventsInOrderWithRepeats(t *testing.T) {
	c, pc := udpCollector(t)
	jr, _ := journal.New("")
	m := NewManager(jr)

	start := time.Now()
	id := m.Start(Spec{Collector: c, Source: "файл x.log", Load: static("a", "b", "c"), Count: 2, Interval: 30 * time.Millisecond})
	for _, want := range []string{"a\n", "b\n", "c\n", "a\n", "b\n", "c\n"} {
		if got := read(t, pc); got != want {
			t.Fatalf("получили %q, ожидали %q", got, want)
		}
	}
	info, _ := m.Wait(id)
	if info.State != Done || info.Sent != 6 || info.Errors != 0 || info.Total != 6 {
		t.Errorf("итог: %+v", info)
	}
	// Пять пауз по 30 мс между шестью отправками.
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Errorf("интервал не выдержан: всё ушло за %s", el)
	}
	text := journalText(jr)
	for _, want := range []string{"старт: файл x.log", "[1/6] отправлено", "[6/6] отправлено", "завершено: отправлено 6, ошибок 0"} {
		if !strings.Contains(text, want) {
			t.Errorf("в журнале нет %q:\n%s", want, text)
		}
	}
}

func TestLoopRunsUntilStopped(t *testing.T) {
	c, pc := udpCollector(t)
	jr, _ := journal.New("")
	m := NewManager(jr)

	id := m.Start(Spec{Collector: c, Source: "ручной ввод", Load: static("ping"), Count: 1, Loop: true, Interval: 10 * time.Millisecond})
	for i := 0; i < 5; i++ {
		read(t, pc)
	}
	if m.Active() != 1 {
		t.Fatal("задача должна ещё выполняться")
	}
	if !m.Stop(id) {
		t.Fatal("Stop вернул false")
	}
	info, _ := m.Wait(id)
	if info.State != Stopped || info.Sent < 5 || info.Total != 0 {
		t.Errorf("итог: %+v", info)
	}
	if m.Stop(id) {
		t.Error("повторный Stop должен вернуть false")
	}
	if !strings.Contains(journalText(jr), "остановлено: отправлено") {
		t.Errorf("в журнале нет записи об остановке:\n%s", journalText(jr))
	}
}

func TestParallelJobs(t *testing.T) {
	c1, pc1 := udpCollector(t)
	c2, pc2 := udpCollector(t)
	jr, _ := journal.New("")
	m := NewManager(jr)

	a := m.Start(Spec{Collector: c1, Source: "a", Load: static("to-first"), Loop: true, Interval: 10 * time.Millisecond})
	b := m.Start(Spec{Collector: c2, Source: "b", Load: static("to-second"), Loop: true, Interval: 10 * time.Millisecond})
	if read(t, pc1) != "to-first\n" || read(t, pc2) != "to-second\n" {
		t.Error("события ушли не на те коллекторы")
	}
	if got := m.Snapshot(); len(got) != 2 || got[0].ID != b || got[1].ID != a {
		t.Errorf("снимок: %+v", got)
	}
	if n := m.StopAll(); n != 2 {
		t.Errorf("StopAll = %d", n)
	}
	m.WaitAll()
	if m.Active() != 0 {
		t.Error("после StopAll активных задач быть не должно")
	}
	m.ClearFinished()
	if len(m.Snapshot()) != 0 {
		t.Error("ClearFinished не очистил список")
	}
}

func TestFailuresAreLoggedWithReason(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	ln.Close()
	c := config.Collector{Name: "dead-tcp", Protocol: config.TCP, Host: host, Port: port, Delimiter: config.DelimLF}
	jr, _ := journal.New("")
	m := NewManager(jr)

	info, _ := m.Wait(m.Start(Spec{Collector: c, Source: "ручной ввод", Load: static("x", "y"), Timeout: 2 * time.Second}))
	if info.State != Failed || info.Sent != 0 || info.Errors != 2 {
		t.Errorf("итог: %+v", info)
	}
	text := journalText(jr)
	for _, want := range []string{"ERR", "[1/2] НЕ отправлено", "порт закрыт", "завершено с ошибкой"} {
		if !strings.Contains(text, want) {
			t.Errorf("в журнале нет %q:\n%s", want, text)
		}
	}

	// Пустой источник и незаданный хост — тоже ошибки с объяснением.
	info, _ = m.Wait(m.Start(Spec{Collector: c, Source: "файл empty.log", Load: static()}))
	if info.State != Failed || !strings.Contains(journalText(jr), "нет событий") {
		t.Errorf("пустой источник: %+v", info)
	}
	c.Host = ""
	info, _ = m.Wait(m.Start(Spec{Collector: c, Source: "ручной ввод", Load: static("x")}))
	if info.State != Failed || !strings.Contains(journalText(jr), "не задан хост") {
		t.Errorf("без хоста: %+v", info)
	}
}

// На закрытый UDP-порт локальная машина отвечает ICMP-отказом: задача должна
// сообщить об этом до подведения итога и пометить итог как сомнительный.
func TestUDPRefusalMarksJobAsWarned(t *testing.T) {
	c, pc := udpCollector(t)
	pc.Close()
	jr, _ := journal.New("")
	m := NewManager(jr)
	info, _ := m.Wait(m.Start(Spec{Collector: c, Source: "ручной ввод", Load: static("x")}))
	text := journalText(jr)
	if !info.Warned && info.Errors == 0 {
		t.Skip("ОС не вернула ICMP-ошибку для UDP (зависит от настроек файрвола)")
	}
	if info.Warned {
		warn := strings.Index(text, "никто не слушает")
		end := strings.Index(text, "завершено с предупреждениями")
		if warn < 0 || end < 0 || warn > end {
			t.Errorf("предупреждение должно идти перед итогом:\n%s", text)
		}
	}
}

func TestParseInterval(t *testing.T) {
	ok := map[string]time.Duration{
		"1":      time.Second,
		"0":      0,
		"0.5":    500 * time.Millisecond,
		"0,25":   250 * time.Millisecond,
		"500ms":  500 * time.Millisecond,
		"2 s":    2 * time.Second,
		"1m":     time.Minute,
		"1m30s":  90 * time.Second,
		"300 мс": 300 * time.Millisecond,
		"2с":     2 * time.Second,
	}
	for in, want := range ok {
		if got, err := ParseInterval(in); err != nil || got != want {
			t.Errorf("ParseInterval(%q) = %v, %v; ожидали %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1", "48h"} {
		if _, err := ParseInterval(in); err == nil {
			t.Errorf("ParseInterval(%q): ожидалась ошибка", in)
		}
	}
	for d, want := range map[time.Duration]string{0: "без паузы", time.Second: "1s", 90 * time.Second: "1m30s", time.Minute: "1m", 2 * time.Hour: "2h"} {
		if got := FormatInterval(d); got != want {
			t.Errorf("FormatInterval(%v) = %q, ожидали %q", d, got, want)
		}
	}
}

func TestPreview(t *testing.T) {
	if got := Preview([]byte("a\tb\nc\x00d\x01"), 100); got != `a→b↵c\0d·` {
		t.Errorf("Preview = %q", got)
	}
	if got := Preview([]byte("абвгд"), 3); got != "абв…" {
		t.Errorf("Preview с обрезкой = %q", got)
	}
}
