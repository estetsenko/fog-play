package fetch

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestPick(t *testing.T) {
	r := Release{Assets: []Asset{
		{Name: "ffmpeg-master-latest-win64-gpl-shared.zip"},
		{Name: "ffmpeg-master-latest-win64-gpl.zip"},
		{Name: "ffmpeg-master-latest-linux64-gpl.tar.xz"},
	}}
	a, ok := r.Pick([]string{"win64-gpl", ".zip"}, "shared")
	if !ok || a.Name != "ffmpeg-master-latest-win64-gpl.zip" {
		t.Errorf("got %v %v", a, ok)
	}
	if _, ok := r.Pick([]string{".dmg"}); ok {
		t.Error("picked a missing asset")
	}
}

func writeZip(t *testing.T, files map[string]string) string {
	path := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(path)
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return path
}

func TestUnzipAndFind(t *testing.T) {
	dir := t.TempDir()
	if err := Unzip(writeZip(t, map[string]string{"ffmpeg/bin/ffmpeg.exe": "x", "ffmpeg/doc/a.txt": "y"}), dir); err != nil {
		t.Fatal(err)
	}
	p, err := FindFile(dir, "FFMPEG.EXE")
	if err != nil || filepath.Base(p) != "ffmpeg.exe" {
		t.Errorf("find: %q %v", p, err)
	}
}

func TestUnzipRejectsEscape(t *testing.T) {
	if err := Unzip(writeZip(t, map[string]string{"../evil.txt": "x"}), t.TempDir()); err == nil {
		t.Error("expected an error for a path outside the folder")
	}
}
