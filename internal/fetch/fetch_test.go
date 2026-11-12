package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGet(t *testing.T) {
	body := "pfetch"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	Client = srv.Client()
	h := sha256.Sum256([]byte(body))
	sum := hex.EncodeToString(h[:])
	dir := t.TempDir()

	p, err := Get(srv.URL+"/f", sum, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Errorf("content %q", b)
	}
	// Cached: served without the network.
	srv.Close()
	if _, err := Get(srv.URL+"/f", sum, dir); err != nil {
		t.Errorf("cache: %v", err)
	}

	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("other")) }))
	defer srv2.Close()
	Client = srv2.Client()
	bad := strings.Repeat("0", 64)
	if _, err := Get(srv2.URL+"/f", bad, dir); err == nil || !strings.Contains(err.Error(), "recipe has") {
		t.Errorf("mismatch: %v", err)
	}
	if _, err := os.Stat(dir + "/" + bad); err == nil {
		t.Error("mismatched file kept")
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("leftovers: %v", left)
	}
	if _, err := Get("http://example.org/f", sum, dir); err == nil {
		t.Error("http accepted")
	}
}

func TestStall(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-stop
	}))
	defer srv.Close()
	defer close(stop)
	Client = srv.Client()
	defer func(d time.Duration) { Stall = d }(Stall)
	Stall = 100 * time.Millisecond
	dir := t.TempDir()
	if _, err := Get(srv.URL+"/f", strings.Repeat("0", 64), dir); err == nil || !strings.Contains(err.Error(), "no data") {
		t.Errorf("got %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("leftovers: %v", left)
	}
}

func TestRedirect(t *testing.T) {
	plain, _ := http.NewRequest("GET", "http://example.org/f", nil)
	tls, _ := http.NewRequest("GET", "https://example.org/f", nil)
	if httpsOnly(plain, nil) == nil || httpsOnly(tls, nil) != nil || httpsOnly(tls, make([]*http.Request, 10)) == nil {
		t.Error("redirect policy")
	}
}
