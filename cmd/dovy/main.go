// Command dovy installs software Debian stable lacks or ships older.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/getdovy/dovy/internal/ui"
)

// Set at build time: -ldflags "-X main.version=1.0.0".
var version = "0.0.0-dev"

// Exit codes (principles topic 12, rule 10).
const (
	exitOK        = 0
	exitFailed    = 1
	exitUsage     = 2
	exitRefused   = 3
	exitCancelled = 4
)

const usage = `usage: dovy <command> [arguments]

Commands:
  install <recipe>...  plan, confirm, install
  remove <recipe>...   plan, confirm, remove
  update               fetch and verify the catalog; changes nothing
  upgrade              one plan for everything
  list [filter]        the catalog; filter: --installed, --updates, --orphaned
  search <word>        search names and descriptions
  show <recipe>        details; --recipe prints the recipe, --diff the changes
  check                report problems; changes nothing

Options:
  --help     this help
  --version  dovy's version
`

// command is one entry of the command set (principles topic 9).
type command struct {
	usage     string
	milestone int
	valid     func(args []string) string // "" or a usage error
}

var commands = map[string]command{
	"install": {"install <recipe>...", 3, recipes},
	"remove":  {"remove <recipe>...", 3, recipes},
	"update":  {"update", 1, none},
	"upgrade": {"upgrade", 9, none},
	"list":    {"list [--installed | --updates | --orphaned]", 1, list},
	"search":  {"search <word>", 1, search},
	"show":    {"show <recipe> [--recipe | --diff]", 1, show},
	"check":   {"check", 9, none},
}

func none(args []string) string {
	if len(args) > 0 {
		return "takes no arguments"
	}
	return ""
}

func recipes(args []string) string {
	if len(args) == 0 {
		return "needs at least one recipe"
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return "unknown option " + a
		}
	}
	return ""
}

func list(args []string) string {
	switch {
	case len(args) == 0:
		return ""
	case len(args) > 1:
		return "takes one filter at most"
	case args[0] == "--installed" || args[0] == "--updates" || args[0] == "--orphaned":
		return ""
	}
	return "unknown filter " + args[0]
}

func search(args []string) string {
	switch {
	case len(args) == 0:
		return "needs a word; dovy list shows everything"
	case len(args) > 1:
		return "takes one word"
	case strings.HasPrefix(args[0], "-"):
		return "takes no options"
	}
	return ""
}

func show(args []string) string {
	var name, opt []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			opt = append(opt, a)
		} else {
			name = append(name, a)
		}
	}
	switch {
	case len(name) != 1:
		return "needs one recipe"
	case len(opt) > 1:
		return "takes one option at most"
	case len(opt) == 1 && opt[0] != "--recipe" && opt[0] != "--diff":
		return "unknown option " + opt[0]
	}
	return ""
}

func main() {
	out, errs := ui.New(os.Stdout), ui.New(os.Stderr)
	a, err := newApp(out, errs)
	if err != nil {
		errs.Fail(err.Error(), "unchanged", "set HOME or XDG_DATA_HOME", "")
		os.Exit(exitFailed)
	}
	os.Exit(run(os.Args[1:], a, os.Geteuid()))
}

func run(args []string, a *app, euid int) int {
	out, errs := a.out.Out, a.errs
	if len(args) == 0 {
		fmt.Fprint(errs.Out, usage)
		return exitUsage
	}
	switch args[0] {
	case "--help", "-h":
		fmt.Fprint(out, usage)
		return exitOK
	case "--version":
		fmt.Fprintln(out, "dovy "+version)
		return exitOK
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(errs.Out, "dovy: unknown command %s\n\n%s", args[0], usage)
		return exitUsage
	}
	rest := args[1:]
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		fmt.Fprintln(out, "usage: dovy "+c.usage)
		return exitOK
	}
	if msg := c.valid(rest); msg != "" {
		fmt.Fprintf(errs.Out, "dovy %s: %s\nusage: dovy %s\n", args[0], msg, c.usage)
		return exitUsage
	}
	// Topic 2, rule 9: dovy runs as the user and asks for sudo itself.
	if euid == 0 {
		errs.Fail("dovy runs as root", "unchanged",
			"run dovy as a regular user; it asks for sudo when needed", "")
		return exitRefused
	}
	switch args[0] {
	case "update":
		return a.update()
	case "list":
		return a.list(rest)
	case "search":
		return a.search(rest[0])
	case "show":
		return a.show(rest)
	}
	errs.Fail(args[0]+" is not built yet", "unchanged",
		fmt.Sprintf("milestone %d (dovy-design/build-order.md)", c.milestone), "")
	return exitFailed
}
