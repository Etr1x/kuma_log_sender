// Package tui — интерактивный интерфейс утилиты на Bubble Tea.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Etr1x/kuma_log_sender/internal/app"
	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/job"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

type pane int

const (
	paneCollectors pane = iota
	paneSources
	paneParams
	paneJobs
	paneJournal
	paneCount
)

// Строки панели «Параметры».
const (
	paramMode = iota
	paramCount
	paramInterval
	paramLoop
	paramRows
)

const (
	tickEvery  = 150 * time.Millisecond
	filesEvery = 2 * time.Second
	flashFor   = 4 * time.Second
	// burstGap — промежуток между нажатиями, недостижимый для человека. Так
	// распознаётся вставка из буфера в терминалах без bracketed paste (старая
	// консоль Windows): там вставленный текст приходит как обычные нажатия, и без
	// этой защиты он был бы воспринят как набор горячих клавиш.
	burstGap = 10 * time.Millisecond
	maxCount = 1_000_000
)

type (
	tickMsg   time.Time
	hotkeyMsg struct{ seq int }
)

// modal — окно поверх основного экрана: форма, ручной ввод, подтверждение, справка.
type modal interface {
	// update обрабатывает сообщение; done=true закрывает окно.
	update(m *Model, msg tea.Msg) (done bool, cmd tea.Cmd)
	view(m *Model) string
}

// Model — состояние всего интерфейса.
type Model struct {
	app *app.App
	now func() time.Time

	w, h  int
	focus pane
	modal modal

	colCursor, colOffset int
	colFilter            string
	srcCursor, srcOffset int
	srcFilter            string
	filtering            bool // идёт ввод фильтра в активной панели-списке

	files   []events.FileInfo
	filesAt time.Time
	manual  string

	mode        events.Mode
	count       int
	interval    time.Duration
	loop        bool
	paramCursor int
	paramInput  *textinput.Model
	returnTo    *pane // куда вернуть фокус после ввода параметра по горячей клавише

	prev preview

	jobs      []job.Info
	jobCursor int

	entries    []journal.Entry
	journalSeq uint64
	scroll     int  // на сколько строк журнал прокручен вверх от конца
	wrap       bool // переносить в журнале и длинные записи об успешной отправке

	flash      string
	flashLevel journal.Level
	flashUntil time.Time

	// Отложенная горячая клавиша и время последнего «текстового» нажатия — см. burstGap.
	pending     string
	pendingText string
	pendingSeq  int
	lastText    time.Time
	burst       bool
}

// New создаёт модель и восстанавливает состояние прошлого запуска.
func New(a *app.App) *Model {
	m := &Model{
		app:      a,
		now:      time.Now,
		mode:     events.ModeLines,
		count:    1,
		interval: time.Second,
	}
	st := a.LoadState()
	if mode, err := events.ParseMode(st.Mode); err == nil {
		m.mode = mode
	}
	if st.Count >= 1 && st.Count <= maxCount {
		m.count = st.Count
	}
	if d, err := job.ParseInterval(st.Interval); err == nil {
		m.interval = d
	}
	m.loop = st.Loop
	m.manual = st.Manual
	m.reloadFiles()
	m.selectCollector(st.Collector)
	m.selectSource(st.Source)
	m.syncJournal()
	m.refreshPreview()
	if len(a.Store.Collectors) == 0 {
		m.setFlash(journal.Info, "Коллекторов пока нет: нажмите a, чтобы добавить первый")
	} else if a.Store.DefaultHost == "" && m.anyWithoutHost() {
		m.setFlash(journal.Warn, "Не задан хост по умолчанию: нажмите g и укажите адрес сервера коллекторов")
	}
	return m
}

func (m *Model) anyWithoutHost() bool {
	for _, c := range m.app.Store.Collectors {
		if c.Host == "" {
			return true
		}
	}
	return false
}

