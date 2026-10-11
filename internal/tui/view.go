package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Etr1x/kuma_log_sender/internal/app"
	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/job"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

const (
	minW = 80
	minH = 20
)

// layout — размеры панелей основного экрана (внешние, вместе с рамками).
type layout struct {
	w, h             int
	colW, srcW, parW int
	topH             int
	prevH            int // 0 — предпросмотр скрыт
	jobsH            int
	journalH         int
}

func (m *Model) layout() layout {
	l := layout{w: max(m.w, minW), h: max(m.h, minH)}
	l.colW = min(max(l.w*32/100, 26), 44)
	l.srcW = min(max(l.w*31/100, 24), 44)
	l.parW = l.w - l.colW - l.srcW

	avail := l.h - 2 // минус шапка и строка подсказок
	jobs := min(max(len(m.jobs), 1), 4)
	if l.h < 30 {
		jobs = min(jobs, 2)
	}
	top := min(max(avail/3-2, 7), 12)
	prev := 5
	switch {
	case l.h < 30:
		prev = 2
	case l.h < 40:
		prev = 3
	}
	// Журналу нужно хотя бы 3–4 строки: ужимаем предпросмотр, затем задачи, затем верх.
	journal := func() int { return avail - (top + 2) - (jobs + 2) - (prev + 2) - 2 }
	for journal() < 4 && prev > 1 {
		prev--
	}
	for journal() < 4 && jobs > 1 {
		jobs--
	}
	for journal() < 3 && top > 5 {
		top--
	}
	l.topH, l.prevH, l.jobsH = top+2, prev+2, jobs+2
	l.journalH = avail - l.topH - l.prevH - l.jobsH
	return l
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "KUMA Log Sender"
	return v
}

func (m *Model) render() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.w < minW || m.h < minH {
		msg := fmt.Sprintf("Окно слишком маленькое: %d×%d.\nНужно не меньше %d×%d — увеличьте окно терминала.", m.w, m.h, minW, minH)
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg)
	}
	base := m.renderMain()
	if m.modal == nil {
		return base
	}
	win := m.modal.view(m)
	ww, wh := lipgloss.Size(win)
	x, y := max((m.w-ww)/2, 0), max((m.h-wh)/2, 0)
	return lipgloss.NewCompositor(lipgloss.NewLayer(base), lipgloss.NewLayer(win).X(x).Y(y).Z(1)).Render()
}

func (m *Model) renderMain() string {
	l := m.layout()
	top := lipgloss.JoinHorizontal(lipgloss.Top, m.collectorsBox(l), m.sourcesBox(l), m.paramsBox(l))
	return strings.Join([]string{
		m.header(l),
		top,
		m.previewBox(l),
		m.jobsBox(l),
		m.journalBox(l),
		m.footer(l),
	}, "\n")
}

func (m *Model) header(l layout) string {
	host := m.app.Store.DefaultHost
	if host == "" {
		host = stWarn.Render("не задан")
	}
	left := stHeader.Render(" KUMA Log Sender ") + stDim.Render(app.Version) +
		stDim.Render("  │  хост по умолчанию: ") + host +
		stDim.Render("  │  таймаут: ") + fmt.Sprintf("%d с", m.app.Store.TimeoutSec)
	// Рабочая папка показывается справа, только если для неё осталось место.
	room := l.w - lipgloss.Width(left) - 3
	if room < 12 {
		return fit(left, l.w)
	}
	base := m.app.Base
	if lipgloss.Width(base) > room {
		base = ansi.TruncateLeft(base, lipgloss.Width(base)-room+1, "…")
	}
	return leftRight(left, stDim.Render(base+" "), l.w)
}

// listRows рисует видимую часть списка: строка под курсором выделяется.
func listRows(n, cursor, offset, height, width int, focused bool, row func(i int) (left, right string)) []string {
	var out []string
	for i := offset; i < n && len(out) < height; i++ {
		left, right := row(i)
		mark := "  "
		if i == cursor {
			mark = "> "
		}
		line := leftRight(mark+left, right, width-1) + " "
		switch {
		case i == cursor && focused:
			line = stSel.Render(ansi.Strip(line))
		case i == cursor:
			line = stSelIdle.Render(line)
		}
		out = append(out, line)
	}
	return out
}

