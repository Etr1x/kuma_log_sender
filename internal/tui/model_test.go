package tui

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Etr1x/kuma_log_sender/internal/app"
	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/job"
)

// harness управляет моделью так же, как это делает Bubble Tea: шлёт сообщения
// и исполняет возвращённые команды. Время в модели подменено, чтобы проверять
// и обычные нажатия, и «очередь» слишком быстрых нажатий при вставке текста.
type harness struct {
	t    *testing.T
	m    *Model
	app  *app.App
	now  time.Time
	udp  net.PacketConn
	quit bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	os.MkdirAll(filepath.Join(logs, "win"), 0o755)
	os.WriteFile(filepath.Join(logs, "auditd.log"), []byte("type=SYSCALL one\ntype=EXECVE two\r\ntype=CWD three\n"), 0o644)
	os.WriteFile(filepath.Join(logs, "cef.log"), []byte("CEF:0|a|b|1|x|test|Low|\n"), 0o644)
	os.WriteFile(filepath.Join(logs, "win", "4625.xml"), []byte("<Event>\n  <EventID>4625</EventID>\n</Event>\n"), 0o644)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	_, p, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(p)

	store, _ := config.Load(filepath.Join(dir, "collectors.json"))
	store.DefaultHost = "127.0.0.1"
	store.TimeoutSec = 2
	for _, c := range []config.Collector{
		{Name: "Auditd UDP", Protocol: config.UDP, Port: port, Delimiter: config.DelimLF},
		{Name: "CEF TCP", Protocol: config.TCP, Port: 5140, Delimiter: config.DelimLF},
		{Name: "WEC HTTP", Protocol: config.HTTP, Port: 5365, Delimiter: config.DelimNUL, TLS: true, Description: "агент Windows"},
	} {
		if err := store.Put("", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	a, err := app.Open(app.ResolvePaths(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Jobs.StopAll()
		a.Jobs.WaitAll()
		a.Close()
	})
	h := &harness{t: t, app: a, now: time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local), udp: pc}
	h.m = New(a)
	h.m.now = func() time.Time { return h.now }
	h.send(tea.WindowSizeMsg{Width: 110, Height: 34})
	return h
}

// send доставляет сообщение и исполняет порождённые им команды.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.exec(cmd)
}

func (h *harness) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.exec(c)
		}
	case tea.QuitMsg:
		h.quit = true
	default:
		h.send(msg)
	}
}

// tick имитирует срабатывание таймера обновления экрана.
func (h *harness) tick() {
	h.m.Update(tickMsg(h.now))
}

func keyMsg(name string) tea.KeyPressMsg {
	special := map[string]tea.KeyPressMsg{
		"enter":     {Code: tea.KeyEnter},
		"tab":       {Code: tea.KeyTab},
		"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
		"esc":       {Code: tea.KeyEscape},
		"up":        {Code: tea.KeyUp},
		"down":      {Code: tea.KeyDown},
		"left":      {Code: tea.KeyLeft},
		"right":     {Code: tea.KeyRight},
		"backspace": {Code: tea.KeyBackspace},
		"end":       {Code: tea.KeyEnd},
		"pgup":      {Code: tea.KeyPgUp},
		"space":     {Code: tea.KeySpace, Text: " "},
		"ctrl+c":    {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+j":    {Code: 'j', Mod: tea.ModCtrl},
		"ctrl+l":    {Code: 'l', Mod: tea.ModCtrl},
	}
	if k, ok := special[name]; ok {
		return k
	}
	r := []rune(name)
	if len(r) != 1 {
		panic("неизвестная клавиша " + name)
	}
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// press — нажатия с человеческой скоростью.
func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.now = h.now.Add(200 * time.Millisecond)
		h.send(keyMsg(k))
	}
}

// typeText набирает текст посимвольно с человеческой скоростью.
func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.press(string(r))
	}
}

// burst — нажатия без пауз, как при вставке в консоль без bracketed paste.
func (h *harness) burst(text string) {
	h.t.Helper()
	h.now = h.now.Add(200 * time.Millisecond)
	for _, r := range text {
		h.now = h.now.Add(50 * time.Microsecond)
		msg := tea.KeyPressMsg{Code: r, Text: string(r)}
		switch r {
		case '\n':
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case '\t':
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		}
		// Команды не исполняем: отложенная клавиша не успевает сработать до следующего нажатия.
		h.m.Update(msg)
	}
}

