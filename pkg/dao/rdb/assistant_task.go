package rdb

import (
	"context"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

// AssistantTaskDAO stores the AI scheduled tasks and their runs.
type AssistantTaskDAO struct {
	db *gorm.DB
}

func NewAssistantTaskDAO(db *gorm.DB) *AssistantTaskDAO { return &AssistantTaskDAO{db: db} }

func (dao *AssistantTaskDAO) Get(ctx context.Context, id uint) (*model.AssistantTask, error) {
	var t model.AssistantTask
	if err := dao.db.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// TaskFilter narrows the list of tasks.
type TaskFilter struct {
	Keyword string
	Enabled *bool
	// OwnerID limits the list to the tasks of one user; 0 lists all.
	OwnerID uint
	Source  string
}

// List returns the tasks, newest first.
func (dao *AssistantTaskDAO) List(ctx context.Context, f TaskFilter, page, pageSize int) ([]*model.AssistantTask, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantTask{})
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR prompt LIKE ? OR owner_name LIKE ?", like, like, like)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	if f.OwnerID != 0 {
		q = q.Where("owner_id = ?", f.OwnerID)
	}
	if f.Source != "" {
		q = q.Where("source = ?", f.Source)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	var list []*model.AssistantTask
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// CountByOwner is how many tasks a user has.
func (dao *AssistantTaskDAO) CountByOwner(ctx context.Context, ownerID uint) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AssistantTask{}).Where("owner_id = ?", ownerID).Count(&n).Error
	return n, err
}

// Save creates (ID 0) or updates a task.
func (dao *AssistantTaskDAO) Save(ctx context.Context, t *model.AssistantTask) error {
	return dao.db.WithContext(ctx).Save(t).Error
}

// Delete removes tasks (and their runs). ownerID, when not 0, limits it to the tasks of that user.
func (dao *AssistantTaskDAO) Delete(ctx context.Context, ids []uint, ownerID uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var deleted int64
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Where("id IN ?", ids)
		if ownerID != 0 {
			q = q.Where("owner_id = ?", ownerID)
		}
		var mine []uint
		if err := q.Model(&model.AssistantTask{}).Pluck("id", &mine).Error; err != nil {
			return err
		}
		if len(mine) == 0 {
			return nil
		}
		if err := tx.Where("task_id IN ?", mine).Delete(&model.AssistantTaskRun{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", mine).Delete(&model.AssistantTask{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

// Due returns the enabled tasks whose next run has come (or was never worked out), at most limit of them.
func (dao *AssistantTaskDAO) Due(ctx context.Context, now time.Time, limit int) ([]*model.AssistantTask, error) {
	var list []*model.AssistantTask
	err := dao.db.WithContext(ctx).Where("enabled = ? AND (next_run_at IS NULL OR next_run_at <= ?)", true, now).
		Order("id ASC").Limit(limit).Find(&list).Error
	return list, err
}

// SetNextRun records when a task runs next.
func (dao *AssistantTaskDAO) SetNextRun(ctx context.Context, id uint, next time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.AssistantTask{}).Where("id = ?", id).Update("next_run_at", next).Error
}

// RecordResult keeps how the last run of a task went.
func (dao *AssistantTaskDAO) RecordResult(ctx context.Context, id uint, at time.Time, status, failure string) error {
	if len(failure) > 500 {
		failure = failure[:500]
	}
	return dao.db.WithContext(ctx).Model(&model.AssistantTask{}).Where("id = ?", id).
		Updates(map[string]any{"last_run_at": at, "last_status": status, "last_error": failure}).Error
}

// RunByJob returns the run a job of the task center made, nil when it has made none yet.
func (dao *AssistantTaskDAO) RunByJob(ctx context.Context, jobID uint) (*model.AssistantTaskRun, error) {
	if jobID == 0 {
		return nil, nil
	}
	var r model.AssistantTaskRun
	err := dao.db.WithContext(ctx).Where("job_id = ?", jobID).Limit(1).Find(&r).Error
	if err != nil || r.ID == 0 {
		return nil, err
	}
	return &r, nil
}

// SaveRun creates (ID 0) or updates a run.
func (dao *AssistantTaskDAO) SaveRun(ctx context.Context, r *model.AssistantTaskRun) error {
	return dao.db.WithContext(ctx).Save(r).Error
}

// Runs lists the runs of a task, newest first.
func (dao *AssistantTaskDAO) Runs(ctx context.Context, taskID uint, page, pageSize int) ([]*model.AssistantTaskRun, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantTaskRun{}).Where("task_id = ?", taskID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	var list []*model.AssistantTaskRun
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// PruneRuns keeps the newest keep runs of a task and deletes the rest.
func (dao *AssistantTaskDAO) PruneRuns(ctx context.Context, taskID uint, keep int) error {
	var ids []uint
	if err := dao.db.WithContext(ctx).Model(&model.AssistantTaskRun{}).Where("task_id = ?", taskID).Order("id DESC").Offset(keep).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return err
	}
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.AssistantTaskRun{}).Error
}
