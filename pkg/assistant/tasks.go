package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/job"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/email"
	"github.com/cylonchau/kube-res-foresight/pkg/timezone"
)

// AI scheduled tasks are a framework: a task is a prompt and a schedule. At the time, the platform asks the language model the prompt (with
// the same read-only tools as the chat), keeps the answer as a run of the task, and mails it to the recipients. Nothing in the platform
// knows what a task is for ("every Monday, mail me the usage status of all modules" is one prompt, one schedule and one address); the work is
// the language model's, the timing the task center's (the job ai.schedule finds the tasks that are due and queues a job ai.run for each,
// which gives a run the retries, the back-off and the audit of every job), and the mail the notification framework's (the channel email).

// The limits of the tasks, which keep a question that is asked over and over from becoming a bill, or a prompt that spreads from becoming a
// flood.
const (
	// MinTaskInterval is the shortest time between two runs of a task.
	MinTaskInterval = time.Hour
	// MaxTasksPerOwner is how many tasks one user may have.
	MaxTasksPerOwner = 20
	maxTaskPrompt    = 2000
	maxRecipients    = 10
	keepRuns         = 30
	maxOutput        = 64 << 10
	// scheduleCheckRuns is how many runs ahead the schedule is looked at to see that they are far enough apart.
	scheduleCheckRuns = 8
)

// Owner is the user a task belongs to: the one who asked the chat to make it, or who made it on the page.
type Owner struct {
	ID   uint
	Name string
}

type ownerKey struct{}

// WithOwner puts the signed-in user in the context. The tools that make and manage tasks exist only for a question that has one: a
// scheduled run has none, so a task cannot make more tasks.
func WithOwner(ctx context.Context, o Owner) context.Context {
	return context.WithValue(ctx, ownerKey{}, o)
}

func ownerOf(ctx context.Context) (Owner, bool) {
	o, ok := ctx.Value(ownerKey{}).(Owner)
	return o, ok && o.ID != 0
}

// NextRun is the first time after from at which the schedule fires, in the zone of the platform.
func NextRun(schedule string, from time.Time) (time.Time, error) {
	s, err := job.ParseCron(schedule)
	if err != nil {
		return time.Time{}, fmt.Errorf("the schedule %q is not valid: %w", schedule, err)
	}
	next := s.Next(from.In(timezone.Location()))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("the schedule %q never fires", schedule)
	}
	return next, nil
}

// CheckSchedule says whether a schedule is valid and keeps the runs at least MinTaskInterval apart (looking at the next few).
func CheckSchedule(schedule string, now time.Time) error {
	at, err := NextRun(schedule, now)
	if err != nil {
		return err
	}
	for i := 0; i < scheduleCheckRuns; i++ {
		next, err := NextRun(schedule, at)
		if err != nil {
			return err
		}
		if next.Sub(at) < MinTaskInterval {
			return fmt.Errorf("the schedule %q runs more often than once an hour: a task runs at most once an hour", schedule)
		}
		at = next
	}
	return nil
}

// TaskInput is what a form, or the chat, gives for a task.
type TaskInput struct {
	Name       string
	Prompt     string
	Schedule   string
	Lang       string
	Enabled    bool
	LLMID      uint
	Recipients []string
}

// ParseRecipients reads the stored list of recipients.
func ParseRecipients(stored string) []string {
	var out []string
	if stored != "" {
		_ = json.Unmarshal([]byte(stored), &out)
	}
	return out
}

// ApplyTask checks the input and writes it into the row.
func ApplyTask(row *model.AssistantTask, in TaskInput, now time.Time) error {
	name, prompt := strings.TrimSpace(in.Name), strings.TrimSpace(in.Prompt)
	lang := in.Lang
	if lang == "" {
		lang = "zh"
	}
	switch {
	case name == "" || utf8.RuneCountInString(name) > 100:
		return errors.New("the name is required and at most 100 characters")
	case prompt == "":
		return errors.New("the prompt is required")
	case utf8.RuneCountInString(prompt) > maxTaskPrompt:
		return fmt.Errorf("the prompt is at most %d characters", maxTaskPrompt)
	case lang != "zh" && lang != "en":
		return errors.New("the language is zh or en")
	case len(in.Recipients) > maxRecipients:
		return fmt.Errorf("at most %d recipients", maxRecipients)
	}
	if err := CheckSchedule(strings.TrimSpace(in.Schedule), now); err != nil {
		return err
	}
	seen := map[string]bool{}
	recipients := make([]string, 0, len(in.Recipients))
	for _, r := range in.Recipients {
		addr, err := email.ValidAddress(r)
		if err != nil {
			return err
		}
		if !seen[strings.ToLower(addr)] {
			seen[strings.ToLower(addr)] = true
			recipients = append(recipients, addr)
		}
	}
	stored, _ := json.Marshal(recipients)
	row.Name, row.Prompt, row.Schedule, row.Lang, row.Enabled, row.LLMID = name, prompt, strings.TrimSpace(in.Schedule), lang, in.Enabled, in.LLMID
	row.Recipients = string(stored)
	return nil
}

