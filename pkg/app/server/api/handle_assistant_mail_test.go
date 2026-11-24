package api

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cylonchau/kube-res-foresight/pkg/notify"
)

func TestTheMailOfAnAnswerKeepsAnHTMLAnswerAsHTMLAndTheSubjectOnOneLine(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	msg := analysisMail(AssistantMailReq{Title: "web-api\nBcc: x@evil", Content: "<table><tr><td>CPU Requests</td><td>4 cores</td></tr></table><script>alert(1)</script>"}, at)
	assert.NotContains(t, msg.Title, "\n", "a title cannot start another header")
	assert.Contains(t, msg.Title, "[kube-res-foresight]")
	assert.Contains(t, msg.HTML, "<table")
	assert.NotContains(t, msg.HTML, "script")
	assert.Contains(t, msg.Content, "CPU Requests")

	text := analysisMail(AssistantMailReq{Title: "web-api", Content: "## Summary\n\nCPU is fine."}, at)
	assert.Empty(t, text.HTML, "Markdown is mailed as text")
	assert.Contains(t, text.Content, "CPU is fine.")
}

func TestTheMailHasTheTablesOfThePageWithTheirStylesOnTheTags(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	req := AssistantMailReq{
		Title:   "web-api",
		Content: `<table><tr><th>Item</th></tr><tr><td class="lv-warn">over</td></tr></table>`,
		Tables: []notify.MailTable{{Caption: "Replicas", Columns: []string{"Plan", "Replicas now", "Replicas recommended"},
			Rows: [][]notify.MailCell{{{Text: "<b>keep the Pod</b>"}, {Text: "12"}, {Text: "10", Level: "warn"}}}}},
	}
	msg := analysisMail(req, at)
	// the tables of the page come first, with a border on every cell and the colour of the level on the cell; what the page wrote is escaped
	assert.Contains(t, msg.HTML, "border:1px solid")
	assert.Contains(t, msg.HTML, "&lt;b&gt;keep the Pod&lt;/b&gt;")
	assert.Contains(t, msg.HTML, "color:#c77d0a")
	assert.Less(t, strings.Index(msg.HTML, "Replicas recommended"), strings.Index(msg.HTML, "Item"))
	// the table of the answer has its styles put on it, and the colour of its level
	assert.Contains(t, msg.HTML, `<th style="border:1px solid #d5d8de`)
	assert.Contains(t, msg.Content, "Plan | Replicas now | Replicas recommended")

	// a text answer with tables of the page is mailed as HTML, the text kept in lines
	text := analysisMail(AssistantMailReq{Title: "x", Content: "## Summary", Tables: req.Tables}, at)
	assert.Contains(t, text.HTML, "## Summary")
	assert.Contains(t, text.HTML, "<table")
}