func position(cursor, n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", cursor+1, n)
}

func (m *Model) filterTitle(title, filter string, p pane) string {
	switch {
	case m.filtering && m.focus == p:
		return title + " /" + filter + "█"
	case filter != "":
		return title + " /" + filter
	}
	return title
}

func (m *Model) collectorsBox(l layout) string {
	vis := m.visCollectors()
	focused := m.focus == paneCollectors
	inner := l.colW - 2
	var lines []string
	switch {
	case len(m.app.Store.Collectors) == 0:
		lines = []string{"", stDim.Render("  список пуст"), "", "  " + stKey.Render("a") + " — добавить коллектор"}
	case len(vis) == 0:
		lines = []string{"", stDim.Render("  ничего не найдено"), stDim.Render("  Esc — сбросить фильтр")}
	default:
		lines = listRows(len(vis), m.colCursor, m.colOffset, l.topH-2, inner, focused, func(i int) (string, string) {
			c := vis[i]
			return c.Name, stDim.Render(fmt.Sprintf("%s %d", c.Scheme(), c.Port))
		})
	}
	return box(m.filterTitle("Коллекторы", m.colFilter, paneCollectors), position(m.colCursor, len(vis)), lines, l.colW, l.topH, focused)
}

func (m *Model) sourcesBox(l layout) string {
	vis := m.visSources()
	focused := m.focus == paneSources
	lines := listRows(len(vis), m.srcCursor, m.srcOffset, l.topH-2, l.srcW-2, focused, func(i int) (string, string) {
		s := vis[i]
		if !s.manual {
			return s.file.Name, stDim.Render(humanSize(s.file.Size))
		}
		if strings.TrimSpace(m.manual) == "" {
			return "[ручной ввод]", stDim.Render("i")
		}
		return "[ввод] " + job.Preview([]byte(m.manual), 60), ""
	})
	if len(m.files) == 0 && len(lines) < l.topH-2 {
		lines = append(lines, "", stDim.Render("  в папке logs нет файлов"))
	}
	return box(m.filterTitle("Источник (logs)", m.srcFilter, paneSources), position(m.srcCursor, len(vis)), lines, l.srcW, l.topH, focused)
}

func (m *Model) paramsBox(l layout) string {
	focused := m.focus == paneParams
	inner := l.parW - 2
	loop := "выкл"
	if m.loop {
		loop = stWarn.Render("вкл") + stDim.Render(" — до остановки")
	}
	count := itoa(m.count)
	if m.loop {
		count = stDim.Render(count + " (не учитывается)")
	}
	values := [paramRows]string{
		paramMode:     m.effMode().Label(),
		paramCount:    count,
		paramInterval: job.FormatInterval(m.interval),
		paramLoop:     loop,
	}
	labels := [paramRows]string{"Режим", "Повторов", "Интервал", "Цикл"}
	keys := [paramRows]string{"m", "c", "t", "l"}

	var lines []string
	for i := 0; i < paramRows; i++ {
		value := values[i]
		editing := focused && i == m.paramCursor && m.paramInput != nil
		if editing {
			value = m.paramInput.View()
		}
		mark := "  "
		if i == m.paramCursor && focused {
			mark = "> "
		}
		line := leftRight(fmt.Sprintf("%s%-9s %s", mark, labels[i], value), stDim.Render(keys[i]), inner-1) + " "
		if i == m.paramCursor && focused && !editing {
			line = stSel.Render(ansi.Strip(line))
		}
		lines = append(lines, line)
	}

	if c, ok := m.curCollector(); ok {
		lines = append(lines,
			" "+stAccent.Render("→ "+c.Target(m.app.Store.DefaultHost)),
			" "+m.totals(),
			" "+stDim.Render("разделитель: ")+c.Delimiter.Label(),
		)
		if c.Description != "" {
			lines = append(lines, " "+stDim.Render(c.Description))
		}
	} else {
		lines = append(lines, stDim.Render(" коллектор не выбран"))
	}
	return box("Параметры", "", lines, l.parW, l.topH, focused)
}