func (m *Model) Init() tea.Cmd {
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clamp()
		return m, nil
	case tickMsg:
		m.onTick()
		return m, tick()
	case hotkeyMsg:
		if msg.seq == m.pendingSeq && m.pending != "" {
			key := m.pending
			m.pending, m.pendingText = "", ""
			return m, m.hotkey(key)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	case tea.PasteMsg:
		return m, m.onPaste(msg)
	}
	if m.modal != nil {
		return m, m.updateModal(msg)
	}
	return m, nil
}

// newInput создаёт однострочное поле ввода. Курсор не мигает: так интерфейс
// не перерисовывается впустую.
func newInput() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	st := in.Styles()
	st.Cursor.Blink = false
	in.SetStyles(st)
	return in
}

func (m *Model) onTick() {
	now := m.now()
	m.jobs = m.app.Jobs.Snapshot()
	m.syncJournal()
	if now.Sub(m.filesAt) >= filesEvery {
		m.reloadFiles()
		m.refreshPreview()
	}
	if m.flash != "" && now.After(m.flashUntil) {
		m.flash = ""
	}
	m.clamp()
}

func (m *Model) syncJournal() {
	seq := m.app.Journal.Seq()
	if seq == m.journalSeq && m.entries != nil {
		return
	}
	m.entries = m.app.Journal.Snapshot()
	// Пока журнал прокручен вверх, новые записи не должны сдвигать то, что читает
	// пользователь: увеличиваем прокрутку на высоту добавившихся строк.
	if m.scroll > 0 {
		width := m.layout().w - 2
		for i := len(m.entries) - 1; i >= 0 && m.entries[i].Seq > m.journalSeq; i-- {
			m.scroll += len(m.entryLines(m.entries[i], width))
		}
	}
	m.journalSeq = seq
}

func (m *Model) setFlash(level journal.Level, format string, args ...any) {
	m.flash = fmt.Sprintf(format, args...)
	m.flashLevel = level
	m.flashUntil = m.now().Add(flashFor)
}

// onKey — единая точка входа для нажатий: здесь же отслеживается «очередь»
// слишком быстрых нажатий, выдающая вставку из буфера обмена.
func (m *Model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	now := m.now()
	text := keyText(msg)
	name := msg.String()
	m.burst = now.Sub(m.lastText) < burstGap
	textual := text != "" || (m.burst && (name == "enter" || name == "tab"))
	defer func() {
		if textual {
			m.lastText = now
		}
	}()

	if name == "ctrl+c" {
		return m.quit()
	}
	if m.modal != nil {
		return m.updateModal(msg)
	}
	if m.filtering {
		m.filterKey(msg)
		return nil
	}
	if m.paramInput != nil {
		return m.paramKey(msg)
	}

	// Основной экран. Печатные клавиши исполняются с задержкой burstGap: если за
	// это время приходит ещё одна — это вставка текста, а не горячая клавиша.
	if m.pending != "" {
		if text != "" || name == "enter" || name == "tab" {
			pasted := m.pendingText + text
			switch name {
			case "enter":
				pasted += "\n"
			case "tab":
				pasted += "\t"
			}
			m.pending, m.pendingText = "", ""
			textual = true
			return m.openManual(pasted, true)
		}
		prev := m.pending
		m.pending, m.pendingText = "", ""
		return tea.Batch(m.hotkey(prev), m.hotkey(hotkey(msg)))
	}
	if text != "" {
		m.pending, m.pendingText = hotkey(msg), text
		m.pendingSeq++
		seq := m.pendingSeq
		return tea.Tick(burstGap, func(time.Time) tea.Msg { return hotkeyMsg{seq} })
	}
	return m.hotkey(hotkey(msg))
}

func (m *Model) updateModal(msg tea.Msg) tea.Cmd {
	cur := m.modal
	done, cmd := cur.update(m, msg)
	// Обработчик мог сам открыть другое окно — его закрывать не нужно.
	if done && m.modal == cur {
		m.modal = nil
	}
	return cmd
}

