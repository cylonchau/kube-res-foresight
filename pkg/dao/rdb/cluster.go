package rdb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// ClusterDAO stores the clusters the operators report from and the events they report (P12).
type ClusterDAO struct{ db *gorm.DB }

func NewClusterDAO(db *gorm.DB) *ClusterDAO { return &ClusterDAO{db: db} }

// List is every cluster, the default one first.
func (dao *ClusterDAO) List(ctx context.Context) ([]model.Cluster, error) {
	var rows []model.Cluster
	err := dao.db.WithContext(ctx).Order("is_default DESC, name ASC").Find(&rows).Error
	return rows, err
}

func (dao *ClusterDAO) Get(ctx context.Context, id uint) (*model.Cluster, error) {
	var row model.Cluster
	if err := dao.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (dao *ClusterDAO) ByName(ctx context.Context, name string) (*model.Cluster, error) {
	var row model.Cluster
	if err := dao.db.WithContext(ctx).Where("name = ?", name).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Default is the default cluster; gorm.ErrRecordNotFound before the migration made it.
func (dao *ClusterDAO) Default(ctx context.Context) (*model.Cluster, error) {
	var row model.Cluster
	if err := dao.db.WithContext(ctx).Where("is_default = ?", true).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (dao *ClusterDAO) Create(ctx context.Context, c *model.Cluster) error {
	return dao.db.WithContext(ctx).Create(c).Error
}

// Update saves what an administrator may change of a cluster: never the name or the default flag.
func (dao *ClusterDAO) Update(ctx context.Context, c *model.Cluster) error {
	return dao.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", c.ID).
		Select("description", "enabled", "datasource_id", "namespace_include", "namespace_exclude").Updates(c).Error
}

// Delete removes a cluster that nothing belongs to: no module, and its keys are set free; its events go with it.
func (dao *ClusterDAO) Delete(ctx context.Context, c *model.Cluster) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.SecretKey{}).Where("cluster = ?", c.Name).Update("cluster", "").Error; err != nil {
			return err
		}
		if err := tx.Where("cluster = ?", c.Name).Delete(&model.ClusterEvent{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Cluster{}, c.ID).Error
	})
}

// Touch records that an operator of the cluster has been heard from.
func (dao *ClusterDAO) Touch(ctx context.Context, name, operator string, at time.Time) error {
	updates := map[string]any{"last_seen_at": at}
	if operator != "" {
		updates["operator"] = operator
	}
	return dao.db.WithContext(ctx).Model(&model.Cluster{}).Where("name = ?", name).Updates(updates).Error
}

// ModuleCount is how many modules belong to the cluster (the default cluster owns the modules that name none).
func (dao *ClusterDAO) ModuleCount(ctx context.Context, c *model.Cluster) (int64, error) {
	var n int64
	q := dao.db.WithContext(ctx).Model(&model.ForecastModule{})
	if c.IsDefault {
		q = q.Where("cluster = ? OR cluster = ?", c.Name, "")
	} else {
		q = q.Where("cluster = ?", c.Name)
	}
	return n, q.Count(&n).Error
}

// KeyIDs are the access keys bound to the cluster.
func (dao *ClusterDAO) KeyIDs(ctx context.Context, name string) ([]int64, error) {
	ids := []int64{}
	err := dao.db.WithContext(ctx).Model(&model.SecretKey{}).Where("cluster = ?", name).Order("id").Pluck("id", &ids).Error
	return ids, err
}

// BindKeys makes exactly these access keys the ones bound to the cluster: the others that were bound to it are set free, and a key that was bound to another cluster
// moves here (a key acts for one cluster). Only keys of the developer-access type can be bound.
func (dao *ClusterDAO) BindKeys(ctx context.Context, name string, ids []int64) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		free := tx.Model(&model.SecretKey{}).Where("cluster = ?", name)
		if len(ids) > 0 {
			free = free.Where("id NOT IN ?", ids)
		}
		if err := free.Update("cluster", "").Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&model.SecretKey{}).Where("id IN ? AND key_type = ?", ids, model.SecretKeyTypeDeveloperAccess).Update("cluster", name).Error
	})
}

// KeyChoice is an access key as the cluster page offers it.
type KeyChoice struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	MaskedValue string `json:"masked_value"`
	Cluster     string `json:"cluster"`
	Status      bool   `json:"status"`
}

// KeyChoices lists the access keys that can be bound.
func (dao *ClusterDAO) KeyChoices(ctx context.Context) ([]KeyChoice, error) {
	var rows []model.SecretKey
	if err := dao.db.WithContext(ctx).Where("key_type = ?", model.SecretKeyTypeDeveloperAccess).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]KeyChoice, 0, len(rows))
	for _, r := range rows {
		out = append(out, KeyChoice{ID: r.ID, Name: r.Name, MaskedValue: r.MaskedValue, Cluster: r.Cluster, Status: r.Status})
	}
	return out, nil
}