// SaveTask checks and stores a task. The owner and the source are those of a new task; the next run is worked out when the task is new, or its
// schedule changed, or it was switched on.
func (s *Service) SaveTask(ctx context.Context, row *model.AssistantTask, in TaskInput, owner Owner, source string) error {
	fresh := row.ID == 0
	before, wasOn := row.Schedule, row.Enabled
	if err := ApplyTask(row, in, s.now()); err != nil {
		return err
	}
	if row.LLMID != 0 {
		if _, err := s.Connections.Get(ctx, row.LLMID); err != nil {
			return errors.New("the language model chosen for the task does not exist")
		}
	}
	if fresh {
		n, err := s.Tasks.CountByOwner(ctx, owner.ID)
		if err != nil {
			return err
		}
		if n >= MaxTasksPerOwner {
			return fmt.Errorf("a user may have %d scheduled tasks: delete one first", MaxTasksPerOwner)
		}
		row.OwnerID, row.OwnerName, row.Source = owner.ID, owner.Name, source
	}
	if fresh || row.Schedule != before || (row.Enabled && !wasOn) || row.NextRunAt == nil {
		next, err := NextRun(row.Schedule, s.now())
		if err != nil {
			return err
		}
		row.NextRunAt = &next
	}
	return s.Tasks.Save(ctx, row)
}

// JobQueue is how the task center is asked to run a task.
type JobQueue interface {
	// Enqueue queues a job that runs the task; runAt is the time the schedule fired, which makes the job unique.
	Enqueue(ctx context.Context, taskID uint, name string, runAt time.Time) error
	// HasOpen says whether a run of the task is still waiting or running.
	HasOpen(ctx context.Context, taskID uint) (bool, error)
}

// ScheduleDue queues a run for every task that is due and works out its next time. A task whose schedule fired while the platform was away
// runs once, not once for every time it missed; one whose last run is still going waits for the next time.
func (s *Service) ScheduleDue(ctx context.Context, q JobQueue) (queued int, err error) {
	now := s.now()
	due, err := s.Tasks.Due(ctx, now, 200)
	if err != nil {
		return 0, err
	}
	for _, t := range due {
		next, nerr := NextRun(t.Schedule, now)
		if nerr != nil {
			_ = s.Tasks.RecordResult(ctx, t.ID, now, model.AIRunFailed, nerr.Error())
			_ = s.Tasks.SetNextRun(ctx, t.ID, now.Add(MinTaskInterval))
			continue
		}
		if t.NextRunAt == nil {
			// a task that was never given its time: it starts from the next time of the schedule
			if err := s.Tasks.SetNextRun(ctx, t.ID, next); err != nil {
				return queued, err
			}
			continue
		}
		open, herr := q.HasOpen(ctx, t.ID)
		if herr != nil {
			return queued, herr
		}
		if !open {
			if err := q.Enqueue(ctx, t.ID, t.Name, *t.NextRunAt); err != nil {
				return queued, err
			}
			queued++
		}
		if err := s.Tasks.SetNextRun(ctx, t.ID, next); err != nil {
			return queued, err
		}
	}
	return queued, nil
}

// taskQuestion is what the model is asked at a run: the prompt, with what the answer is for.
func taskQuestion(t *model.AssistantTask) string {
	return "This question is asked on a schedule, by the task \"" + t.Name + "\". Its answer is kept and mailed to its owner, who is not here to be asked anything: " +
		"write the complete report in Markdown, state the figures with the time they are from, and do not ask questions back.\n\n" + t.Prompt
}

func clipOutput(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return clip(s, maxOutput) + "\n\n… [cut]"
}

func failureText(err error) string { return clip(err.Error(), 480) }

