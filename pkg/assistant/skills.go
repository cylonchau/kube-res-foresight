package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A skill is a command the user types in the chat ("/report"): a job done in one step. Some need no language model at all (the report and
// the export are made from the stored facts, so they work when the model is down or not set up yet); the others are a question the
// platform words well for the user ("/attention": which modules need a look and why) and the model answers with its tools.

// Skill arguments.
const (
	ArgNone     = ""        // takes no argument
	ArgModule   = "module"  // takes the name of a module
	ArgModuleOr = "module?" // may take the name of a module
)

// SkillInfo is what the user interface needs to offer a skill; its name and description in words are the interface's own.
type SkillInfo struct {
	Name       string `json:"name"`
	NeedsModel bool   `json:"needs_model"`
	Arg        string `json:"arg"`
}

type skill struct {
	info SkillInfo
	// direct makes the reply from the stored facts, without a language model.
	direct func(ctx context.Context, s *Service, lang string) (*Reply, error)
	// prompt is the question put to the language model; arg is what the user typed after the command, cleaned.
	prompt func(arg string) string
}

// reportHours is the window of the report and the export a skill makes.
const reportHours = 24

func about(arg, what string) string {
	if arg == "" {
		return what
	}
	return fmt.Sprintf("%s (module: %q)", what, arg)
}

var skills = []skill{
	{
		info: SkillInfo{Name: "report"},
		direct: func(ctx context.Context, s *Service, lang string) (*Reply, error) {
			r, err := s.Status(ctx, reportHours)
			if err != nil {
				return nil, err
			}
			return &Reply{Content: r.Markdown(lang), Tools: []ToolTrace{{Name: "get_service_status", OK: true}},
				Attachments: []Attachment{ExportAttachment("markdown", r.Hours, lang, s.now()), ExportAttachment("json", r.Hours, "", s.now())}}, nil
		},
	},
	{
		info: SkillInfo{Name: "export"},
		direct: func(ctx context.Context, s *Service, lang string) (*Reply, error) {
			r, err := s.Status(ctx, reportHours)
			if err != nil {
				return nil, err
			}
			text := fmt.Sprintf("The service status of the last %d hours is ready: %d modules, %d scale-ups, %d scale-downs, %d failed forecast runs. Download it below.", r.Hours, r.Modules.Total, r.Activity.ScaleUps, r.Activity.ScaleDowns, r.Activity.FailedRuns)
			if lang == "zh" {
				text = fmt.Sprintf("近 %d 小时的服务运行状态已生成：%d 个模块，扩容 %d 次，缩容 %d 次，失败的预测运行 %d 次。可在下面下载。", r.Hours, r.Modules.Total, r.Activity.ScaleUps, r.Activity.ScaleDowns, r.Activity.FailedRuns)
			}
			return &Reply{Content: text, Tools: []ToolTrace{{Name: "export_status_report", OK: true}},
				Attachments: []Attachment{ExportAttachment("markdown", r.Hours, lang, s.now()), ExportAttachment("json", r.Hours, "", s.now())}}, nil
		},
	},
	{
		info: SkillInfo{Name: "attention", NeedsModel: true},
		prompt: func(string) string {
			return "Which modules need a look right now, and why? Use get_service_status. For each module with a flag, say in plain words what the flag means, the likely cause, and the first thing the administrator should check. If no module is flagged, say the service is healthy, and mention anything else that is off (failed forecast runs, failed background tasks, late operator reports)."
		},
	},
	{
		info: SkillInfo{Name: "outage", NeedsModel: true},
		prompt: func(string) string {
			return "Check whether the operator and the platform lost contact recently. Use get_service_status for when the operator last read from the platform and last reported, and for the late reports (kept while the platform was away); then list_scaling_actions for the last 24 hours to see what the operator did meanwhile: which modules were using a cached decision, holding, degraded or recovering, whether any scaling happened in those states, and whether reports arrived late. Give a short verdict first (no outage seen, or an outage seen and how long), then the evidence."
		},
	},
	{
		info: SkillInfo{Name: "actions", NeedsModel: true, Arg: ArgModuleOr},
		prompt: func(arg string) string {
			return about(arg, "Summarise the latest scaling actions the operator reported.") + " Use list_scaling_actions. Count the scale-ups, scale-downs, held and failed ones; point out the module with the most changes and anything unusual (conflicts, errors, actions taken on a fallback or on a cached decision)."
		},
	},
	{
		info: SkillInfo{Name: "effect", NeedsModel: true, Arg: ArgModule},
		prompt: func(arg string) string {
			return about(arg, "Explain the effect report of a module in plain words.") + " Use get_module_effect_report. Cover accuracy, whether scale-ups were in time, wasted or missing capacity (Pod-hours and the capacity SLO) and how steady the replicas were. End with a verdict (good, or needs tuning) and one concrete suggestion."
		},
	},
	{
		info: SkillInfo{Name: "admission", NeedsModel: true},
		prompt: func(string) string {
			return "Summarise the recent admission records. Use list_admissions. How many requests were applied and how many refused, the main reasons for the refusals, and any module whose requests were changed much compared with what the Pods asked for."
		},
	},
}

func skillInfos() []SkillInfo {
	out := make([]SkillInfo, 0, len(skills))
	for _, sk := range skills {
		out = append(out, sk.info)
	}
	return out
}

// cleanArg keeps what a user typed after a command to a short single line of printable characters: it is put into a question, so it
// cannot carry a line break or a control character that would start a new instruction.
func cleanArg(arg string) string {
	arg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, arg)
	arg = strings.Join(strings.Fields(arg), " ")
	if utf8.RuneCountInString(arg) > 100 {
		arg = string([]rune(arg)[:100])
	}
	return arg
}