// totals — сколько отправок получится при текущих настройках.
func (m *Model) totals() string {
	n := len(m.prev.events)
	if m.prev.err != nil {
		return stErr.Render("источник не читается")
	}
	if n == 0 {
		return stWarn.Render("нет событий для отправки")
	}
	more := ""
	if m.prev.partial {
		more = "+"
	}
	evs := fmt.Sprintf("%d%s %s", n, more, plural(n, "событие", "события", "событий"))
	if m.loop {
		return evs + " × ∞"
	}
	total := n * m.count
	s := fmt.Sprintf("%s × %d = %d%s %s", evs, m.count, total, more, plural(total, "отправка", "отправки", "отправок"))
	if d := time.Duration(total-1) * m.interval; d >= time.Second && !m.prev.partial {
		s += stDim.Render(", ≈ " + d.Round(time.Second).String())
	}
	return s
}

func (m *Model) previewBox(l layout) string {
	p := m.prev
	inner := l.w - 2
	rows := l.prevH - 2
	title := "Предпросмотр: " + p.title
	hint := ""
	var lines []string
	switch {
	case p.err != nil:
		lines = []string{" " + stErr.Render(p.err.Error())}
	case len(p.events) == 0:
		lines = []string{stDim.Render(" (пусто — отправлять нечего)")}
		if p.title == "ручной ввод" {
			lines = []string{stDim.Render(" текст не введён: нажмите ") + stKey.Render("i") + stDim.Render(" или просто вставьте лог из буфера обмена")}
		}
	default:
		n := len(p.events)
		more := ""
		if p.partial {
			more = "+"
		}
		hint = fmt.Sprintf("%d%s %s, %s, %s", n, more, plural(n, "событие", "события", "событий"), humanSize(int64(p.bytes)), m.effMode().Label())
		numW := len(itoa(min(n, rows)))
		for i := 0; i < n && i < rows; i++ {
			text := job.Preview(p.events[i], inner)
			if rows > 1 && i == rows-1 && n > rows {
				text = stDim.Render(fmt.Sprintf("… и ещё %d", n-rows+1))
				lines = append(lines, fmt.Sprintf(" %*s  %s", numW, "", text))
				break
			}
			lines = append(lines, fmt.Sprintf(" %s %s", stDim.Render(fmt.Sprintf("%*d│", numW, i+1)), text))
		}
	}
	if len(p.notes) > 0 {
		title += stDim.Render(" (" + strings.Join(p.notes, "; ") + ")")
	}
	return box(title, hint, lines, l.w, l.prevH, false)
}