func (h *harness) screen() string {
	return ansi.Strip(h.m.render())
}

func (h *harness) wantScreen(parts ...string) {
	h.t.Helper()
	s := h.screen()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			h.t.Errorf("на экране нет %q:\n%s", p, s)
		}
	}
}

func (h *harness) datagram() string {
	h.t.Helper()
	buf := make([]byte, 65536)
	h.udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := h.udp.ReadFrom(buf)
	if err != nil {
		h.t.Fatalf("датаграмма не пришла: %v", err)
	}
	return string(buf[:n])
}

func (h *harness) waitJobs() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.app.Jobs.Active() > 0 {
		if time.Now().After(deadline) {
			h.t.Fatal("задачи не завершились")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.tick()
}

func TestKeyNames(t *testing.T) {
	// Тесты опираются на то, что имена клавиш Bubble Tea совпадают с ожидаемыми.
	for _, name := range []string{"enter", "tab", "shift+tab", "esc", "up", "down", "left", "right", "backspace", "end", "pgup", "space", "ctrl+c", "ctrl+j"} {
		if got := keyMsg(name).String(); got != name {
			t.Errorf("клавиша %q называется %q", name, got)
		}
	}
	// Русская раскладка: «у» — это e, «Ы» — это S.
	for ru, lat := range map[string]string{"у": "e", "Ы": "S", "ш": "i", "д": "l", "ь": "m", "с": "c", "е": "t"} {
		if got := hotkey(keyMsg(ru)); got != lat {
			t.Errorf("hotkey(%q) = %q, ожидали %q", ru, got, lat)
		}
	}
}

func TestMainScreen(t *testing.T) {
	h := newHarness(t)
	h.wantScreen("KUMA Log Sender", "Коллекторы", "Auditd UDP", "CEF TCP", "WEC HTTP", "https 5365",
		"Источник", "[ручной ввод]", "auditd.log", "win/4625.xml",
		"Параметры", "построчно", "Журнал", "Задачи")
	if testing.Verbose() {
		t.Logf("110x34:\n%s", h.screen())
		h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
		t.Logf("80x24:\n%s", h.screen())
	}
	// Каждая строка кадра должна точно укладываться в ширину окна.
	for _, size := range [][2]int{{110, 34}, {80, 20}, {80, 24}, {160, 50}, {97, 27}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(h.screen(), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: строк %d", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w != size[0] {
				t.Errorf("%dx%d: ширина строки %d равна %d: %q", size[0], size[1], i, w, line)
			}
		}
	}
	h.send(tea.WindowSizeMsg{Width: 60, Height: 10})
	h.wantScreen("Окно слишком маленькое")
}

func TestSendFileLineByLineAndRepeatWithOneKey(t *testing.T) {
	h := newHarness(t)
	// Коллектор «Auditd UDP» выбран первым; переходим к файлам и выбираем auditd.log.
	h.press("tab", "down")
	h.wantScreen("3 события × 1 = 3 отправки", "Предпросмотр: auditd.log")
	h.press("t")
	h.typeText("0")
	h.press("enter") // интервал 0
	h.press("enter") // отправка
	for _, want := range []string{"type=SYSCALL one\n", "type=EXECVE two\n", "type=CWD three\n"} {
		if got := h.datagram(); got != want {
			t.Fatalf("получили %q, ожидали %q", got, want)
		}
	}
	h.waitJobs()
	h.wantScreen("[3/3] отправлено", "завершено: отправлено 3, ошибок 0")

	// Повторная отправка туда же — одно нажатие.
	h.press("enter")
	if got := h.datagram(); got != "type=SYSCALL one\n" {
		t.Fatalf("повторная отправка: %q", got)
	}
	h.datagram()
	h.datagram()
	h.waitJobs()
	if jobs := h.app.Jobs.Snapshot(); len(jobs) != 2 || jobs[0].Sent != 3 {
		t.Errorf("задачи: %+v", jobs)
	}
}

func TestWholeModeAndCount(t *testing.T) {
	h := newHarness(t)
	h.press("tab", "down", "down", "down") // win/4625.xml
	h.press("m")                           // целиком
	h.press("c")
	h.typeText("2")
	h.press("enter")
	h.press("t")
	h.typeText("0.01")
	h.press("enter")
	h.wantScreen("целиком", "1 событие × 2 = 2 отправки")
	h.press("enter")
	want := "<Event>\n  <EventID>4625</EventID>\n</Event>\n"
	if got := h.datagram(); got != want {
		t.Fatalf("получили %q", got)
	}
	if got := h.datagram(); got != want {
		t.Fatalf("второй повтор: %q", got)
	}
	h.waitJobs()
}

