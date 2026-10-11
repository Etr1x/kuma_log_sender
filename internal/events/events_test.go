package events

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func strs(evs [][]byte) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e)
	}
	return out
}

func TestDecode(t *testing.T) {
	data, notes := Decode([]byte("\xEF\xBB\xBFодин\r\nдва\r\n"))
	if string(data) != "один\nдва\n" || len(notes) != 1 {
		t.Errorf("UTF-8 BOM + CRLF: %q %v", data, notes)
	}
	// «hi\r\n» в UTF-16 LE с BOM — так пишет `>` в Windows PowerShell 5.
	data, notes = Decode([]byte{0xFF, 0xFE, 'h', 0, 'i', 0, '\r', 0, '\n', 0, 0x3F, 0x04, 0})
	if string(data) != "hi\nп" || len(notes) != 1 {
		t.Errorf("UTF-16 LE: %q %v", data, notes)
	}
	data, _ = Decode([]byte{0xFE, 0xFF, 0, 'o', 0, 'k'})
	if string(data) != "ok" {
		t.Errorf("UTF-16 BE: %q", data)
	}
	data, notes = Decode([]byte("plain\n"))
	if string(data) != "plain\n" || notes != nil {
		t.Errorf("без изменений: %q %v", data, notes)
	}
}

func TestSplit(t *testing.T) {
	nul := []byte{0}
	lf := []byte{'\n'}
	cases := []struct {
		name  string
		data  string
		mode  Mode
		delim []byte
		want  []string
	}{
		{"построчно", "a\n\n  \nb b\r\n c\n", ModeLines, lf, []string{"a", "b b", " c"}},
		{"построчно без перевода в конце", "a\nb", ModeLines, nil, []string{"a", "b"}},
		{"целиком", "<Event>\n  <x/>\n</Event>\n\n", ModeWhole, nul, []string{"<Event>\n  <x/>\n</Event>"}},
		{"целиком, разделитель уже в файле", "line1\nline2\x00", ModeWhole, nul, []string{"line1\nline2"}},
		{"целиком, пустой файл", " \n\n", ModeWhole, nil, nil},
		{"по разделителю", "ev1\nmore\x00\nev2\x00\n", ModeDelim, nul, []string{"ev1\nmore", "ev2"}},
		{"по разделителю без разделителя", "a\nb\n", ModeDelim, nil, []string{"a", "b"}},
	}
	for _, c := range cases {
		got := strs(Split([]byte(c.data), c.mode, c.delim))
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: получили %q, ожидали %q", c.name, got, c.want)
		}
	}
}

func TestModes(t *testing.T) {
	if got := Modes([]byte{'\n'}); len(got) != 2 {
		t.Errorf("для \\n режим «по разделителю» не нужен: %v", got)
	}
	if got := Modes([]byte{0}); len(got) != 3 || got[2] != ModeDelim {
		t.Errorf("для \\0 должны быть все три режима: %v", got)
	}
}

func TestListAndReadFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "win"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "b.log"), []byte("1\r\n2\r\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "A.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "win", "4624.xml"), []byte("<e/>"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("x"), 0o644)

	files, err := ListFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if want := []string{"A.txt", "b.log", "win/4624.xml"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("файлы: %v, ожидали %v", names, want)
	}

	evs, _, err := FromFile(files[1].Path, ModeLines, nil)
	if err != nil || !reflect.DeepEqual(strs(evs), []string{"1", "2"}) {
		t.Errorf("FromFile: %q %v", strs(evs), err)
	}
	if files, err := ListFiles(filepath.Join(dir, "нет-такой")); err != nil || files != nil {
		t.Errorf("отсутствующая папка: %v %v", files, err)
	}

	head, truncated, err := Head(files[1].Path, 3)
	if err != nil || !truncated || string(head) != "1\r\n" {
		t.Errorf("Head: %q %v %v", head, truncated, err)
	}
}
