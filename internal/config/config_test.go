package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDelimiter(t *testing.T) {
	cases := map[string]Delimiter{
		"":     DelimNone,
		"none": DelimNone,
		`\n`:   DelimLF,
		"\n":   DelimLF,
		"LF":   DelimLF,
		`\t`:   DelimTab,
		"\t":   DelimTab,
		`\0`:   DelimNUL,
		"\x00": DelimNUL,
		"NUL":  DelimNUL,
	}
	for in, want := range cases {
		got, err := ParseDelimiter(in)
		if err != nil || got != want {
			t.Errorf("ParseDelimiter(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseDelimiter("|"); err == nil {
		t.Error("ожидалась ошибка для неизвестного разделителя")
	}
	if string(DelimNUL.Bytes()) != "\x00" || string(DelimLF.Bytes()) != "\n" || DelimNone.Bytes() != nil {
		t.Error("Bytes() возвращает не те байты")
	}
}

func TestSaveLoadAndEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "collectors.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Collectors) != 0 || s.TimeoutSec != DefaultTimeoutSec {
		t.Fatalf("пустой конфиг: %+v", s.File)
	}
	s.DefaultHost = "kuma.example.local"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Put("", Collector{Name: " b-http ", Protocol: "HTTP", Port: 5365, Delimiter: DelimNUL, TLS: true, Path: "input"}))
	must(s.Put("", Collector{Name: "a-udp", Protocol: UDP, Port: 5144, TLS: true, Path: "/x"}))
	if err := s.Put("", Collector{Name: "A-UDP", Protocol: TCP, Port: 1}); err == nil {
		t.Error("ожидалась ошибка о дубликате имени")
	}
	if err := s.Put("", Collector{Name: "bad", Protocol: TCP, Port: 70000}); err == nil {
		t.Error("ожидалась ошибка о порте")
	}
	must(s.Put("a-udp", Collector{Name: "c-udp", Protocol: UDP, Port: 5144, Host: "10.0.0.1"}))
	must(s.Save())

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"delimiter": "\\0"`) {
		t.Errorf("разделитель должен сохраняться как \\0:\n%s", raw)
	}

	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Collectors) != 2 || s2.Collectors[0].Name != "b-http" || s2.Collectors[1].Name != "c-udp" {
		t.Fatalf("коллекторы: %+v", s2.Collectors)
	}
	h, u := s2.Collectors[0], s2.Collectors[1]
	if h.Path != "/input" || h.Target(s2.DefaultHost) != "https://kuma.example.local:5365/input" {
		t.Errorf("http: %+v → %s", h, h.Target(s2.DefaultHost))
	}
	if u.TLS || u.Path != "" || u.Target(s2.DefaultHost) != "udp://10.0.0.1:5144" {
		t.Errorf("udp: %+v → %s", u, u.Target(s2.DefaultHost))
	}
	if _, err := (Collector{Protocol: UDP, Port: 1}).Addr(""); err == nil {
		t.Error("ожидалась ошибка о незаданном хосте")
	}

	if c, err := s2.Match("HTTP"); err != nil || c.Name != "b-http" {
		t.Errorf("Match по подстроке: %v, %v", c.Name, err)
	}
	if _, err := s2.Match("-"); err == nil {
		t.Error("ожидалась ошибка о неоднозначном имени")
	}
	if !s2.Delete("C-UDP") || len(s2.Collectors) != 1 {
		t.Error("Delete не сработал")
	}
}

func TestHandEditedDelimiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collectors.json")
	// В файле, правленном руками, разделитель может быть записан как в интерфейсе KUMA
	// (\n, \0 — второе в JSON формально недопустимо), с двойной чертой или словом.
	os.WriteFile(path, []byte(`{"collectors":[
		{"name":"a","protocol":"tcp","port":1,"delimiter":"\n"},
		{"name":"b","protocol":"tcp","port":2,"delimiter":"null"},
		{"name":"c","protocol":"udp","port":3},
		{"name":"d","protocol":"http","port":4,"delimiter":"\0","description":"путь C:\\0 и \\0 не трогаем"},
		{"name":"e","protocol":"tcp","port":5,"delimiter":"\\0"},
		{"name":"f","protocol":"tcp","port":6,"delimiter":"\t"}]}`), 0o644)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Delimiter{DelimLF, DelimNUL, DelimNone, DelimNUL, DelimNUL, DelimTab}
	for i, c := range s.Collectors {
		if c.Delimiter != want[i] {
			t.Errorf("коллектор %s: разделитель %q, ожидали %q", c.Name, c.Delimiter, want[i])
		}
	}
	if got := s.Collectors[3].Description; got != `путь C:\0 и \0 не трогаем` {
		t.Errorf("описание изменилось: %q", got)
	}

	// Синтаксическая ошибка сообщается с номером строки.
	os.WriteFile(path, []byte("{\n  \"collectors\": [\n    {\"name\": \"a\",,}\n  ]\n}"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "строка 3") {
		t.Errorf("ожидалась ошибка с номером строки, получили: %v", err)
	}
}

// Пример конфига из репозитория должен оставаться рабочим.
func TestExampleConfigLoads(t *testing.T) {
	s, err := Load(filepath.Join("..", "..", "collectors.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Collectors) != 5 || s.DefaultHost == "" {
		t.Errorf("пример конфига: %+v", s.File)
	}
	if i := s.Index("KUMA Agent WEC"); i < 0 || s.Collectors[i].Delimiter != DelimNUL || !s.Collectors[i].TLS {
		t.Errorf("WEC из примера разобран неверно")
	}
}
