package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/job"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

// --- Ручной ввод ---

// tabMark подменяет табуляцию внутри поля ввода: компонент textarea заменяет
// настоящий \t пробелами, что испортило бы логи с табами. Внутри редактора таб
// хранится как символ из области частного использования, на экране рисуется
// стрелкой, а при отправке превращается обратно в \t.
const (
	tabMark = ""
	tabShow = "→"
)

func encodeTabs(s string) string { return strings.ReplaceAll(s, "\t", tabMark) }
func decodeTabs(s string) string { return strings.ReplaceAll(s, tabMark, "\t") }

// manualMaxLines — предел компонента textarea: строки сверх него он молча отбрасывает.
const manualMaxLines = 10000

type manualModal struct {
	ta textarea.Model
}

// insert добавляет текст в поле ввода, сохраняя табуляции, и предупреждает,
// если текст не поместился целиком.
func (mm *manualModal) insert(m *Model, text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if mm.ta.LineCount()+strings.Count(text, "\n") > manualMaxLines {
		m.setFlash(journal.Warn, "В поле ввода помещается не больше %d строк — большие наборы отправляйте из файла в папке logs", manualMaxLines)
	}
	mm.ta.InsertString(encodeTabs(text))
}

// openManual открывает окно ручного ввода с заданным текстом. fromKeys означает,
// что текст пришёл как поток нажатий (вставка без bracketed paste) и продолжится.
func (m *Model) openManual(text string, fromKeys bool) tea.Cmd {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0 // иначе компонент не даёт ввести больше 99 строк
	ta.Placeholder = "Введите или вставьте лог…"
	// Enter отправляет, поэтому перевод строки — на других сочетаниях.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter", "shift+enter"))
	st := ta.Styles()
	st.Focused.CursorLine = st.Focused.Text
	st.Cursor.Blink = false
	ta.SetStyles(st)
	ta.SetWidth(m.manualWidth() - 4)
	cmd := ta.Focus()
	mm := &manualModal{ta: ta}
	mm.insert(m, text)
	m.modal = mm
	if fromKeys {
		m.lastText = m.now()
	}
	return cmd
}

func (m *Model) manualWidth() int { return min(max(m.w-8, 40), 100) }

// height подбирает высоту поля под текст: от 5 строк до того, что позволяет окно.
func (mm *manualModal) height(m *Model) int {
	w := max(mm.ta.Width(), 1)
	rows := 1
	for _, line := range strings.Split(mm.ta.Value(), "\n") {
		rows += ansi.StringWidth(line)/w + 1
	}
	return min(max(rows, 5), max(m.h-12, 5))
}

func (mm *manualModal) value() string {
	return decodeTabs(mm.ta.Value())
}

func (mm *manualModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		mm.insert(m, msg.Content)
		return false, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			m.manual = mm.value()
			m.refreshPreview()
			return true, nil
		case "enter":
			if m.burst {
				mm.ta.InsertString("\n") // перевод строки внутри вставляемого текста
				return false, nil
			}
			m.manual = mm.value()
			m.selectSource("")
			m.refreshPreview()
			if strings.TrimSpace(m.manual) == "" {
				return true, nil
			}
			return true, m.send()
		case "tab":
			mm.ta.InsertString(tabMark)
			return false, nil
		case "ctrl+r":
			mm.ta.Reset()
			return false, nil
		}
	}
	var cmd tea.Cmd
	mm.ta, cmd = mm.ta.Update(msg)
	return false, cmd
}

func (mm *manualModal) view(m *Model) string {
	w := m.manualWidth()
	mm.ta.SetWidth(w - 4)
	mm.ta.SetHeight(mm.height(m))
	title := "Ручной ввод"
	target := stWarn.Render("коллектор не выбран")
	if c, ok := m.curCollector(); ok {
		title += " → " + c.Name
		target = stAccent.Render(c.Target(m.app.Store.DefaultHost)) + stDim.Render(", разделитель: ") + c.Delimiter.Label()
	}
	text := mm.value()
	n := len(eventsOf(m, text))
	summary := fmt.Sprintf("%s, %d %s", m.effMode().Label(), n, plural(n, "событие", "события", "событий"))
	repeats := fmt.Sprintf("× %d", m.count)
	if m.loop {
		repeats = "× ∞ (цикл)"
	}
	lines := []string{" " + target, " " + stDim.Render("режим: ") + summary + " " + repeats + stDim.Render(", интервал: ") + job.FormatInterval(m.interval), ""}
	for _, l := range strings.Split(mm.ta.View(), "\n") {
		lines = append(lines, " "+strings.ReplaceAll(l, tabMark, tabShow))
	}
	lines = append(lines, "",
		help("Enter", "отправить", "Ctrl+J", "новая строка", "Tab", "табуляция", "Ctrl+R", "очистить", "Esc", "закрыть"))
	return box(title, "", lines, w, len(lines)+2, true)
}

// eventsOf считает, на сколько событий разобьётся текст при текущих настройках.
func eventsOf(m *Model, text string) [][]byte {
	var delim []byte
	if c, ok := m.curCollector(); ok {
		delim = c.Delimiter.Bytes()
	}
	return events.FromText(text, m.effMode(), delim)
}