func TestLoopAndStop(t *testing.T) {
	h := newHarness(t)
	h.press("tab", "down", "down") // cef.log
	h.press("l")
	h.press("t")
	h.typeText("20ms")
	h.press("enter")
	h.wantScreen("× ∞", "вкл")
	h.press("enter")
	for i := 0; i < 4; i++ {
		h.datagram()
	}
	h.tick()
	h.wantScreen("активных: 1", "идёт")
	h.press("s")
	h.waitJobs()
	h.wantScreen("остановлена", "остановлено: отправлено")
	if info := h.app.Jobs.Snapshot()[0]; info.State != job.Stopped || info.Sent < 4 {
		t.Errorf("итог: %+v", info)
	}
}

func TestManualInput(t *testing.T) {
	h := newHarness(t)
	h.press("i")
	h.wantScreen("Ручной ввод → Auditd UDP", "Enter отправить")
	h.typeText("привет kuma")
	h.press("tab")
	h.typeText("x")
	h.press("ctrl+j")
	h.typeText("вторая")
	h.wantScreen("привет kuma→x", "вторая", "построчно, 2 события")
	h.m.interval = 0
	h.press("enter")
	if got := h.datagram(); got != "привет kuma\tx\n" {
		t.Fatalf("первое событие: %q", got)
	}
	if got := h.datagram(); got != "вторая\n" {
		t.Fatalf("второе событие: %q", got)
	}
	h.waitJobs()
	if h.m.modal != nil {
		t.Error("окно ввода должно закрыться после отправки")
	}
	h.wantScreen("[ввод] привет kuma→x↵")
	// Enter повторяет отправку того же текста.
	h.press("enter")
	if got := h.datagram(); got != "привет kuma\tx\n" {
		t.Fatalf("повтор: %q", got)
	}
	h.datagram()
	h.waitJobs()
}

func TestBracketedPasteOnMainScreenOpensManualInput(t *testing.T) {
	h := newHarness(t)
	h.send(tea.PasteMsg{Content: "line one\r\nline\ttwo\r\n"})
	if _, ok := h.m.modal.(*manualModal); !ok {
		t.Fatal("вставка должна открыть окно ручного ввода")
	}
	h.wantScreen("line one", "line→two")
	h.m.interval = 0
	h.press("enter")
	if got := h.datagram(); got != "line one\n" {
		t.Fatalf("%q", got)
	}
	if got := h.datagram(); got != "line\ttwo\n" {
		t.Fatalf("%q", got)
	}
	h.waitJobs()
}

// В консоли без bracketed paste вставленный текст приходит потоком нажатий.
// Он не должен сработать как горячие клавиши (d — удаление, q — выход, Enter — отправка).
func TestKeyBurstIsTreatedAsPaste(t *testing.T) {
	h := newHarness(t)
	before := len(h.app.Store.Collectors)
	h.burst("delete all\nquit\tyes\n")
	mm, ok := h.m.modal.(*manualModal)
	if !ok {
		t.Fatalf("ожидалось окно ручного ввода, а открыто %T", h.m.modal)
	}
	if got := mm.value(); got != "delete all\nquit\tyes\n" {
		t.Errorf("в поле ввода %q", got)
	}
	if h.quit || len(h.app.Store.Collectors) != before || len(h.app.Jobs.Snapshot()) != 0 {
		t.Error("вставка сработала как горячие клавиши")
	}
	// После паузы Enter — это уже осознанная отправка.
	h.m.interval = 0
	h.press("enter")
	if got := h.datagram(); got != "delete all\n" {
		t.Fatalf("%q", got)
	}
	h.datagram()
	h.waitJobs()

	// То же внутри окна подтверждения: «y» из вставки не должна подтвердить удаление.
	h.press("d")
	h.wantScreen("Удалить коллектор «Auditd UDP»?")
	h.burst("yyy")
	if len(h.app.Store.Collectors) != before {
		t.Fatal("вставка подтвердила удаление")
	}
	h.press("n")
}