func (m *Model) jobsBox(l layout) string {
	focused := m.focus == paneJobs
	inner := l.w - 2
	rows := l.jobsH - 2
	active := 0
	for _, j := range m.jobs {
		if j.State == job.Running {
			active++
		}
	}
	title := "Задачи"
	if active > 0 {
		title += fmt.Sprintf(" — активных: %d", active)
	}
	if len(m.jobs) == 0 {
		line := stDim.Render(" задач нет: ") + stKey.Render("Enter") + stDim.Render(" отправляет выбранный источник на выбранный коллектор")
		return box(title, "", []string{line}, l.w, l.jobsH, focused)
	}
	offset := min(max(m.jobCursor-rows+1, 0), max(len(m.jobs)-rows, 0))
	if !focused {
		offset = 0
	}
	now := m.now()
	var lines []string
	for i := offset; i < len(m.jobs) && len(lines) < rows; i++ {
		j := m.jobs[i]
		state := j.State.Label()
		switch {
		case j.State == job.Running:
			state = stOK.Render(fmt.Sprintf("%-11s", state))
		case j.State == job.Failed:
			state = stErr.Render(fmt.Sprintf("%-11s", state))
		case j.Warned || j.Errors > 0:
			state = stWarn.Render(fmt.Sprintf("%-11s", state+" (!)"))
		default:
			state = stDim.Render(fmt.Sprintf("%-11s", state))
		}
		total := "∞"
		if j.Total > 0 {
			total = fmt.Sprint(j.Total)
		} else if !j.Loop {
			total = "?"
		}
		progress := fmt.Sprintf("%d/%s", j.Sent+j.Errors, total)
		errs := ""
		if j.Errors > 0 {
			errs = stErr.Render(fmt.Sprintf("  ошибок: %d", j.Errors))
		}
		next := ""
		switch {
		case j.State == job.Running && !j.NextAt.IsZero():
			if d := j.NextAt.Sub(now); d > 0 {
				next = stDim.Render(fmt.Sprintf("  следующая через %.1f с", d.Seconds()))
			}
		case j.State != job.Running && !j.Finished.IsZero():
			next = stDim.Render("  " + j.Finished.Format("15:04:05"))
		}
		mark := "  "
		if i == m.jobCursor && focused {
			mark = "> "
		}
		left := fmt.Sprintf("%s#%-3d %s %s ← %s", mark, j.ID, state, j.Collector, strings.TrimPrefix(j.Source, "файл "))
		line := leftRight(left, progress+errs+next+" ", inner)
		if i == m.jobCursor && focused {
			line = stSel.Render(ansi.Strip(line))
		}
		lines = append(lines, line)
	}
	hint := ""
	if len(m.jobs) > rows {
		hint = fmt.Sprintf("всего %d", len(m.jobs))
	}
	return box(title, hint, lines, l.w, l.jobsH, focused)
}

// entryLines — запись журнала, разбитая на строки шириной width. Ошибки и
// предупреждения всегда переносятся целиком, чтобы причина не обрезалась.
// Записи об успешной отправке занимают одну строку (текст события обрезается),
// пока клавишей w не включён полный перенос; в sender.log всё пишется полностью.
func (m *Model) entryLines(e journal.Entry, width int) []string {
	prefix := " " + e.Time.Format("15:04:05.000") + " " + e.Level.Tag() + " "
	if e.Job > 0 {
		prefix += fmt.Sprintf("#%d ", e.Job)
	}
	room := width - lipgloss.Width(prefix) - 1
	if room < 10 {
		return []string{e.Text}
	}
	if e.Level == journal.OK && !m.wrap {
		return []string{ansi.Truncate(e.Text, room, "…")}
	}
	return strings.Split(ansi.Wrap(e.Text, room, ""), "\n")
}

func (m *Model) journalBox(l layout) string {
	focused := m.focus == paneJournal
	inner := l.w - 2
	rows := l.journalH - 2
	need := rows + m.scroll

	// Собираем строки с конца, пока не наберётся нужное количество.
	var rev []string
	for i := len(m.entries) - 1; i >= 0 && len(rev) < need; i-- {
		e := m.entries[i]
		tag := e.Level.Tag()
		text := lipgloss.NewStyle()
		switch e.Level {
		case journal.OK:
			tag = stOK.Render(tag)
		case journal.Warn:
			tag, text = stWarn.Render(tag), stWarn
		case journal.Error:
			tag, text = stErr.Bold(true).Render(tag), stErr
		default:
			tag, text = stDim.Render(tag), stDim
		}
		prefix := " " + stDim.Render(e.Time.Format("15:04:05.000")) + " " + tag + " "
		if e.Job > 0 {
			prefix += stAccent.Render(fmt.Sprintf("#%d ", e.Job))
		}
		pad := strings.Repeat(" ", lipgloss.Width(prefix))
		parts := m.entryLines(e, inner)
		for k := len(parts) - 1; k >= 0; k-- {
			if k == 0 {
				rev = append(rev, prefix+text.Render(parts[k]))
			} else {
				rev = append(rev, pad+text.Render(parts[k]))
			}
		}
	}
	start := min(m.scroll, max(len(rev)-rows, 0))
	end := min(start+rows, len(rev))
	lines := make([]string, 0, rows)
	for i := end - 1; i >= start; i-- {
		lines = append(lines, rev[i])
	}
	if len(m.entries) == 0 {
		lines = []string{stDim.Render(" здесь появится каждая отправка: что, куда, сколько байт и — если не ушло — почему")}
	}
	title := "Журнал"
	hint := "полностью — в " + app.LogName
	if m.scroll > 0 {
		hint = fmt.Sprintf("прокрутка: −%d строк, End — к последним", m.scroll)
	}
	return box(title, hint, lines, l.w, l.journalH, focused)
}

