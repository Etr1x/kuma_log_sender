package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

type fieldKind int

const (
	kindText fieldKind = iota
	kindChoice
	kindToggle
)

// field — одно поле формы.
type field struct {
	key, label, hint string
	kind             fieldKind
	input            textinput.Model // kindText
	options          []string        // kindChoice: подписи вариантов
	sel              int             // kindChoice: выбранный вариант
	on               bool            // kindToggle
	touched          bool            // значение менял пользователь
	hidden           func(f *formModal) bool
}

// formModal — окно с формой: редактор коллектора или настройки.
type formModal struct {
	title    string
	fields   []*field
	cursor   int
	err      string
	onChange func(f *formModal, key string)
	submit   func(m *Model, f *formModal) error
}

const formLabelW = 22

func textField(key, label, value, placeholder, hint string) *field {
	in := newInput()
	in.Placeholder = placeholder
	in.SetValue(value)
	in.CursorEnd()
	return &field{key: key, label: label, hint: hint, kind: kindText, input: in}
}

func (f *formModal) get(key string) *field {
	for _, fl := range f.fields {
		if fl.key == key {
			return fl
		}
	}
	panic("нет поля формы " + key)
}

func (f *formModal) text(key string) string {
	return strings.TrimSpace(f.get(key).input.Value())
}

func (f *formModal) visible(fl *field) bool {
	return fl.hidden == nil || !fl.hidden(f)
}

// step переводит курсор на соседнее видимое поле.
func (f *formModal) step(delta int) tea.Cmd {
	f.fields[f.cursor].input.Blur()
	n := len(f.fields)
	for i := 0; i < n; i++ {
		f.cursor = (f.cursor + delta + n) % n
		if f.visible(f.fields[f.cursor]) {
			break
		}
	}
	return f.focusCurrent()
}

func (f *formModal) focusCurrent() tea.Cmd {
	if cur := f.fields[f.cursor]; cur.kind == kindText {
		return cur.input.Focus()
	}
	return nil
}

// focusField ставит курсор на поле с ошибкой.
func (f *formModal) focusField(key string) {
	for i, fl := range f.fields {
		if fl.key == key && f.visible(fl) {
			f.fields[f.cursor].input.Blur()
			f.cursor = i
			f.focusCurrent()
		}
	}
}

func (f *formModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	cur := f.fields[f.cursor]
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if cur.kind == kindText {
			msg.Content = oneLineText(msg.Content)
			var cmd tea.Cmd
			cur.input, cmd = cur.input.Update(msg)
			cur.touched = true
			return false, cmd
		}
		return false, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return true, nil
		case "enter", "ctrl+s":
			if m.burst {
				return false, nil // перевод строки из вставленного текста — не подтверждение
			}
			if err := f.submit(m, f); err != nil {
				f.err = err.Error()
				var fe *fieldError
				if errors.As(err, &fe) {
					f.focusField(fe.key)
				}
				return false, nil
			}
			return true, nil
		case "up", "shift+tab":
			return false, f.step(-1)
		case "down", "tab":
			return false, f.step(+1)
		}
		f.err = ""
		switch cur.kind {
		case kindChoice:
			switch msg.String() {
			case "left":
				cur.sel = (cur.sel + len(cur.options) - 1) % len(cur.options)
			case "right", "space":
				cur.sel = (cur.sel + 1) % len(cur.options)
			default:
				return false, nil
			}
		case kindToggle:
			switch msg.String() {
			case "left", "right", "space":
				cur.on = !cur.on
			default:
				return false, nil
			}
		default:
			var cmd tea.Cmd
			before := cur.input.Value()
			cur.input, cmd = cur.input.Update(msg)
			if cur.input.Value() != before {
				cur.touched = true
			}
			return false, cmd
		}
		cur.touched = true
		if f.onChange != nil {
			f.onChange(f, cur.key)
		}
		return false, nil
	}
	if cur.kind == kindText {
		var cmd tea.Cmd
		cur.input, cmd = cur.input.Update(msg)
		return false, cmd
	}
	return false, nil
}