// RunTask runs a task now: it asks the model, keeps the answer as a run and mails it to the recipients that have not had it. jobID is the job
// of the task center that runs it (a retry of the job continues its run: the model is not asked again, and nobody is mailed twice); 0 when
// there is none. An error is returned for what a retry can mend (the model or the mail server did not answer); a task that cannot run for
// want of setup is recorded as failed and not retried.
func (s *Service) RunTask(ctx context.Context, taskID, jobID uint, trigger string) error {
	t, err := s.Tasks.Get(ctx, taskID)
	if err != nil {
		return nil // the task was deleted after the run was queued
	}
	run, err := s.Tasks.RunByJob(ctx, jobID)
	if err != nil {
		return err
	}
	if run == nil {
		run = &model.AssistantTaskRun{TaskID: t.ID, JobID: jobID, Trigger: trigger, Status: model.AIRunRunning, StartedAt: s.now()}
	}
	run.Status, run.Error = model.AIRunRunning, ""
	if err := s.Tasks.SaveRun(ctx, run); err != nil {
		return err
	}
	finish := func(status, failure string, retry error) error {
		now := s.now()
		run.Status, run.Error, run.FinishedAt = status, clip(failure, 480), &now
		if err := s.Tasks.SaveRun(ctx, run); err != nil && retry == nil {
			retry = err
		}
		_ = s.Tasks.RecordResult(ctx, t.ID, now, status, failure)
		_ = s.Tasks.PruneRuns(ctx, t.ID, keepRuns)
		return retry
	}

	if run.Output == "" {
		conn, err := s.skillConnection(ctx, t.LLMID)
		if err != nil {
			return finish(model.AIRunFailed, err.Error(), nil) // not set up: a retry would not mend it
		}
		reply, err := s.answer(ctx, *conn, []Turn{{Role: "user", Content: taskQuestion(t)}}, t.Lang)
		if err != nil {
			var notReady ErrNotReady
			if errors.As(err, &notReady) {
				return finish(model.AIRunFailed, err.Error(), nil)
			}
			return finish(model.AIRunFailed, failureText(err), err)
		}
		run.Output = clipOutput(reply.Content)
		names := make([]string, 0, len(reply.Tools))
		for _, tr := range reply.Tools {
			names = append(names, tr.Name)
		}
		run.Tools = clip(strings.Join(names, ","), 480)
		if err := s.Tasks.SaveRun(ctx, run); err != nil {
			return err
		}
	}

	delivered := ParseRecipients(run.Delivered)
	have := map[string]bool{}
	for _, a := range delivered {
		have[strings.ToLower(a)] = true
	}
	var failed []string
	for _, to := range ParseRecipients(t.Recipients) {
		if have[strings.ToLower(to)] {
			continue
		}
		if err := s.deliver(ctx, t, run, to); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", to, err))
			continue
		}
		delivered = append(delivered, to)
		stored, _ := json.Marshal(delivered)
		run.Delivered = string(stored)
		_ = s.Tasks.SaveRun(ctx, run)
	}
	if len(failed) > 0 {
		run.DeliveryError = clip(strings.Join(failed, "; "), 480)
		return finish(model.AIRunFailed, "the report was made but could not be mailed to everyone: "+run.DeliveryError, errors.New(run.DeliveryError))
	}
	run.DeliveryError = ""
	return finish(model.AIRunSuccess, "", nil)
}

// deliver mails the report of a run to one recipient through the channel email of the notification framework.
func (s *Service) deliver(ctx context.Context, t *model.AssistantTask, run *model.AssistantTaskRun, to string) error {
	ch, ok := notify.Get(email.Channel)
	if !ok {
		return errors.New("the e-mail channel is not available in this process")
	}
	zone := timezone.Location()
	footer := fmt.Sprintf("\n\n--\n%s · %s · kube-res-foresight", t.Name, run.StartedAt.In(zone).Format("2006-01-02 15:04 MST"))
	return ch.Send(ctx, to, notify.Message{Title: "[kube-res-foresight] " + t.Name, Content: run.Output + footer})
}

// ---- the tools of a question that has a signed-in user: make, list and delete the tasks of that user

func listOwn(id uint) rdb.TaskFilter { return rdb.TaskFilter{OwnerID: id} }

func maskAddress(a string) string {
	at := strings.LastIndex(a, "@")
	if at <= 1 {
		return a
	}
	return a[:1] + "***" + a[at:]
}

