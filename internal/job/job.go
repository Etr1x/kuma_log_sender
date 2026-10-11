// Package job выполняет отправку в фоне: несколько задач могут идти одновременно,
// каждая — со своим коллектором, набором событий, числом повторов и интервалом.
package job

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Etr1x/kuma_log_sender/internal/config"
	"github.com/Etr1x/kuma_log_sender/internal/journal"
	"github.com/Etr1x/kuma_log_sender/internal/sender"
)

// Spec — что и куда отправлять.
type Spec struct {
	Collector   config.Collector
	DefaultHost string
	Timeout     time.Duration
	// Source — название источника для журнала: «файл x.log» или «ручной ввод».
	Source string
	// Load читает события в момент старта задачи, чтобы подхватить свежее
	// содержимое файла и не блокировать интерфейс.
	Load     func() (events [][]byte, notes []string, err error)
	Count    int           // сколько раз проиграть весь набор событий; игнорируется при Loop
	Loop     bool          // повторять бесконечно, пока задачу не остановят
	Interval time.Duration // пауза между отправками
}

type State int

const (
	Running State = iota
	Done
	Stopped
	Failed // не удалось начать или не ушло ни одного события
)

func (s State) Label() string {
	switch s {
	case Done:
		return "готово"
	case Stopped:
		return "остановлена"
	case Failed:
		return "ошибка"
	}
	return "идёт"
}

// Info — снимок состояния задачи для отображения.
type Info struct {
	ID        int
	Collector string
	Target    string
	Source    string
	State     State
	Loop      bool
	Interval  time.Duration
	Total     int64 // сколько отправок запланировано; 0 — неизвестно или бесконечно
	Sent      int64
	Errors    int64
	Warned    bool // транспорт сообщил о проблеме, не привязанной к отправке (например, ICMP-отказ по UDP)
	Started   time.Time
	Finished  time.Time
	NextAt    time.Time // когда будет следующая отправка (нулевое — прямо сейчас)
}

type job struct {
	id     int
	spec   Spec
	target string
	cancel context.CancelFunc
	done   chan struct{}

	started  time.Time
	total    atomic.Int64
	sent     atomic.Int64
	errors   atomic.Int64
	warned   atomic.Bool
	nextAt   atomic.Int64
	state    atomic.Int32
	finished atomic.Int64
}

func (j *job) info() Info {
	in := Info{
		ID:        j.id,
		Collector: j.spec.Collector.Name,
		Target:    j.target,
		Source:    j.spec.Source,
		State:     State(j.state.Load()),
		Loop:      j.spec.Loop,
		Interval:  j.spec.Interval,
		Total:     j.total.Load(),
		Sent:      j.sent.Load(),
		Errors:    j.errors.Load(),
		Warned:    j.warned.Load(),
		Started:   j.started,
	}
	if t := j.finished.Load(); t != 0 {
		in.Finished = time.Unix(0, t)
	}
	if t := j.nextAt.Load(); t != 0 && in.State == Running {
		in.NextAt = time.Unix(0, t)
	}
	return in
}

// keepFinished — сколько завершённых задач остаётся в списке.
const keepFinished = 30

// Manager запускает задачи и хранит их список. Потокобезопасен.
type Manager struct {
	journal *journal.Journal
	mu      sync.Mutex
	jobs    []*job
	nextID  int
	wg      sync.WaitGroup
}

func NewManager(j *journal.Journal) *Manager {
	return &Manager{journal: j}
}

// Start запускает задачу в фоне и возвращает её номер.
func (m *Manager) Start(spec Spec) int {
	if spec.Count < 1 {
		spec.Count = 1
	}
	if spec.Interval < 0 {
		spec.Interval = 0
	}
	if spec.Timeout <= 0 {
		spec.Timeout = config.DefaultTimeoutSec * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.nextID++
	j := &job{
		id:      m.nextID,
		spec:    spec,
		target:  spec.Collector.Target(spec.DefaultHost),
		cancel:  cancel,
		done:    make(chan struct{}),
		started: time.Now(),
	}
	m.jobs = append(m.jobs, j)
	m.prune()
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(j.done)
		defer cancel()
		state := j.run(ctx, m.journal)
		j.finished.Store(time.Now().UnixNano())
		j.state.Store(int32(state))
	}()
	return j.id
}

