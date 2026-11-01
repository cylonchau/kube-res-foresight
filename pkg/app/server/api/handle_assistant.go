package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/gin-gonic/gin"
)

// AssistantRouter serves the chat assistant: whether it can be used, one question and its answer, and the export of the service state.
// The conversation is kept by the page: the platform stores none of it.
type AssistantRouter struct {
	Service *assistant.Service
}

// AssistantChatReq is the conversation so far; the last message is the question.
type AssistantChatReq struct {
	Messages []assistant.Turn `json:"messages"`
	// Lang is the language of the console, zh or en.
	Lang string `json:"lang"`
}

func assistantLang(raw string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "en") {
		return "en"
	}
	return "zh"
}

// Status serves GET /assistant/status: whether the chat can be used, and what is missing when it cannot. It never carries the key.
func (r *AssistantRouter) Status(c *gin.Context) {
	av, err := r.Service.Available(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, av)
}

// Chat serves POST /assistant/chat.
// @Summary Ask the assistant a question about the service
// @Tags Assistant
// @Router /api/v1/assistant/chat [post]
func (r *AssistantRouter) Chat(c *gin.Context) {
	var req AssistantChatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	reply, err := r.Service.Chat(c.Request.Context(), req.Messages, assistantLang(req.Lang))
	if err != nil {
		var notReady assistant.ErrNotReady
		switch {
		case errors.As(err, &notReady):
			query.ErrorResponse(c, http.StatusConflict, err)
		case errors.Is(err, assistant.ErrBusy):
			query.ErrorResponse(c, http.StatusTooManyRequests, err)
		case assistant.IsRequestError(err):
			query.BadRequest(c, err)
		default:
			// the language model could not be reached or refused: a failure of the service behind the assistant, not of the platform
			query.ErrorResponse(c, http.StatusBadGateway, err)
		}
		return
	}
	query.SuccessResponse(c, nil, reply)
}

// Export serves GET /assistant/export?format=markdown|json&hours=&lang=: the running state of the service as a file to keep. It is the
// same snapshot the assistant reads, with no secret in it.
// @Summary Export the running state of the service
// @Tags Assistant
// @Router /api/v1/assistant/export [get]
func (r *AssistantRouter) Export(c *gin.Context) {
	hours := 0
	if raw := c.Query("hours"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > forecastsvc.MaxEffectHours {
			query.BadRequest(c, fmt.Errorf("hours must be a whole number between 1 and %d", forecastsvc.MaxEffectHours))
			return
		}
		hours = n
	}
	format := strings.ToLower(c.DefaultQuery("format", "markdown"))
	if format != "markdown" && format != "json" {
		query.BadRequest(c, errors.New("format is markdown or json"))
		return
	}
	report, err := r.Service.Status(c.Request.Context(), hours)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	file := assistant.ExportAttachment(format, report.Hours, "", report.GeneratedAt)
	c.Header("Content-Disposition", `attachment; filename="`+file.Name+`"`)
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			query.InternalError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(report.Markdown(assistantLang(c.Query("lang")))))
}