// UserEmails are the addresses of a user the reports of their tasks are mailed to: the one of the account and the e-mail contacts.
func (s *Service) UserEmails(ctx context.Context, userID uint) ([]string, error) {
	var user model.User
	if err := s.DB.WithContext(ctx).Select("id", "email").Where("id = ?", userID).Limit(1).Find(&user).Error; err != nil {
		return nil, err
	}
	var contacts []model.UserContact
	if err := s.DB.WithContext(ctx).Where("user_id = ? AND type = ?", userID, "email").Find(&contacts).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range append([]string{user.Email}, func() []string {
		var v []string
		for _, c := range contacts {
			v = append(v, c.Value)
		}
		return v
	}()...) {
		if addr, err := email.ValidAddress(a); err == nil && !seen[strings.ToLower(addr)] {
			seen[strings.ToLower(addr)] = true
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out, nil
}

type taskView struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Schedule   string     `json:"schedule"`
	Enabled    bool       `json:"enabled"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	Recipients []string   `json:"recipients"`
	Prompt     string     `json:"prompt"`
}

func viewOf(t *model.AssistantTask) taskView {
	to := ParseRecipients(t.Recipients)
	masked := make([]string, 0, len(to))
	for _, a := range to {
		masked = append(masked, maskAddress(a))
	}
	return taskView{ID: t.ID, Name: t.Name, Schedule: t.Schedule, Enabled: t.Enabled, NextRunAt: t.NextRunAt, LastRunAt: t.LastRunAt,
		LastStatus: t.LastStatus, LastError: t.LastError, Recipients: masked, Prompt: clip(t.Prompt, 300)}
}

// OwnTasks lists the tasks of the user in the context.
func (s *Service) OwnTasks(ctx context.Context) ([]taskView, error) {
	o, ok := ownerOf(ctx)
	if !ok {
		return nil, RequestError{Msg: "scheduled tasks belong to a signed-in user"}
	}
	list, _, err := s.Tasks.List(ctx, listOwn(o.ID), 1, MaxTasksPerOwner)
	if err != nil {
		return nil, err
	}
	out := make([]taskView, 0, len(list))
	for _, t := range list {
		out = append(out, viewOf(t))
	}
	return out, nil
}

func (s *Service) taskTools() []tool {
	return []tool{
		{
			spec: ToolSpec{Name: "create_scheduled_task", Description: "Make a scheduled task for the user: on the schedule the platform asks you the prompt (with the same read-only tools) and mails the answer to the user's own e-mail address. Use it ONLY when the user clearly asks for something to be done regularly or sent to them regularly, and only after the what and the when are clear (ask if they are not). The report cannot be sent to anybody else, and a task runs at most once an hour. After it is made, tell the user in plain words: what it will do, when it runs next, and that they can see or delete it with /tasks or in the menu AI configuration.",
				Parameters: object(map[string]any{
					"name":     map[string]any{"type": "string", "description": "A short name for the task, for example: Weekly usage status."},
					"schedule": map[string]any{"type": "string", "description": "When it runs, in the time zone of the platform (UTC+8): a cron expression of 5 fields \"minute hour day-of-month month day-of-week\", for example \"0 9 * * 1\" is every Monday at 09:00, \"0 8 * * *\" is every day at 08:00, \"0 9 1 * *\" is the 1st of every month at 09:00; or \"@every 24h\". Not more often than once an hour."},
					"prompt":   map[string]any{"type": "string", "description": "What to ask at each run, written as a complete instruction that stands on its own (what to report, over which time, in what form), for example: Report the usage status of all modules over the last 7 days, the modules that need attention, and the scaling actions."},
					"language": map[string]any{"type": "string", "enum": []string{"zh", "en"}, "description": "The language of the report; the language of the user by default."},
				}, "name", "schedule", "prompt")},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				o, _ := ownerOf(ctx)
				var a struct{ Name, Schedule, Prompt, Language string }
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				mails, err := s.UserEmails(ctx, o.ID)
				if err != nil {
					return toolResult{}, err
				}
				if len(mails) == 0 {
					return toolResult{}, errors.New("the user has no e-mail address: they must set one in the user center first, then ask again")
				}
				row := &model.AssistantTask{}
				if err := s.SaveTask(ctx, row, TaskInput{Name: a.Name, Prompt: a.Prompt, Schedule: a.Schedule, Lang: a.Language, Enabled: true, Recipients: mails}, o, model.AITaskFromChat); err != nil {
					return toolResult{}, err
				}
				return toolResult{Data: map[string]any{"created": true, "task": viewOf(row), "time_zone": timezone.Location().String(),
					"note": "The report is mailed to the user's own address; the mail channel must be set up by an administrator (system parameters notify.email.*), or the runs fail and say so."}}, nil
			},
		},
		{
			spec: ToolSpec{Name: "list_scheduled_tasks", Description: "The scheduled tasks of the user: name, schedule, whether it is on, when it runs next, how the last run went.",
				Parameters: object(map[string]any{})},
			run: func(ctx context.Context, _ json.RawMessage) (toolResult, error) {
				v, err := s.OwnTasks(ctx)
				return toolResult{Data: v}, err
			},
		},
		{
			spec: ToolSpec{Name: "delete_scheduled_task", Description: "Delete one of the user's own scheduled tasks, by the id list_scheduled_tasks gives. Only when the user asks for it.",
				Parameters: object(map[string]any{"id": map[string]any{"type": "integer"}}, "id")},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				o, _ := ownerOf(ctx)
				var a struct{ ID uint }
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				n, err := s.Tasks.Delete(ctx, []uint{a.ID}, o.ID)
				if err != nil {
					return toolResult{}, err
				}
				if n == 0 {
					return toolResult{}, errors.New("the user has no task with this id: call list_scheduled_tasks")
				}
				return toolResult{Data: map[string]any{"deleted": true, "id": a.ID}}, nil
			},
		},
	}
}