func TestCollectorCRUD(t *testing.T) {
	h := newHarness(t)
	h.press("a")
	h.wantScreen("Новый коллектор", "Протокол", "например: Linux Auditd")
	h.typeText("Мой TCP")
	h.press("down")
	h.wantScreen("‹ udp ›")
	h.press("left") // протокол: udp → tcp
	h.wantScreen("‹ tcp ›")
	h.press("down")
	h.typeText("10.1.2.3")
	h.press("down")
	h.typeText("6514")
	h.press("down")
	h.wantScreen(`‹ \n ›`) // разделитель подставился по умолчанию для tcp
	h.press("down", "space")
	h.wantScreen("[x] да", "Проверять сертификат")
	h.press("enter")
	if h.m.modal != nil {
		t.Fatalf("форма не закрылась:\n%s", h.screen())
	}
	i := h.app.Store.Index("мой tcp")
	if i < 0 {
		t.Fatal("коллектор не сохранён")
	}
	want := config.Collector{Name: "Мой TCP", Protocol: config.TCP, Host: "10.1.2.3", Port: 6514, Delimiter: config.DelimLF, TLS: true}
	if got := h.app.Store.Collectors[i]; got != want {
		t.Errorf("сохранено %+v, ожидали %+v", got, want)
	}
	h.wantScreen("Мой TCP", "tcp+tls://10.1.2.3:6514", "Коллектор «Мой TCP» сохранён")
	// Изменения сразу на диске.
	if disk, err := config.Load(h.app.Config); err != nil || disk.Index("Мой TCP") < 0 {
		t.Errorf("в файле коллектора нет: %v", err)
	}

	// Редактирование в русской раскладке: «у» = e.
	h.press("у")
	h.wantScreen("Коллектор: Мой TCP")
	h.press("down", "down", "down")
	h.press("backspace", "backspace", "backspace", "backspace")
	h.typeText("70000")
	h.press("enter")
	h.wantScreen("Порт — число от 1 до 65535")
	h.press("backspace", "backspace", "backspace", "backspace", "backspace")
	h.typeText("601")
	h.press("enter")
	if got := h.app.Store.Collectors[h.app.Store.Index("Мой TCP")].Port; got != 601 {
		t.Errorf("порт после правки: %d", got)
	}

	// Дубликат имени отклоняется.
	h.press("y")
	h.press("enter")
	if h.app.Store.Index("Мой TCP (копия)") < 0 {
		t.Error("копия не создана")
	}
	h.press("a")
	h.typeText("CEF TCP")
	h.press("down", "down", "down")
	h.typeText("1")
	h.press("enter")
	h.wantScreen("уже есть")
	h.press("esc")

	// Удаление с подтверждением.
	h.m.selectCollector("Мой TCP (копия)")
	h.press("d", "y")
	if h.app.Store.Index("Мой TCP (копия)") >= 0 {
		t.Error("коллектор не удалён")
	}
}

func TestSettingsAndMissingHost(t *testing.T) {
	h := newHarness(t)
	h.press("g")
	h.wantScreen("Настройки", "Хост по умолчанию")
	for i := 0; i < len("127.0.0.1"); i++ {
		h.press("backspace")
	}
	h.press("enter")
	if h.app.Store.DefaultHost != "" {
		t.Fatalf("хост: %q", h.app.Store.DefaultHost)
	}
	h.wantScreen("не задан", "udp://<хост не задан>")
	// Отправка без хоста — понятная ошибка в журнале, а не тишина.
	h.press("tab", "down", "enter")
	h.waitJobs()
	h.wantScreen("не задан хост")
}

