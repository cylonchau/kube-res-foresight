package assistant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/timezone"
)

// A skill is a command the user types in the chat ("/report"): a job done in one step. The built-in ones are in the code: some need no
// language model at all (the report and the export are made from the stored facts, so they work when the model is down or not set up),
// the others are a question the platform words well for the user ("/attention": which modules need a look and why), answered by the model
// with its tools. An administrator adds their own (a prompt with "{{args}}" for what the user types after the command) in the menu
// AI configuration > Skill management, and chooses which model each skill asks; the default model answers the ones that choose none.

// Skill arguments, as the interface is told them.
const (
	ArgNone     = ""        // takes no argument
	ArgModule   = "module"  // needs the name of a module
	ArgModuleOr = "module?" // may take the name of a module
	ArgText     = "text"    // a custom skill: needs a text
	ArgTextOr   = "text?"   // a custom skill: may take a text
)

// SkillInfo is what the user interface needs to offer a skill. The words of a built-in skill are the interface's own, by name; those of a
// custom one are here.
type SkillInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Builtin     bool   `json:"builtin"`
	NeedsModel  bool   `json:"needs_model"`
	Arg         string `json:"arg"`
	ArgHint     string `json:"arg_hint,omitempty"`
}

// SkillView is a skill as the management page shows it: what it is, and what an administrator set about it.
type SkillView struct {
	SkillInfo
	ID      uint   `json:"id"`
	Enabled bool   `json:"enabled"`
	LLMID   uint   `json:"llm_id"`
	Prompt  string `json:"prompt"`
	// Direct skills are made from the stored facts and ask no model.
	Direct    bool      `json:"direct"`
	UpdatedAt time.Time `json:"updated_at"`
}

type builtin struct {
	info SkillInfo
	// direct makes the reply from the stored facts, without a language model.
	direct func(ctx context.Context, s *Service, lang string) (*Reply, error)
	// prompt is the question put to the language model; arg is what the user typed after the command, cleaned.
	prompt func(arg string) string
	// title and description are the words of the skill in English, kept in the row so a list is readable without the interface
	title, description string
}

// reportHours is the window of the report and the export a skill makes.
const reportHours = 24

func about(arg, what string) string {
	if arg == "" {
		return what
	}
	return fmt.Sprintf("%s (module: %q)", what, arg)
}

