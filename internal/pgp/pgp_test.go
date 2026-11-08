package pgp

import (
	"bytes"
	"os"
	"testing"
)

// Real vendor keys, as fetched for the firefox and google-chrome recipes.
func TestRead(t *testing.T) {
	cases := map[string]string{
		"testdata/mozilla.asc": "35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3",
		"testdata/google.asc":  "EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796",
	}
	for f, want := range cases {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		k, err := Read(b)
		if err != nil || k.Fingerprint != want {
			t.Errorf("%s: %v %v", f, k, err)
			continue
		}
		// The binary form reads the same.
		if k2, err := Read(k.Binary); err != nil || k2.Fingerprint != want || bytes.Contains(k.Binary, []byte("BEGIN")) {
			t.Errorf("%s binary: %v %v", f, k2, err)
		}
	}
}

func TestRefuses(t *testing.T) {
	moz, _ := os.ReadFile("testdata/mozilla.asc")
	goog, _ := os.ReadFile("testdata/google.asc")
	a, _ := Read(moz)
	b, _ := Read(goog)
	if _, err := Read(append(append([]byte{}, a.Binary...), b.Binary...)); err == nil {
		t.Error("two keys accepted")
	}
	if _, err := Read(a.Binary[:len(a.Binary)/2]); err == nil {
		t.Error("truncated key accepted")
	}
	if _, err := Read([]byte("not a key")); err == nil {
		t.Error("text accepted")
	}
}
