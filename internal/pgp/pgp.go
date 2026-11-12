// Package pgp reads an OpenPGP public key file just far enough to check its
// fingerprint (principles 2.4): dovy needs no gpg.
package pgp

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Tags of the packets dovy looks at (RFC 9580).
const (
	tagPublicKey = 6
)

// Key is a key file read: its primary keys' fingerprints and the binary
// keyring.
type Key struct {
	Fingerprints []string // uppercase hex, in file order
	Binary       []byte
}

// Matches reports whether the file's primary keys are exactly want: none
// missing, none extra (2.4).
func (k *Key) Matches(want []string) bool {
	if len(k.Fingerprints) != len(want) {
		return false
	}
	got := map[string]bool{}
	for _, f := range k.Fingerprints {
		got[f] = true
	}
	for _, f := range want {
		if !got[strings.ToUpper(strings.ReplaceAll(f, " ", ""))] {
			return false
		}
	}
	return len(got) == len(want)
}

// Read parses an armored or binary key file.
func Read(data []byte) (*Key, error) {
	bin := data
	if bytes.Contains(data, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		var err error
		if bin, err = dearmor(string(data)); err != nil {
			return nil, err
		}
	}
	var fprs []string
	for rest := bin; len(rest) > 0; {
		tag, body, next, err := packet(rest)
		if err != nil {
			return nil, err
		}
		if tag == tagPublicKey {
			fpr, err := fingerprint(body)
			if err != nil {
				return nil, err
			}
			fprs = append(fprs, fpr)
		}
		rest = next
	}
	if len(fprs) == 0 {
		return nil, errors.New("no public key in the file")
	}
	return &Key{Fingerprints: fprs, Binary: bin}, nil
}

// fingerprint of a public key packet body: v4 SHA-1, v6 SHA-256.
func fingerprint(body []byte) (string, error) {
	if len(body) == 0 {
		return "", errors.New("empty key packet")
	}
	switch body[0] {
	case 4:
		h := sha1.New()
		h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
		h.Write(body)
		return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
	case 6:
		h := sha256.New()
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(body)))
		h.Write(append([]byte{0x9b}, n[:]...))
		h.Write(body)
		return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
	}
	return "", fmt.Errorf("key version %d not supported", body[0])
}

// packet splits off one packet: its tag, body and what follows.
func packet(b []byte) (tag int, body, rest []byte, err error) {
	if b[0]&0x80 == 0 {
		return 0, nil, nil, errors.New("not an OpenPGP packet")
	}
	var n, hdr int
	if b[0]&0x40 == 0 { // old format
		tag = int(b[0]>>2) & 0x0f
		switch b[0] & 3 {
		case 0:
			hdr, n = 2, int(at(b, 1))
		case 1:
			hdr, n = 3, int(at(b, 1))<<8|int(at(b, 2))
		case 2:
			hdr, n = 5, int(binary.BigEndian.Uint32(pad(b, 1, 4)))
		default:
			return 0, nil, nil, errors.New("indeterminate packet length")
		}
	} else { // new format
		tag = int(b[0] & 0x3f)
		switch o := int(at(b, 1)); {
		case o < 192:
			hdr, n = 2, o
		case o < 224:
			hdr, n = 3, (o-192)<<8+int(at(b, 2))+192
		case o == 255:
			hdr, n = 6, int(binary.BigEndian.Uint32(pad(b, 2, 4)))
		default:
			return 0, nil, nil, errors.New("partial packet length in a key")
		}
	}
	if n < 0 || hdr+n > len(b) {
		return 0, nil, nil, errors.New("truncated key file")
	}
	return tag, b[hdr : hdr+n], b[hdr+n:], nil
}

func at(b []byte, i int) byte {
	if i < len(b) {
		return b[i]
	}
	return 0
}

func pad(b []byte, from, n int) []byte {
	out := make([]byte, n)
	if from < len(b) {
		copy(out, b[from:])
	}
	return out
}

// dearmor returns the binary data of the ASCII-armored blocks, in order.
func dearmor(s string) ([]byte, error) {
	var out []byte
	var b strings.Builder
	in, body := false, false
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		switch {
		case strings.HasPrefix(l, "-----BEGIN PGP PUBLIC KEY BLOCK-----"):
			in, body = true, false
			b.Reset()
		case strings.HasPrefix(l, "-----END PGP PUBLIC KEY BLOCK-----"):
			if in { // each block is padded on its own
				bin, err := base64.StdEncoding.DecodeString(b.String())
				if err != nil {
					return nil, fmt.Errorf("armored key: %w", err)
				}
				out = append(out, bin...)
			}
			in = false
		case !in:
		case !body: // armor headers end at the first blank line
			if strings.TrimSpace(l) == "" {
				body = true
			}
		case strings.HasPrefix(l, "="): // checksum line
		default:
			b.WriteString(strings.TrimSpace(l))
		}
	}
	if in {
		return nil, errors.New("armored key: no END line")
	}
	return out, nil
}