// --- Подтверждение ---

type confirmModal struct {
	text  string
	yes   func(m *Model) tea.Cmd
	armed bool // «y» нажата и ждёт burstGap: не начало ли это вставленного текста
	seq   int
}

type confirmMsg struct{ seq int }

func newConfirm(text string, yes func(m *Model) tea.Cmd) *confirmModal {
	return &confirmModal{text: text, yes: yes}
}

func (c *confirmModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case confirmMsg:
		if c.armed && msg.seq == c.seq {
			return true, c.yes(m)
		}
	case tea.KeyPressMsg:
		// Любое нажатие сразу вслед за «y» означает вставку текста — подтверждение отменяется.
		wasArmed := c.armed
		c.armed = false
		c.seq++
		if m.burst || wasArmed {
			return false, nil
		}
		switch lower(hotkey(msg)) {
		case "y":
			c.armed = true
			seq := c.seq
			return false, tea.Tick(burstGap, func(time.Time) tea.Msg { return confirmMsg{seq} })
		case "n", "esc", "q":
			return true, nil
		}
	}
	return false, nil
}

func (c *confirmModal) view(m *Model) string {
	w := min(max(ansi.StringWidth(c.text)+6, 44), m.w-6)
	lines := []string{""}
	for _, part := range strings.Split(ansi.Wordwrap(c.text, w-4, ""), "\n") {
		lines = append(lines, " "+part)
	}
	lines = append(lines, "", help("y", "да", "n / Esc", "нет"))
	return box("Подтверждение", "", lines, w, len(lines)+2, true)
}

// --- Справка ---

type helpModal struct {
	offset int
}

func newHelp() *helpModal { return &helpModal{} }

var helpText = []struct{ keys, what string }{
	{"", "Отправка"},
	{"Enter", "отправить выбранный источник на выбранный коллектор (повторное нажатие — повторная отправка)"},
	{"i", "ручной ввод лога; вставка из буфера обмена на основном экране открывает его сразу с текстом"},
	{"s / S", "остановить последнюю (или выбранную в панели «Задачи») задачу / остановить все"},
	{"", "Параметры отправки"},
	{"m", "режим: построчно — каждая строка отдельным событием; целиком — весь текст одним событием; по разделителю — резать по \\0 или \\t"},
	{"c  + −", "число повторов: ввести / увеличить / уменьшить"},
	{"t", "интервал между отправками: 1, 0.5, 500ms, 2m; 0 — без паузы"},
	{"l", "цикл: отправлять бесконечно, пока задачу не остановят"},
	{"пробел  + −", "в панели «Параметры»: изменить значение в строке под курсором; цифры — ввести значение"},
	{"", "Коллекторы"},
	{"a / e / d / y", "добавить / изменить / удалить / создать копию"},
	{"g", "настройки: хост по умолчанию и таймаут"},
	{"r  F5", "перечитать collectors.json и папку logs с диска"},
	{"", "Навигация"},
	{"Tab  Shift+Tab  ← →", "переход между панелями"},
	{"↑ ↓  PgUp PgDn  Home End", "перемещение по списку, прокрутка журнала"},
	{"/", "фильтр по имени в списках коллекторов и файлов; Esc — сбросить"},
	{"x", "убрать завершённые задачи из списка"},
	{"w", "журнал: показывать события целиком (с переносом строк) или по одной строке"},
	{"Ctrl+L", "очистить журнал на экране (файл sender.log не трогается)"},
	{"?  F1", "эта справка"},
	{"q  Ctrl+C", "выход"},
	{"", ""},
	{"", "Горячие клавиши работают и в русской раскладке."},
}

func (h *helpModal) lines(w int) []string {
	const keyW = 26
	var out []string
	for _, row := range helpText {
		if row.keys == "" {
			if row.what != "" {
				out = append(out, "", " "+stTitleFocus.Render(row.what))
			} else {
				out = append(out, "")
			}
			continue
		}
		parts := strings.Split(ansi.Wordwrap(row.what, w-keyW-3, ""), "\n")
		for i, part := range parts {
			keys := ""
			if i == 0 {
				keys = row.keys
			}
			out = append(out, " "+stKey.Render(fmt.Sprintf("%-*s", keyW, keys))+part)
		}
	}
	return out[1:]
}

func (h *helpModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false, nil
	}
	switch hotkey(k) {
	case "up", "k":
		h.offset--
	case "down", "j":
		h.offset++
	case "pgup":
		h.offset -= 10
	case "pgdown", "space":
		h.offset += 10
	default:
		return true, nil
	}
	return false, nil
}

func (h *helpModal) view(m *Model) string {
	w := min(m.w-4, 104)
	all := h.lines(w - 2)
	rows := min(len(all), m.h-6)
	h.offset = min(max(h.offset, 0), len(all)-rows)
	lines := append([]string{}, all[h.offset:h.offset+rows]...)
	hint := ""
	if rows < len(all) {
		hint = "↑↓ — прокрутка"
	}
	lines = append(lines, "", stDim.Render(" любая клавиша — закрыть"))
	return box("Справка", hint, lines, w, len(lines)+2, true)
}
