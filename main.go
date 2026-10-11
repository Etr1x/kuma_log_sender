// kuma_log_sender — утилита для отправки тестовых событий на коллекторы SIEM KUMA.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/Etr1x/kuma_log_sender/internal/app"
	"github.com/Etr1x/kuma_log_sender/internal/cli"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
	"github.com/Etr1x/kuma_log_sender/internal/sender"
	"github.com/Etr1x/kuma_log_sender/internal/tui"
)

// version — номер версии; отсюда же его берёт сборщик архивов (tools/bundle).
var version = "1.0.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	app.Version = version
	sender.UserAgent = "kuma_log_sender/" + version

	fs := flag.NewFlagSet("kuma_log_sender", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Print(cli.Usage)
			return 0
		}
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n\n%s", err, cli.Usage)
		return 2
	}
	rest := fs.Args()
	cmd := ""
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}

	switch {
	case *showVersion || cmd == "version":
		fmt.Println("kuma_log_sender", version)
		return 0
	case cmd == "help":
		fmt.Print(cli.Usage)
		return 0
	case cmd == "listen":
		return cli.Listen(rest)
	case cmd != "" && cmd != "send" && cmd != "list":
		fmt.Fprintf(os.Stderr, "Ошибка: неизвестная команда %q\n\n%s", cmd, cli.Usage)
		return 2
	}

	a, err := app.Open(app.ResolvePaths(*dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		if cmd == "" {
			pause()
		}
		return 1
	}
	defer a.Close()

	switch cmd {
	case "send":
		return cli.Send(a, rest)
	case "list":
		return cli.List(a)
	}

	a.Journal.Add(journal.Info, 0, "запуск kuma_log_sender %s, рабочая папка: %s", version, a.Base)
	_, err = tea.NewProgram(tui.New(a)).Run()

	// Активные задачи останавливаем и даём им записать итог в журнал.
	a.Jobs.StopAll()
	done := make(chan struct{})
	go func() { a.Jobs.WaitAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка интерфейса: %v\n", err)
		pause()
		return 1
	}
	return 0
}

// pause ждёт Enter перед выходом из интерактивного режима с ошибкой: при запуске
// двойным щелчком окно консоли иначе закроется раньше, чем сообщение успеют прочесть.
func pause() {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return
	}
	fmt.Fprint(os.Stderr, "\nНажмите Enter для выхода…")
	fmt.Fscanln(os.Stdin)
}
