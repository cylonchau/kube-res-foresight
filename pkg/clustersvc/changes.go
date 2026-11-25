package clustersvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The changes of the objects of a cluster (P12 E7, E8): taken from the operator, kept for the retention, explained when they are read, and their usage asked of Prometheus
// once the time after them has passed.

const (
	// causeWindow is how long before a change another change may have happened for the one to be a cause of the other (and how long after, for an effect).
	causeWindow = 10 * time.Minute
	// eventWindow is how far from a change an event about the same object is taken to be about it.
	eventWindow = 5 * time.Minute
	// linkLimit bounds how many changes and events are looked at for one explanation.
	linkLimit = 30
	// fillBatch is how many changes one run of the usage task asks about, and usageTries how often a change is asked before it is given up.
	fillBatch  = 50
	usageTries = 5
	// maxSnapshotBytes is the biggest snapshot kept; a bigger one loses its list of Pods first.
	maxSnapshotBytes = 64 << 10
)

// ChangeDTO is a change as the page and the explanation show it.
type ChangeDTO struct {
	model.ClusterChange
	Reasons  []clusteragent.Reason  `json:"reasons"`
	Snapshot *clusteragent.Snapshot `json:"snapshot"`
	Usage    *UsageRecord           `json:"usage"`
}

// UsagePoint is the usage of the object at one moment: Label is before, at or after, At the time asked, CPU in cores and Memory in bytes (nil when Prometheus had none).
type UsagePoint struct {
	Label  string    `json:"label"`
	At     time.Time `json:"at"`
	CPU    *float64  `json:"cpu"`
	Memory *float64  `json:"memory"`
}

// UsageRecord is the usage around a change, as it was asked of Prometheus.
type UsageRecord struct {
	// Subject says whose usage it is (a node, a Pod, the workload of a Pod or of an HPA) and Source the data source it was asked of.
	Subject string       `json:"subject"`
	Source  string       `json:"source,omitempty"`
	Note    string       `json:"note,omitempty"`
	Points  []UsagePoint `json:"points"`
}

// View makes the page's form of a stored change.
func View(row model.ClusterChange) ChangeDTO {
	out := ChangeDTO{ClusterChange: row, Reasons: []clusteragent.Reason{}}
	_ = json.Unmarshal([]byte(row.Reasons), &out.Reasons)
	if out.Reasons == nil {
		out.Reasons = []clusteragent.Reason{}
	}
	if row.Snapshot != "" {
		var s clusteragent.Snapshot
		if json.Unmarshal([]byte(row.Snapshot), &s) == nil {
			out.Snapshot = &s
		}
	}
	if row.Usage != "" {
		var u UsageRecord
		if json.Unmarshal([]byte(row.Usage), &u) == nil {
			out.Usage = &u
		}
	}
	return out
}

// ChangeResult says what became of a batch.
type ChangeResult struct {
	Added int `json:"added"`
	// Expired are changes already past the retention: they are not stored.
	Expired int `json:"expired"`
}

