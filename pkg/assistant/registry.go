package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
)

// The registry is the one list of what the assistant can do. Every tool of it has the words that tell a person (and a model) what it is for,
// the route of the platform it reads through, and the pages where it matters most. The route is what the permission of the user is checked
// against: a person who may not read the effect reports does not get the assistant's effect reports either. The manifest the model is shown, the
// capability list of the question and describe_capabilities are all made from it, so a tool that is not in it cannot be offered, and one the user
// may not use is not offered to them.

// Principal is who asks: the user of a signed-in session, or a developer credential, with what the permission system needs to answer for them.
// A question with no principal (a scheduled run, which the platform makes by itself) is not filtered.
type Principal struct {
	// Name is the user name (what the permission policies are written for), or "developer:<key>" for a credential.
	Name string
	// Roles are the role codes of the user; Admin is whether one of them is the administrator, which the permission system lets through everywhere.
	Roles []string
	Admin bool
	// Developer is a scoped credential of a program: it may use only the permissions it was given, listed by their ids.
	Developer     bool
	PermissionIDs []uint
}

type principalKey struct{}

// WithPrincipal puts who asks in the context of a question.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalOf(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Authorizer says whether a principal may call a route of the platform. The platform's permission system answers; nil allows everything (a
// test).
type Authorizer interface {
	Allows(ctx context.Context, p Principal, method, path string) bool
}

// toolMeta is what the registry knows of each tool besides its definition.
type toolMeta struct {
	// summary is one line, in the words a person would use, of what the tool reads.
	summary string
	// api is the route the tool reads through, "METHOD /path" as the router names it (":id" for an id: the permission system matches the template,
	// for a user and for a developer credential); empty when it reads nothing of the platform (the list of tools).
	api string
	// pages are the route names of the pages where the tool matters most; they come first in the manifest of a question asked from there.
	pages []string
	// own marks a tool that changes something: only the scheduled tasks of the user themselves, never a setting, a module or a replica.
	own bool
}

var toolMetas = map[string]toolMeta{
	"describe_capabilities": {summary: "What this assistant can do for you, what it cannot do, and where to do those things"},
	"get_service_status": {summary: "How the service is running: modules by mode, which need a look and why, what the operator applied, failed runs, background tasks",
		api: "GET /api/v1/dashboard", pages: []string{"dashboard"}},
	"list_modules": {summary: "Every module (a workload the platform forecasts): name, namespace, workload, mode, enabled, bypass",
		api: "GET /api/v1/modules", pages: []string{"forecast-modules_modules"}},
	"get_module_effect_report": {summary: "How good the forecasts of one module were: accuracy, scale-ups in time, capacity wasted or missing, stability",
		api: "GET /api/v1/modules/:id/effect-report", pages: []string{"forecast-results_effect", "forecast-results_history"}},
	"get_module_recommendation": {summary: "The newest recommendation of one module: CPU, memory, replicas, who decided it, whether it fell back",
		api: "GET /api/v1/recommendations", pages: []string{"forecast-actions_recommendations", "forecast-modules_modules"}},
	"list_scaling_actions": {summary: "What the operator did: scale-ups and scale-downs with their results and reasons",
		api: "GET /api/v1/scaling-decisions", pages: []string{"forecast-actions_executions"}},
	"list_admissions": {summary: "The admission records: what Kyverno asked the platform and what was applied to the Pods",
		api: "GET /api/v1/admissions", pages: []string{"forecast-actions_admissions"}},
	"list_algorithms": {summary: "The forecast algorithms of the platform: which are enabled, with their weight and group",
		api: "GET /api/v1/algorithms", pages: []string{"forecast_algorithms"}},
	"export_status_report":  {summary: "Make the service status a file to download (Markdown or JSON)", api: "GET /api/v1/assistant/export"},
	"create_scheduled_task": {summary: "Make a scheduled task that mails you an answer on a schedule (your own tasks only)", api: "GET /api/v1/assistant/tasks", own: true},
	"list_scheduled_tasks":  {summary: "Your scheduled tasks", api: "GET /api/v1/assistant/tasks", own: true},
	"delete_scheduled_task": {summary: "Delete one of your scheduled tasks", api: "GET /api/v1/assistant/tasks", own: true},
}