// EnsureDefault makes the default cluster when there is none (the migration calls it), named as given; it never renames one that exists.
func (dao *ClusterDAO) EnsureDefault(ctx context.Context, name string) (*model.Cluster, error) {
	if row, err := dao.Default(ctx); err == nil {
		return row, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	row := &model.Cluster{Name: name, Description: "", Enabled: true, IsDefault: true}
	// a cluster of that name that is not marked default (made by hand before) becomes the default one
	var existing model.Cluster
	if err := dao.db.WithContext(ctx).Where("name = ?", name).First(&existing).Error; err == nil {
		existing.IsDefault = true
		return &existing, dao.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", existing.ID).Update("is_default", true).Error
	}
	return row, dao.db.WithContext(ctx).Create(row).Error
}

// ---- events

// EventFilter narrows the events of one cluster.
type EventFilter struct {
	Cluster   string
	Type      string
	Origin    string
	Category  string
	Kind      string
	Namespace string
	Name      string
	Reason    string
	Component string
	Search    string
	Since     *time.Time
	Until     *time.Time
}

func (f EventFilter) apply(q *gorm.DB) *gorm.DB {
	q = q.Where("cluster = ?", f.Cluster)
	eq := func(col, v string) {
		if v = strings.TrimSpace(v); v != "" {
			q = q.Where(col+" = ?", v)
		}
	}
	eq("type", f.Type)
	eq("origin", f.Origin)
	eq("category", f.Category)
	eq("kind", f.Kind)
	eq("namespace", f.Namespace)
	eq("name", f.Name)
	eq("reason", f.Reason)
	eq("component", f.Component)
	if s := strings.TrimSpace(f.Search); s != "" {
		like := "%" + strings.ToLower(escapeLike(s)) + "%"
		q = q.Where("LOWER(message) LIKE ? ESCAPE '\\' OR LOWER(name) LIKE ? ESCAPE '\\'", like, like)
	}
	if f.Since != nil {
		q = q.Where("last_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("last_at <= ?", *f.Until)
	}
	return q
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// UpsertEvents stores the events of a batch: a new one is inserted, one that is already known (the same uid in the same cluster) is brought up to date if the batch has
// it later, and one that is older than what is stored is left alone, so a batch that arrives late (an outage of the platform) never takes a newer count back.
// It says how many were new and how many updated.
func (dao *ClusterDAO) UpsertEvents(ctx context.Context, cluster string, rows []model.ClusterEvent) (added, updated int, err error) {
	// the same event twice in one batch is one: the later (or the one counted more) stands
	latest := make(map[string]model.ClusterEvent, len(rows))
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		r.Cluster = cluster
		if cur, ok := latest[r.UID]; ok {
			if r.LastAt.Before(cur.LastAt) || (r.LastAt.Equal(cur.LastAt) && r.Count <= cur.Count) {
				continue
			}
		} else {
			order = append(order, r.UID)
		}
		latest[r.UID] = r
	}
	if len(order) == 0 {
		return 0, 0, nil
	}
	err = dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var have []model.ClusterEvent
		if err := tx.Where("cluster = ? AND uid IN ?", cluster, order).Find(&have).Error; err != nil {
			return err
		}
		known := make(map[string]model.ClusterEvent, len(have))
		for _, h := range have {
			known[h.UID] = h
		}
		var fresh []model.ClusterEvent
		for _, uid := range order {
			r := latest[uid]
			h, ok := known[uid]
			if !ok {
				fresh = append(fresh, r)
				continue
			}
			if r.LastAt.Before(h.LastAt) || (r.LastAt.Equal(h.LastAt) && r.Count <= h.Count) {
				continue
			}
			if r.FirstAt.IsZero() || h.FirstAt.Before(r.FirstAt) {
				r.FirstAt = h.FirstAt
			}
			if err := tx.Model(&model.ClusterEvent{}).Where("id = ?", h.ID).
				Select("type", "reason", "message", "component", "host", "count", "kind", "namespace", "name", "object_uid", "first_at", "last_at", "received_at", "operator",
					"origin", "category", "rule", "raw").Updates(&r).Error; err != nil {
				return err
			}
			updated++
		}
		if len(fresh) > 0 {
			if err := tx.CreateInBatches(&fresh, 100).Error; err != nil {
				return err
			}
			added = len(fresh)
		}
		return nil
	})
	return added, updated, err
}

// ListEvents is a page of the events of a cluster, the latest first, and how many there are in all.
func (dao *ClusterDAO) ListEvents(ctx context.Context, f EventFilter, page, size int) ([]model.ClusterEvent, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	var total int64
	if err := f.apply(dao.db.WithContext(ctx).Model(&model.ClusterEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.ClusterEvent
	err := f.apply(dao.db.WithContext(ctx).Model(&model.ClusterEvent{})).Order("last_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}

// EventCount is how many events the cluster has kept, and when the newest happened.
func (dao *ClusterDAO) EventCount(ctx context.Context, cluster string) (int64, *time.Time, error) {
	var n int64
	if err := dao.db.WithContext(ctx).Model(&model.ClusterEvent{}).Where("cluster = ?", cluster).Count(&n).Error; err != nil || n == 0 {
		return n, nil, err
	}
	var last []time.Time
	if err := dao.db.WithContext(ctx).Model(&model.ClusterEvent{}).Where("cluster = ?", cluster).Order("last_at DESC").Limit(1).Pluck("last_at", &last).Error; err != nil || len(last) == 0 {
		return n, nil, err
	}
	return n, &last[0], nil
}

// PruneEvents deletes the events that last happened before the time, in every cluster, and says how many.
func (dao *ClusterDAO) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("last_at < ?", before).Delete(&model.ClusterEvent{})
	return res.RowsAffected, res.Error
}