func (f *formModal) view(m *Model) string {
	w := min(m.w-6, 78)
	inner := w - 2
	valueW := inner - formLabelW - 3
	var lines []string
	lines = append(lines, "")
	for i, fl := range f.fields {
		if !f.visible(fl) {
			continue
		}
		focused := i == f.cursor
		var value string
		switch fl.kind {
		case kindChoice:
			value = fl.options[fl.sel]
			if focused {
				value = stAccent.Render("‹ ") + stBold.Render(value) + stAccent.Render(" ›")
			}
		case kindToggle:
			value = "[ ] нет"
			if fl.on {
				value = "[x] да"
			}
			if focused {
				value = stBold.Render(value)
			}
		default:
			fl.input.SetWidth(valueW - 1) // ещё одна ячейка уходит под курсор
			switch {
			case focused:
				value = fl.input.View()
			case fl.input.Value() == "":
				value = stDim.Render(ansi.Truncate(fl.input.Placeholder, valueW, "…"))
			default:
				value = ansi.Truncate(fl.input.Value(), valueW, "…")
			}
		}
		mark, label := "  ", fmt.Sprintf("%-*s", formLabelW, fl.label)
		if focused {
			mark, label = stAccent.Render("> "), stTitleFocus.Render(label)
		}
		lines = append(lines, mark+label+" "+value)
	}
	lines = append(lines, "", stDim.Render(strings.Repeat("─", inner)))
	hint := f.fields[f.cursor].hint
	for _, part := range strings.Split(ansi.Wordwrap(hint, inner-2, ""), "\n") {
		lines = append(lines, " "+stDim.Render(part))
	}
	if f.err != "" {
		for _, part := range strings.Split(ansi.Wordwrap(f.err, inner-2, ""), "\n") {
			lines = append(lines, " "+stErr.Render(part))
		}
	}
	lines = append(lines, "", help("Enter", "сохранить", "Esc", "отмена", "↑↓ Tab", "поля", "←→ пробел", "выбор"))
	return box(f.title, "", lines, w, len(lines)+2, true)
}

// fieldError — ошибка проверки, привязанная к полю формы.
type fieldError struct {
	key string
	msg string
}

func (e *fieldError) Error() string { return e.msg }

func protocolIndex(p config.Protocol) int {
	for i, v := range config.Protocols {
		if v == p {
			return i
		}
	}
	return 0
}

func delimiterIndex(d config.Delimiter) int {
	for i, v := range config.Delimiters {
		if v == d {
			return i
		}
	}
	return 0
}

