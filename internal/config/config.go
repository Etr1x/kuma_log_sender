// Package config хранит список коллекторов KUMA и общие настройки утилиты
// в одном JSON-файле (collectors.json).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	FileVersion       = 1
	DefaultTimeoutSec = 5
	DefaultHTTPPath   = "/input"
)

// Protocol — тип коннектора коллектора.
type Protocol string

const (
	TCP  Protocol = "tcp"
	UDP  Protocol = "udp"
	HTTP Protocol = "http"
)

var Protocols = []Protocol{TCP, UDP, HTTP}

// Delimiter — разделитель событий, как он называется в интерфейсе KUMA:
// пусто, \n, \t или \0. Хранится в «экранированном» виде, чтобы файл можно
// было править руками.
type Delimiter string

const (
	DelimNone Delimiter = ""
	DelimLF   Delimiter = `\n`
	DelimTab  Delimiter = `\t`
	DelimNUL  Delimiter = `\0`
)

var Delimiters = []Delimiter{DelimNone, DelimLF, DelimTab, DelimNUL}

// ParseDelimiter понимает и запись KUMA (\n, \t, \0), и сами управляющие
// символы, и словесные синонимы.
func ParseDelimiter(s string) (Delimiter, error) {
	switch s {
	case "\n", "\r\n":
		return DelimLF, nil
	case "\t":
		return DelimTab, nil
	case "\x00":
		return DelimNUL, nil
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "no", "нет", "-":
		return DelimNone, nil
	case `\n`, "lf", "newline":
		return DelimLF, nil
	case `\t`, "tab":
		return DelimTab, nil
	case `\0`, "nul", "null", "0", `\x00`, `\u0000`:
		return DelimNUL, nil
	}
	return DelimNone, fmt.Errorf(`неизвестный разделитель %q (допустимо: \n, \t, \0 или пусто)`, s)
}

// Bytes возвращает байты, которые дописываются после каждого события.
func (d Delimiter) Bytes() []byte {
	switch d {
	case DelimLF:
		return []byte{'\n'}
	case DelimTab:
		return []byte{'\t'}
	case DelimNUL:
		return []byte{0}
	}
	return nil
}

func (d Delimiter) Label() string {
	if d == DelimNone {
		return "нет"
	}
	return string(d)
}

func (d *Delimiter) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParseDelimiter(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// DefaultDelimiter — разделитель по умолчанию для нового коллектора. У tcp-коннектора
// KUMA он всегда задан (\n), у udp и http по умолчанию пуст.
func DefaultDelimiter(p Protocol) Delimiter {
	if p == TCP {
		return DelimLF
	}
	return DelimNone
}

// Collector — один коллектор KUMA, точнее его входной коннектор.
type Collector struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Protocol    Protocol  `json:"protocol"`
	Host        string    `json:"host,omitempty"` // пусто — берётся default_host
	Port        int       `json:"port"`
	Delimiter   Delimiter `json:"delimiter"`
	TLS         bool      `json:"tls,omitempty"`        // только tcp и http
	TLSVerify   bool      `json:"tls_verify,omitempty"` // проверять сертификат сервера
	Path        string    `json:"path,omitempty"`       // только http
}

// Normalize приводит поля к каноническому виду.
func (c *Collector) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Description = strings.TrimSpace(c.Description)
	c.Host = strings.TrimSpace(c.Host)
	c.Protocol = Protocol(strings.ToLower(strings.TrimSpace(string(c.Protocol))))
	c.Path = strings.TrimSpace(c.Path)
	switch c.Protocol {
	case HTTP:
		if c.Path == "" {
			c.Path = DefaultHTTPPath
		}
		if !strings.HasPrefix(c.Path, "/") {
			c.Path = "/" + c.Path
		}
	case UDP:
		c.Path = ""
		c.TLS = false
	default:
		c.Path = ""
	}
	if !c.TLS {
		c.TLSVerify = false
	}
}

func (c Collector) Validate() error {
	if c.Name == "" {
		return errors.New("не задано имя коллектора")
	}
	switch c.Protocol {
	case TCP, UDP, HTTP:
	default:
		return fmt.Errorf("неизвестный протокол %q (допустимо: tcp, udp, http)", c.Protocol)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("порт должен быть числом от 1 до 65535 (сейчас %d)", c.Port)
	}
	if strings.ContainsAny(c.Host, " /\\") {
		return fmt.Errorf("в поле «хост» нужен только адрес или имя, без схемы и пути (сейчас %q)", c.Host)
	}
	return nil
}

// Scheme — схема для отображения: tcp, tcp+tls, udp, http, https.
func (c Collector) Scheme() string {
	switch {
	case c.Protocol == HTTP && c.TLS:
		return "https"
	case c.Protocol == TCP && c.TLS:
		return "tcp+tls"
	}
	return string(c.Protocol)
}

func (c Collector) ResolveHost(defaultHost string) string {
	if c.Host != "" {
		return c.Host
	}
	return strings.TrimSpace(defaultHost)
}

// Addr возвращает host:port для подключения.
func (c Collector) Addr(defaultHost string) (string, error) {
	host := c.ResolveHost(defaultHost)
	if host == "" {
		return "", errors.New("не задан хост: укажите его у коллектора или задайте хост по умолчанию в настройках")
	}
	return net.JoinHostPort(host, strconv.Itoa(c.Port)), nil
}

