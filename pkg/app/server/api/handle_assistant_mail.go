package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/email"
	"github.com/cylonchau/kube-res-foresight/pkg/timezone"
	"github.com/gin-gonic/gin"
)

// maxMailBytes bounds the answer that is mailed: a report of the AI is some pages, not a file.
const maxMailBytes = 256 * 1024

// AssistantMailReq is an answer of the AI assistant to mail to the signed-in user.
type AssistantMailReq struct {
	// Title names what the answer is about (for example the module); it follows the fixed start of the subject.
	Title string `json:"title"`
	// Content is the answer as the model wrote it: an HTML page or fragment (it is mailed as one, with its text beside it), or Markdown / text.
	Content string `json:"content" binding:"required"`
}

// MailToMe serves POST /assistant/mail-me {title, content}: mails an answer of the AI assistant (the analysis of the resource dashboard) to the address of the
// signed-in user, the one of the account and their e-mail contacts, through the e-mail channel the system parameters set up. It sends to nobody else: the
// recipient is never taken from the request.
// @Summary Mail an AI answer to the signed-in user
// @Tags Assistant
// @Router /api/v1/assistant/mail-me [post]
func (r *AssistantRouter) MailToMe(c *gin.Context) {
	owner, ok := ownerOfRequest(c)
	if !ok {
		query.BadRequest(c, errors.New("mailing needs a signed-in user: a developer credential has no address"))
		return
	}
	var req AssistantMailReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > maxMailBytes {
		query.BadRequest(c, errors.New("the answer to mail is empty or too large"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	addresses, err := r.Service.UserEmails(ctx, owner.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if len(addresses) == 0 {
		query.BadRequest(c, errors.New("your account has no e-mail address: set one in your profile"))
		return
	}
	channel, ok := notify.Get(email.Channel)
	if !ok {
		channel = email.New(r.Service.Config)
	}
	msg := analysisMail(req, time.Now().In(timezone.Location()))
	var failed []string
	sent := 0
	for _, to := range addresses {
		if err := channel.Send(ctx, to, msg); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		sent++
	}
	if sent == 0 {
		query.BadRequest(c, errors.New(strings.Join(failed, "; ")))
		return
	}
	query.SuccessResponse(c, nil, gin.H{"sent": sent, "to": addresses[:sent], "failed": failed})
}

// analysisMail builds the mail of an answer: the subject names the module, an HTML answer goes out as HTML (with its text beside it).
func analysisMail(req AssistantMailReq, at time.Time) notify.Message {
	title := strings.TrimSpace(req.Title)
	subject := "[kube-res-foresight] AI resource analysis"
	if title != "" {
		subject += " · " + strings.NewReplacer("\r", " ", "\n", " ").Replace(title)
	}
	line := fmt.Sprintf("%s · %s · kube-res-foresight", title, at.Format("2006-01-02 15:04 MST"))
	msg := notify.Message{Title: subject, Content: req.Content + "\n\n--\n" + line}
	if page := notify.HTMLBody(req.Content); page != "" {
		footer := `<p style="color:#888;font-size:12px;border-top:1px solid #ddd;padding-top:8px">` + html.EscapeString(line) + "</p>"
		msg.Content = notify.HTMLText(page) + "\n\n--\n" + line
		msg.HTML = page + footer
	}
	return msg
}