// IngestChanges stores a batch of the changes of the cluster. Nothing in it is translated: the operator's words are kept, and the usage is asked later.
func (s *Service) IngestChanges(ctx context.Context, c *model.Cluster, batch clusteragent.ChangeBatch) (*ChangeResult, error) {
	if err := batch.Validate(); err != nil {
		return nil, err
	}
	now := s.now()
	oldest := now.Add(-s.Retention(ctx))
	out := &ChangeResult{}
	rows := make([]model.ClusterChange, 0, len(batch.Changes))
	for _, ch := range batch.Changes {
		at, seen := ch.OccurredAt.UTC(), ch.ObservedAt.UTC()
		if ch.OccurredAt.IsZero() {
			at = seen
		}
		if seen.IsZero() || seen.Before(at) {
			seen = at
		}
		if at.Before(oldest) {
			out.Expired++
			continue
		}
		if at.After(now.Add(5 * time.Minute)) { // a change cannot be from the future
			at = now
			if seen.Before(at) {
				seen = at
			}
		}
		reasons, _ := json.Marshal(ch.Reasons)
		if ch.Reasons == nil {
			reasons = []byte("[]")
		}
		var snap []byte
		if ch.Snapshot != nil {
			snap, _ = json.Marshal(ch.Snapshot)
			if len(snap) > maxSnapshotBytes {
				cut := *ch.Snapshot
				cut.Pods = nil
				snap, _ = json.Marshal(cut)
			}
		}
		rows = append(rows, model.ClusterChange{
			RecordID: clusteragent.Clip(strings.TrimSpace(ch.RecordID), 64), Kind: ch.Kind, Namespace: clusteragent.Clip(ch.Namespace, 253), Name: clusteragent.Clip(ch.Name, 253),
			ObjectUID: clusteragent.Clip(ch.ObjectUID, 64), Type: clusteragent.Clip(ch.Type, 32), From: clusteragent.Clip(ch.From, 255), To: clusteragent.Clip(ch.To, 255),
			OccurredAt: at, ObservedAt: seen, ReceivedAt: now, Operator: clusteragent.Clip(batch.Operator, 253),
			Node: clusteragent.Clip(ch.Node, 253), Workload: clusteragent.Clip(ch.Workload, 253), Reasons: string(reasons), Snapshot: string(snap), UsageStatus: model.UsagePending,
		})
	}
	var err error
	if out.Added, err = s.DAO.AddChanges(ctx, c.Name, rows); err != nil {
		return nil, err
	}
	return out, nil
}

// Link says how another change is tied to this one. Relation is caused_by (the other change is a cause of this one, or this one a cause of it, by Direction) or related
// (they are in the same place at the same time, which is not a reason). Evidence is what the relation rests on, in words.
type Link struct {
	Relation  string    `json:"relation"`
	Direction string    `json:"direction"`
	Evidence  string    `json:"evidence"`
	Change    ChangeDTO `json:"change"`
}

// The relations and directions of a link.
const (
	RelationCausedBy = "caused_by"
	RelationRelated  = "related"
	// DirectionCause is a change that came before this one and may have caused it; DirectionEffect one that came after and may be an effect of it.
	DirectionCause  = "cause"
	DirectionEffect = "effect"
)

// Explanation is a change with what surrounds it: the events about the object around that time (the reasons Kubernetes gave), and the changes of other objects that are
// tied to it.
type Explanation struct {
	Change  ChangeDTO            `json:"change"`
	Events  []model.ClusterEvent `json:"events"`
	Causes  []Link               `json:"causes"`
	Effects []Link               `json:"effects"`
}

// mentions says whether the reasons of a change or the events about its object name the thing: it is the one kind of evidence the platform takes that one object's change
// is the cause of another's.
func mentions(c ChangeDTO, events []model.ClusterEvent, what string) (string, bool) {
	if what == "" {
		return "", false
	}
	for _, r := range c.Reasons {
		if strings.Contains(r.Message, what) || strings.Contains(r.Reason, what) {
			return fmt.Sprintf("the reason %q of the %s says %s", clip(r.Reason+" "+r.Message, 140), strings.ToLower(c.Kind), what), true
		}
	}
	for _, e := range events {
		if strings.Contains(e.Message, what) {
			return fmt.Sprintf("the event %s of %s says: %s", e.Reason, e.Component, clip(e.Message, 140)), true
		}
	}
	return "", false
}

func clip(s string, n int) string { return clusteragent.Clip(strings.TrimSpace(s), n) }

func lowered(c ChangeDTO) bool {
	var from, to int
	if _, err := fmt.Sscanf(c.From, "%d", &from); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(c.To, "%d", &to); err != nil {
		return false
	}
	return to < from
}

