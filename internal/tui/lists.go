package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

// source — элемент панели «Источник»: ручной ввод либо файл из папки logs.
type source struct {
	manual bool
	file   events.FileInfo
}

func matches(filter string, fields ...string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.Join(fields, " ")), strings.ToLower(filter))
}

// --- Коллекторы ---

func (m *Model) visCollectors() []config.Collector {
	all := m.app.Store.Collectors
	if m.colFilter == "" {
		return all
	}
	var out []config.Collector
	for _, c := range all {
		if matches(m.colFilter, c.Name, c.Scheme(), fmt.Sprint(c.Port), c.Host, c.Description) {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) curCollector() (config.Collector, bool) {
	vis := m.visCollectors()
	if m.colCursor < 0 || m.colCursor >= len(vis) {
		return config.Collector{}, false
	}
	return vis[m.colCursor], true
}

// selectCollector ставит курсор на коллектор с данным именем, если он виден.
func (m *Model) selectCollector(name string) {
	for i, c := range m.visCollectors() {
		if strings.EqualFold(c.Name, name) {
			m.colCursor = i
			break
		}
	}
	m.clamp()
}

// --- Источники ---

// visSources: первым всегда идёт ручной ввод, за ним файлы с учётом фильтра.
func (m *Model) visSources() []source {
	out := []source{{manual: true}}
	for _, f := range m.files {
		if matches(m.srcFilter, f.Name) {
			out = append(out, source{file: f})
		}
	}
	return out
}

func (m *Model) curSource() source {
	vis := m.visSources()
	if m.srcCursor < 0 || m.srcCursor >= len(vis) {
		return vis[0]
	}
	return vis[m.srcCursor]
}

// selectSource ставит курсор на файл с данным именем; пустое имя — ручной ввод.
func (m *Model) selectSource(file string) {
	m.srcCursor = 0
	if file != "" {
		for i, s := range m.visSources() {
			if !s.manual && s.file.Name == file {
				m.srcCursor = i
				break
			}
		}
	}
	m.clamp()
}

// reloadFiles перечитывает папку logs, сохраняя выбор по имени файла.
func (m *Model) reloadFiles() {
	cur := m.curSource()
	files, err := events.ListFiles(m.app.LogsDir)
	if err != nil && m.filesAt.IsZero() {
		m.setFlash(journal.Warn, "Папка логов недоступна: %v", err)
	}
	m.files = files
	m.filesAt = m.now()
	if !cur.manual {
		for i, s := range m.visSources() {
			if !s.manual && s.file.Name == cur.file.Name {
				m.srcCursor = i
				break
			}
		}
	}
	m.clamp()
}

// --- Фильтр ---

func (m *Model) filter() string {
	if m.focus == paneSources {
		return m.srcFilter
	}
	return m.colFilter
}

func (m *Model) setFilter(s string) {
	if m.focus == paneSources {
		m.srcFilter, m.srcCursor = s, 0
		// При фильтрации файлов курсор сразу встаёт на первый найденный файл.
		if s != "" && len(m.visSources()) > 1 {
			m.srcCursor = 1
		}
	} else {
		m.colFilter, m.colCursor = s, 0
	}
	m.clamp()
	m.refreshPreview()
}

func (m *Model) filterKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.setFilter("")
	case "enter":
		if !m.burst {
			m.filtering = false
		}
	case "up":
		m.move(-1)
	case "down":
		m.move(+1)
	case "tab":
		m.filtering = false
	case "backspace":
		if r := []rune(m.filter()); len(r) > 0 {
			m.setFilter(string(r[:len(r)-1]))
		} else {
			m.filtering = false
		}
	default:
		if text := keyText(msg); text != "" {
			m.setFilter(m.filter() + text)
		}
	}
}

// --- Перемещение ---

func (m *Model) move(delta int) {
	switch m.focus {
	case paneCollectors:
		m.colCursor += delta
		m.clamp()
		m.refreshPreview()
	case paneSources:
		m.srcCursor += delta
		m.clamp()
		m.refreshPreview()
	case paneParams:
		m.paramCursor = min(max(m.paramCursor+delta, 0), paramRows-1)
	case paneJobs:
		m.jobCursor += delta
		m.clamp()
	case paneJournal:
		m.scroll -= delta
		m.clamp()
	}
}

// page — размер «страницы» для PgUp/PgDn в активной панели.
func (m *Model) page() int {
	l := m.layout()
	switch m.focus {
	case paneJobs:
		return max(l.jobsH-2, 1)
	case paneJournal:
		return max(l.journalH-3, 1)
	}
	return max(l.topH-2, 1)
}

func clampCursor(cursor, offset *int, n, visible int) {
	*cursor = min(max(*cursor, 0), max(n-1, 0))
	if visible < 1 {
		visible = 1
	}
	if *cursor < *offset {
		*offset = *cursor
	}
	if *cursor >= *offset+visible {
		*offset = *cursor - visible + 1
	}
	*offset = min(max(*offset, 0), max(n-visible, 0))
}

// clamp возвращает курсоры и прокрутку в допустимые границы после любых изменений.
func (m *Model) clamp() {
	l := m.layout()
	clampCursor(&m.colCursor, &m.colOffset, len(m.visCollectors()), l.topH-2)
	clampCursor(&m.srcCursor, &m.srcOffset, len(m.visSources()), l.topH-2)
	m.jobCursor = min(max(m.jobCursor, 0), max(len(m.jobs)-1, 0))
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > 0 {
		total := 0
		for _, e := range m.entries {
			total += len(m.entryLines(e, l.w-2))
		}
		m.scroll = min(m.scroll, max(total-(l.journalH-2), 0))
	}
}
