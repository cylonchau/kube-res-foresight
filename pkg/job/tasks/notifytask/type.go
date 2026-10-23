// Package notifytask implements the "notify" job.Task: delivers one message
// to one recipient on one channel. It has no channel-specific knowledge
// itself — it just looks the channel up in the pkg/notify registry and hands
// off, so adding a new channel (email, webhook, ...) never requires touching
// this task. Channel implementations (e.g. pkg/notify/telegram) register
// themselves via notify.Register — see pkg/job/tasks/registry.go.
//
// Producer tasks never send
// anything themselves — they call notify.Broadcast, which enqueues one
// "notify" job per recipient via notifyjob.Enqueue, so an actual delivery
// attempt gets the job engine's normal retry/backoff and can't take the
// detection task down with it.
package notifytask

import "github.com/cylonchau/kubeforecast/pkg/job/tasks/notifyjob"

// JobType is this task's identifier.
const JobType = notifyjob.JobType
