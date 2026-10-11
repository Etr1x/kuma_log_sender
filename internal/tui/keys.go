package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Горячие клавиши должны работать и в русской раскладке: «у» — это та же
// клавиша, что и «e». Таблица сопоставляет символы ЙЦУКЕН с QWERTY.
var ruToLat = func() map[rune]rune {
	const (
		ru = "йцукенгшщзхъфывапролджэячсмитьбюё" + "ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЯЧСМИТЬБЮЁ"
		en = "qwertyuiop[]asdfghjkl;'zxcvbnm,.`" + "QWERTYUIOP{}ASDFGHJKL:\"ZXCVBNM<>~"
	)
	m := map[rune]rune{}
	enr := []rune(en)
	for i, r := range []rune(ru) {
		m[r] = enr[i]
	}
	return m
}()

// keyText возвращает печатный текст клавиши или пустую строку для служебных
// клавиш и сочетаний с Ctrl/Alt.
func keyText(msg tea.KeyPressMsg) string {
	k := msg.Key()
	// Ctrl+Alt — это AltGr: с ним на некоторых раскладках набираются обычные символы.
	if mod := k.Mod & (tea.ModCtrl | tea.ModAlt | tea.ModMeta | tea.ModSuper | tea.ModHyper); mod != 0 && mod != tea.ModCtrl|tea.ModAlt {
		return ""
	}
	return k.Text
}

// hotkey приводит нажатие к имени горячей клавиши в латинской раскладке.
func hotkey(msg tea.KeyPressMsg) string {
	text := keyText(msg)
	if text == "" {
		return msg.String()
	}
	if r := []rune(text); len(r) == 1 {
		if lat, ok := ruToLat[r[0]]; ok {
			return string(lat)
		}
	}
	if text == " " {
		return "space"
	}
	return text
}

// lower сводит регистр для сочетаний, где он не важен (y/Y в подтверждении).
func lower(key string) string {
	if len([]rune(key)) == 1 {
		return strings.ToLower(key)
	}
	return key
}