// withMeta puts the registry's words on the tools.
func withMeta(set []tool) []tool {
	for i := range set {
		if m, ok := toolMetas[set[i].spec.Name]; ok {
			set[i].meta = m
		}
	}
	return set
}

// allowed says whether the user of the question may use the tool: its route is checked against their permissions. A question with no user (a
// scheduled run) and a service without an authorizer are not filtered.
func (s *Service) allowed(ctx context.Context, t tool) bool {
	if t.meta.api == "" || s.Authorizer == nil {
		return true
	}
	return s.canRead(ctx, t.meta.api)
}

// cannotDo is what the assistant never does, with the place that does it. It is the answer to "can you change ...?".
func cannotDo(lang string) (what []string, where []string) {
	if lang == "zh" {
		return []string{
			"修改任何设置、模块、算法、副本数或 Pod 资源（只读）",
			"运行预测、回测、采集，或让 operator 扩缩容",
			"直接查询集群或 Prometheus（只能通过工具读平台已经存下的数据，不能写任意 PromQL）",
			"查看没有配置成模块的工作负载的 Pod（平台只统计模块）",
		}, []string{
			"模块和固定周期：模块管理 → 模块列表",
			"算法的启用与参数：预测管理 → 算法管理",
			"系统参数、LDAP、邮件：系统管理 → 系统参数",
			"推荐与执行记录：推荐与执行",
		}
	}
	return []string{
		"change any setting, module, algorithm, replica count or Pod resource (read only)",
		"run a forecast, a backtest or a collection, or make the operator scale",
		"query the cluster or Prometheus directly (only what the platform stored, through the tools; no free PromQL)",
		"look at the Pods of a workload that is not configured as a module (the platform counts Pods of modules only)",
	}, []string{
		"modules and fixed periods: Module management > Module list",
		"algorithms, enabled or with parameters: Forecast management > Algorithm management",
		"system parameters, LDAP, mail: System > System parameters",
		"recommendations and what was executed: Recommendations and execution",
	}
}

// manifest lists what the user may ask for, one line each, the tools that matter on the page they are looking at first, within a budget of
// tokens (what does not fit is named as left out, and describe_capabilities lists all of it).
func manifest(tools []tool, page string, budget int) string {
	ordered := append([]tool(nil), tools...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := onPage(ordered[i], page), onPage(ordered[j], page)
		if a != b {
			return a
		}
		return ordered[i].spec.Name < ordered[j].spec.Name
	})
	var b strings.Builder
	used, left := 0, 0
	for _, t := range ordered {
		if t.meta.summary == "" {
			continue
		}
		line := "- " + t.spec.Name + ": " + t.meta.summary + "\n"
		if used+estimateTokens(line) > budget {
			left++
			continue
		}
		used += estimateTokens(line)
		b.WriteString(line)
	}
	if left > 0 {
		fmt.Fprintf(&b, "- (%d more: call describe_capabilities)\n", left)
	}
	return strings.TrimRight(b.String(), "\n")
}

func onPage(t tool, page string) bool {
	if page == "" {
		return false
	}
	for _, p := range t.meta.pages {
		if p == page {
			return true
		}
	}
	return false
}

