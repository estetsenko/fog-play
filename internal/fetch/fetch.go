// Package fetch downloads release builds of the third-party programs fogplay
// runs (Sunshine on hosts, Moonlight on players) from their GitHub releases,
// so users don't install them by hand.
package fetch

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitHubAPI is the GitHub REST base URL; tests point it at a fake.
var GitHubAPI = "https://api.github.com"

var httpClient = &http.Client{Timeout: 10 * time.Minute}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Latest returns the newest stable release of owner/repo.
func Latest(ctx context.Context, repo string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", GitHubAPI+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github %s latest release: %s", repo, resp.Status)
	}
	var r Release
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Pick returns the first asset whose lower-cased name contains every one of
// want and none of avoid.
func (r Release) Pick(want []string, avoid ...string) (Asset, bool) {
next:
	for _, a := range r.Assets {
		n := strings.ToLower(a.Name)
		for _, w := range want {
			if !strings.Contains(n, strings.ToLower(w)) {
				continue next
			}
		}
		for _, x := range avoid {
			if strings.Contains(n, strings.ToLower(x)) {
				continue next
			}
		}
		return a, true
	}
	return Asset{}, false
}

// Download saves url to path, writing to a temp file first so a failed
// download never leaves a half file behind.
func Download(ctx context.Context, url, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Unzip extracts archive into dir, refusing entries that would escape it.
func Unzip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		dst := filepath.Join(root, f.Name)
		if dst != root && !strings.HasPrefix(dst, root+string(os.PathSeparator)) {
			return fmt.Errorf("unzip: %q escapes the target folder", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, dst); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// FindFile returns the first file under dir whose name matches one of names
// case-insensitively.
func FindFile(dir string, names ...string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		for _, n := range names {
			if strings.EqualFold(d.Name(), n) {
				found = p
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err == nil && found == "" {
		err = errors.New("not found")
	}
	return found, err
}

// CacheDir is where downloaded programs live, e.g. ~/.cache/fogplay/<name>.
func CacheDir(name string) (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "fogplay", name), nil
}
