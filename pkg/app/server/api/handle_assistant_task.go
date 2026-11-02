package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timezone"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The AI scheduled tasks (the menu AI configuration > scheduled tasks): the tasks of every user, made in the chat or here, and the runs of each.

// ownerOfRequest is the signed-in user of the request; a call with a developer credential has none.
func ownerOfRequest(c *gin.Context) (assistant.Owner, bool) {
	id, ok := c.Get("userId")
	uid, isUint := id.(uint)
	if !ok || !isUint || uid == 0 {
		return assistant.Owner{}, false
	}
	name, _ := c.Get("username")
	n, _ := name.(string)
	return assistant.Owner{ID: uid, Name: n}, true
}

// AssistantTaskReq is the form of a task.
type AssistantTaskReq struct {
	Name       string   `json:"name"`
	Prompt     string   `json:"prompt"`
	Schedule   string   `json:"schedule"`
	Lang       string   `json:"lang"`
	Enabled    bool     `json:"enabled"`
	LLMID      uint     `json:"llm_id"`
	Recipients []string `json:"recipients"`
}

// AssistantTaskDTO is a task as the page shows it.
type AssistantTaskDTO struct {
	ID         uint       `json:"id"`
	CreatedAt  time.Time  `json:"created_at"`
	Name       string     `json:"name"`
	OwnerName  string     `json:"owner_name"`
	Prompt     string     `json:"prompt"`
	Schedule   string     `json:"schedule"`
	Enabled    bool       `json:"enabled"`
	LLMID      uint       `json:"llm_id"`
	Lang       string     `json:"lang"`
	Recipients []string   `json:"recipients"`
	Source     string     `json:"source"`
	NextRunAt  *time.Time `json:"next_run_at"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastStatus string     `json:"last_status"`
	LastError  string     `json:"last_error"`
}

func taskDTO(t *model.AssistantTask) AssistantTaskDTO {
	to := assistant.ParseRecipients(t.Recipients)
	if to == nil {
		to = []string{}
	}
	return AssistantTaskDTO{ID: t.ID, CreatedAt: t.CreatedAt, Name: t.Name, OwnerName: t.OwnerName, Prompt: t.Prompt, Schedule: t.Schedule, Enabled: t.Enabled,
		LLMID: t.LLMID, Lang: t.Lang, Recipients: to, Source: t.Source, NextRunAt: t.NextRunAt, LastRunAt: t.LastRunAt, LastStatus: t.LastStatus, LastError: t.LastError}
}

func (q AssistantTaskReq) input() assistant.TaskInput {
	return assistant.TaskInput{Name: q.Name, Prompt: q.Prompt, Schedule: q.Schedule, Lang: q.Lang, Enabled: q.Enabled, LLMID: q.LLMID, Recipients: q.Recipients}
}

// ListTasks serves GET /assistant/tasks?keyword=&enabled=&source=chat|page&page=&page_size=.
func (r *AssistantRouter) ListTasks(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Service.Tasks.List(c.Request.Context(), rdb.TaskFilter{Keyword: c.Query("keyword"), Enabled: boolQuery(c, "enabled"), Source: c.Query("source")}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]AssistantTaskDTO, 0, len(list))
	for _, t := range list {
		out = append(out, taskDTO(t))
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// PreviewSchedule serves GET /assistant/tasks/preview?schedule=: the next times a schedule fires, or why it is not valid.
func (r *AssistantRouter) PreviewSchedule(c *gin.Context) {
	schedule := strings.TrimSpace(c.Query("schedule"))
	out := gin.H{"time_zone": timezone.Location().String(), "next": []time.Time{}, "error": ""}
	if err := assistant.CheckSchedule(schedule, time.Now()); err != nil {
		out["error"] = err.Error()
		query.SuccessResponse(c, nil, out)
		return
	}
	next := make([]time.Time, 0, 5)
	at := time.Now()
	for i := 0; i < 5; i++ {
		n, err := assistant.NextRun(schedule, at)
		if err != nil {
			break
		}
		next, at = append(next, n), n
	}
	out["next"] = next
	query.SuccessResponse(c, nil, out)
}

// CreateTask serves POST /assistant/tasks. The owner is the user who makes it; the recipients may be any addresses an administrator names.
func (r *AssistantRouter) CreateTask(c *gin.Context) {
	var req AssistantTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	owner, ok := ownerOfRequest(c)
	if !ok {
		query.Unauthorized(c, query.ErrUserContextMissing)
		return
	}
	row := &model.AssistantTask{}
	if err := r.Service.SaveTask(c.Request.Context(), row, req.input(), owner, model.AITaskFromPage); err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, taskDTO(row))
}

// UpdateTask serves PUT /assistant/tasks/:id.
func (r *AssistantRouter) UpdateTask(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req AssistantTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Service.Tasks.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	owner := assistant.Owner{ID: row.OwnerID, Name: row.OwnerName}
	if err := r.Service.SaveTask(c.Request.Context(), row, req.input(), owner, row.Source); err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, taskDTO(row))
}

// DeleteTask serves DELETE /assistant/tasks/:id.
func (r *AssistantRouter) DeleteTask(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	n, err := r.Service.Tasks.Delete(c.Request.Context(), []uint{id}, 0)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if n == 0 {
		query.NotFound(c, errors.New("task not found"))
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// BatchDeleteTasks serves POST /assistant/tasks/batch-delete.
func (r *AssistantRouter) BatchDeleteTasks(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if _, err := r.Service.Tasks.Delete(c.Request.Context(), req.IDs, 0); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// RunTaskNow serves POST /assistant/tasks/:id/run: it queues a run in the task center, which the job engine takes up at once.
func (r *AssistantRouter) RunTaskNow(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	row, err := r.Service.Tasks.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	if r.Queue == nil {
		query.ErrorResponse(c, http.StatusServiceUnavailable, errors.New("the job engine is not available in this process"))
		return
	}
	if err := r.Queue.EnqueueNow(c.Request.Context(), row.ID, row.Name); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "queued")
}

// TaskRuns serves GET /assistant/tasks/:id/runs?page=&page_size=: what each run answered and to whom it was mailed.
func (r *AssistantRouter) TaskRuns(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if _, err := r.Service.Tasks.Get(c.Request.Context(), id); err != nil {
		query.HandleDBError(c, err)
		return
	}
	page, size := pageParams(c)
	list, total, err := r.Service.Tasks.Runs(c.Request.Context(), id, page, size)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		query.InternalError(c, err)
		return
	}
	type run struct {
		*model.AssistantTaskRun
		Delivered []string `json:"delivered"`
	}
	out := make([]run, 0, len(list))
	for _, item := range list {
		d := assistant.ParseRecipients(item.Delivered)
		if d == nil {
			d = []string{}
		}
		out = append(out, run{AssistantTaskRun: item, Delivered: d})
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}
