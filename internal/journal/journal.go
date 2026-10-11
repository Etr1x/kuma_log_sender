// Package journal ведёт журнал отправки: хранит последние записи в памяти
// для показа на экране и дублирует всё в файл.
package journal

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type Level int

const (
	Info Level = iota
	OK
	Warn
	Error
)

// Tag — трёхбуквенная метка уровня; одинаковая на экране и в файле.
func (l Level) Tag() string {
	switch l {
	case OK:
		return "OK "
	case Warn:
		return "WRN"
	case Error:
		return "ERR"
	}
	return "INF"
}

// Entry — одна запись журнала. Job == 0 означает запись вне задач.
type Entry struct {
	Seq   uint64
	Time  time.Time
	Level Level
	Job   int
	Text  string
}

// Line форматирует запись для файла.
func (e Entry) Line() string {
	job := "     "
	if e.Job > 0 {
		job = fmt.Sprintf("#%-4d", e.Job)
	}
	return fmt.Sprintf("%s %s %s %s", e.Time.Format("2006-01-02 15:04:05.000"), e.Level.Tag(), job, e.Text)
}

const (
	keep        = 5000     // сколько записей держать в памяти
	rotateBytes = 10 << 20 // при каком размере файл журнала переименовывается в .1
)

// Journal потокобезопасен.
type Journal struct {
	mu      sync.Mutex
	entries []Entry
	seq     uint64
	file    *os.File
	fileErr error
	// Echo, если задан, получает каждую запись сразу после добавления (CLI-режим).
	Echo func(Entry)
}

// New открывает журнал. path может быть пустым — тогда запись в файл не ведётся.
// Ошибка открытия файла не мешает работе: она возвращается для показа, а журнал
// остаётся рабочим в памяти.
func New(path string) (*Journal, error) {
	j := &Journal{}
	if path == "" {
		return j, nil
	}
	if st, err := os.Stat(path); err == nil && st.Size() > rotateBytes {
		os.Remove(path + ".1")
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return j, fmt.Errorf("журнал в файл не ведётся: %w", err)
	}
	j.file = f
	return j, nil
}

func (j *Journal) Add(level Level, job int, format string, args ...any) {
	text := format
	if len(args) > 0 {
		text = fmt.Sprintf(format, args...)
	}
	j.mu.Lock()
	j.seq++
	e := Entry{Seq: j.seq, Time: time.Now(), Level: level, Job: job, Text: text}
	j.entries = append(j.entries, e)
	if len(j.entries) > keep+keep/4 {
		j.entries = append(j.entries[:0:0], j.entries[len(j.entries)-keep:]...)
	}
	if j.file != nil && j.fileErr == nil {
		_, j.fileErr = j.file.WriteString(e.Line() + "\n")
	}
	echo := j.Echo
	j.mu.Unlock()
	if echo != nil {
		echo(e)
	}
}

// Seq возвращает номер последней записи; по нему интерфейс понимает, появилось ли новое.
func (j *Journal) Seq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

// Snapshot возвращает копию записей, хранящихся в памяти.
func (j *Journal) Snapshot() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Entry(nil), j.entries...)
}

// Clear очищает журнал на экране; файл не трогается.
func (j *Journal) Clear() {
	j.mu.Lock()
	j.entries = nil
	j.seq++
	j.mu.Unlock()
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