// RunSkill runs the skill called name with what the user typed after it. It needs the assistant to be on; a skill that needs the
// language model needs one to be set up, and one that does not, does not.
func (s *Service) RunSkill(ctx context.Context, name, arg, lang string) (*Reply, error) {
	var sk *skill
	for i := range skills {
		if skills[i].info.Name == name {
			sk = &skills[i]
		}
	}
	if sk == nil {
		return nil, RequestError{Msg: fmt.Sprintf("there is no skill named %q", name)}
	}
	arg = cleanArg(arg)
	switch {
	case sk.info.Arg == ArgNone && arg != "":
		return nil, RequestError{Msg: fmt.Sprintf("/%s takes no argument", name)}
	case sk.info.Arg == ArgModule && arg == "":
		return nil, RequestError{Msg: fmt.Sprintf("/%s needs the name of a module", name)}
	}
	if sk.direct != nil {
		enabled, err := Enabled(ctx, s.Config)
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, ErrNotReady{Reason: ReasonDisabled, Problem: "the assistant is switched off (system parameter assistant.enabled)"}
		}
		return sk.direct(ctx, s, lang)
	}
	conn, err := s.ready(ctx)
	if err != nil {
		return nil, err
	}
	return s.answer(ctx, *conn, []Turn{{Role: "user", Content: sk.prompt(arg)}}, lang)
}

// ProbeResult is the answer of a language model to the test of a connection.
type ProbeResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Message   string `json:"message"`
}

// Probe asks the connection one small question, with a tool offered the way the assistant offers them: it tells whether the address
// answers, the key is accepted, the model exists, and the service takes tools. What is tested is the connection as it is given, not
// the one that is stored.
func (s *Service) Probe(ctx context.Context, conn Connection) ProbeResult {
	timeout := conn.Timeout
	if timeout <= 0 || timeout > time.Minute {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	newLLM := s.NewLLM
	if newLLM == nil {
		newLLM = func(c Connection) LLM { return NewOpenAI(c.Endpoint, c.APIKey, c.Model, c.Timeout) }
	}
	ping := ToolSpec{Name: "ping", Description: "Does nothing; offered only to check that the service accepts tools.", Parameters: object(map[string]any{})}
	started := s.now()
	_, err := newLLM(conn).Complete(ctx, []Message{{Role: "user", Content: "Reply with the single word: ok"}}, []ToolSpec{ping}, 16)
	took := s.now().Sub(started).Milliseconds()
	if err != nil {
		return ProbeResult{LatencyMS: took, Message: err.Error()}
	}
	return ProbeResult{OK: true, LatencyMS: took, Message: "ok"}
}