// prune оставляет в списке все активные задачи и последние keepFinished завершённых.
func (m *Manager) prune() {
	finished := 0
	for _, j := range m.jobs {
		if State(j.state.Load()) != Running {
			finished++
		}
	}
	if finished <= keepFinished {
		return
	}
	drop := finished - keepFinished
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if drop > 0 && State(j.state.Load()) != Running {
			drop--
			continue
		}
		kept = append(kept, j)
	}
	m.jobs = kept
}

func (m *Manager) find(id int) *job {
	for _, j := range m.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// Stop останавливает задачу. Возвращает false, если она не найдена или уже завершена.
func (m *Manager) Stop(id int) bool {
	m.mu.Lock()
	j := m.find(id)
	m.mu.Unlock()
	if j == nil || State(j.state.Load()) != Running {
		return false
	}
	j.cancel()
	return true
}

// StopAll останавливает все активные задачи и возвращает их число.
func (m *Manager) StopAll() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if State(j.state.Load()) == Running {
			j.cancel()
			n++
		}
	}
	return n
}

// ClearFinished убирает из списка завершённые задачи.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if State(j.state.Load()) == Running {
			kept = append(kept, j)
		}
	}
	m.jobs = kept
}

// Active возвращает число выполняющихся задач.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if State(j.state.Load()) == Running {
			n++
		}
	}
	return n
}

// Snapshot возвращает задачи: сначала активные, затем завершённые; внутри групп — новые сверху.
func (m *Manager) Snapshot() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	var running, rest []Info
	for i := len(m.jobs) - 1; i >= 0; i-- {
		in := m.jobs[i].info()
		if in.State == Running {
			running = append(running, in)
		} else {
			rest = append(rest, in)
		}
	}
	return append(running, rest...)
}

// Wait ждёт завершения задачи и возвращает её итог.
func (m *Manager) Wait(id int) (Info, bool) {
	m.mu.Lock()
	j := m.find(id)
	m.mu.Unlock()
	if j == nil {
		return Info{}, false
	}
	<-j.done
	return j.info(), true
}

// WaitAll ждёт завершения всех задач (после StopAll — быстро).
func (m *Manager) WaitAll() {
	m.wg.Wait()
}

func (j *job) run(ctx context.Context, jr *journal.Journal) State {
	spec := j.spec
	c := spec.Collector

	evs, notes, err := spec.Load()
	if err != nil {
		jr.Add(journal.Error, j.id, "%s → %s: не удалось прочитать источник: %v", spec.Source, c.Name, err)
		return Failed
	}
	if len(evs) == 0 {
		jr.Add(journal.Error, j.id, "%s → %s: в источнике нет событий (пусто)", spec.Source, c.Name)
		return Failed
	}
	repeats := "∞"
	if !spec.Loop {
		j.total.Store(int64(len(evs)) * int64(spec.Count))
		repeats = fmt.Sprint(spec.Count)
	}
	var size int
	for _, ev := range evs {
		size += len(ev)
	}
	jr.Add(journal.Info, j.id, "старт: %s → «%s» %s | событий: %d (%d Б), повторов: %s, интервал: %s, разделитель: %s",
		spec.Source, c.Name, j.target, len(evs), size, repeats, FormatInterval(spec.Interval), c.Delimiter.Label())
	for _, n := range notes {
		jr.Add(journal.Info, j.id, "%s: %s", spec.Source, n)
	}

	snd, err := sender.New(c, spec.DefaultHost, sender.Options{
		Timeout: spec.Timeout,
		Notify: func(warn bool, msg string) {
			level := journal.Info
			if warn {
				level = journal.Warn
				j.warned.Store(true)
			}
			jr.Add(level, j.id, "%s", msg)
		},
	})
	if err != nil {
		jr.Add(journal.Error, j.id, "%s: %v", c.Name, err)
		return Failed
	}
	closed := false
	closeSender := func() {
		if !closed {
			closed = true
			snd.Close()
		}
	}
	defer closeSender()

	delim := c.Delimiter.Bytes()
	start := time.Now()
	stopped := false
	n := int64(0)

loop:
	for pass := 1; spec.Loop || pass <= spec.Count; pass++ {
		for _, ev := range evs {
			if n > 0 && spec.Interval > 0 {
				j.nextAt.Store(time.Now().Add(spec.Interval).UnixNano())
				t := time.NewTimer(spec.Interval)
				select {
				case <-ctx.Done():
					t.Stop()
					stopped = true
					break loop
				case <-t.C:
				}
				j.nextAt.Store(0)
			} else if ctx.Err() != nil {
				stopped = true
				break loop
			}
			n++
			label := fmt.Sprintf("[%d/%s]", n, repeatsTotal(j.total.Load()))
			sctx, cancel := context.WithTimeout(ctx, spec.Timeout)
			res, err := snd.Send(sctx, sender.Frame(ev, delim))
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					stopped = true
					break loop
				}
				j.errors.Add(1)
				jr.Add(journal.Error, j.id, "%s НЕ отправлено → %s: %s", label, j.target, sender.Describe(err, c.Protocol))
				continue
			}
			j.sent.Add(1)
			if res.Note != "" {
				jr.Add(journal.Info, j.id, "%s", res.Note)
			}
			status := ""
			if res.Status != "" {
				status = ", " + res.Status
			}
			jr.Add(journal.OK, j.id, "%s отправлено → %s: %d Б за %s%s | %s",
				label, j.target, res.Bytes, sender.FormatTook(res.Took), status, Preview(ev, 160))
		}
	}

	// Закрываем отправителя до подведения итога: при закрытии он дожидается
	// запоздавших сообщений транспорта (ICMP-отказ на UDP-датаграмму).
	closeSender()
	sent, failed := j.sent.Load(), j.errors.Load()
	summary := fmt.Sprintf("отправлено %d, ошибок %d, за %s", sent, failed, FormatElapsed(time.Since(start)))
	warned := j.warned.Load()
	if c.Protocol == config.UDP && sent > 0 && !warned {
		summary += " (UDP доставку не подтверждает — проверьте события в KUMA)"
	}
	switch {
	case stopped:
		jr.Add(journal.Info, j.id, "остановлено: %s", summary)
		return Stopped
	case sent == 0:
		jr.Add(journal.Error, j.id, "завершено с ошибкой: %s", summary)
		return Failed
	case failed > 0:
		jr.Add(journal.Warn, j.id, "завершено с ошибками: %s", summary)
	case warned:
		jr.Add(journal.Warn, j.id, "завершено с предупреждениями — события, скорее всего, не дошли: %s", summary)
	default:
		jr.Add(journal.Info, j.id, "завершено: %s", summary)
	}
	return Done
}

