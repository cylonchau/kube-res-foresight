// Package clustersvc is the platform side of the Cluster Agent (P12): the clusters operators report from and the credentials bound to them, what each operator is told
// to collect (its namespaces), and the events it reports, read with the rules of package clusteragent and kept for the retention of the system parameter
// cluster.retention_days. Nothing here reads a cluster: the operator does, read-only, and the platform keeps what it was told.
package clustersvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// The system parameters of the Cluster Agent that are not whole numbers (the retention is one, see timing.ClusterRetentionDays).
const (
	KeyEventRules     = "cluster.event_rules"
	KeySkipReasons    = "cluster.event_skip_reasons"
	KeyCollectEvents  = "cluster.collect_events"
	KeyCollectChanges = "cluster.collect_changes"
	DefaultSkipReason = "Pulling,Pulled,Created,Started"
)

// DefaultConfigs are the parameters for the seed.
func DefaultConfigs() []model.SysConfig {
	return []model.SysConfig{
		{ConfigKey: KeyCollectEvents, ConfigValue: "true", ConfigName: "Collect Cluster Events", Remark: "Whether the operators send the Kubernetes events of their clusters (true or false). The events are the reasons of what happens to nodes, Pods, workloads and autoscalers. Switching it off stops the sending at every operator within a minute; what was kept stays until it is past cluster.retention_days."},
		{ConfigKey: KeyCollectChanges, ConfigValue: "true", ConfigName: "Collect Cluster Changes", Remark: "Whether the operators send the state changes of the nodes, Pods, workloads and HPAs of their clusters (true or false): a node that is not ready, a Pod that restarts or is evicted, a workload whose replicas changed, an HPA that decided. Switching it off stops the sending at every operator within a minute; what was kept stays until it is past cluster.retention_days."},
		{ConfigKey: KeySkipReasons, ConfigValue: DefaultSkipReason, ConfigName: "Cluster Event Reasons Not Sent", Remark: "Comma separated reasons of events the operators do not send, because there are very many and they say nothing of the cause of a change (a container being pulled, created or started). Empty sends every event."},
		{ConfigKey: KeyEventRules, ConfigValue: "", ConfigName: "Cluster Event Rules", Remark: "Rules that say who did what an event says, tried before the built-in ones: a JSON list such as [{\"name\":\"acme-cloud\",\"component\":\"acme-node-lifecycle\",\"origin\":\"auto\",\"category\":\"cloud\"}]. component, reason and kind match an event (a name, or a name with * at the start and/or end); origin is auto or manual; category is free text. Use it to teach the platform another cloud or autoscaler, or to say that an event is done by a person. A bad list is ignored and the built-in rules are used."},
	}
}

