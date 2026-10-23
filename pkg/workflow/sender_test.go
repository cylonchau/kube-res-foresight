package workflow

import (
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestButtonsFromTemplate(t *testing.T) {
	in := []template.Button{
		{Label: "Open App", ActionType: template.ActionTypeWebApp, ActionTarget: "#/tickets/42"},
		{Label: "Approve", ActionType: template.ActionTypeCallback, ActionTarget: "leader_approve:42"},
		{Label: "Docs", ActionType: template.ActionTypeURL, ActionTarget: "https://example.com"},
		{Label: "Unknown", ActionType: "not_a_real_type", ActionTarget: "whatever"},
	}

	out := ButtonsFromTemplate(in)

	assert.Equal(t, []Button{
		{Label: "Open App", ActionType: "webapp", Target: "#/tickets/42"},
		{Label: "Approve", ActionType: "callback", Target: "leader_approve:42"},
		{Label: "Docs", ActionType: "url", Target: "https://example.com"},
	}, out, "an unrecognized ActionType must be dropped, not passed through blindly")
}

func TestButtonsFromTemplateEmpty(t *testing.T) {
	assert.Empty(t, ButtonsFromTemplate(nil))
	assert.Empty(t, ButtonsFromTemplate([]template.Button{}))
}
