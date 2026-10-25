// Package template renders an admin-editable model.NotifyTemplate into a
// concrete title/body/buttons for one send. It supports standard Go text/template
// syntax (e.g. {{.name}}, {{if .reason}}...{{end}}, {{.nick_name | upper}}),
// built-in helper functions, and legacy {placeholder} string substitution for
// full backward compatibility.
package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	texttmpl "text/template"
)

// Button is a channel-agnostic rendered button. ActionType is "webapp"
// (an optional link target at ActionTarget, or none if
// ActionTarget is empty), "callback" (ActionTarget is the action prefix;
// the caller appends ":<entityID>" once it knows which
// entity this send is for),
// or "url" (opens ActionTarget as an ordinary external link — e.g. a help
// page or an external ticket system; not routed through the bot at all).
type Button struct {
	Label        string `json:"label"`
	ActionType   string `json:"action_type"`
	ActionTarget string `json:"action_target"`
}

const (
	ActionTypeWebApp   = "webapp"
	ActionTypeCallback = "callback"
	ActionTypeURL      = "url"
)

// Source is the subset of model.NotifyTemplate needed to render — kept as
// plain strings (not model.NotifyTemplate itself) so this package has zero
// dependencies beyond the standard library.
type Source struct {
	TitleTemplate string
	BodyTemplate  string
	ButtonsJSON   string
}

var funcMap = texttmpl.FuncMap{
	"upper": func(v any) string { return strings.ToUpper(toStr(v)) },
	"lower": func(v any) string { return strings.ToLower(toStr(v)) },
	"trim":  func(v any) string { return strings.TrimSpace(toStr(v)) },
	"title": func(v any) string { return strings.Title(toStr(v)) },
	"default": func(def string, val any) string {
		if val == nil {
			return def
		}
		str := fmt.Sprintf("%v", val)
		if strings.TrimSpace(str) == "" {
			return def
		}
		return str
	},
	"contains":  func(s, sub any) bool { return strings.Contains(toStr(s), toStr(sub)) },
	"hasPrefix": func(s, p any) bool { return strings.HasPrefix(toStr(s), toStr(p)) },
	"hasSuffix": func(s, p any) bool { return strings.HasSuffix(toStr(s), toStr(p)) },
}

// Render parses and executes TitleTemplate and BodyTemplate against vars
// using Go text/template (falling back to / augmenting with {placeholder}
// string replacement), and unmarshals & renders ButtonsJSON.
func Render(src Source, vars map[string]string) (title, body string, buttons []Button, err error) {
	if vars == nil {
		vars = map[string]string{}
	}

	title, err = renderString(src.TitleTemplate, vars)
	if err != nil {
		return "", "", nil, fmt.Errorf("render title: %w", err)
	}

	body, err = renderString(src.BodyTemplate, vars)
	if err != nil {
		return "", "", nil, fmt.Errorf("render body: %w", err)
	}

	if strings.TrimSpace(src.ButtonsJSON) != "" {
		if err := json.Unmarshal([]byte(src.ButtonsJSON), &buttons); err != nil {
			return "", "", nil, fmt.Errorf("parse buttons: %w", err)
		}
		for i := range buttons {
			renderedLabel, err := renderString(buttons[i].Label, vars)
			if err == nil {
				buttons[i].Label = renderedLabel
			}
			renderedTarget, err := renderString(buttons[i].ActionTarget, vars)
			if err == nil {
				buttons[i].ActionTarget = renderedTarget
			}
		}
	}

	return title, body, buttons, nil
}

// renderString renders a string using Go text/template and legacy {var} replacement.
// templateData hands vars to text/template. Every value is a string, and in
// Go templates any non-empty string is truthy — so a count of "0" made
// {{if .failed_count}}...{{end}} render its section anyway, which defeated
// the whole point of wrapping optional sections (e.g. "❓ 查询失败（0）")
// in a conditional. The literal value "0" is therefore passed as the
// integer 0 instead: {{if}} treats it as false, while {{.x}} still prints
// "0". Only "0" is special-cased on purpose — converting other numerics
// would break {{eq .x "1"}}-style string comparisons and the string-typed
// helper funcs for no benefit.
func templateData(vars map[string]string) map[string]any {
	data := make(map[string]any, len(vars))
	for k, v := range vars {
		if v == "0" {
			data[k] = 0
		} else {
			data[k] = v
		}
	}
	return data
}

// toStr lets the string helper funcs below accept the typed 0 templateData
// may hand them, instead of failing with a type error.
func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func renderString(text string, vars map[string]string) (string, error) {
	if text == "" {
		return "", nil
	}

	// 1. If text contains Go template actions {{...}}, execute text/template
	out := text
	if strings.Contains(text, "{{") {
		tmpl, err := texttmpl.New("").Funcs(funcMap).Option("missingkey=zero").Parse(text)
		if err != nil {
			// If text/template parsing fails (e.g. invalid syntax), fall back to literal
			// string with {var} substitution so minor typos don't panic or fail entirely.
			out = text
		} else {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, templateData(vars)); err == nil {
				out = buf.String()
			}
		}
	}

	// 2. Legacy {placeholder} substitution for backwards compatibility
	out = substituteLegacy(out, vars)

	return out, nil
}

// ValidateTemplate verifies that text is a valid Go text/template syntax.
func ValidateTemplate(text string) error {
	if text == "" || !strings.Contains(text, "{{") {
		return nil
	}
	_, err := texttmpl.New("validate").Funcs(funcMap).Parse(text)
	return err
}

// ValidateButtons verifies that buttonsJSON is a valid JSON array of buttons
// and each button has valid action_type and label.
func ValidateButtons(buttonsJSON string) ([]Button, error) {
	if strings.TrimSpace(buttonsJSON) == "" {
		return nil, nil
	}
	var buttons []Button
	if err := json.Unmarshal([]byte(buttonsJSON), &buttons); err != nil {
		return nil, fmt.Errorf("buttons_json is not a valid JSON array: %w", err)
	}
	for i, b := range buttons {
		if strings.TrimSpace(b.Label) == "" {
			return nil, fmt.Errorf("button at index %d has empty label", i)
		}
		switch b.ActionType {
		case ActionTypeWebApp, ActionTypeCallback, ActionTypeURL:
		default:
			return nil, fmt.Errorf("button at index %d has invalid action_type %q (must be %s, %s, or %s)", i, b.ActionType, ActionTypeWebApp, ActionTypeCallback, ActionTypeURL)
		}
	}
	return buttons, nil
}

// BindCallbackEntity appends ":<entityID>" to every callback-type button's
// ActionTarget, turning a template's generic action prefix into a concrete,
// routable callback_data value for one specific entity. webapp and url buttons pass through
// unchanged.
func BindCallbackEntity(buttons []Button, entityID int64) []Button {
	bound := make([]Button, len(buttons))
	for i, b := range buttons {
		if b.ActionType == ActionTypeCallback && b.ActionTarget != "" {
			b.ActionTarget = fmt.Sprintf("%s:%d", b.ActionTarget, entityID)
		}
		bound[i] = b
	}
	return bound
}

func substituteLegacy(text string, vars map[string]string) string {
	if text == "" || len(vars) == 0 {
		return text
	}
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(text)
}
