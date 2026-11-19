package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// A language model is a service the platform does not run: what is sent to it leaves the platform. The credentials never do (no tool returns one).
// The names of the namespaces, the workloads and the modules do by default, because without a name the assistant cannot say which module it
// means. A deployment that does not want them to leave turns on the system parameter assistant.redact_names: every name the platform knows is
// replaced by a stable alias (module-1, namespace-2, workload-3) in everything the model is sent, and put back in what it answers, so the user
// reads their own names and the model never sees them. The aliases are made from the list of modules in a fixed order, so the same name has the
// same alias from one question to the next and a conversation stays coherent. It covers the names the platform has stored (the modules, their
// namespaces and workloads); a name the user types that the platform does not know is the user's to send. A name shorter than three characters
// is not replaced: it would also replace the same letters inside every word.

const (
	// KeyRedactNames is the system parameter that turns the replacement of names on.
	KeyRedactNames = "assistant.redact_names"
	minRedactedLen = 3
)

// Redactor replaces the names of the platform by aliases for the model and puts them back.
type Redactor struct {
	toAlias *strings.Replacer
	toReal  *strings.Replacer
	names   int
}

type pair struct{ real, alias string }

// newRedactor builds the redactor for a question: nil when the parameter is off, or when there are no names to hide.
func (s *Service) newRedactor(ctx context.Context) *Redactor {
	if s.Config == nil || s.Modules == nil {
		return nil
	}
	on, err := s.Config.GetBool(ctx, KeyRedactNames, false)
	if err != nil || !on {
		return nil
	}
	mods, err := s.Modules.AllModules(ctx)
	if err != nil || len(mods) == 0 {
		return nil
	}
	return redactorOf(mods)
}

func redactorOf(mods []*model.ForecastModule) *Redactor {
	collect := func(label string, value func(*model.ForecastModule) string) []pair {
		seen := map[string]bool{}
		var names []string
		for _, m := range mods {
			if v := strings.TrimSpace(value(m)); len([]rune(v)) >= minRedactedLen && !seen[v] {
				seen[v] = true
				names = append(names, v)
			}
		}
		sort.Strings(names) // a fixed order: the same name is the same alias in every question
		out := make([]pair, 0, len(names))
		for i, n := range names {
			out = append(out, pair{n, fmt.Sprintf("%s-%d", label, i+1)})
		}
		return out
	}
	var pairs []pair
	pairs = append(pairs, collect("module", func(m *model.ForecastModule) string { return m.Name })...)
	pairs = append(pairs, collect("namespace", func(m *model.ForecastModule) string { return m.Namespace })...)
	pairs = append(pairs, collect("workload", func(m *model.ForecastModule) string { return m.Workload })...)
	if len(pairs) == 0 {
		return nil
	}
	// the longest first: "shop-api" is replaced as a whole before "shop" can take a part of it
	forward := append([]pair(nil), pairs...)
	sort.SliceStable(forward, func(i, j int) bool { return len(forward[i].real) > len(forward[j].real) })
	backward := append([]pair(nil), pairs...)
	sort.SliceStable(backward, func(i, j int) bool { return len(backward[i].alias) > len(backward[j].alias) })
	f, b := make([]string, 0, len(pairs)*2), make([]string, 0, len(pairs)*2)
	for _, p := range forward {
		f = append(f, p.real, p.alias)
	}
	for _, p := range backward {
		b = append(b, p.alias, p.real)
	}
	return &Redactor{toAlias: strings.NewReplacer(f...), toReal: strings.NewReplacer(b...), names: len(pairs)}
}

// Hide is a text as the model is given it.
func (r *Redactor) Hide(text string) string {
	if r == nil || text == "" {
		return text
	}
	return r.toAlias.Replace(text)
}

// Show is a text of the model as the user is given it.
func (r *Redactor) Show(text string) string {
	if r == nil || text == "" {
		return text
	}
	return r.toReal.Replace(text)
}

// redactingLLM stands between the question and the model: what goes out has the aliases, what comes back has the names. The tools are run with the
// names (the model's arguments are put back before the call), so the tools and the stored conversation know nothing of it.
type redactingLLM struct {
	inner LLM
	r     *Redactor
}

// hide is the messages as the model is given them.
func (l redactingLLM) hide(messages []Message) []Message {
	hidden := make([]Message, len(messages))
	for i, m := range messages {
		m.Content = l.r.Hide(m.Content)
		if len(m.ToolCalls) > 0 {
			calls := make([]ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				c.Arguments = l.r.Hide(c.Arguments)
				calls[j] = c
			}
			m.ToolCalls = calls
		}
		hidden[i] = m
	}
	return hidden
}

// show is an answer of the model as the user is given it.
func (l redactingLLM) show(done *Completion) *Completion {
	out := *done
	out.Content = l.r.Show(done.Content)
	if len(done.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, len(done.ToolCalls))
		for i, c := range done.ToolCalls {
			c.Arguments = l.r.Show(c.Arguments)
			out.ToolCalls[i] = c
		}
	}
	return &out
}

func (l redactingLLM) Complete(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int) (*Completion, error) {
	done, err := l.inner.Complete(ctx, l.hide(messages), tools, maxTokens)
	if err != nil || done == nil {
		return done, err
	}
	return l.show(done), nil
}
