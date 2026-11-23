package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTheMailOfAnAnswerKeepsAnHTMLAnswerAsHTMLAndTheSubjectOnOneLine(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	msg := analysisMail(AssistantMailReq{Title: "web-api\nBcc: x@evil", Content: "<table><tr><td>CPU Requests</td><td>4 cores</td></tr></table><script>alert(1)</script>"}, at)
	assert.NotContains(t, msg.Title, "\n", "a title cannot start another header")
	assert.Contains(t, msg.Title, "[kube-res-foresight]")
	assert.Contains(t, msg.HTML, "<table>")
	assert.NotContains(t, msg.HTML, "script")
	assert.Contains(t, msg.Content, "CPU Requests")

	text := analysisMail(AssistantMailReq{Title: "web-api", Content: "## Summary\n\nCPU is fine."}, at)
	assert.Empty(t, text.HTML, "Markdown is mailed as text")
	assert.Contains(t, text.Content, "CPU is fine.")
}
