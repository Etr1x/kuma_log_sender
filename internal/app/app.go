// Package app связывает части утилиты: пути к файлам, конфиг, журнал и задачи.
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/job"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

// Version выставляет main; показывается в шапке интерфейса.
var Version = "dev"

const (
	ConfigName = "collectors.json"
	LogsName   = "logs"
	LogName    = "sender.log"
	StateName  = "state.json"
	// EnvHome — переменная окружения с рабочей папкой утилиты.
	EnvHome = "KUMA_LOG_SENDER_HOME"
)

// Paths — расположение файлов утилиты. Всё лежит в одной рабочей папке,
// чтобы утилиту можно было переносить целиком.
type Paths struct {
	Base    string
	Config  string
	LogsDir string
	LogFile string
	State   string
}

// ResolvePaths выбирает рабочую папку: явно заданную (флаг или переменная
// окружения), затем текущую, если в ней уже есть конфиг или папка logs,
// и наконец папку, где лежит исполняемый файл.
func ResolvePaths(dir string) Paths {
	if dir == "" {
		dir = os.Getenv(EnvHome)
	}
	if dir == "" {
		dir = defaultBase()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return Paths{
		Base:    dir,
		Config:  filepath.Join(dir, ConfigName),
		LogsDir: filepath.Join(dir, LogsName),
		LogFile: filepath.Join(dir, LogName),
		State:   filepath.Join(dir, StateName),
	}
}

func defaultBase() string {
	cwd, _ := os.Getwd()
	if cwd != "" && (exists(filepath.Join(cwd, ConfigName)) || exists(filepath.Join(cwd, LogsName))) {
		return cwd
	}
	exe, err := os.Executable()
	if err != nil {
		return cwd
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	exeDir := filepath.Dir(exe)
	// При запуске через `go run` бинарь лежит во временной папке — она не годится.
	if tmp := os.TempDir(); cwd != "" && tmp != "" && strings.HasPrefix(strings.ToLower(exeDir), strings.ToLower(filepath.Clean(tmp))) {
		return cwd
	}
	return exeDir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// App — общее состояние утилиты, не зависящее от интерфейса.
type App struct {
	Paths
	Store   *config.Store
	Journal *journal.Journal
	Jobs    *job.Manager
}

// Open загружает конфиг, открывает журнал и готовит менеджер задач.
// Некритичные проблемы (не открылся файл журнала, не создалась папка логов)
// попадают в сам журнал.
func Open(p Paths) (*App, error) {
	store, err := config.Load(p.Config)
	if err != nil {
		return nil, err
	}
	jr, jerr := journal.New(p.LogFile)
	a := &App{Paths: p, Store: store, Journal: jr, Jobs: job.NewManager(jr)}
	if jerr != nil {
		jr.Add(journal.Warn, 0, "%v", jerr)
	}
	if err := os.MkdirAll(p.LogsDir, 0o755); err != nil {
		jr.Add(journal.Warn, 0, "не удалось создать папку логов: %v", err)
	}
	return a, nil
}

func (a *App) Close() {
	a.Journal.Close()
}

// State — то, что интерфейс запоминает между запусками.
type State struct {
	Collector string `json:"collector"`
	Source    string `json:"source"` // имя файла в logs или пусто для ручного ввода
	Manual    string `json:"manual"`
	Mode      string `json:"mode"`
	Count     int    `json:"count"`
	Interval  string `json:"interval"`
	Loop      bool   `json:"loop"`
}

// LoadState читает сохранённое состояние; при любой ошибке возвращает пустое.
func (p Paths) LoadState() State {
	var s State
	if raw, err := os.ReadFile(p.State); err == nil {
		json.Unmarshal(raw, &s)
	}
	return s
}

func (p Paths) SaveState(s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.State, raw, 0o644); err != nil {
		return fmt.Errorf("запись %s: %w", p.State, err)
	}
	return nil
}