func (m *Model) onPaste(msg tea.PasteMsg) tea.Cmd {
	switch {
	case m.modal != nil:
		return m.updateModal(msg)
	case m.filtering:
		m.setFilter(m.filter() + oneLineText(msg.Content))
		return nil
	case m.paramInput != nil:
		in, cmd := m.paramInput.Update(msg)
		m.paramInput = &in
		return cmd
	}
	// Вставка на основном экране — самый короткий путь отправить лог из буфера обмена.
	return m.openManual(msg.Content, false)
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// hotkey исполняет действие основного экрана.
func (m *Model) hotkey(key string) tea.Cmd {
	switch key {
	case "enter":
		return m.send()
	case "tab", "right":
		m.focus = (m.focus + 1) % paneCount
	case "shift+tab", "left":
		m.focus = (m.focus + paneCount - 1) % paneCount
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(+1)
	case "pgup":
		m.move(-m.page())
	case "pgdown":
		m.move(+m.page())
	case "home":
		m.move(-1 << 30)
	case "end":
		m.move(1 << 30)
	case "space":
		if m.focus == paneParams {
			m.paramAdjust(+1)
		}
	case "esc":
		if m.colFilter != "" || m.srcFilter != "" {
			m.colFilter, m.srcFilter = "", ""
			m.clamp()
			m.refreshPreview()
		} else {
			m.scroll = 0
		}
	case "/", ".":
		if m.focus != paneSources {
			m.focus = paneCollectors
		}
		m.filtering = true
	case "i":
		return m.openManual(m.manual, false)
	case "a":
		m.modal = newCollectorForm(m, config.Collector{Protocol: config.UDP}, "")
	case "e":
		if c, ok := m.curCollector(); ok {
			m.modal = newCollectorForm(m, c, c.Name)
		}
	case "y":
		if c, ok := m.curCollector(); ok {
			c.Name += " (копия)"
			m.modal = newCollectorForm(m, c, "")
		}
	case "d":
		if c, ok := m.curCollector(); ok {
			m.modal = newConfirm(fmt.Sprintf("Удалить коллектор «%s»?", c.Name), func(m *Model) tea.Cmd {
				m.app.Store.Delete(c.Name)
				m.saveStore("Коллектор «%s» удалён", c.Name)
				m.clamp()
				m.refreshPreview()
				return nil
			})
		}
	case "g":
		m.modal = newSettingsForm(m)
	case "m":
		m.cycleMode(+1)
	case "l":
		m.loop = !m.loop
	case "c":
		return m.editParam(paramCount)
	case "t":
		return m.editParam(paramInterval)
	case "+", "=":
		// В панели параметров меняется строка под курсором, в остальных — число повторов.
		if m.focus == paneParams {
			m.paramAdjust(+1)
		} else {
			m.setCount(m.count + 1)
		}
	case "-", "_":
		if m.focus == paneParams {
			m.paramAdjust(-1)
		} else {
			m.setCount(m.count - 1)
		}
	case "s":
		m.stopJob()
	case "S":
		if n := m.app.Jobs.StopAll(); n > 0 {
			m.setFlash(journal.Info, "Остановлено задач: %d", n)
		} else {
			m.setFlash(journal.Info, "Активных задач нет")
		}
	case "x":
		m.app.Jobs.ClearFinished()
		m.jobs = m.app.Jobs.Snapshot()
		m.clamp()
	case "w":
		m.wrap = !m.wrap
		m.scroll = 0
	case "ctrl+l":
		m.app.Journal.Clear()
		m.syncJournal()
		m.scroll = 0
	case "r", "f5":
		m.reload()
	case "?", ",", "f1":
		m.modal = newHelp()
	case "q":
		return m.quit()
	default:
		// Цифра в полях «Повторов» и «Интервал» сразу начинает ввод значения.
		if m.focus == paneParams && len(key) == 1 && key[0] >= '0' && key[0] <= '9' &&
			(m.paramCursor == paramCount || m.paramCursor == paramInterval) {
			return m.startParamEdit(key)
		}
	}
	return nil
}

// reload перечитывает конфиг с диска (его могли поправить руками) и список файлов.
func (m *Model) reload() {
	name := ""
	if c, ok := m.curCollector(); ok {
		name = c.Name
	}
	store, err := config.Load(m.app.Config)
	if err != nil {
		m.setFlash(journal.Error, "Конфиг не перечитан: %v", err)
	} else {
		m.app.Store = store
		m.setFlash(journal.Info, "Конфиг и список файлов перечитаны")
	}
	m.reloadFiles()
	m.selectCollector(name)
	m.refreshPreview()
}

func (m *Model) saveStore(format string, args ...any) {
	if err := m.app.Store.Save(); err != nil {
		m.setFlash(journal.Error, "Не удалось сохранить %s: %v", app.ConfigName, err)
		m.app.Journal.Add(journal.Error, 0, "не удалось сохранить конфиг: %v", err)
		return
	}
	m.setFlash(journal.OK, format, args...)
}

func (m *Model) quit() tea.Cmd {
	if n := m.app.Jobs.Active(); n > 0 {
		if _, ok := m.modal.(*confirmModal); ok {
			// Повторный Ctrl+C при открытом подтверждении — выходим без вопросов.
			return m.shutdown()
		}
		m.modal = newConfirm(fmt.Sprintf("Выполняется задач: %d. Остановить их и выйти?", n), func(m *Model) tea.Cmd {
			return m.shutdown()
		})
		return nil
	}
	return m.shutdown()
}

func (m *Model) shutdown() tea.Cmd {
	m.app.Jobs.StopAll()
	m.saveState()
	return tea.Quit
}

func (m *Model) saveState() {
	st := app.State{
		Manual:   m.manual,
		Mode:     string(m.mode),
		Count:    m.count,
		Interval: m.interval.String(),
		Loop:     m.loop,
	}
	if c, ok := m.curCollector(); ok {
		st.Collector = c.Name
	}
	if src := m.curSource(); !src.manual {
		st.Source = src.file.Name
	}
	m.app.SaveState(st)
}

// --- Отправка ---

// send запускает задачу с текущими коллектором, источником и параметрами.
func (m *Model) send() tea.Cmd {
	c, ok := m.curCollector()
	if !ok {
		m.setFlash(journal.Warn, "Нет коллектора: нажмите a, чтобы добавить")
		return nil
	}
	src := m.curSource()
	if src.manual && strings.TrimSpace(m.manual) == "" {
		return m.openManual("", false)
	}
	mode, delim := m.effMode(), c.Delimiter.Bytes()
	spec := job.Spec{
		Collector:   c,
		DefaultHost: m.app.Store.DefaultHost,
		Timeout:     m.app.Store.Timeout(),
		Count:       m.count,
		Loop:        m.loop,
		Interval:    m.interval,
	}
	if src.manual {
		text := m.manual
		spec.Source = "ручной ввод"
		spec.Load = func() ([][]byte, []string, error) { return events.FromText(text, mode, delim), nil, nil }
	} else {
		path := src.file.Path
		spec.Source = "файл " + src.file.Name
		spec.Load = func() ([][]byte, []string, error) { return events.FromFile(path, mode, delim) }
	}
	id := m.app.Jobs.Start(spec)
	m.jobs = m.app.Jobs.Snapshot()
	m.scroll = 0
	m.setFlash(journal.OK, "Задача #%d запущена: %s → %s", id, spec.Source, c.Name)
	m.saveState()
	return nil
}

func (m *Model) stopJob() {
	var target *job.Info
	if m.focus == paneJobs && m.jobCursor < len(m.jobs) && m.jobs[m.jobCursor].State == job.Running {
		target = &m.jobs[m.jobCursor]
	} else {
		for i := range m.jobs {
			if m.jobs[i].State == job.Running {
				target = &m.jobs[i]
				break
			}
		}
	}
	if target == nil || !m.app.Jobs.Stop(target.ID) {
		m.setFlash(journal.Info, "Активных задач нет")
		return
	}
	m.setFlash(journal.Info, "Задача #%d остановлена", target.ID)
}

// --- Параметры ---

// effMode — режим с учётом коллектора: «по разделителю» имеет смысл только для \0 и \t.
func (m *Model) effMode() events.Mode {
	for _, mode := range m.modes() {
		if mode == m.mode {
			return mode
		}
	}
	return events.ModeLines
}

func (m *Model) modes() []events.Mode {
	var delim []byte
	if c, ok := m.curCollector(); ok {
		delim = c.Delimiter.Bytes()
	}
	return events.Modes(delim)
}

func (m *Model) cycleMode(step int) {
	modes := m.modes()
	cur := 0
	for i, mode := range modes {
		if mode == m.effMode() {
			cur = i
		}
	}
	m.mode = modes[(cur+step+len(modes))%len(modes)]
	m.refreshPreview()
}

func (m *Model) setCount(n int) {
	m.count = min(max(n, 1), maxCount)
}

// paramAdjust меняет значение параметра под курсором (пробел, + и −).
func (m *Model) paramAdjust(step int) {
	switch m.paramCursor {
	case paramMode:
		m.cycleMode(step)
	case paramCount:
		m.setCount(m.count + step)
	case paramInterval:
		m.interval = stepInterval(m.interval, step)
	case paramLoop:
		m.loop = !m.loop
	}
}

// intervalSteps — «ступеньки» для изменения интервала стрелками.
var intervalSteps = []time.Duration{
	0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 10 * time.Minute,
}

func stepInterval(d time.Duration, step int) time.Duration {
	if step > 0 {
		for _, s := range intervalSteps {
			if s > d {
				return s
			}
		}
		return d
	}
	for i := len(intervalSteps) - 1; i >= 0; i-- {
		if intervalSteps[i] < d {
			return intervalSteps[i]
		}
	}
	return 0
}

// editParam начинает ввод значения по горячей клавише из любой панели. После
// ввода фокус возвращается туда, где был: иначе стрелки неожиданно начали бы
// двигать курсор в панели параметров вместо списка коллекторов или файлов.
func (m *Model) editParam(row int) tea.Cmd {
	if m.focus != paneParams {
		back := m.focus
		m.returnTo = &back
	}
	m.focus, m.paramCursor = paneParams, row
	return m.startParamEdit("")
}

// endParamEdit закрывает поле ввода параметра; back=true возвращает фокус в исходную панель.
func (m *Model) endParamEdit(back bool) {
	m.paramInput = nil
	if back && m.returnTo != nil {
		m.focus = *m.returnTo
	}
	m.returnTo = nil
}

func (m *Model) startParamEdit(initial string) tea.Cmd {
	in := newInput()
	in.CharLimit = 12
	in.SetWidth(14)
	in.SetValue(initial)
	in.CursorEnd()
	if m.paramCursor == paramCount {
		in.Placeholder = strconv.Itoa(m.count)
	} else {
		in.Placeholder = job.FormatInterval(m.interval)
	}
	cmd := in.Focus()
	m.paramInput = &in
	return cmd
}

func (m *Model) paramKey(msg tea.KeyPressMsg) tea.Cmd {
	name := msg.String()
	switch name {
	case "esc":
		m.endParamEdit(true)
		return nil
	case "enter", "tab", "up", "down":
		if m.burst && name == "enter" {
			return nil
		}
		if !m.commitParam() {
			return nil
		}
		// Enter возвращает туда, откуда пришли; стрелки и Tab оставляют в панели параметров.
		m.endParamEdit(name == "enter")
		switch name {
		case "up":
			m.move(-1)
		case "down":
			m.move(+1)
		}
		return nil
	}
	in, cmd := m.paramInput.Update(msg)
	m.paramInput = &in
	return cmd
}

// commitParam применяет введённое значение; пустой ввод оставляет прежнее.
func (m *Model) commitParam() bool {
	val := strings.TrimSpace(m.paramInput.Value())
	if val == "" {
		return true
	}
	if m.paramCursor == paramCount {
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 || n > maxCount {
			m.setFlash(journal.Error, "Число повторов — целое от 1 до %d (для бесконечной отправки включите цикл: l)", maxCount)
			return false
		}
		m.count = n
	} else {
		d, err := job.ParseInterval(val)
		if err != nil {
			m.setFlash(journal.Error, "Интервал: %v", err)
			return false
		}
		m.interval = d
	}
	return true
}

func itoa(n int) string { return strconv.Itoa(n) }

func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}