// newCollectorForm открывает редактор коллектора. Пустой oldName — создание нового.
func newCollectorForm(m *Model, c config.Collector, oldName string) *formModal {
	defHost := m.app.Store.DefaultHost
	hostPlaceholder := "хост по умолчанию не задан (g)"
	if defHost != "" {
		hostPlaceholder = defHost + " (по умолчанию)"
	}
	port := ""
	if c.Port > 0 {
		port = strconv.Itoa(c.Port)
	}
	isNew := oldName == "" && c.Name == ""
	if isNew {
		c.Delimiter = config.DefaultDelimiter(c.Protocol)
	}
	path := c.Path
	if path == "" {
		path = config.DefaultHTTPPath
	}

	proto := &field{key: "protocol", label: "Протокол", kind: kindChoice, sel: protocolIndex(c.Protocol),
		hint: "Тип коннектора у коллектора в KUMA: tcp, udp или http."}
	for _, p := range config.Protocols {
		proto.options = append(proto.options, string(p))
	}
	delim := &field{key: "delimiter", label: "Разделитель", kind: kindChoice, sel: delimiterIndex(c.Delimiter), touched: !isNew,
		hint: "Должен совпадать с полем «Разделитель» коннектора. Дописывается после каждого события; " +
			"«нет» — ничего не дописывать (значение по умолчанию у udp и http)."}
	for _, d := range config.Delimiters {
		delim.options = append(delim.options, d.Label())
	}
	protocol := func(f *formModal) config.Protocol { return config.Protocols[f.get("protocol").sel] }

	f := &formModal{
		title: "Новый коллектор",
		fields: []*field{
			textField("name", "Имя", c.Name, "например: Linux Auditd", "Название коллектора в списке."),
			proto,
			textField("host", "Хост", c.Host, hostPlaceholder,
				"Адрес или DNS-имя сервера коллектора. Оставьте пустым, чтобы использовать хост по умолчанию из настроек (g)."),
			textField("port", "Порт", port, "например: 5144", "Порт из поля URL коннектора."),
			delim,
			{key: "tls", label: "TLS", kind: kindToggle, on: c.TLS,
				hint:   "Включите, если у коннектора «Режим TLS» отличается от «Выключено». Для http это означает HTTPS.",
				hidden: func(f *formModal) bool { return protocol(f) == config.UDP }},
			{key: "verify", label: "Проверять сертификат", kind: kindToggle, on: c.TLSVerify,
				hint:   "Обычно выключено: у коллекторов самоподписанные сертификаты. Если включить, сертификат сервера проверяется по системному хранилищу.",
				hidden: func(f *formModal) bool { return protocol(f) == config.UDP || !f.get("tls").on }},
			textField("path", "Путь", path, config.DefaultHTTPPath, "Путь HTTP-запроса. У http-коннектора KUMA это /input."),
			textField("description", "Описание", c.Description, "необязательно", "Произвольная пометка: показывается в панели параметров."),
		},
	}
	f.get("path").hidden = func(f *formModal) bool { return protocol(f) != config.HTTP }
	if oldName != "" {
		f.title = "Коллектор: " + oldName
	}
	f.onChange = func(f *formModal, key string) {
		// Пока пользователь сам не выбрал разделитель, подставляем типичный для протокола.
		if d := f.get("delimiter"); key == "protocol" && !d.touched {
			d.sel = delimiterIndex(config.DefaultDelimiter(protocol(f)))
		}
	}
	f.submit = func(m *Model, f *formModal) error {
		nc := config.Collector{
			Name:        f.text("name"),
			Description: f.text("description"),
			Protocol:    protocol(f),
			Host:        f.text("host"),
			Delimiter:   config.Delimiters[f.get("delimiter").sel],
			TLS:         f.get("tls").on,
			TLSVerify:   f.get("verify").on,
			Path:        f.text("path"),
		}
		if nc.Name == "" {
			return &fieldError{"name", "Укажите имя коллектора."}
		}
		p, err := strconv.Atoi(f.text("port"))
		if err != nil || p < 1 || p > 65535 {
			return &fieldError{"port", "Порт — число от 1 до 65535."}
		}
		nc.Port = p
		if i := m.app.Store.Index(nc.Name); i >= 0 && !strings.EqualFold(nc.Name, oldName) {
			return &fieldError{"name", fmt.Sprintf("Коллектор с именем «%s» уже есть.", nc.Name)}
		}
		if strings.ContainsAny(nc.Host, " /\\") {
			return &fieldError{"host", "В поле «Хост» нужен только адрес или имя, без http:// и пути."}
		}
		if nc.Host == "" && m.app.Store.DefaultHost == "" {
			return &fieldError{"host", "Укажите хост: хост по умолчанию пока не задан (его можно задать в настройках — клавиша g на основном экране)."}
		}
		if err := m.app.Store.Put(oldName, nc); err != nil {
			return err
		}
		m.saveStore("Коллектор «%s» сохранён", nc.Name)
		m.colFilter = ""
		m.selectCollector(nc.Name)
		m.refreshPreview()
		return nil
	}
	f.focusCurrent()
	return f
}

func newSettingsForm(m *Model) *formModal {
	f := &formModal{
		title: "Настройки",
		fields: []*field{
			textField("host", "Хост по умолчанию", m.app.Store.DefaultHost, "например: kuma-collector.example.local",
				"Адрес сервера коллекторов. Используется для всех коллекторов, у которых поле «Хост» пустое."),
			textField("timeout", "Таймаут, секунд", strconv.Itoa(m.app.Store.TimeoutSec), "5",
				"Сколько ждать подключения и ответа коллектора, прежде чем считать отправку неудачной."),
		},
	}
	f.submit = func(m *Model, f *formModal) error {
		host := f.text("host")
		if strings.ContainsAny(host, " /\\") {
			return &fieldError{"host", "Нужен только адрес или имя сервера, без http:// и пути."}
		}
		t, err := strconv.Atoi(f.text("timeout"))
		if err != nil || t < 1 || t > 300 {
			return &fieldError{"timeout", "Таймаут — целое число секунд от 1 до 300."}
		}
		m.app.Store.DefaultHost, m.app.Store.TimeoutSec = host, t
		m.saveStore("Настройки сохранены")
		return nil
	}
	f.focusCurrent()
	return f
}