func TestFilterAndNavigation(t *testing.T) {
	h := newHarness(t)
	h.press("/")
	h.typeText("wec")
	h.wantScreen("Коллекторы /wec█", "https://127.0.0.1:5365/input", `разделитель: \0`, "агент Windows")
	if c, _ := h.m.curCollector(); c.Name != "WEC HTTP" {
		t.Errorf("выбран %q", c.Name)
	}
	h.press("enter")
	// Для разделителя \0 доступен третий режим.
	h.press("m", "m")
	h.wantScreen("по разделителю")
	h.press("esc")
	if len(h.m.visCollectors()) != 3 {
		t.Error("Esc должен сбросить фильтр")
	}
	// У коллектора с \n режима «по разделителю» нет — он откатывается к «построчно».
	h.m.selectCollector("Auditd UDP")
	h.m.refreshPreview()
	if h.m.effMode() != events.ModeLines {
		t.Errorf("режим: %v", h.m.effMode())
	}

	h.press("tab", "/")
	h.typeText("4625")
	if src := h.m.curSource(); src.manual || src.file.Name != "win/4625.xml" {
		t.Errorf("источник: %+v", src)
	}
	h.press("esc")

	// Панель параметров: пробел, + и − меняют строку под курсором, цифры начинают ввод.
	h.press("tab", "down", "+", "space")
	if h.m.focus != paneParams || h.m.count != 3 {
		t.Errorf("фокус %v, повторов: %d", h.m.focus, h.m.count)
	}
	h.press("down", "-")
	if h.m.interval != 500*time.Millisecond {
		t.Errorf("интервал: %v", h.m.interval)
	}
	h.press("down", "space")
	if !h.m.loop {
		t.Error("цикл не включился")
	}
	h.press("up", "up")
	h.typeText("7")
	h.press("enter")
	if h.m.count != 7 || h.m.focus != paneParams {
		t.Errorf("повторов после ввода: %d, фокус %v", h.m.count, h.m.focus)
	}
	// Стрелки влево-вправо везде переключают панели и не меняют значения.
	h.press("left", "left", "right")
	if h.m.focus != paneSources || h.m.count != 7 {
		t.Errorf("фокус %v, повторов %d", h.m.focus, h.m.count)
	}
	// Горячие клавиши c и t не уводят фокус из списка: после ввода стрелки снова двигают список.
	h.press("c")
	h.typeText("4")
	h.press("enter")
	if h.m.count != 4 || h.m.focus != paneSources {
		t.Errorf("после c: повторов %d, фокус %v", h.m.count, h.m.focus)
	}
	h.press("t", "esc")
	if h.m.focus != paneSources || h.m.paramInput != nil {
		t.Errorf("после t, Esc: фокус %v", h.m.focus)
	}
	// Вне панели параметров + и − меняют число повторов.
	h.press("+", "+", "-")
	if h.m.count != 5 {
		t.Errorf("повторов после + + −: %d", h.m.count)
	}
	h.press("?")
	h.wantScreen("Справка", "повторная отправка)")
	h.press("esc")
}

func TestQuitAsksWhenJobsAreRunning(t *testing.T) {
	h := newHarness(t)
	h.press("q")
	if !h.quit {
		t.Fatal("без активных задач выход сразу")
	}
	h.quit = false
	h.press("tab", "down", "l", "enter")
	h.datagram()
	h.press("q")
	h.wantScreen("Выполняется задач: 1")
	if h.quit {
		t.Fatal("выход без подтверждения")
	}
	h.press("n")
	h.press("q", "y")
	if !h.quit {
		t.Error("после подтверждения должен быть выход")
	}
	h.waitJobs()
	// Состояние сохранено для следующего запуска.
	st := h.app.LoadState()
	if st.Collector != "Auditd UDP" || st.Source != "auditd.log" || !st.Loop {
		t.Errorf("сохранённое состояние: %+v", st)
	}
	m2 := New(h.app)
	if c, _ := m2.curCollector(); c.Name != "Auditd UDP" || m2.curSource().file.Name != "auditd.log" || !m2.loop {
		t.Error("состояние не восстановлено")
	}
}

// TestGallery ничего не проверяет, а при запуске с -v печатает основные экраны —
// так удобно просматривать интерфейс после правок.
func TestGallery(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("только с -v")
	}
	h := newHarness(t)
	show := func(name string) { t.Logf("%s:\n%s", name, h.screen()) }

	h.press("tab", "down")
	h.m.interval = 20 * time.Millisecond
	h.press("enter")
	h.datagram()
	h.datagram()
	h.datagram()
	h.waitJobs()
	h.m.selectCollector("CEF TCP")
	h.press("enter")
	h.waitJobs()
	h.tick()
	show("после отправки (вторая задача — на закрытый TCP-порт)")

	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	show("80x24")
	h.send(tea.WindowSizeMsg{Width: 110, Height: 34})

	h.press("i")
	h.typeText("CEF:0|a|b")
	h.press("tab")
	h.typeText("x")
	show("ручной ввод")
	h.press("esc")

	h.m.selectCollector("WEC HTTP")
	h.press("e")
	h.press("down", "down", "down", "down")
	show("редактор коллектора")
	h.press("esc", "g")
	show("настройки")
	h.press("esc", "d")
	show("подтверждение")
	h.press("n")
}