func help(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, stKey.Render(pairs[i])+" "+stDim.Render(pairs[i+1]))
	}
	return " " + strings.Join(parts, stDim.Render(" · "))
}

func (m *Model) footer(l layout) string {
	if m.flash != "" {
		st := stDim
		switch m.flashLevel {
		case journal.OK:
			st = stOK
		case journal.Warn:
			st = stWarn
		case journal.Error:
			st = stErr
		case journal.Info:
			st = stAccent
		}
		return fit(" "+st.Render(m.flash), l.w)
	}
	var s string
	switch {
	case m.filtering:
		s = help("ввод", "фильтр", "Enter", "применить", "Esc", "сбросить", "↑↓", "выбор")
	case m.paramInput != nil:
		s = help("Enter", "применить", "Esc", "отмена")
	case m.focus == paneParams:
		s = help("Enter", "отправить", "пробел + −", "изменить", "цифры", "ввести значение", "Tab", "панель")
	case m.focus == paneJobs:
		s = help("Enter", "отправить", "s", "стоп", "S", "стоп все", "x", "убрать завершённые", "Tab", "панель")
	case m.focus == paneJournal:
		s = help("Enter", "отправить", "↑↓ PgUp PgDn", "прокрутка", "End", "к последним", "w", "перенос строк", "Ctrl+L", "очистить")
	case m.focus == paneCollectors:
		s = help("Enter", "отправить", "a", "новый", "e", "изменить", "d", "удалить", "y", "копия", "/", "фильтр", "i", "ввод", "s", "стоп")
	default:
		s = help("Enter", "отправить", "i", "ввод", "m", "режим", "c", "повторы", "t", "интервал", "l", "цикл", "s", "стоп", "/", "фильтр")
	}
	return leftRight(s, stKey.Render("?")+" "+stDim.Render("справка "), l.w)
}

// --- Предпросмотр ---

// preview — что будет отправлено при текущем выборе.
type preview struct {
	title   string
	events  [][]byte
	bytes   int
	partial bool // построен по началу большого файла; событий на самом деле больше
	notes   []string
	err     error
	stamp   string
}

const (
	previewFull = 4 << 20   // файлы до этого размера читаются для предпросмотра целиком
	previewHead = 256 << 10 // от более крупных берётся только начало
)

// refreshPreview пересчитывает предпросмотр, если изменился выбор или сам файл.
func (m *Model) refreshPreview() {
	var delim []byte
	if c, ok := m.curCollector(); ok {
		delim = c.Delimiter.Bytes()
	}
	mode := m.effMode()
	src := m.curSource()
	stamp := fmt.Sprintf("%s|%q|", mode, delim)
	if src.manual {
		stamp += "manual|" + m.manual
	} else {
		stamp += fmt.Sprintf("%s|%d|%d", src.file.Path, src.file.Size, src.file.ModTime.UnixNano())
	}
	if stamp == m.prev.stamp {
		return
	}
	p := preview{stamp: stamp}
	switch {
	case src.manual:
		p.title = "ручной ввод"
		p.events = events.FromText(m.manual, mode, delim)
	case src.file.Size <= previewFull:
		p.title = src.file.Name
		p.events, p.notes, p.err = events.FromFile(src.file.Path, mode, delim)
	default:
		p.title = src.file.Name
		raw, truncated, err := events.Head(src.file.Path, previewHead)
		data, notes := events.Decode(raw)
		p.events, p.notes, p.err = events.Split(data, mode, delim), notes, err
		if truncated {
			p.partial = true
			if len(p.events) > 1 {
				p.events = p.events[:len(p.events)-1] // последнее событие могло быть оборвано
			}
		}
	}
	for _, ev := range p.events {
		p.bytes += len(ev)
	}
	m.prev = p
}
