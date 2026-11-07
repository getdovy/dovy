package ui

import "strconv"

// Row is one package in a plan table.
type Row struct {
	Name, Version, Source, Size, Note string
}

// Section is one plan table: Replacing, Installing, ...
type Section struct {
	Color, Title string
	Rows         []Row
}

// Pair is an item and its reason.
type Pair struct{ Item, Reason string }

// Count is a summary line: "Install  1  package".
type Count struct {
	Verb string
	N    int
	Unit string
}

// Plan prints the plan tables. All tables share column widths; below the
// table width each package takes two lines.
func (u *UI) Plan(sections []Section) {
	wn, wv, ws, wz := 7, 7, 6, 4
	for _, s := range sections {
		for _, r := range s.Rows {
			wn, wv = max(wn, width(r.Name)), max(wv, width(r.Version))
			ws, wz = max(ws, width(r.Source)), max(wz, width(r.Size))
		}
	}
	narrow := 2+wn+2+wv+2+ws+2+wz > u.Width
	for _, s := range sections {
		u.Rule(s.Color, s.Title)
		if !narrow {
			u.line("  " + u.Paint(Dim, pad("Package", wn)+"  "+pad("Version", wv)+"  "+pad("Source", ws)+"  "+rpad("Size", wz)))
		}
		for _, r := range s.Rows {
			if narrow {
				u.line("  " + u.Paint(s.Color, r.Name))
				u.line("    " + r.Version + " · " + u.Paint(Dim, r.Source) + " · " + r.Size)
			} else {
				u.line("  " + u.Paint(s.Color, pad(r.Name, wn)) + "  " + pad(r.Version, wv) + "  " + u.Paint(Dim, pad(r.Source, ws)) + "  " + rpad(r.Size, wz))
			}
			if r.Note != "" {
				u.line("    " + u.Paint(Dim, r.Note))
			}
		}
		u.Blank()
	}
}

// Pairs prints a titled list of items with reasons.
func (u *UI) Pairs(color, title string, rows []Pair) {
	u.Rule(color, title)
	w := 0
	for _, r := range rows {
		w = max(w, width(r.Item))
	}
	for _, r := range rows {
		u.line("  " + pad(r.Item, w) + "  " + u.Paint(Dim, r.Reason))
	}
	u.Blank()
}

// Summary prints counts, then totals such as download size.
func (u *UI) Summary(counts []Count, totals []Pair) {
	u.Rule("", "Summary")
	for _, c := range counts {
		u.line(" " + pad(c.Verb, 14) + " " + rpad(strconv.Itoa(c.N), 3) + "  " + c.Unit)
	}
	if len(totals) > 0 {
		u.Blank()
	}
	for _, t := range totals {
		u.line(" " + pad(t.Item, 18) + " " + t.Reason)
	}
}

// Grid prints list and search results: a header and four columns, the last
// one free. The third column (status) is colored by its value.
func (u *UI) Grid(title string, header [4]string, rows [][4]string) {
	u.Rule("", title)
	var w [3]int
	for _, r := range append([][4]string{header}, rows...) {
		for i := range w {
			w[i] = max(w[i], width(r[i]))
		}
	}
	u.line("  " + u.Paint(Dim, pad(header[0], w[0])+"  "+pad(header[1], w[1])+"  "+pad(header[2], w[2])+"  "+header[3]))
	for _, r := range rows {
		u.line("  " + pad(r[0], w[0]) + "  " + pad(r[1], w[1]) + "  " + u.Paint(statusColor(r[2]), pad(r[2], w[2])) + "  " + u.Paint(Dim, r[3]))
	}
}

func statusColor(s string) string {
	switch {
	case s == "installed":
		return Green
	case len(s) > 6 && s[:6] == "update":
		return Cyan
	case s == "orphaned" || s == "not available":
		return Yellow
	}
	return ""
}
