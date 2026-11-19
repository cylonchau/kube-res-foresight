package assistant

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mods() []*model.ForecastModule {
	return []*model.ForecastModule{
		{Name: "shop", Namespace: "prod", Workload: "shop-api"},
		{Name: "shop-admin", Namespace: "prod", Workload: "admin"},
		{Name: "db", Namespace: "data", Workload: "postgres"},
	}
}

func TestNamesAreReplacedByStableAliasesAndPutBack(t *testing.T) {
	r := redactorOf(mods())
	require.NotNil(t, r)
	hidden := r.Hide("How is shop-admin? And shop in prod, workload shop-api? The db in data.")
	for _, name := range []string{"shop-admin", "shop-api", "shop", "prod", "postgres", "data"} {
		assert.NotContains(t, hidden, name, "the model is never given %q", name)
	}
	assert.Equal(t, "How is shop-admin? And shop in prod, workload shop-api? The db in data.", r.Show(hidden), "what the model says is put back as the user's own names")

	// the same name has the same alias in the next question
	again := redactorOf(mods())
	assert.Equal(t, hidden, again.Hide("How is shop-admin? And shop in prod, workload shop-api? The db in data."))

	// the longest name is replaced as a whole, so "shop-admin" is not "<alias of shop>-admin"
	assert.NotContains(t, r.Hide("shop-admin"), "-admin")
	// an alias is put back whole: module-10 is not "module-1" and a 0
	many := make([]*model.ForecastModule, 0, 12)
	for i := 0; i < 12; i++ {
		many = append(many, &model.ForecastModule{Name: "svc" + string(rune('a'+i)) + "x"})
	}
	big := redactorOf(many)
	text := "svcax svckx svclx"
	assert.Equal(t, text, big.Show(big.Hide(text)))
}

func TestANameThatIsTooShortIsLeftAloneAndNoRedactorMeansNoChange(t *testing.T) {
	r := redactorOf([]*model.ForecastModule{{Name: "db", Namespace: "x", Workload: "w"}})
	assert.Nil(t, r, "no name of three characters: nothing to hide")
	var none *Redactor
	assert.Equal(t, "shop stays shop", none.Hide("shop stays shop"))
	assert.Equal(t, "module-1", none.Show("module-1"))
}

type recorder struct {
	seen  []Message
	reply *Completion
}

func (r *recorder) Complete(_ context.Context, messages []Message, _ []ToolSpec, _ int) (*Completion, error) {
	r.seen = append([]Message(nil), messages...)
	return r.reply, nil
}

func TestTheModelSeesAliasesAndTheToolsAreRunWithTheNames(t *testing.T) {
	r := redactorOf(mods())
	alias := r.Hide("shop")
	inner := &recorder{reply: &Completion{Content: "all fine with " + alias, ToolCalls: []ToolCall{{ID: "1", Name: "get_module_effect_report", Arguments: `{"module":"` + alias + `"}`}}}}
	llm := redactingLLM{inner: inner, r: r}
	done, err := llm.Complete(context.Background(), []Message{
		{Role: "user", Content: "how is shop?"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "0", Name: "list_modules", Arguments: `{"module":"shop"}`}}},
		{Role: "tool", ToolCallID: "0", Content: `{"namespace":"prod","workload":"shop-api"}`},
	}, nil, 100)
	require.NoError(t, err)
	for _, m := range inner.seen {
		assert.NotContains(t, m.Content, "shop")
		assert.NotContains(t, m.Content, "prod")
		for _, c := range m.ToolCalls {
			assert.NotContains(t, c.Arguments, "shop")
		}
	}
	assert.Equal(t, "all fine with shop", done.Content, "the user reads their own name")
	assert.JSONEq(t, `{"module":"shop"}`, done.ToolCalls[0].Arguments, "the tool is run with the real name")
}
