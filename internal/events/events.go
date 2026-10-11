// Package events превращает содержимое файла или ручной ввод в список
// событий, готовых к отправке на коллектор.
package events

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Mode — способ разбиения источника на события.
type Mode string

const (
	// ModeLines — каждая непустая строка отправляется отдельным событием.
	ModeLines Mode = "lines"
	// ModeWhole — весь текст отправляется одним событием (многострочные события).
	ModeWhole Mode = "whole"
	// ModeDelim — текст режется по разделителю коллектора (\0 или \t), что позволяет
	// хранить в одном файле несколько многострочных событий.
	ModeDelim Mode = "delim"
)

func (m Mode) Label() string {
	switch m {
	case ModeWhole:
		return "целиком"
	case ModeDelim:
		return "по разделителю"
	}
	return "построчно"
}

func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "lines", "line", "построчно":
		return ModeLines, nil
	case "whole", "file", "целиком":
		return ModeWhole, nil
	case "delim", "delimiter", "по разделителю":
		return ModeDelim, nil
	}
	return ModeLines, fmt.Errorf("неизвестный режим %q (допустимо: lines, whole, delim)", s)
}

// Modes возвращает режимы, имеющие смысл для данного разделителя: резать по \n —
// то же самое, что построчно, а без разделителя резать не по чему.
func Modes(delim []byte) []Mode {
	if len(delim) == 0 || bytes.Equal(delim, []byte{'\n'}) {
		return []Mode{ModeLines, ModeWhole}
	}
	return []Mode{ModeLines, ModeWhole, ModeDelim}
}

// Decode приводит сырые байты файла к виду, пригодному для отправки: снимает BOM,
// перекодирует UTF-16 (так пишет PowerShell 5 при перенаправлении вывода) в UTF-8
// и заменяет CRLF на LF, чтобы в события не попадал хвостовой \r.
// Возвращает пояснения о том, что было изменено.
func Decode(raw []byte) (data []byte, notes []string) {
	data = raw
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
		notes = append(notes, "снят BOM UTF-8")
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		data = decodeUTF16(data[2:], binary.LittleEndian)
		notes = append(notes, "UTF-16 LE перекодирован в UTF-8")
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		data = decodeUTF16(data[2:], binary.BigEndian)
		notes = append(notes, "UTF-16 BE перекодирован в UTF-8")
	}
	if bytes.Contains(data, []byte("\r\n")) {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	return data, notes
}

func decodeUTF16(b []byte, order binary.ByteOrder) []byte {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	out := make([]byte, 0, len(b))
	for _, r := range utf16.Decode(u) {
		out = utf8.AppendRune(out, r)
	}
	return out
}

// Split режет подготовленные данные на события согласно режиму. Пустые события
// отбрасываются. delim — байты разделителя коллектора (может быть nil).
func Split(data []byte, mode Mode, delim []byte) [][]byte {
	var out [][]byte
	switch mode {
	case ModeWhole:
		ev := bytes.TrimRight(data, "\r\n")
		// Файл мог быть подготовлен «по инструкции» с уже дописанным разделителем
		// в конце (truncate -s +1) — второй раз его добавлять не нужно.
		if len(delim) > 0 {
			ev = bytes.TrimRight(bytes.TrimSuffix(ev, delim), "\r\n")
		}
		if len(bytes.TrimSpace(ev)) > 0 {
			out = append(out, ev)
		}
	case ModeDelim:
		if len(delim) == 0 {
			return Split(data, ModeLines, nil)
		}
		for _, part := range bytes.Split(data, delim) {
			part = bytes.Trim(part, "\r\n")
			if len(bytes.TrimSpace(part)) > 0 {
				out = append(out, part)
			}
		}
	default:
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimRight(line, "\r")
			if len(bytes.TrimSpace(line)) > 0 {
				out = append(out, line)
			}
		}
	}
	return out
}

// FromText разбирает текст, введённый вручную.
func FromText(text string, mode Mode, delim []byte) [][]byte {
	data, _ := Decode([]byte(text))
	return Split(data, mode, delim)
}

// MaxFileSize — предел размера файла с логами: он целиком читается в память.
const MaxFileSize = 256 << 20

// FromFile читает файл и разбирает его на события.
func FromFile(path string, mode Mode, delim []byte) (evs [][]byte, notes []string, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if st.Size() > MaxFileSize {
		return nil, nil, fmt.Errorf("файл слишком большой (%d МБ, предел %d МБ)", st.Size()>>20, MaxFileSize>>20)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	data, notes := Decode(raw)
	return Split(data, mode, delim), notes, nil
}

// Head читает не более limit байт файла — для предпросмотра больших файлов.
// truncated сообщает, что файл длиннее прочитанного.
func Head(path string, limit int64) (raw []byte, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	raw, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > limit {
		return raw[:limit], true, nil
	}
	return raw, false, nil
}

// FileInfo — файл с логами из папки logs.
type FileInfo struct {
	Name    string // путь относительно папки logs, через «/»
	Path    string
	Size    int64
	ModTime time.Time
}

// ListFiles рекурсивно перечисляет файлы в dir. Скрытые файлы и папки пропускаются.
// Отсутствующая папка — не ошибка.
func ListFiles(dir string) ([]FileInfo, error) {
	var out []FileInfo
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil // недоступную подпапку просто пропускаем
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = d.Name()
		}
		out = append(out, FileInfo{Name: filepath.ToSlash(rel), Path: path, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, err
}
