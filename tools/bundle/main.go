// Команда bundle собирает готовые к переносу архивы утилиты:
//
//	go run ./tools/bundle [-version 1.2.3]
//
// В папке dist появляются zip для Windows и tar.gz для Linux. Номер версии берётся
// из параметра (так делает сборка релиза — по имени тега), иначе из git describe. В каждом — один
// статически собранный исполняемый файл, пример конфига, примеры логов и документация.
// Архивы пишутся средствами Go, поэтому сборка одинаково работает на Windows и Linux,
// а у Linux-бинаря в архиве сохраняется бит исполнения.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const name = "kuma_log_sender"

// extras — что кладётся в архив рядом с исполняемым файлом.
var extras = []string{"collectors.example.json", "README.md", "LICENSE", "docs", "logs/examples"}

type entry struct {
	path string // путь внутри архива
	data []byte
	mode fs.FileMode
}

func main() {
	targets := flag.String("targets", "windows/amd64,linux/amd64", "список GOOS/GOARCH через запятую")
	out := flag.String("out", "dist", "папка для архивов")
	version := flag.String("version", "", "номер версии; по умолчанию — из git describe")
	flag.Parse()

	if *version == "" {
		*version = gitVersion()
	}
	if _, err := os.Stat("main.go"); err != nil {
		check(fmt.Errorf("запускайте из корня репозитория: %w", err))
	}
	check(os.MkdirAll(*out, 0o755))

	var common []entry
	for _, e := range extras {
		check(filepath.WalkDir(e, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			common = append(common, entry{path: filepath.ToSlash(path), data: data, mode: 0o644})
			return err
		}))
	}

	for _, target := range strings.Split(*targets, ",") {
		goos, goarch, ok := strings.Cut(strings.TrimSpace(target), "/")
		if !ok {
			check(fmt.Errorf("цель %q должна иметь вид GOOS/GOARCH", target))
		}
		bin := name
		if goos == "windows" {
			bin += ".exe"
		}
		tmp := filepath.Join(*out, ".build-"+goos+"-"+goarch, bin)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+*version, "-o", tmp, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		check(cmd.Run())
		data, err := os.ReadFile(tmp)
		check(err)
		check(os.RemoveAll(filepath.Dir(tmp)))

		files := append([]entry{{path: bin, data: data, mode: 0o755}}, common...)
		base := fmt.Sprintf("%s_%s_%s_%s", name, *version, goos, goarch)
		var archive string
		if goos == "windows" {
			archive = filepath.Join(*out, base+".zip")
			check(writeZip(archive, files))
		} else {
			archive = filepath.Join(*out, base+".tar.gz")
			check(writeTarGz(archive, files))
		}
		st, _ := os.Stat(archive)
		fmt.Printf("%s  (%.1f МБ)\n", archive, float64(st.Size())/(1<<20))
	}
}

// gitVersion описывает текущий коммит относительно последнего тега: 1.2.3 на самом
// теге, 1.2.3-4-gabc1234 после него. Без git или тегов возвращает dev.
func gitVersion() string {
	out, err := exec.Command("git", "describe", "--tags", "--always", "--dirty").Output()
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if err != nil || v == "" {
		return "dev"
	}
	return v
}

func writeZip(path string, files []entry) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: name + "/" + f.path, Method: zip.Deflate, Modified: time.Now()}
		h.SetMode(f.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(f.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeTarGz(path string, files []entry) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		h := &tar.Header{Name: name + "/" + f.path, Mode: int64(f.mode), Size: int64(len(f.data)), ModTime: time.Now(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, bytes.NewReader(f.data)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