var builtins = []builtin{
	{
		info:  SkillInfo{Name: "report", Builtin: true},
		title: "Usage report", description: "The usage report of the last 24 hours, made at once from the stored facts (no language model), with the files to keep.",
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
		info:  SkillInfo{Name: "export", Builtin: true},
		title: "Export status", description: "Export the service status as files (a document and JSON), without a language model.",
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
		info:  SkillInfo{Name: "tasks", Builtin: true},
		title: "My scheduled tasks", description: "The AI scheduled tasks of the user: schedule, next run, how the last run went, where the report is mailed.",
		direct: func(ctx context.Context, s *Service, lang string) (*Reply, error) {
			list, err := s.OwnTasks(ctx)
			if err != nil {
				return nil, err
			}
			head, none := "| 名称 | 计划 | 状态 | 下次运行 | 上次结果 | 收件人 |\n|---|---|---|---|---|---|\n", "你还没有 AI 定时任务。对我说「每周一早上 9 点把所有模块的使用情况发到我的邮箱」就能创建一个。"
			if lang == "en" {
				head, none = "| Name | Schedule | State | Next run | Last result | Recipients |\n|---|---|---|---|---|---|\n", "You have no AI scheduled tasks. Tell me \"every Monday at 9 mail me the usage of all modules\" to make one."
			}
			if len(list) == 0 {
				return &Reply{Content: none, Tools: []ToolTrace{}, Attachments: []Attachment{}}, nil
			}
			zone := timezone.Location()
			var b strings.Builder
			b.WriteString(head)
			for _, t := range list {
				next, last := "-", t.LastStatus
				if t.NextRunAt != nil {
					next = t.NextRunAt.In(zone).Format("2006-01-02 15:04")
				}
				if last == "" {
					last = "-"
				}
				if t.LastError != "" {
					last += ": " + clip(strings.ReplaceAll(t.LastError, "|", "/"), 80)
				}
				state := "on"
				if !t.Enabled {
					state = "off"
				}
				fmt.Fprintf(&b, "| %s (#%d) | `%s` | %s | %s | %s | %s |\n", strings.ReplaceAll(t.Name, "|", "/"), t.ID, t.Schedule, state, next, last, strings.Join(t.Recipients, ", "))
			}
			return &Reply{Content: b.String(), Tools: []ToolTrace{}, Attachments: []Attachment{}}, nil
		},
	},
	{
		info:  SkillInfo{Name: "attention", Builtin: true, NeedsModel: true},
		title: "Modules that need a look", description: "Which modules need a look, why, and what to check first.",
		prompt: func(string) string {
			return "Which modules need a look right now, and why? Use get_service_status. For each module with a flag, say in plain words what the flag means, the likely cause, and the first thing the administrator should check. If no module is flagged, say the service is healthy, and mention anything else that is off (failed forecast runs, failed background tasks, late operator reports)."
		},
	},
	{
		info:  SkillInfo{Name: "outage", Builtin: true, NeedsModel: true},
		title: "Outage check", description: "Whether the operator and the platform lost contact, and what the operator did meanwhile (late reports, cached, holding and degraded states).",
		prompt: func(string) string {
			return "Check whether the operator and the platform lost contact recently. Use get_service_status for when the operator last read from the platform and last reported, and for the late reports (kept while the platform was away); then list_scaling_actions for the last 24 hours to see what the operator did meanwhile: which modules were using a cached decision, holding, degraded or recovering, whether any scaling happened in those states, and whether reports arrived late. Give a short verdict first (no outage seen, or an outage seen and how long), then the evidence."
		},
	},
	{
		info:  SkillInfo{Name: "actions", Builtin: true, NeedsModel: true, Arg: ArgModuleOr, ArgHint: "module"},
		title: "Scaling actions", description: "A summary of the latest scaling actions, of all modules or of one.",
		prompt: func(arg string) string {
			return about(arg, "Summarise the latest scaling actions the operator reported.") + " Use list_scaling_actions. Count the scale-ups, scale-downs, held and failed ones; point out the module with the most changes and anything unusual (conflicts, errors, actions taken on a fallback or on a cached decision)."
		},
	},
	{
		info:  SkillInfo{Name: "effect", Builtin: true, NeedsModel: true, Arg: ArgModule, ArgHint: "module"},
		title: "Module effect", description: "Explain the effect report of a module: accuracy, timeliness, waste, capacity SLO, stability, with a verdict.",
		prompt: func(arg string) string {
			return about(arg, "Explain the effect report of a module in plain words.") + " Use get_module_effect_report. Cover accuracy, whether scale-ups were in time, wasted or missing capacity (Pod-hours and the capacity SLO) and how steady the replicas were. End with a verdict (good, or needs tuning) and one concrete suggestion."
		},
	},
	{
		info:  SkillInfo{Name: "admission", Builtin: true, NeedsModel: true},
		title: "Admission records", description: "A summary of the recent admission records: applied and refused, the reasons, the largest changes.",
		prompt: func(string) string {
			return "Summarise the recent admission records. Use list_admissions. How many requests were applied and how many refused, the main reasons for the refusals, and any module whose requests were changed much compared with what the Pods asked for."
		},
	},
}

func argMode(arg string) string {
	switch arg {
	case ArgModule, ArgText:
		return model.SkillArgRequired
	case ArgModuleOr, ArgTextOr:
		return model.SkillArgOptional
	}
	return model.SkillArgNone
}

// BuiltinRows are the rows of the built-in skills, for a database that has none yet.
func BuiltinRows() []*model.AssistantSkill {
	out := make([]*model.AssistantSkill, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, &model.AssistantSkill{Name: b.info.Name, Title: b.title, Description: b.description, Builtin: true,
			ArgMode: argMode(b.info.Arg), ArgHint: b.info.ArgHint, Enabled: true})
	}
	return out
}

// reservedSkillNames cannot be the name of a custom skill.
func reservedSkillNames() map[string]bool {
	out := map[string]bool{"help": true}
	for _, b := range builtins {
		out[b.info.Name] = true
	}
	return out
}

