package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