// linkOf says how cause and effect are tied, or nil when they are not. It is the whole of what the platform calls a cause, and it is strict: caused_by needs the effect or
// the way the objects belong together to say so, and two changes that are only near each other are related, which the page shows as "not necessarily the reason".
func linkOf(cause, effect ChangeDTO, effectEvents []model.ClusterEvent) (relation, evidence string, ok bool) {
	gap := effect.OccurredAt.Sub(cause.OccurredAt)
	if gap < 0 || gap > causeWindow {
		return "", "", false
	}
	switch {
	case cause.Kind == clusteragent.KindNode && effect.Kind == clusteragent.KindPod && effect.Node == cause.Name:
		switch cause.Type {
		case "ready", "pressure", "taint", "deleted", "unschedulable":
		default:
			return "", "", false
		}
		if ev, ok := mentions(effect, effectEvents, cause.Name); ok {
			return RelationCausedBy, ev, true
		}
		return RelationRelated, "the Pod was on the node when the node changed; nothing in its reasons or events names the node", true
	case cause.Kind == clusteragent.KindWorkload && cause.Type == "replicas" && effect.Kind == clusteragent.KindPod && (effect.Type == "terminating" || effect.Type == "deleted") &&
		effect.Namespace == cause.Namespace && effect.Workload == cause.Name && lowered(cause):
		if effect.Snapshot != nil && effect.Snapshot.Owner == "StatefulSet/"+cause.Name {
			return RelationCausedBy, fmt.Sprintf("the Pod is owned by the StatefulSet %s, whose replicas were lowered from %s to %s", cause.Name, cause.From, cause.To), true
		}
		return RelationRelated, fmt.Sprintf("the Pod belongs to the workload %s, whose replicas were lowered from %s to %s (which of its Pods goes is not tracked)", cause.Name, cause.From, cause.To), true
	case cause.Kind == clusteragent.KindHPA && cause.Type == "desired" && effect.Kind == clusteragent.KindWorkload && effect.Type == "replicas" &&
		effect.Namespace == cause.Namespace && effect.Name == cause.Workload:
		if effect.To == cause.To {
			return RelationCausedBy, fmt.Sprintf("the HPA %s targets this workload and decided on %s replicas, which is what the replicas became", cause.Name, cause.To), true
		}
		return RelationRelated, fmt.Sprintf("the HPA %s targets this workload; it decided on %s and the replicas became %s", cause.Name, cause.To, effect.To), true
	}
	return "", "", false
}