var skillName = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// SkillInput is what the form of a custom skill sends.
type SkillInput struct {
	Name, Title, Description, Prompt, ArgMode, ArgHint string
	LLMID                                              uint
	Enabled                                            bool
}

const maxPromptRunes = 4000

// ApplySkill checks the input of a custom skill and writes it into the row.
func ApplySkill(row *model.AssistantSkill, in SkillInput) error {
	name := strings.TrimSpace(in.Name)
	title, description, prompt, hint := strings.TrimSpace(in.Title), strings.TrimSpace(in.Description), strings.TrimSpace(in.Prompt), strings.TrimSpace(in.ArgHint)
	mode := in.ArgMode
	if mode == "" {
		mode = model.SkillArgNone
	}
	switch {
	case !skillName.MatchString(name):
		return errors.New("the command is 2 to 32 characters: lower-case letters, digits, - and _, starting with a letter")
	case reservedSkillNames()[name]:
		return fmt.Errorf("/%s is a command of the assistant itself", name)
	case utf8.RuneCountInString(title) > 100:
		return errors.New("the title is at most 100 characters")
	case utf8.RuneCountInString(description) > maxDescriptionRunes:
		return fmt.Errorf("the description is at most %d characters", maxDescriptionRunes)
	case prompt == "":
		return errors.New("the prompt is required")
	case utf8.RuneCountInString(prompt) > maxPromptRunes:
		return fmt.Errorf("the prompt is at most %d characters", maxPromptRunes)
	case mode != model.SkillArgNone && mode != model.SkillArgRequired && mode != model.SkillArgOptional:
		return errors.New("the argument is none, required or optional")
	case utf8.RuneCountInString(hint) > 50:
		return errors.New("the argument hint is at most 50 characters")
	}
	if title == "" {
		title = name
	}
	row.Name, row.Title, row.Description, row.Prompt, row.ArgMode, row.ArgHint = name, title, description, prompt, mode, hint
	row.LLMID, row.Enabled, row.Builtin = in.LLMID, in.Enabled, false
	return nil
}

func (s *Service) builtinRows(ctx context.Context) (map[string]*model.AssistantSkill, []*model.AssistantSkill, error) {
	rows, err := s.SkillStore.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]*model.AssistantSkill, len(rows))
	var custom []*model.AssistantSkill
	for _, r := range rows {
		if r.Builtin {
			byName[r.Name] = r
		} else {
			custom = append(custom, r)
		}
	}
	return byName, custom, nil
}

