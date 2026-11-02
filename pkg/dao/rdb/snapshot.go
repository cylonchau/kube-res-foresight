package rdb

import (
	"context"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// LastSnapshotAt is when the newest snapshot of a module was made, nil when it has none.
func (dao *PredictionDAO) LastSnapshotAt(ctx context.Context, moduleID uint) (*time.Time, error) {
	var rec model.Recommendation
	err := dao.db.WithContext(ctx).Where("module_id = ? AND snapshot = ?", moduleID, true).Order("version DESC").Limit(1).Take(&rec).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at := rec.CreatedAt
	return &at, nil
}

// SnapshotFilter selects snapshots of the history of recommendations.
type SnapshotFilter struct {
	ModuleID    uint
	Since       *time.Time
	Until       *time.Time
	Sort        string
	Dir         string
	WithoutBody bool // leave the stored detail out: a list does not need it
	// CoversVersion keeps the snapshots a recommendation version belongs to: the newest snapshot at or before it (a version that
	// repeats an unchanged recommendation is not a snapshot of its own, it is part of the span of the one before it).
	CoversVersion int
}

// SearchSnapshots lists the snapshots (never the repeats of an unchanged recommendation), newest first unless sorted.
func (dao *PredictionDAO) SearchSnapshots(ctx context.Context, f SnapshotFilter, page, pageSize int) ([]*model.Recommendation, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("snapshot = ?", true)
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	if f.CoversVersion > 0 {
		q = q.Where("version <= ?", f.CoversVersion)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "created_at DESC, id DESC"
	if f.CoversVersion > 0 {
		order = "version DESC, id DESC" // the newest snapshot at or before the version comes first
	}
	switch f.Sort {
	case "version", "cpu_milli", "memory_bytes", "replicas", "valid_until", "resource_version":
		dir := "DESC"
		if f.Dir == "ascend" || f.Dir == "asc" {
			dir = "ASC"
		}
		order = f.Sort + " " + dir + ", id DESC"
	}
	page, pageSize = normalizePage(page, pageSize)
	if f.WithoutBody {
		q = q.Omit("detail")
	}
	var list []*model.Recommendation
	err := q.Order(order).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// SnapshotByID returns one snapshot with its detail.
func (dao *PredictionDAO) SnapshotByID(ctx context.Context, id uint) (*model.Recommendation, error) {
	var rec model.Recommendation
	if err := dao.db.WithContext(ctx).Where("id = ? AND snapshot = ?", id, true).Take(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// NextSnapshotVersion is the version of the first snapshot of the module after the given one: the span of a snapshot runs
// until it. 0 means the snapshot is still the current one.
func (dao *PredictionDAO) NextSnapshotVersion(ctx context.Context, moduleID uint, version int) (int, error) {
	var next []int
	err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("module_id = ? AND snapshot = ? AND version > ?", moduleID, true, version).
		Order("version ASC").Limit(1).Pluck("version", &next).Error
	if err != nil || len(next) == 0 {
		return 0, err
	}
	return next[0], nil
}

// SnapshotOutcome is what became of the versions of a snapshot's span: how often the operator applied, saw or failed to
// apply its replicas, and how often an admission carried its resources.
type SnapshotOutcome struct {
	Applied   int64 // the operator scaled to the decided replicas
	Observed  int64 // the replicas already were the decided ones
	Held      int64 // the operator held back (cooldown, step limit, no fresh decision)
	Conflict  int64
	Error     int64
	Admitted  int64 // admission answers that gave a Pod the snapshot's resources
	LastAt    *time.Time
	LastError string
}

// OutcomeOfSpan counts the executions and admissions that used the versions from..to-1 of a module (to 0 = open ended).
func (dao *PredictionDAO) OutcomeOfSpan(ctx context.Context, moduleID uint, from, to int) (SnapshotOutcome, error) {
	var out SnapshotOutcome
	exec := dao.db.WithContext(ctx).Model(&model.ScalingExecution{}).Where("module_id = ? AND decision_version >= ?", moduleID, from)
	if to > 0 {
		exec = exec.Where("decision_version < ?", to)
	}
	var rows []struct {
		Result string
		N      int64
	}
	if err := exec.Select("result, COUNT(*) AS n").Group("result").Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		switch r.Result {
		case model.ExecApplied:
			out.Applied = r.N
		case model.ExecObserved:
			out.Observed = r.N
		case model.ExecHeld:
			out.Held = r.N
		case model.ExecConflict:
			out.Conflict = r.N
		case model.ExecError:
			out.Error = r.N
		}
	}
	var last model.ScalingExecution
	lastQ := dao.db.WithContext(ctx).Where("module_id = ? AND decision_version >= ?", moduleID, from)
	if to > 0 {
		lastQ = lastQ.Where("decision_version < ?", to)
	}
	if err := lastQ.Order("id DESC").Limit(1).Find(&last).Error; err != nil {
		return out, err
	}
	if last.ID != 0 {
		at := last.CreatedAt
		out.LastAt = &at
		if last.Result == model.ExecError || last.Result == model.ExecConflict {
			out.LastError = last.Reason
		}
	}
	adm := dao.db.WithContext(ctx).Model(&model.AdmissionDecision{}).Where("module_id = ? AND applied = ? AND version >= ?", moduleID, true, from)
	if to > 0 {
		adm = adm.Where("version < ?", to)
	}
	if err := adm.Count(&out.Admitted).Error; err != nil {
		return out, err
	}
	return out, nil
}

// SnapshotExecutions lists the executions reported for the span of a snapshot, newest first, at most limit.
func (dao *PredictionDAO) SnapshotExecutions(ctx context.Context, moduleID uint, from, to, limit int) ([]*model.ScalingExecution, error) {
	q := dao.db.WithContext(ctx).Where("module_id = ? AND decision_version >= ?", moduleID, from)
	if to > 0 {
		q = q.Where("decision_version < ?", to)
	}
	var list []*model.ScalingExecution
	err := q.Order("id DESC").Limit(limit).Find(&list).Error
	return list, err
}

// BackfillSnapshots marks snapshots in the recommendations of a module that has none (an installation from before the
// snapshot history): the first, the latest, and every one whose replicas or resources differ from the one before it.
// It is safe to repeat: a module with a snapshot is left alone.
func (dao *PredictionDAO) BackfillSnapshots(ctx context.Context) (int64, error) {
	var modules []uint
	if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Distinct().Pluck("module_id", &modules).Error; err != nil {
		return 0, err
	}
	var marked int64
	for _, id := range modules {
		var have int64
		if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("module_id = ? AND snapshot = ?", id, true).Count(&have).Error; err != nil {
			return marked, err
		}
		if have > 0 {
			continue
		}
		var rows []model.Recommendation
		if err := dao.db.WithContext(ctx).Omit("detail").Where("module_id = ?", id).Order("version ASC").Find(&rows).Error; err != nil {
			return marked, err
		}
		for i := range rows {
			why := ""
			switch {
			case i == 0:
				why = "first recommendation of the module"
			case i == len(rows)-1:
				why = "the current recommendation"
			case rows[i].Replicas != rows[i-1].Replicas || rows[i].CPUMilli != rows[i-1].CPUMilli || rows[i].MemoryBytes != rows[i-1].MemoryBytes || rows[i].Algorithm != rows[i-1].Algorithm:
				why = "replicas, resources or algorithm changed (marked when the history was introduced)"
			}
			if why == "" {
				continue
			}
			if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("id = ?", rows[i].ID).Updates(map[string]any{"snapshot": true, "snapshot_reason": why}).Error; err != nil {
				return marked, err
			}
			marked++
		}
	}
	return marked, nil
}

// DegradedSnapshots lists the snapshots in which a field fell back or the OOM protection raised the memory, newest first, with
// the filters of the degradation records. The detail is left out.
func (dao *PredictionDAO) DegradedSnapshots(ctx context.Context, f FallbackFilter, limit int) ([]*model.Recommendation, error) {
	q := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Omit("detail").Where("snapshot = ? AND (degraded = ? OR oom_floor = ?)", true, true, true)
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Reason != "" {
		if f.Reason == "oom_floor" {
			q = q.Where("oom_floor = ?", true)
		} else {
			q = q.Where("degrade_reasons LIKE ?", "%\""+strings.ReplaceAll(strings.ReplaceAll(f.Reason, "%", ""), "\"", "")+"\"%")
		}
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var list []*model.Recommendation
	err := q.Order("id DESC").Limit(limit).Find(&list).Error
	return list, err
}

// PreviousRecommendation is the newest recommendation of the module before the given version (any kind), nil when there is none.
func (dao *PredictionDAO) PreviousRecommendation(ctx context.Context, moduleID uint, beforeVersion int) (*model.Recommendation, error) {
	var rec model.Recommendation
	err := dao.db.WithContext(ctx).Omit("detail").Where("module_id = ? AND version < ?", moduleID, beforeVersion).Order("version DESC").Limit(1).Find(&rec).Error
	if err != nil || rec.ID == 0 {
		return nil, err
	}
	return &rec, nil
}

// PreviousHealthy is the newest recommendation before the given version that was neither degraded nor raised by the OOM
// protection: the configuration that was in force before a degradation began. nil when there is none.
func (dao *PredictionDAO) PreviousHealthy(ctx context.Context, moduleID uint, beforeVersion int) (*model.Recommendation, error) {
	var rec model.Recommendation
	err := dao.db.WithContext(ctx).Omit("detail").Where("module_id = ? AND version < ? AND degraded = ? AND oom_floor = ?", moduleID, beforeVersion, false, false).
		Order("version DESC").Limit(1).Find(&rec).Error
	if err != nil || rec.ID == 0 {
		return nil, err
	}
	return &rec, nil
}

// ProblemExecutions lists the operator reports that did not go as decided: an error, a conflict, and a hold because there was no
// usable decision (held for a cooldown or a step limit is pacing, not a problem). Newest first.
func (dao *PredictionDAO) ProblemExecutions(ctx context.Context, moduleID uint, since, until *time.Time, limit int) ([]*model.ScalingExecution, error) {
	q := dao.db.WithContext(ctx).Model(&model.ScalingExecution{}).Where("(result IN ? OR (result = ? AND (reason LIKE ? OR reason LIKE ?)))",
		[]string{model.ExecError, model.ExecConflict}, model.ExecHeld, "%decision unavailable%", "%decision expired%")
	if moduleID != 0 {
		q = q.Where("module_id = ?", moduleID)
	}
	if since != nil {
		q = q.Where("created_at >= ?", *since)
	}
	if until != nil {
		q = q.Where("created_at <= ?", *until)
	}
	var list []*model.ScalingExecution
	err := q.Order("id DESC").Limit(limit).Find(&list).Error
	return list, err
}

// NextSuccessfulExecution is the first report of the module after the given execution in which the operator applied the decision
// or found the replicas already right: its time is when a failed or held change was resolved. nil while it is not.
func (dao *PredictionDAO) NextSuccessfulExecution(ctx context.Context, moduleID uint, afterID uint) (*model.ScalingExecution, error) {
	var e model.ScalingExecution
	err := dao.db.WithContext(ctx).Where("module_id = ? AND id > ? AND result IN ?", moduleID, afterID, []string{model.ExecApplied, model.ExecObserved}).
		Order("id ASC").Limit(1).Find(&e).Error
	if err != nil || e.ID == 0 {
		return nil, err
	}
	return &e, nil
}