// capabilityTool is describe_capabilities: what the assistant can do for this user, what it cannot, and where those things are done.
func (s *Service) capabilityTool(tools func() []tool) tool {
	return tool{
		spec: ToolSpec{Name: "describe_capabilities", Description: "What this assistant can do for the user you are talking to (the tools they may use, with what each reads), what it cannot do, and which page does what it cannot. Call it for any question about what the platform or the assistant can do, before you say that something is not possible.",
			Parameters: object(map[string]any{"lang": map[string]any{"type": "string", "enum": []string{"zh", "en"}, "description": "The language of the 'cannot' lists (default en)."}})},
		run: func(_ context.Context, args json.RawMessage) (toolResult, error) {
			var a struct{ Lang string }
			if err := parse(args, &a); err != nil {
				return toolResult{}, err
			}
			type item struct {
				Name    string   `json:"name"`
				Summary string   `json:"summary"`
				Pages   []string `json:"pages,omitempty"`
			}
			can := []item{}
			for _, t := range tools() {
				if t.meta.summary != "" {
					can = append(can, item{Name: t.spec.Name, Summary: t.meta.summary, Pages: t.meta.pages})
				}
			}
			what, where := cannotDo(a.Lang)
			return toolResult{Data: map[string]any{"can": can, "cannot": what, "where_to_do_it": where}}, nil
		},
	}
}

// moduleByName finds a module by its name for a tool.
func (s *Service) moduleByName(ctx context.Context, name string) (uint, error) {
	m, err := s.Modules.ByName(ctx, strings.TrimSpace(name))
	if err != nil {
		return 0, fmt.Errorf("no module is named %q: call list_modules for the names", name)
	}
	return m.ID, nil
}

// registryTools are the tools the registry adds to the original ones: the algorithms, the newest recommendation of a module.
func (s *Service) registryTools() []tool {
	return []tool{
		{
			spec: ToolSpec{Name: "list_algorithms", Description: "The forecast algorithms of the platform: for each, whether it is enabled, its weight, and its group. One setting for each algorithm applies to every module.",
				Parameters: object(map[string]any{})},
			run: func(ctx context.Context, _ json.RawMessage) (toolResult, error) {
				configs, err := s.Forecast.Algorithms.All(ctx)
				if err != nil {
					return toolResult{}, err
				}
				type row struct {
					ID       string  `json:"id"`
					Enabled  bool    `json:"enabled"`
					Weight   float64 `json:"weight"`
					Category string  `json:"category,omitempty"`
					Custom   bool    `json:"custom,omitempty"`
				}
				out := make([]row, 0, len(configs))
				for _, c := range configs {
					r := row{ID: c.AlgorithmID, Enabled: c.Enabled, Weight: c.Weight, Custom: c.Formula != "" || c.Base != ""}
					if def, ok := forecast.Lookup(c.AlgorithmID); ok {
						r.Category = def.Category
					}
					out = append(out, r)
				}
				return toolResult{Data: out}, nil
			},
		},
		{
			spec: ToolSpec{Name: "get_module_recommendation", Description: "The newest recommendation of one module: the Pod CPU and memory, the replicas, which algorithm decided it, until when it is valid, and whether it fell back to a default or was raised by the OOM protection.",
				Parameters: object(map[string]any{"module": moduleProp}, "module")},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct{ Module string }
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				id, err := s.moduleByName(ctx, a.Module)
				if err != nil {
					return toolResult{}, err
				}
				r, err := s.Predictions.LatestRecommendation(ctx, id)
				if err != nil {
					return toolResult{}, err
				}
				if r == nil {
					return toolResult{Data: map[string]any{"module": a.Module, "recommendation": nil, "note": "the platform has made no recommendation for this module yet"}}, nil
				}
				return toolResult{Data: map[string]any{
					"module": a.Module, "version": r.Version, "mode": r.Mode, "made_at": r.CreatedAt, "valid_until": r.ValidUntil,
					"cpu_milli": ifSet(r.CPUSet, r.CPUMilli), "memory_bytes": ifSet(r.MemorySet, r.MemoryBytes), "replicas": ifSet(r.ReplicasSet, int64(r.Replicas)),
					"cpu_source": r.CPUSource, "memory_source": r.MemorySource, "replicas_source": r.ReplicasSource, "algorithm": r.Algorithm,
					"degraded": r.Degraded, "oom_floor": r.OOMFloor, "spec_held": r.ResourceHeld,
				}}, nil
			},
		},
	}
}

// ifSet is the value of a field the recommendation set, and nil for one it did not (0 would read as a recommendation of zero).
func ifSet(set bool, v int64) any {
	if !set {
		return nil
	}
	return v
}
