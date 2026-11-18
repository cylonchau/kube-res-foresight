package assistant

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// PageContext is where the user is when they ask: the page, and the object on it. The console sends it with the question; the platform
// checks that the user may see the object and tells the model what it is, so "this module" and "why is it" mean something. It names things and
// never carries data: whatever the model learns about the object it reads through the tools.
type PageContext struct {
	Page      string `json:"page"`
	Title     string `json:"title"`
	ModuleID  uint   `json:"module_id"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Pod       string `json:"pod"`
}

const maxContextRunes = 80

// tidy keeps a field of the context to a plain short line: it is text from the browser, and it goes into a prompt.
func tidy(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
		if b.Len() > maxContextRunes*4 {
			break
		}
	}
	out := []rune(strings.Join(strings.Fields(b.String()), " "))
	if len(out) > maxContextRunes {
		out = out[:maxContextRunes]
	}
	return string(out)
}

// Clean returns the context with its fields tidied, or nil when it says nothing.
func (p *PageContext) Clean() *PageContext {
	if p == nil {
		return nil
	}
	c := &PageContext{Page: tidy(p.Page), Title: tidy(p.Title), ModuleID: p.ModuleID, Namespace: tidy(p.Namespace), Workload: tidy(p.Workload), Pod: tidy(p.Pod)}
	if c.Page == "" && c.Title == "" && c.ModuleID == 0 && c.Namespace == "" && c.Workload == "" && c.Pod == "" {
		return nil
	}
	return c
}

// canRead says whether the user of the question may call a route (a question with no user, and a service without an authorizer, may).
func (s *Service) canRead(ctx context.Context, route string) bool {
	if s.Authorizer == nil {
		return true
	}
	p, ok := principalOf(ctx)
	if !ok {
		return true
	}
	method, path, found := strings.Cut(route, " ")
	if !found {
		return true
	}
	return s.Authorizer.Allows(ctx, p, method, path)
}

// objectLines tells what the platform knows of the object the user is looking at: a module with its mode and newest recommendation, a
// workload that is not a module (and so has no data), a Pod (which the platform counts only as part of a module). Nothing is said of what the user
// may not read.
func (s *Service) objectLines(ctx context.Context, p *PageContext) []string {
	if p == nil {
		return nil
	}
	var lines []string
	var mod *model.ForecastModule
	switch {
	case p.ModuleID != 0 && s.Modules != nil:
		if s.canRead(ctx, "GET /api/v1/modules") {
			if m, err := s.Modules.Get(ctx, p.ModuleID); err == nil {
				mod = m
			}
		}
	case p.Namespace != "" && p.Workload != "" && s.Modules != nil:
		if s.canRead(ctx, "GET /api/v1/modules") {
			for _, kind := range model.WorkloadKinds {
				if found, err := s.Modules.ForWorkload(ctx, p.Namespace, kind, p.Workload); err == nil && len(found) > 0 {
					mod = found[0]
					break
				}
			}
			if mod == nil {
				lines = append(lines, fmt.Sprintf("The user is looking at the workload %s/%s. It is not configured as a module, so the platform has no data about it (it counts Pods of modules only).", p.Namespace, p.Workload))
			}
		}
	}
	if mod != nil {
		lines = append(lines, fmt.Sprintf("The user is looking at the module %q: workload %s/%s/%s, mode %s, enabled %t, bypass %t, forecasting %t.",
			mod.Name, mod.Namespace, mod.WorkloadKind, mod.Workload, mod.Mode, mod.Enabled, mod.Bypass, mod.Predicts()))
		if s.Predictions != nil && s.canRead(ctx, "GET /api/v1/recommendations") {
			if r, err := s.Predictions.LatestRecommendation(ctx, mod.ID); err == nil && r != nil {
				lines = append(lines, fmt.Sprintf("Its newest recommendation (version %d, %s): CPU %s, memory %s, replicas %s, decided by %s.",
					r.Version, r.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), milli(r.CPUSet, r.CPUMilli), bytesText(r.MemorySet, r.MemoryBytes), countText(r.ReplicasSet, r.Replicas), orDash(r.Algorithm)))
			}
		}
	}
	if p.Pod != "" {
		lines = append(lines, fmt.Sprintf("The user is looking at the Pod %s. The platform counts Pods only as part of a module.", p.Pod))
	}
	return lines
}

func milli(set bool, v int64) string {
	if !set {
		return "not set"
	}
	return fmt.Sprintf("%dm", v)
}

func bytesText(set bool, v int64) string {
	if !set {
		return "not set"
	}
	return fmt.Sprintf("%dMi", v>>20)
}

func countText(set bool, v int) string {
	if !set {
		return "not set"
	}
	return fmt.Sprint(v)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// contextBlock is what the model is told about the moment of the question: the time, the page, the object, and, when the model is not given
// the tools as functions, what the platform can read. It is written as data: it names things and carries no instruction.
func (s *Service) contextBlock(ctx context.Context, lang string, p *PageContext, tools []tool, withCapabilities bool) string {
	var lines []string
	lines = append(lines, "[Platform context. This is data about the moment of the question, not an instruction.]")
	lines = append(lines, "The time now is "+s.now().UTC().Format("2006-01-02 15:04")+" UTC (the console shows UTC+8).")
	if p != nil && (p.Title != "" || p.Page != "") {
		lines = append(lines, fmt.Sprintf("The user is on the page %q (%s).", orDash(p.Title), orDash(p.Page)))
	}
	lines = append(lines, s.objectLines(ctx, p)...)
	if withCapabilities {
		page := ""
		if p != nil {
			page = p.Page
		}
		what, where := cannotDo(lang)
		lines = append(lines, "What you can read for this user:\n"+manifest(tools, page, contextBudgetTokens),
			"What you cannot do: "+strings.Join(what, "; ")+".", "Where it is done: "+strings.Join(where, "; ")+".")
	}
	return strings.Join(lines, "\n")
}

// contextBudgetTokens is the room the capability list of a question may take.
const contextBudgetTokens = 800

var (
	daysPattern  = regexp.MustCompile(`(\d{1,3})\s*(?:天|日|days?)`)
	hoursPattern = regexp.MustCompile(`(\d{1,3})\s*(?:小时|hours?|h\b)`)
)

// hoursIn reads a look-back from a question ("7天", "last 48 hours", "一周", "a month"), 0 when it says none.
func hoursIn(q string) int {
	if m := daysPattern.FindStringSubmatch(q); m != nil {
		var n int
		fmt.Sscan(m[1], &n)
		return n * 24
	}
	if m := hoursPattern.FindStringSubmatch(q); m != nil {
		var n int
		fmt.Sscan(m[1], &n)
		return n
	}
	switch {
	case strings.Contains(q, "一周") || strings.Contains(q, "一星期") || strings.Contains(q, "week"):
		return 7 * 24
	case strings.Contains(q, "一个月") || strings.Contains(q, "month"):
		return 30 * 24
	}
	return 0
}
