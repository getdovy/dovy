// Package fetch downloads recipe sources and checks their sha256.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var Stall = time.Minute

// Client has no overall timeout: a slow large file is fine, a stalled one is not (see Stall).
var Client = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		ForceAttemptHTTP2:     true,
	},
	CheckRedirect: httpsOnly,
}

func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to %s: https only", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return nil
}

// Progress: total is -1 when the server does not say.
var Progress func(url string, got, total int64)

type counter struct {
	url        string
	got, total int64
}

func (c *counter) Write(p []byte) (int, error) {
	c.got += int64(len(p))
	if Progress != nil {
		Progress(c.url, c.got, c.total)
	}
	return len(p), nil
}

func get(url string) (*http.Response, io.Reader, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	resp, err := Client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	t := time.AfterFunc(Stall, cancel)
	body := readerFunc(func(p []byte) (int, error) {
		n, err := resp.Body.Read(p)
		if n > 0 {
			t.Reset(Stall)
		}
		if err != nil && ctx.Err() != nil {
			err = fmt.Errorf("no data for %s", Stall)
		}
		return n, err
	})
	return resp, body, func() { t.Stop(); cancel(); resp.Body.Close() }, nil
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// Get names files by sha256; a cached file with a wrong hash is removed.
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
	resp, body, done, err := get(url)
	if err != nil {
		return "", err
	}
	defer done()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp(dir, ".part-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h, &counter{url: url, total: resp.ContentLength}), body)
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

// Size is -1 when the server does not say in time.
func Size(url string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return -1
	}
	resp, err := Client.Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	return resp.ContentLength
}

// Bytes is for keys, which are checked by fingerprint rather than sha256.
func Bytes(url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%s: https only", url)
	}
	resp, body, done, err := get(url)
	if err != nil {
		return nil, err
	}
	defer done()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, err
}
