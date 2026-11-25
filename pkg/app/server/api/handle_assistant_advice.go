package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The periodic resource advice: one AI task (source "advice") that every day reads the analysis of the resource dashboard of the modules that most need a look and writes a
// short report, which the resource dashboard shows. Anyone who may see the dashboard reads the report; making the task and running it now need the AI task permission.

// AdviceRunDTO is one run of the advice task as the dashboard shows it.
type AdviceRunDTO struct {
	ID         uint       `json:"id"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error,omitempty"`
	// Output is the report (only on the report, not on the last run); Tools are the tools the model read with.
	Output string `json:"output,omitempty"`
	Tools  string `json:"tools,omitempty"`
}

// AdviceStateDTO is what the dashboard shows of the advice: the task (nil when there is none), the newest report that was made, and the newest run of any outcome, so a run
// that failed is seen beside the report of the one before it.
type AdviceStateDTO struct {
	Task   *AssistantTaskDTO `json:"task"`
	Report *AdviceRunDTO     `json:"report"`
	Last   *AdviceRunDTO     `json:"last"`
}

// adviceTask is the advice task, or nil when there is none.
func (r *AssistantRouter) adviceTask(ctx context.Context) (*model.AssistantTask, error) {
	list, _, err := r.Service.Tasks.List(ctx, rdb.TaskFilter{Source: model.AITaskFromAdvice}, 1, 1)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list[0], nil
}

func (r *AssistantRouter) adviceState(ctx context.Context) (*AdviceStateDTO, error) {
	out := &AdviceStateDTO{}
	t, err := r.adviceTask(ctx)
	if err != nil || t == nil {
		return out, err
	}
	dto := taskDTO(t)
	out.Task = &dto
	runs, _, err := r.Service.Tasks.Runs(ctx, t.ID, 1, 20)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	for _, run := range runs { // newest first
		item := &AdviceRunDTO{ID: run.ID, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error, Tools: run.Tools}
		if out.Last == nil {
			out.Last = &AdviceRunDTO{ID: item.ID, Status: item.Status, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, Error: item.Error}
		}
		if run.Status == model.AIRunSuccess && run.Output != "" {
			item.Output = run.Output
			out.Report = item
			break
		}
	}
	return out, nil
}

// AdviceState serves GET /overprovision/ai-advice.
// @Summary The periodic AI advice of the resource dashboard
// @Tags Assistant
// @Router /api/v1/overprovision/ai-advice [get]
func (r *AssistantRouter) AdviceState(c *gin.Context) {
	out, err := r.adviceState(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// AdviceEnable serves POST /overprovision/ai-advice/enable {lang}: it makes the advice task (every day at 8, in the language asked) for the signed-in user, or switches the one
// that exists back on. There is one such task for the platform; the report is the same for everyone who sees the dashboard.
// @Summary Switch the periodic AI advice on
// @Tags Assistant
// @Router /api/v1/overprovision/ai-advice/enable [post]
func (r *AssistantRouter) AdviceEnable(c *gin.Context) {
	var req struct {
		Lang string `json:"lang"`
	}
	_ = c.ShouldBindJSON(&req)
	lang := "zh"
	if req.Lang == "en" || req.Lang == "en-US" {
		lang = "en"
	}
	owner, ok := ownerOfRequest(c)
	if !ok {
		query.Unauthorized(c, query.ErrUserContextMissing)
		return
	}
	ctx := c.Request.Context()
	row, err := r.adviceTask(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if row == nil {
		row = &model.AssistantTask{}
		in := assistant.TaskInput{Name: assistant.AdviceTaskName, Prompt: assistant.AdvicePrompt(lang), Schedule: assistant.DefaultAdviceSchedule, Lang: lang, Enabled: true}
		if err := r.Service.SaveTask(ctx, row, in, owner, model.AITaskFromAdvice); err != nil {
			query.BadRequest(c, err)
			return
		}
	} else if !row.Enabled {
		in := assistant.TaskInput{Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, Lang: row.Lang, Enabled: true, LLMID: row.LLMID, Recipients: assistant.ParseRecipients(row.Recipients)}
		if err := r.Service.SaveTask(ctx, row, in, owner, model.AITaskFromAdvice); err != nil {
			query.BadRequest(c, err)
			return
		}
	}
	out, err := r.adviceState(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// AdviceRun serves POST /overprovision/ai-advice/run: it queues a run of the advice task in the task center now.
// @Summary Run the periodic AI advice now
// @Tags Assistant
// @Router /api/v1/overprovision/ai-advice/run [post]
func (r *AssistantRouter) AdviceRun(c *gin.Context) {
	ctx := c.Request.Context()
	row, err := r.adviceTask(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if row == nil {
		query.BadRequest(c, errors.New("the periodic advice is not switched on"))
		return
	}
	if r.Queue == nil {
		query.ErrorResponse(c, http.StatusServiceUnavailable, errors.New("the job engine is not available in this process"))
		return
	}
	if err := r.Queue.EnqueueNow(ctx, row.ID, row.Name); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "queued")
}