var (
	// ErrUnknownCluster is a cluster that is not registered.
	ErrUnknownCluster = errors.New("the cluster is not registered on the platform")
	// ErrForbidden is a credential used for a cluster it is not bound to.
	ErrForbidden = errors.New("the credential is not bound to this cluster")
	// ErrDisabled is a cluster that an administrator switched off.
	ErrDisabled = errors.New("the cluster is switched off on the platform")
	// ErrInvalid is a cluster setting that does not pass its checks.
	ErrInvalid = errors.New("invalid cluster setting")
	// ErrInUse is a cluster that cannot be deleted because modules belong to it.
	ErrInUse = errors.New("the cluster still has modules")
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Service is the platform side of the Cluster Agent.
type Service struct {
	DAO    *rdb.ClusterDAO
	Config *rdb.SysConfigDAO
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) text(ctx context.Context, key, fallback string) string {
	if s.Config == nil {
		return fallback
	}
	v, err := s.Config.GetValueOrDefault(ctx, key, fallback)
	if err != nil {
		return fallback
	}
	return v
}

// Retention is how long the events are kept.
func (s *Service) Retention(ctx context.Context) time.Duration {
	days := timing.ClusterRetentionDays.Default
	if s.Config != nil {
		raw, _ := s.Config.GetValueOrDefault(ctx, timing.ClusterRetentionDays.Key, "")
		days = timing.ClusterRetentionDays.Parse(raw)
	}
	return time.Duration(days) * 24 * time.Hour
}

// Resolve says which cluster a request is for. requested is the cluster the operator was started for (empty when it was started without one), bound the cluster the
// credential it uses is bound to (empty when it is not bound).
//   - a bound credential acts for its cluster only: another one asked for is refused;
//   - a credential that is not bound acts for the default cluster only, which is what every operator did before clusters were registered, so such an operator goes on working;
//   - the cluster must be registered and switched on.
func (s *Service) Resolve(ctx context.Context, requested, bound string) (*model.Cluster, error) {
	requested, bound = strings.TrimSpace(requested), strings.TrimSpace(bound)
	var c *model.Cluster
	var err error
	if bound != "" {
		if requested != "" && requested != bound {
			return nil, fmt.Errorf("%w: the credential is bound to cluster %q and the operator was started for %q", ErrForbidden, bound, requested)
		}
		c, err = s.DAO.ByName(ctx, bound)
	} else {
		c, err = s.DAO.Default(ctx)
		if err == nil && requested != "" && requested != c.Name {
			return nil, fmt.Errorf("%w: bind the credential to cluster %q on the clusters page (an unbound credential acts for %q only)", ErrForbidden, requested, c.Name)
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUnknownCluster
	}
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, ErrDisabled
	}
	return c, nil
}

// Scope is the namespaces a cluster is collected from.
func Scope(c *model.Cluster) clusteragent.Scope {
	return clusteragent.Scope{Include: clusteragent.ParseNames(c.NamespaceInclude), Exclude: clusteragent.ParseNames(c.NamespaceExclude)}
}

// Settings is what an operator of the cluster is told (GET /operator/cluster-settings).
func (s *Service) Settings(ctx context.Context, c *model.Cluster) clusteragent.Settings {
	collect := true
	if s.Config != nil {
		if v, err := s.Config.GetBool(ctx, KeyCollectEvents, true); err == nil {
			collect = v
		}
	}
	skip := []string{}
	for _, r := range strings.Split(s.text(ctx, KeySkipReasons, DefaultSkipReason), ",") {
		if r = strings.TrimSpace(r); r != "" {
			skip = append(skip, r)
		}
	}
	changes := true
	if s.Config != nil {
		if v, err := s.Config.GetBool(ctx, KeyCollectChanges, true); err == nil {
			changes = v
		}
	}
	return clusteragent.Settings{Cluster: c.Name, Namespaces: Scope(c), CollectEvents: collect, CollectChanges: changes, SkipReasons: skip}
}

// Rules are the custom rules of the system parameter; a list that cannot be read gives none and the reason.
func (s *Service) Rules(ctx context.Context) ([]clusteragent.Rule, error) {
	return clusteragent.ParseRules(s.text(ctx, KeyEventRules, ""))
}

// Input is what an administrator sets on a cluster.
type Input struct {
	Name             string
	Description      string
	Enabled          bool
	DatasourceID     uint
	NamespaceInclude []string
	NamespaceExclude []string
}

func (in Input) check() ([]string, []string, error) {
	inc, err := clusteragent.CleanNames(in.NamespaceInclude)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: namespaces to collect: %v", ErrInvalid, err)
	}
	exc, err := clusteragent.CleanNames(in.NamespaceExclude)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: namespaces to leave out: %v", ErrInvalid, err)
	}
	if len(in.Description) > 255 {
		return nil, nil, fmt.Errorf("%w: the description is longer than 255 characters", ErrInvalid)
	}
	return inc, exc, nil
}

// Create registers a cluster. The name is what an operator is started with; it is lower-case letters, digits and -, and it can be changed afterwards (see Update).
func (s *Service) Create(ctx context.Context, in Input) (*model.Cluster, error) {
	name := strings.TrimSpace(in.Name)
	if err := validName(name); err != nil {
		return nil, err
	}
	inc, exc, err := in.check()
	if err != nil {
		return nil, err
	}
	if _, err := s.DAO.ByName(ctx, name); err == nil {
		return nil, fmt.Errorf("%w: a cluster named %q exists", ErrInvalid, name)
	}
	c := &model.Cluster{Name: name, Description: in.Description, Enabled: in.Enabled, DatasourceID: in.DatasourceID,
		NamespaceInclude: clusteragent.FormatNames(inc), NamespaceExclude: clusteragent.FormatNames(exc)}
	return c, s.DAO.Create(ctx, c)
}

// keyClusterName is the system parameter that names the cluster of a platform that has one: the default cluster's name is kept in it too, so what reads it (the nodes
// page, the Grafana dashboard, the mail) says the same name.
const keyClusterName = "cluster.name"

// validName checks a name a cluster is given.
func validName(name string) error {
	if !nameRe.MatchString(name) || len(name) > 63 {
		return fmt.Errorf("%w: the name is lower-case letters, digits and -, starting and ending with a letter or digit (63 characters at most)", ErrInvalid)
	}
	return nil
}

