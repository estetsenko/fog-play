package fetch

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestPick(t *testing.T) {
	r := Release{Assets: []Asset{
		{Name: "MoonlightDebuggingSymbols-x64-6.1.0.zip"},
		{Name: "MoonlightPortable-x64-6.1.0.zip"},
		{Name: "MoonlightSetup-6.1.0.exe"},
	}}
	a, ok := r.Pick([]string{"portable-x64", ".zip"}, "symbols")
	if !ok || a.Name != "MoonlightPortable-x64-6.1.0.zip" {
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
	if err := Unzip(writeZip(t, map[string]string{"Sunshine/sunshine.exe": "x", "Sunshine/assets/a.txt": "y"}), dir); err != nil {
		t.Fatal(err)
	}
	p, err := FindFile(dir, "SUNSHINE.EXE")
	if err != nil || filepath.Base(p) != "sunshine.exe" {
		t.Errorf("find: %q %v", p, err)
	}
}

func TestUnzipRejectsEscape(t *testing.T) {
	if err := Unzip(writeZip(t, map[string]string{"../evil.txt": "x"}), t.TempDir()); err == nil {
		t.Error("expected an error for a path outside the folder")
	}
}