// Target — человекочитаемый адрес назначения, например udp://kuma:5144.
func (c Collector) Target(defaultHost string) string {
	host := c.ResolveHost(defaultHost)
	if host == "" {
		host = "<хост не задан>"
	}
	s := c.Scheme() + "://" + net.JoinHostPort(host, strconv.Itoa(c.Port))
	if c.Protocol == HTTP {
		s += c.Path
	}
	return s
}

// File — содержимое collectors.json.
type File struct {
	Version     int         `json:"version"`
	DefaultHost string      `json:"default_host"`
	TimeoutSec  int         `json:"timeout_sec"`
	Collectors  []Collector `json:"collectors"`
}

// Store — загруженный конфиг вместе с путём, куда его сохранять.
type Store struct {
	File
	Path string
}

// Load читает конфиг. Отсутствующий файл — не ошибка: возвращается пустой Store.
func Load(path string) (*Store, error) {
	s := &Store{Path: path, File: File{Version: FileVersion, TimeoutSec: DefaultTimeoutSec}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // BOM добавляют «Блокнот» и PowerShell 5
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(tolerateNUL(trimmed), &s.File); err != nil {
		return nil, fmt.Errorf("разбор %s: %s", path, describeJSONError(trimmed, err))
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = DefaultTimeoutSec
	}
	s.Version = FileVersion
	s.DefaultHost = strings.TrimSpace(s.DefaultHost)
	seen := map[string]bool{}
	for i := range s.Collectors {
		c := &s.Collectors[i]
		c.Normalize()
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: коллектор №%d (%q): %w", path, i+1, c.Name, err)
		}
		key := strings.ToLower(c.Name)
		if seen[key] {
			return nil, fmt.Errorf("%s: имя коллектора %q встречается дважды", path, c.Name)
		}
		seen[key] = true
	}
	s.sort()
	return s, nil
}

// tolerateNUL заменяет в строках JSON запись \0 на допустимую \u0000. Разделитель
// в KUMA пишется как \0, и при ручной правке файла его естественно набрать именно
// так, хотя в JSON такой escape-последовательности нет.
func tolerateNUL(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(`\0`)) {
		return raw
	}
	out := make([]byte, 0, len(raw)+8)
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			inString = !inString
		case c == '\\' && inString && i+1 < len(raw):
			i++
			if raw[i] == '0' {
				out = append(out, `\u0000`...)
			} else {
				out = append(out, c, raw[i])
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// describeJSONError дополняет синтаксическую ошибку номером строки.
func describeJSONError(raw []byte, err error) string {
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		return err.Error()
	}
	offset := min(int(syn.Offset), len(raw))
	line := 1 + bytes.Count(raw[:offset], []byte{'\n'})
	return fmt.Sprintf("строка %d: %v", line, err)
}

// Save атомарно записывает конфиг на диск.
func (s *Store) Save() error {
	s.sort()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	f := s.File
	if f.Collectors == nil {
		f.Collectors = []Collector{}
	}
	if err := enc.Encode(f); err != nil {
		return err
	}
	if dir := filepath.Dir(s.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("запись %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("запись %s: %w", s.Path, err)
	}
	return nil
}

func (s *Store) sort() {
	sort.SliceStable(s.Collectors, func(i, j int) bool {
		return strings.ToLower(s.Collectors[i].Name) < strings.ToLower(s.Collectors[j].Name)
	})
}

func (s *Store) Timeout() time.Duration {
	if s.TimeoutSec <= 0 {
		return DefaultTimeoutSec * time.Second
	}
	return time.Duration(s.TimeoutSec) * time.Second
}

// Index ищет коллектор по точному имени без учёта регистра.
func (s *Store) Index(name string) int {
	for i, c := range s.Collectors {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

// Match ищет коллектор для CLI: сначала по точному имени, затем по подстроке,
// если она однозначна.
func (s *Store) Match(query string) (Collector, error) {
	if i := s.Index(query); i >= 0 {
		return s.Collectors[i], nil
	}
	var found []Collector
	q := strings.ToLower(query)
	for _, c := range s.Collectors {
		if strings.Contains(strings.ToLower(c.Name), q) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return Collector{}, fmt.Errorf("коллектор %q не найден", query)
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, c := range found {
		names[i] = c.Name
	}
	return Collector{}, fmt.Errorf("под %q подходит несколько коллекторов: %s", query, strings.Join(names, ", "))
}

// Put добавляет коллектор или заменяет существующий с именем oldName
// (пустой oldName — добавление). Список остаётся отсортированным по имени.
func (s *Store) Put(oldName string, c Collector) error {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	old := -1
	if oldName != "" {
		old = s.Index(oldName)
	}
	if i := s.Index(c.Name); i >= 0 && i != old {
		return fmt.Errorf("коллектор с именем %q уже есть", c.Name)
	}
	if old >= 0 {
		s.Collectors[old] = c
	} else {
		s.Collectors = append(s.Collectors, c)
	}
	s.sort()
	return nil
}

func (s *Store) Delete(name string) bool {
	i := s.Index(name)
	if i < 0 {
		return false
	}
	s.Collectors = append(s.Collectors[:i], s.Collectors[i+1:]...)
	return true
}
