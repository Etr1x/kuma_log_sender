package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Используются только 16 базовых цветов ANSI: они есть в любом терминале,
// включая старую консоль Windows, и подстраиваются под его цветовую схему.
var (
	cAccent = lipgloss.Color("14")
	cDim    = lipgloss.Color("8")
	cOK     = lipgloss.Color("10")
	cErr    = lipgloss.Color("9")
	cWarn   = lipgloss.Color("11")

	stDim    = lipgloss.NewStyle().Foreground(cDim)
	stBold   = lipgloss.NewStyle().Bold(true)
	stAccent = lipgloss.NewStyle().Foreground(cAccent)
	stKey    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	stOK     = lipgloss.NewStyle().Foreground(cOK)
	stErr    = lipgloss.NewStyle().Foreground(cErr)
	stWarn   = lipgloss.NewStyle().Foreground(cWarn)
	stHeader = lipgloss.NewStyle().Bold(true).Foreground(cAccent)

	stBorder      = lipgloss.NewStyle().Foreground(cDim)
	stBorderFocus = lipgloss.NewStyle().Foreground(cAccent)
	stTitle       = lipgloss.NewStyle()
	stTitleFocus  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)

	// Выбранная строка: в активной панели — инверсия, в неактивной — только жирный.
	stSel     = lipgloss.NewStyle().Reverse(true)
	stSelIdle = lipgloss.NewStyle().Bold(true)
)

// fit обрезает или дополняет пробелами строку до ширины w (в ячейках терминала).
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "…")
		sw = lipgloss.Width(s)
	}
	if sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// leftRight размещает left слева, а right — у правого края строки шириной w.
func leftRight(left, right string, w int) string {
	rw := lipgloss.Width(right)
	if rw == 0 || rw+2 > w {
		return fit(left, w)
	}
	return fit(left, w-rw-1) + " " + right
}

// box рисует рамку с заголовком в верхней линии. w и h — внешние размеры.
// hint выводится в правой части верхней линии, если помещается.
func box(title, hint string, lines []string, w, h int, focused bool) string {
	bs, ts := stBorder, stTitle
	if focused {
		bs, ts = stBorderFocus, stTitleFocus
	}
	inner := w - 2
	if inner < 1 || h < 2 {
		return ""
	}
	title = " " + title + " "
	if lipgloss.Width(title) > inner-1 {
		title = ansi.Truncate(title, inner-1, "…")
	}
	tw := lipgloss.Width(title)
	if hint != "" {
		hint = " " + hint + " "
		if tw+lipgloss.Width(hint)+2 > inner {
			hint = ""
		}
	}
	var b strings.Builder
	b.WriteString(bs.Render("┌─") + ts.Render(title))
	if hint == "" {
		b.WriteString(bs.Render(strings.Repeat("─", inner-1-tw) + "┐"))
	} else {
		b.WriteString(bs.Render(strings.Repeat("─", inner-2-tw-lipgloss.Width(hint))) + stDim.Render(hint) + bs.Render("─┐"))
	}
	side := bs.Render("│")
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString("\n" + side + fit(line, inner) + side)
	}
	b.WriteString("\n" + bs.Render("└"+strings.Repeat("─", inner)+"┘"))
	return b.String()
}

// plural выбирает форму слова по числу: 1 событие, 2 события, 5 событий.
func plural(n int, one, few, many string) string {
	n %= 100
	if n < 0 {
		n = -n
	}
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// humanSize выводит размер в байтах коротко: 512 Б, 3.2 К, 1.5 М.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return itoa(int(n)) + " Б"
	case n < 1024*1024:
		return trimZero(float64(n)/1024) + " К"
	}
	return trimZero(float64(n)/(1024*1024)) + " М"
}
