package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The manual and the completions are written by hand (specs/package.md):
// every command must appear in each.
func TestPackagingNamesEveryCommand(t *testing.T) {
	files := map[string]func(cmd string) string{ // a pattern naming cmd
		"../../packaging/dovy.1":                func(c string) string { return `(?m)^\.B[IR]? ` + c + `\b` },
		"../../packaging/completions/dovy.bash": func(c string) string { return `\b` + c + `\b` },
		"../../packaging/completions/_dovy":     func(c string) string { return `'` + c + `:` },
		"../../packaging/completions/dovy.fish": func(c string) string { return `-a ` + c + ` ` },
	}
	for path, form := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for c := range commands {
			if !regexp.MustCompile(form(c)).Match(b) {
				t.Errorf("%s does not name the command %s", path, c)
			}
		}
	}
	man, _ := os.ReadFile("../../packaging/dovy.1")
	for _, opt := range []string{`\-\-help`, `\-\-version`, `\-\-installed`, `\-\-updates`, `\-\-orphaned`, `\-\-recipe`, `\-\-diff`} {
		if !strings.Contains(string(man), opt) {
			t.Errorf("dovy.1 does not name %s", strings.ReplaceAll(opt, `\`, ""))
		}
	}
}