func repeatsTotal(total int64) string {
	if total <= 0 {
		return "∞"
	}
	return fmt.Sprint(total)
}

// Preview возвращает начало события одной строкой: управляющие символы
// заменяются видимыми обозначениями, длинный текст обрезается.
func Preview(ev []byte, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range string(ev) {
		if n >= max {
			b.WriteRune('…')
			break
		}
		switch {
		case r == '\n':
			b.WriteRune('↵')
		case r == '\t':
			b.WriteRune('→')
		case r == '\r':
			continue
		case r == 0:
			b.WriteString(`\0`)
			n++
		case unicode.IsControl(r) || r == unicode.ReplacementChar:
			b.WriteRune('·')
		default:
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// FormatElapsed выводит длительность задачи: 12 мс, 3.4 с, 2m15s.
func FormatElapsed(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1 мс"
	case d < time.Second:
		return fmt.Sprintf("%d мс", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f с", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// FormatInterval выводит интервал коротко: 1s, 500ms, 1m30s, «без паузы».
func FormatInterval(d time.Duration) string {
	if d <= 0 {
		return "без паузы"
	}
	s := d.String()
	// time.Duration печатает 1m0s и 1h0m0s — убираем нулевые хвосты.
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// ParseInterval разбирает интервал. Число без единиц — секунды («1», «0.5»),
// допустимы и единицы Go: 500ms, 2s, 1m. Запятая принимается как десятичная точка.
func ParseInterval(s string) (time.Duration, error) {
	s = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), ",", ".")
	// Русские обозначения единиц — на случай ввода в русской раскладке.
	for _, r := range [][2]string{{"мс", "ms"}, {"сек", "s"}, {"мин", "m"}, {"с", "s"}, {"м", "m"}, {"ч", "h"}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, fmt.Errorf("интервал не задан")
	}
	if last := s[len(s)-1]; last >= '0' && last <= '9' || last == '.' {
		s += "s"
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("не удалось разобрать интервал (примеры: 1, 0.5, 500ms, 2m)")
	}
	if d < 0 {
		return 0, fmt.Errorf("интервал не может быть отрицательным")
	}
	if d > 24*time.Hour {
		return 0, fmt.Errorf("интервал не может быть больше суток")
	}
	return d, nil
}
