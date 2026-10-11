// Package cli — режим командной строки: отправка без интерфейса (для скриптов),
// список коллекторов и тестовый приёмник.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/app"
	"github.com/Etr1x/kuma_log_sender/internal/events"
	"github.com/Etr1x/kuma_log_sender/internal/job"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
)

const Usage = `KUMA Log Sender — отправка тестовых событий на коллекторы KUMA.

Использование:
  kuma_log_sender [--dir ПАПКА]                 интерактивный режим
  kuma_log_sender [--dir ПАПКА] send ...        отправка без интерфейса
  kuma_log_sender [--dir ПАПКА] list            список коллекторов
  kuma_log_sender listen ...                    тестовый приёмник (вместо коллектора)
  kuma_log_sender version

  --dir ПАПКА   рабочая папка с collectors.json, logs/ и sender.log
                (по умолчанию — папка с исполняемым файлом; можно задать
                переменной окружения KUMA_LOG_SENDER_HOME)

Справка по подкоманде: kuma_log_sender send -h, kuma_log_sender listen -h
`

func newFlags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	return fs
}

// Send выполняет подкоманду send. Возвращает код завершения процесса.
func Send(a *app.App, args []string) int {
	fs := newFlags("send", `Отправка событий без интерфейса.

Примеры:
  kuma_log_sender send -c "Linux Auditd" -f auditd.log
  kuma_log_sender send -c auditd -t "тестовое событие" -n 5 -i 0.5
  type events.txt | kuma_log_sender send -c "CEF TCP" -f -

Параметры:
`)
	var collector, file, text, mode, interval string
	var count int
	var loop bool
	for _, n := range []string{"c", "collector"} {
		fs.StringVar(&collector, n, "", "имя коллектора из collectors.json (достаточно однозначной части имени)")
	}
	for _, n := range []string{"f", "file"} {
		fs.StringVar(&file, n, "", "файл с событиями: путь или имя файла из папки logs; «-» — читать stdin")
	}
	for _, n := range []string{"t", "text"} {
		fs.StringVar(&text, n, "", "текст события")
	}
	for _, n := range []string{"n", "count"} {
		fs.IntVar(&count, n, 1, "сколько раз отправить весь набор событий")
	}
	for _, n := range []string{"i", "interval"} {
		fs.StringVar(&interval, n, "1", "интервал между отправками: 1, 0.5, 500ms, 2m; 0 — без паузы")
	}
	for _, n := range []string{"m", "mode"} {
		fs.StringVar(&mode, n, "lines", "режим: lines — построчно, whole — целиком одним событием, delim — резать по разделителю коллектора")
	}
	fs.BoolVar(&loop, "loop", false, "отправлять бесконечно, до Ctrl+C")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "Ошибка: "+format+"\n", args...)
		return 2
	}
	if collector == "" {
		return fail("не указан коллектор (-c). Список: kuma_log_sender list")
	}
	if (file == "") == (text == "") {
		return fail("укажите что-то одно: файл (-f) или текст (-t)")
	}
	c, err := a.Store.Match(collector)
	if err != nil {
		return fail("%v", err)
	}
	evMode, err := events.ParseMode(mode)
	if err != nil {
		return fail("%v", err)
	}
	every, err := job.ParseInterval(interval)
	if err != nil {
		return fail("интервал: %v", err)
	}
	if count < 1 {
		return fail("число повторов должно быть не меньше 1")
	}

	delim := c.Delimiter.Bytes()
	spec := job.Spec{
		Collector:   c,
		DefaultHost: a.Store.DefaultHost,
		Timeout:     a.Store.Timeout(),
		Count:       count,
		Loop:        loop,
		Interval:    every,
	}
	switch {
	case text != "":
		spec.Source = "текст из командной строки"
		spec.Load = func() ([][]byte, []string, error) { return events.FromText(text, evMode, delim), nil, nil }
	case file == "-":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fail("чтение stdin: %v", err)
		}
		spec.Source = "stdin"
		spec.Load = func() ([][]byte, []string, error) {
			data, notes := events.Decode(raw)
			return events.Split(data, evMode, delim), notes, nil
		}
	default:
		path := file
		if _, err := os.Stat(path); err != nil {
			// Не нашли как путь — ищем в папке logs.
			if inLogs := filepath.Join(a.LogsDir, file); exists(inLogs) {
				path = inLogs
			} else {
				return fail("файл %q не найден ни по этому пути, ни в папке %s", file, a.LogsDir)
			}
		}
		spec.Source = "файл " + filepath.Base(path)
		spec.Load = func() ([][]byte, []string, error) { return events.FromFile(path, evMode, delim) }
	}

	a.Journal.Echo = printEntry
	id := a.Jobs.Start(spec)

	// Ctrl+C останавливает задачу, а не обрывает процесс: итог попадёт в журнал.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		a.Jobs.StopAll()
	}()

	info, _ := a.Jobs.Wait(id)
	if info.State == job.Failed || info.Errors > 0 || info.Warned {
		return 1
	}
	return 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printEntry(e journal.Entry) {
	out := os.Stdout
	if e.Level == journal.Error {
		out = os.Stderr
	}
	fmt.Fprintf(out, "%s %s %s\n", e.Time.Format("15:04:05.000"), e.Level.Tag(), e.Text)
}

// List печатает коллекторы из конфига.
func List(a *app.App) int {
	if len(a.Store.Collectors) == 0 {
		fmt.Printf("В %s нет коллекторов. Добавьте их в интерактивном режиме (клавиша a).\n", a.Config)
		return 0
	}
	fmt.Printf("Конфиг: %s\nХост по умолчанию: %s\n\n", a.Config, orDash(a.Store.DefaultHost))
	width := 4
	for _, c := range a.Store.Collectors {
		width = max(width, len([]rune(c.Name)))
	}
	fmt.Printf("%-*s  %-44s  %s\n", width, "ИМЯ", "АДРЕС", "РАЗДЕЛИТЕЛЬ")
	for _, c := range a.Store.Collectors {
		fmt.Printf("%-*s  %-44s  %s\n", width, c.Name, c.Target(a.Store.DefaultHost), c.Delimiter.Label())
	}
	return 0
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "не задан"
	}
	return s
}

// waitInterrupt блокируется до Ctrl+C.
func waitInterrupt() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
}

func stamp() string { return time.Now().Format("15:04:05.000") }
