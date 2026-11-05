package notify

import (
	"html"
	"regexp"
	"strings"
)

var (
	fenceOpen   = regexp.MustCompile("(?is)^\\s*```(?:html)?\\s*\n")
	fenceClose  = regexp.MustCompile("(?s)\n\\s*```\\s*$")
	htmlStart   = regexp.MustCompile(`(?is)^\s*(?:<!doctype\s+html|<html[\s>]|<(?:head|body|div|p|h[1-6]|table|ul|ol|section|article|style|span|br)[\s>/])`)
	scriptBlock = regexp.MustCompile(`(?is)<(script|iframe|object|embed)\b.*?</(?:script|iframe|object|embed)\s*>`)
	styleBlock  = regexp.MustCompile(`(?is)<(?:style|head)\b.*?</(?:style|head)\s*>`)
	lineBreaks  = regexp.MustCompile(`(?i)<br\s*/?>|</(?:p|div|h[1-6]|tr|li|table|ul|ol|section|article)\s*>`)
	cellEnds    = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	anyTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
)

// HTMLBody returns the text as an HTML document when it is one, and "" when it is plain text. A model that was asked for an HTML mail
// often wraps the page in a Markdown code fence, which is taken off; a script, frame or object is dropped, as no mail shows them.
func HTMLBody(text string) string {
	s := strings.TrimSpace(text)
	s = fenceOpen.ReplaceAllString(s, "")
	s = fenceClose.ReplaceAllString(s, "")
	if !htmlStart.MatchString(s) {
		return ""
	}
	return strings.TrimSpace(scriptBlock.ReplaceAllString(s, ""))
}

// HTMLText is the plain-text reading of an HTML document, for the part of a mail that is shown when HTML is not: the lines of the
// page stay lines and the cells of a row stay in a row.
func HTMLText(doc string) string {
	s := styleBlock.ReplaceAllString(doc, "")
	s = cellEnds.ReplaceAllString(s, " | ")
	s = lineBreaks.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(strings.TrimSpace(line), " |")
	}
	return strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
