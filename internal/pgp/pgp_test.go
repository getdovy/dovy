package pgp

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Real vendor keys, as fetched for the firefox, google-chrome and brave
// repositories. Brave's file holds three primary keys.
func TestRead(t *testing.T) {
	cases := map[string]string{
		"testdata/mozilla.asc": "35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3",
		"testdata/google.asc":  "EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796",
		"testdata/brave.gpg":   "DBF1A116C220B8C7164F98230686B78420038257 47D32A74E9A9E013A4B4926C68D513D36A73CD96 B2A3DCA350E67256740DF904DE4EC67BE4B0DCA0",
	}
	for f, want := range cases {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		k, err := Read(b)
		if err != nil || strings.Join(k.Fingerprints, " ") != want {
			t.Errorf("%s: %v %v", f, k, err)
			continue
		}
		if !k.Matches(strings.Fields(want)) {
			t.Errorf("%s: does not match its own fingerprints", f)
		}
		// The binary form reads the same.
		if k2, err := Read(k.Binary); err != nil || strings.Join(k2.Fingerprints, " ") != want || bytes.Contains(k.Binary, []byte("BEGIN")) {
			t.Errorf("%s binary: %v %v", f, k2, err)
		}
	}
}

func TestMatches(t *testing.T) {
	b, _ := os.ReadFile("testdata/brave.gpg")
	k, _ := Read(b)
	all := []string{"DBF1A116C220B8C7164F98230686B78420038257", "47D32A74E9A9E013A4B4926C68D513D36A73CD96", "B2A3DCA350E67256740DF904DE4EC67BE4B0DCA0"}
	if k.Matches(all[:2]) {
		t.Error("extra key in the file accepted")
	}
	if k.Matches(append(all, "35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3")) {
		t.Error("missing key accepted")
	}
	if !k.Matches([]string{all[2], all[0], "47D3 2A74 E9A9 E013 A4B4 926C 68D5 13D3 6A73 CD96"}) {
		t.Error("order or spacing matters")
	}
}

func TestRefuses(t *testing.T) {
	moz, _ := os.ReadFile("testdata/mozilla.asc")
	a, _ := Read(moz)
	if _, err := Read(a.Binary[:len(a.Binary)/2]); err == nil {
		t.Error("truncated key accepted")
	}
	if _, err := Read([]byte("not a key")); err == nil {
		t.Error("text accepted")
	}
}

// Two armored blocks in one file: both keys, as in the two files.
func TestTwoBlocks(t *testing.T) {
	moz, _ := os.ReadFile("testdata/mozilla.asc")
	goog, _ := os.ReadFile("testdata/google.asc")
	a, _ := Read(moz)
	b, _ := Read(goog)
	both, err := Read(append(append(moz, '\n'), goog...))
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]string{}, a.Fingerprints...), b.Fingerprints...); strings.Join(both.Fingerprints, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", both.Fingerprints, want)
	}
	if _, err := Read(moz[:len(moz)/2]); err == nil {
		t.Error("unterminated block accepted")
	}
}