// Explain reads one change with what surrounds it. The events are the ones about the same object within a few minutes; the links are worked out now, from the changes that
// are kept, so a change or an event that arrived after this one is still found.
func (s *Service) Explain(ctx context.Context, c *model.Cluster, id uint) (*Explanation, error) {
	row, err := s.DAO.GetChange(ctx, c.Name, id)
	if err != nil {
		return nil, err
	}
	ch := View(*row)
	out := &Explanation{Change: ch, Events: []model.ClusterEvent{}, Causes: []Link{}, Effects: []Link{}}
	eventsOf := func(x ChangeDTO) []model.ClusterEvent {
		list, _ := s.DAO.EventsAround(ctx, c.Name, x.ObjectUID, x.Kind, x.Namespace, x.Name, x.OccurredAt.Add(-eventWindow), x.OccurredAt.Add(eventWindow), linkLimit)
		return list
	}
	own := eventsOf(ch)
	if own != nil {
		out.Events = own
	}

	// the changes that came before, in the places this one is tied to
	var before []model.ClusterChange
	around := func(f rdb.ChangeFilter, from, to time.Time) []model.ClusterChange {
		f.Cluster = c.Name
		list, _ := s.DAO.ChangesAround(ctx, f, from, to, linkLimit)
		return list
	}
	switch ch.Kind {
	case clusteragent.KindPod:
		if ch.Node != "" {
			before = append(before, around(rdb.ChangeFilter{Kind: clusteragent.KindNode, Name: ch.Node}, ch.OccurredAt.Add(-causeWindow), ch.OccurredAt)...)
		}
		if ch.Workload != "" {
			before = append(before, around(rdb.ChangeFilter{Kind: clusteragent.KindWorkload, Namespace: ch.Namespace, Name: ch.Workload}, ch.OccurredAt.Add(-causeWindow), ch.OccurredAt)...)
		}
	case clusteragent.KindWorkload:
		before = append(before, around(rdb.ChangeFilter{Kind: clusteragent.KindHPA, Namespace: ch.Namespace, Workload: ch.Name}, ch.OccurredAt.Add(-causeWindow), ch.OccurredAt)...)
	}
	for _, b := range before {
		if b.ID == ch.ID {
			continue
		}
		if rel, ev, ok := linkOf(View(b), ch, own); ok {
			out.Causes = append(out.Causes, Link{Relation: rel, Direction: DirectionCause, Evidence: ev, Change: View(b)})
		}
	}

	// the changes that came after
	var after []model.ClusterChange
	switch ch.Kind {
	case clusteragent.KindNode:
		after = around(rdb.ChangeFilter{Kind: clusteragent.KindPod, Node: ch.Name}, ch.OccurredAt, ch.OccurredAt.Add(causeWindow))
	case clusteragent.KindWorkload:
		after = around(rdb.ChangeFilter{Kind: clusteragent.KindPod, Namespace: ch.Namespace, Workload: ch.Name}, ch.OccurredAt, ch.OccurredAt.Add(causeWindow))
	case clusteragent.KindHPA:
		after = around(rdb.ChangeFilter{Kind: clusteragent.KindWorkload, Namespace: ch.Namespace, Name: ch.Workload}, ch.OccurredAt, ch.OccurredAt.Add(causeWindow))
	}
	for _, a := range after {
		if a.ID == ch.ID {
			continue
		}
		v := View(a)
		if rel, ev, ok := linkOf(ch, v, eventsOf(v)); ok {
			out.Effects = append(out.Effects, Link{Relation: rel, Direction: DirectionEffect, Evidence: ev, Change: v})
		}
	}
	return out, nil
}

// ---- the usage around a change, asked of Prometheus

// UsageSubject is whose usage is asked: a node by its name, a Pod by its namespace, name and workload, a workload by its namespace and name.
type UsageSubject struct {
	Kind      string
	Namespace string
	Name      string
	Workload  string
}

// UsageValue is the usage of a subject at a time: CPU in cores and memory in bytes, nil when Prometheus had none; Note says why when it had none.
type UsageValue struct {
	CPU    *float64
	Memory *float64
	Note   string
}

// UsageSource asks Prometheus for the usage of an object at a time. datasourceID 0 is the data source of the system parameter. It returns the name of the data source it
// asked. An error is a source that could not be reached or read (asked again later); an answer with no figure and a note is a source that has none (not asked again).
type UsageSource interface {
	UsageAt(ctx context.Context, datasourceID uint, s UsageSubject, at time.Time) (UsageValue, string, error)
}

// ErrNoUsageSource is no way to ask: no data source, or its metrics not declared. It is not asked again.
var ErrNoUsageSource = errors.New("no data source to ask the usage of")

func subjectOf(c model.ClusterChange) (UsageSubject, string) {
	switch c.Kind {
	case clusteragent.KindNode:
		return UsageSubject{Kind: c.Kind, Name: c.Name}, "node " + c.Name
	case clusteragent.KindPod:
		return UsageSubject{Kind: c.Kind, Namespace: c.Namespace, Name: c.Name, Workload: c.Workload}, "pod " + c.Namespace + "/" + c.Name
	case clusteragent.KindHPA:
		return UsageSubject{Kind: clusteragent.KindWorkload, Namespace: c.Namespace, Name: c.Workload, Workload: c.Workload}, "workload " + c.Namespace + "/" + c.Workload + " (the target of the HPA)"
	}
	return UsageSubject{Kind: clusteragent.KindWorkload, Namespace: c.Namespace, Name: c.Name, Workload: c.Name}, "workload " + c.Namespace + "/" + c.Name
}

