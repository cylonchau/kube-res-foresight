package notify

import (
	"html"
	"regexp"
	"strings"
)

// A mail client shows no style sheet of the page an answer was written for (the borders of its tables, the colours of its levels): a table of the answer arrives as
// lines of text. The styles below are put on the tags themselves, which every client reads.

// Colours of a level, the same ones the dashboard writes them in (green reasonable, orange to watch or over-provisioned, red under-provisioned or at risk, blue for information).
var levelStyles = map[string]string{
	"ok":   "color:#18a058;background:#e8f6ee;font-weight:600",
	"warn": "color:#c77d0a;background:#fdf3e1;font-weight:600",
	"bad":  "color:#d03050;background:#fbe9ed;font-weight:600",
	"info": "color:#2080f0;background:#e8f1fd;font-weight:600",
}

var formulaStyles = map[string]string{
	"fx":   "margin:6px 0;padding:6px 10px;border:1px solid #d5d8de;border-radius:6px;background:#f6f7f9;line-height:24px",
	"fx-n": "font-style:italic",
	"fx-c": "padding:0 5px;font-weight:600;color:#c77d0a;background:#fdf3e1;border-radius:4px",
	"fx-r": "padding:0 5px;font-weight:700;color:#18a058;background:#e8f6ee;border-radius:4px",
}

const (
	tableStyle   = "border-collapse:collapse;width:100%;margin:8px 0 14px;font-size:13px"
	cellStyle    = "border:1px solid #d5d8de;padding:6px 8px;text-align:left;vertical-align:top"
	headStyle    = "border:1px solid #d5d8de;padding:6px 8px;text-align:left;vertical-align:top;background:#f1f3f6;font-weight:600"
	captionStyle = "text-align:left;font-weight:600;padding:4px 0;font-size:14px"
	headingStyle = "margin:16px 0 6px;font-size:16px"
)

var (
	openTag   = regexp.MustCompile(`(?is)<(table|thead|tbody|tr|th|td|caption|h[1-4]|div|span)\b([^>]*)>`)
	styleAttr = regexp.MustCompile(`(?is)\sstyle\s*=\s*("[^"]*"|'[^']*')`)
	classAttr = regexp.MustCompile(`(?is)\sclass\s*=\s*("([^"]*)"|'([^']*)')`)
	levelName = regexp.MustCompile(`lv-(ok|warn|bad|info)`)
	fxName    = regexp.MustCompile(`(?:^|\s)(fx|fx-c|fx-n|fx-r)(?:\s|$)`)
)

// InlineMailStyles puts the styles of a table, its cells and its headings, and the colour of a level (class lv-ok, lv-warn, lv-bad, lv-info), on the tags of an HTML
// answer, so a mail client shows it as the page does. A style the answer already has on a tag is kept and wins.
func InlineMailStyles(page string) string {
	return openTag.ReplaceAllStringFunc(page, func(tag string) string {
		m := openTag.FindStringSubmatch(tag)
		name, attrs := strings.ToLower(m[1]), m[2]
		var style string
		switch name {
		case "table":
			style = tableStyle
		case "th":
			style = headStyle
		case "td":
			style = cellStyle
		case "caption":
			style = captionStyle
		case "h1", "h2", "h3", "h4":
			style = headingStyle
		case "div", "span":
			// a formula the answer typeset: a block of lines, a coefficient, a name, a result
			c := classAttr.FindStringSubmatch(attrs)
			if c == nil {
				return tag
			}
			f := fxName.FindStringSubmatch(c[2] + c[3])
			if f == nil {
				return tag
			}
			style = formulaStyles[f[1]]
		default:
			return tag
		}
		if c := classAttr.FindStringSubmatch(attrs); c != nil {
			classes := c[2] + c[3]
			if lv := levelName.FindStringSubmatch(classes); lv != nil && (name == "td" || name == "th") {
				style += ";" + levelStyles[lv[1]]
			}
		}
		if own := styleAttr.FindStringSubmatch(attrs); own != nil {
			style += ";" + strings.Trim(own[1], `"'`)
			attrs = styleAttr.ReplaceAllString(attrs, "")
		}
		return "<" + m[1] + attrs + ` style="` + html.EscapeString(style) + `">`
	})
}

// MailCell is one cell of a table the platform writes into a mail; Level (ok, warn, bad, info or empty) colours it.
type MailCell struct {
	Text  string `json:"text"`
	Level string `json:"level"`
}

// MailTable is a table the platform writes into a mail from figures it holds, so the mail has it whatever the answer of the model holds.
type MailTable struct {
	Caption string       `json:"caption"`
	Columns []string     `json:"columns"`
	Rows    [][]MailCell `json:"rows"`
}

// Limits of the tables of a mail: it is a summary of some lines, not a report.
const (
	maxMailTables = 6
	maxMailRows   = 40
	maxMailCols   = 12
	maxCellRunes  = 300
)

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// MailTablesHTML writes the tables as HTML with their styles on the tags, every text escaped; "" when there is none. The text version has the same cells in rows.
func MailTablesHTML(tables []MailTable) (htmlOut, text string) {
	var h, t strings.Builder
	for i, tb := range tables {
		if i >= maxMailTables || len(tb.Columns) == 0 {
			break
		}
		cols := tb.Columns
		if len(cols) > maxMailCols {
			cols = cols[:maxMailCols]
		}
		h.WriteString(`<table style="` + tableStyle + `">`)
		if c := clipRunes(tb.Caption, maxCellRunes); c != "" {
			h.WriteString(`<caption style="` + captionStyle + `">` + html.EscapeString(c) + "</caption>")
			t.WriteString(c + "\n")
		}
		h.WriteString("<thead><tr>")
		heads := make([]string, len(cols))
		for k, col := range cols {
			heads[k] = clipRunes(col, maxCellRunes)
			h.WriteString(`<th style="` + headStyle + `">` + html.EscapeString(heads[k]) + "</th>")
		}
		h.WriteString("</tr></thead><tbody>")
		t.WriteString(strings.Join(heads, " | ") + "\n")
		for j, row := range tb.Rows {
			if j >= maxMailRows {
				break
			}
			h.WriteString("<tr>")
			cells := make([]string, 0, len(cols))
			for k := range cols {
				var cell MailCell
				if k < len(row) {
					cell = row[k]
				}
				style := cellStyle
				if extra, ok := levelStyles[cell.Level]; ok {
					style += ";" + extra
				}
				text := clipRunes(cell.Text, maxCellRunes)
				h.WriteString(`<td style="` + html.EscapeString(style) + `">` + html.EscapeString(text) + "</td>")
				cells = append(cells, text)
			}
			h.WriteString("</tr>")
			t.WriteString(strings.Join(cells, " | ") + "\n")
		}
		h.WriteString("</tbody></table>")
		t.WriteString("\n")
	}
	return h.String(), strings.TrimSpace(t.String())
}
