package workflow

import "github.com/cylonchau/kubeforecast/pkg/notify/template"

// Button is one platform-agnostic notification button — the payload shape
// every ChatDriver.SendMessage call carries. Exactly one of ActionType's
// meanings applies to Target: "webapp" (a Mini-App/web URL, empty means the
// driver's own default app URL), "callback" (routed back through
// DispatchEvent, keyed by event name), or "url" (an ordinary external
// link).
type Button struct {
	Label      string
	ActionType string // "webapp" | "callback" | "url"
	Target     string
}

// ButtonsFromTemplate converts a flat list of rendered NotifyTemplate
// buttons (channel-agnostic; see pkg/notify/template) into the Sender-level
// Button shape. Shared by every caller that renders a template and then
// hands its buttons to a ChatDriver — the engine's own notify actions
// (engine.go's executeNotifyAction) and, outside the engine entirely, the
// job engine's broadcast notifier (pkg/notify/telegram.Notifier.Send) —
// so the two can't drift on how a template button's ActionType/ActionTarget
// map onto a Sender Button.
func ButtonsFromTemplate(buttons []template.Button) []Button {
	out := make([]Button, 0, len(buttons))
	for _, b := range buttons {
		switch b.ActionType {
		case template.ActionTypeWebApp, template.ActionTypeCallback, template.ActionTypeURL:
			out = append(out, Button{Label: b.Label, ActionType: b.ActionType, Target: b.ActionTarget})
		}
	}
	return out
}