// UsageTimes are the three moments asked about a change.
func (s *Service) usageOffsets(ctx context.Context) (before, after time.Duration) {
	read := func(p timing.Param) time.Duration {
		n := p.Default
		if s.Config != nil {
			raw, _ := s.Config.GetValueOrDefault(ctx, p.Key, "")
			n = p.Parse(raw)
		}
		return time.Duration(n) * time.Second
	}
	return read(timing.ClusterUsageBefore), read(timing.ClusterUsageAfter)
}

// FillResult says what one run of the usage task did.
type FillResult struct {
	Filled      int
	Unavailable int
	Waiting     int
}

// FillUsage asks Prometheus for the usage around the changes whose time after them has passed: before, at and after, with the data source of the cluster. Figures that
// Prometheus does not have are said (no data source, the metric not declared, no series), never written as zero; a source that cannot be reached is asked again, up to
// usageTries times.
func (s *Service) FillUsage(ctx context.Context, src UsageSource) (FillResult, error) {
	var res FillResult
	if src == nil {
		return res, nil
	}
	before, after := s.usageOffsets(ctx)
	rows, err := s.DAO.PendingUsage(ctx, s.now().Add(-after), fillBatch)
	if err != nil {
		return res, err
	}
	datasources := map[string]uint{}
	for _, row := range rows {
		ds, ok := datasources[row.Cluster]
		if !ok {
			if cl, err := s.DAO.ByName(ctx, row.Cluster); err == nil {
				ds = cl.DatasourceID
			}
			datasources[row.Cluster] = ds
		}
		subject, label := subjectOf(row)
		rec := UsageRecord{Subject: label}
		offsets := []struct {
			label string
			at    time.Time
		}{{"before", row.OccurredAt.Add(-before)}, {"at", row.OccurredAt}, {"after", row.OccurredAt.Add(after)}}
		var notes []string
		var failure error
		got := 0
		for _, o := range offsets {
			v, name, err := src.UsageAt(ctx, ds, subject, o.at)
			if err != nil {
				failure = err
				break
			}
			if name != "" {
				rec.Source = name
			}
			rec.Points = append(rec.Points, UsagePoint{Label: o.label, At: o.at.UTC(), CPU: v.CPU, Memory: v.Memory})
			if v.CPU != nil || v.Memory != nil {
				got++
			}
			if v.Note != "" {
				notes = append(notes, v.Note)
			}
		}
		save := func(status string, note string, tries int) error {
			raw, _ := json.Marshal(rec)
			return s.DAO.SaveUsage(ctx, row.ID, status, string(raw), clusteragent.Clip(note, 255), tries)
		}
		switch {
		case failure != nil && errors.Is(failure, ErrNoUsageSource):
			rec.Points = nil
			if err := save(model.UsageUnavailable, failure.Error(), row.UsageTries+1); err != nil {
				return res, err
			}
			res.Unavailable++
		case failure != nil:
			if row.UsageTries+1 >= usageTries {
				rec.Points = nil
				if err := save(model.UsageUnavailable, "the data source could not be asked: "+failure.Error(), row.UsageTries+1); err != nil {
					return res, err
				}
				res.Unavailable++
			} else {
				if err := s.DAO.SaveUsage(ctx, row.ID, model.UsagePending, "", clusteragent.Clip(failure.Error(), 255), row.UsageTries+1); err != nil {
					return res, err
				}
				res.Waiting++
			}
		case got == 0:
			note := "Prometheus has no figure for it"
			if len(notes) > 0 {
				note = notes[0]
			}
			if err := save(model.UsageUnavailable, note, row.UsageTries+1); err != nil {
				return res, err
			}
			res.Unavailable++
		default:
			note := ""
			if got < len(offsets) && len(notes) > 0 {
				note = notes[0]
			}
			if err := save(model.UsageFilled, note, row.UsageTries+1); err != nil {
				return res, err
			}
			res.Filled++
		}
	}
	return res, nil
}
