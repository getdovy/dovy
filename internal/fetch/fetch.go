// Package fetch downloads recipe sources and checks their sha256
// (principles 2.4).
package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is used for downloads; tests replace it.
var Client = &http.Client{Timeout: 30 * time.Minute}

// Get returns the path of url's file in dir, named by its sha256.
// A cached file is used when its hash matches; a mismatch removes the file.
func Get(url, sum, dir string) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("%s: https only", url)
	}
	path := filepath.Join(dir, sum)
	if hashOf(path) == sum {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	resp, err := Client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp(dir, ".part-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("%s: sha256 %s, recipe has %s", url, got, sum)
	}
	return path, os.Rename(f.Name(), path)
}

func hashOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Size asks the server for url's size; -1 when it does not say.
func Size(url string) int64 {
	resp, err := Client.Head(url)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	return resp.ContentLength
}

// Bytes downloads a small file over HTTPS, at most limit bytes; for keys,
// checked by fingerprint rather than sha256 (2.4).
func Bytes(url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%s: https only", url)
	}
	resp, err := Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, err
}
