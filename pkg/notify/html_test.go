package notify

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTMLBodyTellsAPageFromText(t *testing.T) {
	page := "<h1>Usage</h1><table><tr><td>cpu</td><td>4</td></tr></table>"
	assert.Equal(t, page, HTMLBody(page))
	assert.Equal(t, page, HTMLBody("```html\n"+page+"\n```"), "the code fence a model puts round a page is taken off")
	assert.Equal(t, "<!DOCTYPE html><html><body>x</body></html>", HTMLBody("  <!DOCTYPE html><html><body>x</body></html>"))
	assert.Empty(t, HTMLBody("cpu < 4 and memory > 2"), "plain text with angle brackets is not a page")
	assert.Empty(t, HTMLBody("# Report\n\n| a | b |"), "Markdown is not a page")
	assert.NotContains(t, HTMLBody("<p>a</p><script>alert(1)</script>"), "script")
}

func TestHTMLTextKeepsLinesAndRows(t *testing.T) {
	text := HTMLText("<html><head><style>p{color:red}</style></head><body><h1>Usage</h1><p>a &amp; b</p>" +
		"<table><tr><th>name</th><th>cpu</th></tr><tr><td>web</td><td>4</td></tr></table></body></html>")
	assert.Equal(t, "Usage\na & b\nname | cpu\nweb | 4", text)
}

func TestInlineMailStylesPutTheStyleOnTheTagAndKeepWhatTheAnswerHas(t *testing.T) {
	out := InlineMailStyles(`<table><tr><th>A</th><td class="lv-bad" style="color:red">x</td></tr></table><h2>T</h2>`)
	assert.Contains(t, out, `<table style="border-collapse:collapse`)
	assert.Contains(t, out, `<th style="border:1px solid #d5d8de`)
	// the colour of the level is on the cell, and what the answer wrote comes last so it wins
	assert.Contains(t, out, "background:#fbe9ed")
	assert.Contains(t, out, ";color:red")
	assert.Contains(t, out, `<h2 style="margin:16px 0 6px`)
	assert.NotContains(t, out, "<tr style", "rows are left as they are")
}
