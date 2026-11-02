// Package notifyjob is the shared wire contract and enqueue helper between
// the "notify" job.Task (pkg/job/tasks/notifytask, which delivers one
// message) and every generic task that produces messages. It is split out
// because producers need the exact same Payload shape and enqueue-with-
// idempotency-key logic, and none of them should depend on each other
// directly.
package notifyjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/template"
)

// JobType is the "notify" task's identifier — see pkg/job/tasks/notifytask.
const JobType = "notify"

// Payload is the job.Payload JSON contract for a "notify" job: one job = one
// message to one recipient on one channel.
type Payload struct {
	Channel   string            `json:"channel"`
	Recipient string            `json:"recipient"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	Buttons   []template.Button `json:"buttons,omitempty"`

	// Optional engine controls: severity drives escalation (P0/P1), DedupKey
	// suppresses repeats, EscalateChannel is used when a P0/P1 is not ACKed.
	Severity        string `json:"severity,omitempty"`
	DedupKey        string `json:"dedup_key,omitempty"`
	EscalateChannel string `json:"escalate_channel,omitempty"`
}

// Enqueue builds a notify.EnqueueFunc backed by jobDAO. sourceJob identifies
// the source job triggering the
// broadcast; it's folded into the idempotency key so retries of that same
// job re-use the same notify jobs instead of enqueuing duplicates for every
// recipient on every retry attempt. The message content is also hashed into
// the key: a single detection run can broadcast several distinct messages to
// the same recipient (one per hit, each carrying its own entity-specific
// button), and without the content hash they'd all collide on the same key
// and only the first would ever be enqueued.
func Enqueue(jobDAO *rdb.JobDAO, sourceJob *model.Job) notify.EnqueueFunc {
	return func(ctx context.Context, channel, recipient string, msg notify.Message) error {
		payload, err := json.Marshal(Payload{
			Channel:   channel,
			Recipient: recipient,
			Title:     msg.Title,
			Content:   msg.Content,
			Buttons:   msg.Buttons,
		})
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(msg.Title + "\x00" + msg.Content))
		key := fmt.Sprintf("notify-%s-%d-%s-%s-%s", sourceJob.JobType, sourceJob.ID, channel, recipient, hex.EncodeToString(sum[:8]))
		return jobDAO.CreateIdempotent(ctx, &model.Job{
			JobType:        JobType,
			TargetType:     "notify_channel",
			TargetName:     recipient,
			IdempotencyKey: key,
			Payload:        string(payload),
			MaxRetry:       3,
		})
	}
}