// Skills lists every skill, the built-in ones first, with what an administrator set about each: whether it is on and which model it asks.
// A built-in skill that has no row yet is on and asks the default model.
func (s *Service) Skills(ctx context.Context) ([]SkillView, error) {
	byName, custom, err := s.builtinRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SkillView, 0, len(builtins)+len(custom))
	for _, b := range builtins {
		v := SkillView{SkillInfo: b.info, Enabled: true, Direct: b.direct != nil}
		v.Title, v.Description = b.title, b.description
		if b.prompt != nil {
			v.Prompt = b.prompt("{{args}}")
		}
		if row := byName[b.info.Name]; row != nil {
			v.ID, v.Enabled, v.LLMID, v.UpdatedAt = row.ID, row.Enabled, row.LLMID, row.UpdatedAt
		}
		out = append(out, v)
	}
	for _, r := range custom {
		arg, needs := ArgNone, true
		switch r.ArgMode {
		case model.SkillArgRequired:
			arg = ArgText
		case model.SkillArgOptional:
			arg = ArgTextOr
		}
		out = append(out, SkillView{SkillInfo: SkillInfo{Name: r.Name, Title: r.Title, Description: r.Description, NeedsModel: needs, Arg: arg, ArgHint: r.ArgHint},
			ID: r.ID, Enabled: r.Enabled, LLMID: r.LLMID, Prompt: r.Prompt, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// EnsureBuiltinSkills gives the built-in skills their rows, so a management page can change them.
func (s *Service) EnsureBuiltinSkills(ctx context.Context) error {
	return s.SkillStore.EnsureBuiltin(ctx, BuiltinRows())
}

// cleanArg keeps what a user typed after a command to a short single line of printable characters: it is put into a question, so it
// cannot carry a line break or a control character that would start a new instruction.
func cleanArg(arg string, max int) string {
	arg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, arg)
	arg = strings.Join(strings.Fields(arg), " ")
	if utf8.RuneCountInString(arg) > max {
		arg = string([]rune(arg)[:max])
	}
	return arg
}

// skillConnection is the model a skill asks: the one it names, which has to be there and enabled, else the default.
func (s *Service) skillConnection(ctx context.Context, llmID uint) (*Connection, error) {
	if llmID == 0 {
		return s.ready(ctx)
	}
	enabled, err := Enabled(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrNotReady{Reason: ReasonDisabled, Problem: "the assistant is switched off (system parameter assistant.enabled)"}
	}
	row, err := s.Connections.Get(ctx, llmID)
	if err != nil || !row.Enabled {
		return nil, ErrNotReady{Reason: ReasonBadConnection, Problem: "the language model chosen for this skill is not available: enable it, or choose another in the menu Skill management"}
	}
	c := FromRow(row)
	if p := c.Problem(); p != "" {
		return nil, ErrNotReady{Reason: ReasonBadConnection, Problem: p}
	}
	return &c, nil
}

// RunSkill runs the skill called name with what the user typed after it. It needs the assistant to be on and the skill to be on; a skill
// that needs the language model needs one to be set up, and one that does not, does not.
func (s *Service) RunSkill(ctx context.Context, name, arg, lang string) (*Reply, error) {
	views, err := s.Skills(ctx)
	if err != nil {
		return nil, err
	}
	var v *SkillView
	for i := range views {
		if views[i].Name == name {
			v = &views[i]
		}
	}
	if v == nil {
		return nil, RequestError{Msg: fmt.Sprintf("there is no skill named %q", name)}
	}
	if !v.Enabled {
		return nil, RequestError{Msg: fmt.Sprintf("the skill /%s is switched off", name)}
	}
	max := 100
	if v.Arg == ArgText || v.Arg == ArgTextOr {
		max = 300
	}
	arg = cleanArg(arg, max)
	switch {
	case v.Arg == ArgNone && arg != "":
		return nil, RequestError{Msg: fmt.Sprintf("/%s takes no argument", name)}
	case (v.Arg == ArgModule || v.Arg == ArgText) && arg == "":
		what := "a text"
		if v.Arg == ArgModule {
			what = "the name of a module"
		}
		return nil, RequestError{Msg: fmt.Sprintf("/%s needs %s", name, what)}
	}

	if v.Direct {
		enabled, err := Enabled(ctx, s.Config)
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, ErrNotReady{Reason: ReasonDisabled, Problem: "the assistant is switched off (system parameter assistant.enabled)"}
		}
		for _, b := range builtins {
			if b.info.Name == name {
				return b.direct(ctx, s, lang)
			}
		}
	}
	conn, err := s.skillConnection(ctx, v.LLMID)
	if err != nil {
		return nil, err
	}
	var question string
	if v.Builtin {
		for _, b := range builtins {
			if b.info.Name == name {
				question = b.prompt(arg)
			}
		}
	} else {
		question = strings.ReplaceAll(v.Prompt, "{{args}}", arg)
		if arg != "" && !strings.Contains(v.Prompt, "{{args}}") {
			question += "\n\nArgument: " + arg
		}
	}
	return s.answer(ctx, *conn, []Turn{{Role: "user", Content: question}}, lang)
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
	ping := ToolSpec{Name: "ping", Description: "Does nothing; offered only to check that the service accepts tools.", Parameters: object(map[string]any{})}
	started := s.now()
	_, err := s.llmFor(conn).Complete(ctx, []Message{{Role: "user", Content: "Reply with the single word: ok"}}, []ToolSpec{ping}, 64)
	took := s.now().Sub(started).Milliseconds()
	if err != nil {
		return ProbeResult{LatencyMS: took, Message: err.Error()}
	}
	return ProbeResult{OK: true, LatencyMS: took, Message: "ok"}
}
