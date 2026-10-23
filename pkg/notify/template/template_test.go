package template

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRenderSupportsTemplatesLegacyAndButtons(t *testing.T) {
	title, body, buttons, err := Render(Source{
		TitleTemplate: `{{.asset | upper}} alert`,
		BodyTemplate:  `{{if .count}}count={{.count}}{{else}}empty{{end}} {asset}`,
		ButtonsJSON:   `[{"label":"Open {{.asset}}","action_type":"callback","action_target":"open_{asset}"}]`,
	}, map[string]string{"asset": "btc", "count": "0"})
	require.NoError(t, err)
	assert.Equal(t, "BTC alert", title)
	assert.Equal(t, "empty btc", body)
	require.Len(t, buttons, 1)
	assert.Equal(t, "Open btc", buttons[0].Label)
	assert.Equal(t, "open_btc", buttons[0].ActionTarget)

	_, _, _, err = Render(Source{ButtonsJSON: "{"}, nil)
	assert.ErrorContains(t, err, "parse buttons")
	_, _, _, err = Render(Source{TitleTemplate: `{{template`}, nil)
	assert.NoError(t, err, "invalid syntax is intentionally preserved as literal text")
}

func TestTemplateValidationAndCallbackBinding(t *testing.T) {
	assert.NoError(t, ValidateTemplate("plain"))
	assert.NoError(t, ValidateTemplate(`hello {{.name}}`))
	assert.Error(t, ValidateTemplate(`{{if}}`))

	buttons, err := ValidateButtons(`[{"label":"Open","action_type":"webapp"},{"label":"Approve","action_type":"callback","action_target":"approve"},{"label":"Docs","action_type":"url","action_target":"https://example.com"}]`)
	require.NoError(t, err)
	bound := BindCallbackEntity(buttons, 42)
	assert.Equal(t, "approve:42", bound[1].ActionTarget)
	assert.Equal(t, "https://example.com", bound[2].ActionTarget)
	assert.Equal(t, "approve", buttons[1].ActionTarget, "input must not be mutated")

	_, err = ValidateButtons(`{"label":"x"}`)
	assert.Error(t, err)
	_, err = ValidateButtons(`[{"label":"","action_type":"url"}]`)
	assert.ErrorContains(t, err, "empty label")
	_, err = ValidateButtons(`[{"label":"x","action_type":"unknown"}]`)
	assert.ErrorContains(t, err, "invalid action_type")
	empty, err := ValidateButtons("")
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestStringHelpers(t *testing.T) {
	out, err := renderString(`{{.name | trim | lower}} {{default "fallback" .missing}} {{contains .name "A"}} {{hasPrefix .name " "}} {{hasSuffix .name " "}}`, map[string]string{"name": " Alice "})
	require.NoError(t, err)
	assert.Equal(t, "alice fallback true true true", out)
	assert.Equal(t, "x-1", substituteLegacy("{a}-{b}", map[string]string{"a": "x", "b": "1"}))
	assert.Equal(t, "plain", substituteLegacy("plain", nil))
}
