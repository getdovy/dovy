package apt

import (
	"os/exec"
	"strings"
)

// Compare orders Debian versions as dpkg does, see deb-version(7).
func Compare(a, b string) int {
	ea, ua, ra := split(a)
	eb, ub, rb := split(b)
	if c := cmpNum(ea, eb); c != 0 {
		return c
	}
	if c := cmpPart(ua, ub); c != 0 {
		return c
	}
	return cmpPart(ra, rb)
}

func split(v string) (epoch, upstream, revision string) {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		epoch, v = v[:i], v[i+1:]
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// order: '~' sorts before everything, even the end of the string; letters before non-letters.
func order(c byte) int {
	switch {
	case c == '~':
		return -1
	case isDigit(c):
		return 0
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		return int(c)
	}
	return int(c) + 256
}

func cmpPart(a, b string) int {
	for a != "" || b != "" {
		for (a != "" && !isDigit(a[0])) || (b != "" && !isDigit(b[0])) {
			ca, cb := 0, 0
			if a != "" {
				ca = order(a[0])
			}
			if b != "" {
				cb = order(b[0])
			}
			if ca != cb {
				return ca - cb
			}
			a, b = a[1:], b[1:]
		}
		i, j := 0, 0
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		if c := cmpNum(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]
	}
	return 0
}

func cmpNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func Stable(opts []string, pkgs []string) map[string]string {
	if len(pkgs) == 0 {
		return nil
	}
	out, _ := exec.Command("apt-cache", append(append(append([]string{}, opts...), "madison"), pkgs...)...).Output()
	return parseMadison(string(out), false)
}

func Backports(opts []string, pkgs []string) map[string]string {
	if len(pkgs) == 0 {
		return nil
	}
	out, _ := exec.Command("apt-cache", append(append(append([]string{}, opts...), "madison"), pkgs...)...).Output()
	return parseMadison(string(out), true)
}

// parseMadison reads " pkg | version | url suite/component arch Packages", skipping ~dovy builds.
func parseMadison(out string, backports bool) map[string]string {
	best := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "|")
		if len(f) != 3 {
			continue
		}
		name, ver, src := strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.Fields(f[2])
		if len(src) < 2 || strings.Contains(src[1], "-backports") != backports || strings.Contains(ver, "~dovy") {
			continue
		}
		if cur, ok := best[name]; !ok || Compare(ver, cur) > 0 {
			best[name] = ver
		}
	}
	return best
}