// Update changes a cluster: what it is for, its switch, its data source and its namespaces, and its name. A name that is changed (in.Name, when it is given and is not the
// name it has) carries every row that refers to it, the modules and the access keys that name the cluster, its events, changes and nodes; an operator that was started with
// the old name has to be started with the new one (the platform answers it as a cluster that is not registered). The default cluster can be renamed too, and cannot be switched
// off (the operators that name no cluster belong to it).
func (s *Service) Update(ctx context.Context, id uint, in Input) (*model.Cluster, error) {
	c, err := s.DAO.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	newName := strings.TrimSpace(in.Name)
	if newName != "" && newName != c.Name {
		if err := validName(newName); err != nil {
			return nil, err
		}
		if _, err := s.DAO.ByName(ctx, newName); err == nil {
			return nil, fmt.Errorf("%w: a cluster named %q exists", ErrInvalid, newName)
		}
		if err := s.DAO.Rename(ctx, c, newName); err != nil {
			return nil, err
		}
		if c.IsDefault && s.Config != nil {
			// the name of the cluster of the platform is the name of the default one
			_ = s.Config.UpdateValue(ctx, keyClusterName, newName)
		}
		c.Name = newName
	}
	if c.IsDefault && !in.Enabled {
		return nil, fmt.Errorf("%w: the default cluster cannot be switched off", ErrInvalid)
	}
	inc, exc, err := in.check()
	if err != nil {
		return nil, err
	}
	c.Description, c.Enabled, c.DatasourceID = in.Description, in.Enabled, in.DatasourceID
	c.NamespaceInclude, c.NamespaceExclude = clusteragent.FormatNames(inc), clusteragent.FormatNames(exc)
	return c, s.DAO.Update(ctx, c)
}

// Delete removes a cluster that has no module; the default one cannot be removed.
func (s *Service) Delete(ctx context.Context, id uint) error {
	c, err := s.DAO.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.IsDefault {
		return fmt.Errorf("%w: the default cluster cannot be deleted", ErrInvalid)
	}
	if n, err := s.DAO.ModuleCount(ctx, c); err != nil {
		return err
	} else if n > 0 {
		return fmt.Errorf("%w: move or delete its %d module(s) first", ErrInUse, n)
	}
	return s.DAO.Delete(ctx, c)
}

// IngestResult says what became of a batch.
type IngestResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	// Expired are events already past the retention: they are not stored (the next clean-up would delete them).
	Expired int `json:"expired"`
	// RulesError is set when the custom rules could not be read and the built-in ones were used.
	RulesError string `json:"rules_error,omitempty"`
}

// IngestEvents reads and stores a batch of events of the cluster. An event is kept as it was said; the rules only add who did it.
func (s *Service) IngestEvents(ctx context.Context, c *model.Cluster, batch clusteragent.Batch) (*IngestResult, error) {
	if err := batch.Validate(); err != nil {
		return nil, err
	}
	out := &IngestResult{}
	rules, err := s.Rules(ctx)
	if err != nil {
		out.RulesError = err.Error()
		rules = nil
	}
	now := s.now()
	oldest := now.Add(-s.Retention(ctx))
	rows := make([]model.ClusterEvent, 0, len(batch.Events))
	for _, e := range batch.Events {
		last, first := e.LastAt.UTC(), e.FirstAt.UTC()
		if e.LastAt.IsZero() {
			last = first
		}
		if first.IsZero() || first.After(last) {
			first = last
		}
		if last.Before(oldest) {
			out.Expired++
			continue
		}
		if last.After(now.Add(5 * time.Minute)) { // an event cannot be from the future
			last = now
			if first.After(last) {
				first = last
			}
		}
		res := clusteragent.Classify(rules, clusteragent.Subject{Component: e.Component, Reason: e.Reason, Kind: e.Kind})
		raw, _ := json.Marshal(e)
		count := e.Count
		if count < 1 {
			count = 1
		}
		rows = append(rows, model.ClusterEvent{
			UID: clusteragent.Clip(strings.TrimSpace(e.UID), 64), Type: clusteragent.Clip(e.Type, 16), Reason: clusteragent.Clip(e.Reason, 128), Message: clusteragent.Clip(e.Message, 4096),
			Component: clusteragent.Clip(e.Component, 253), Host: clusteragent.Clip(e.Host, 253), Count: count,
			Kind: clusteragent.Clip(e.Kind, 64), Namespace: clusteragent.Clip(e.Namespace, 253), Name: clusteragent.Clip(e.Name, 253), ObjectUID: clusteragent.Clip(e.ObjectUID, 64),
			FirstAt: first, LastAt: last, ReceivedAt: now, Operator: clusteragent.Clip(batch.Operator, 253),
			Origin: res.Origin, Category: res.Category, Rule: clusteragent.Clip(res.Rule, 128), Raw: string(raw),
		})
	}
	if out.Added, out.Updated, err = s.DAO.UpsertEvents(ctx, c.Name, rows); err != nil {
		return nil, err
	}
	return out, nil
}

// Touch records that an operator of the cluster has been heard from; a failure is logged and never fails the request that carries it.
func (s *Service) Touch(ctx context.Context, c *model.Cluster, operator string) {
	if err := s.DAO.Touch(ctx, c.Name, clusteragent.Clip(operator, 253), s.now()); err != nil {
		logger.Warn("record the contact of the operator of a cluster failed", logger.String("cluster", c.Name), logger.Err(err))
	}
}

// Prune deletes the events and the changes past the retention, and says how many rows went.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	before := s.now().Add(-s.Retention(ctx))
	events, err := s.DAO.PruneEvents(ctx, before)
	if err != nil {
		return events, err
	}
	changes, err := s.DAO.PruneChanges(ctx, before)
	return events + changes, err
}
