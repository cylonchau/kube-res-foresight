package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"time"
)

type TaskDefinitionDAO struct {
	db *gorm.DB
}

func NewTaskDefinitionDAO(db *gorm.DB) *TaskDefinitionDAO {
	return &TaskDefinitionDAO{db: db}
}

// ListEnabled returns all active task definitions
func (dao *TaskDefinitionDAO) ListEnabled(ctx context.Context) ([]model.TaskDefinition, error) {
	var tasks []model.TaskDefinition
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Find(&tasks).Error
	return tasks, err
}

// GetByType returns a task definition by its type
func (dao *TaskDefinitionDAO) GetByType(ctx context.Context, taskType string) (*model.TaskDefinition, error) {
	var task model.TaskDefinition
	err := dao.db.WithContext(ctx).Where("task_type = ?", taskType).First(&task).Error
	return &task, err
}

// List returns a paginated list of task definitions
func (dao *TaskDefinitionDAO) List(ctx context.Context, opt *apis.ListOptions) ([]model.TaskDefinition, int64, error) {
	var tasks []model.TaskDefinition
	var total int64
	db := dao.db.WithContext(ctx).Model(&model.TaskDefinition{})
	if opt.Keyword != "" {
		db = db.Where("task_type LIKE ? OR description LIKE ?", "%"+opt.Keyword+"%", "%"+opt.Keyword+"%")
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := db.Offset(int(opt.Offset())).Limit(int(opt.PageSize)).Find(&tasks).Error
	return tasks, total, err
}

// Update partially updates a task definition
func (dao *TaskDefinitionDAO) Update(ctx context.Context, id uint64, data interface{}) error {
	return dao.db.WithContext(ctx).Model(&model.TaskDefinition{}).Where("id = ?", id).Updates(data).Error
}

// ResetSchedule makes a changed cron expression effective on the next scanner tick.
func (dao *TaskDefinitionDAO) ResetSchedule(ctx context.Context, id uint64) error {
	return dao.db.WithContext(ctx).Model(&model.TaskDefinition{}).Where("id = ?", id).Update("next_run_time", nil).Error
}

func (dao *TaskDefinitionDAO) SetNextRunTime(ctx context.Context, id uint, next time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.TaskDefinition{}).
		Where("id = ?", id).Update("next_run_time", next).Error
}

// UpdateRunTimes updates the last and next run times for a task
func (dao *TaskDefinitionDAO) UpdateRunTimes(ctx context.Context, id uint, lastRun, nextRun time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.TaskDefinition{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"last_run_time": lastRun,
			"next_run_time": nextRun,
		}).Error
}

// CreateDefaultIfNotExists seeds a default task definition
func (dao *TaskDefinitionDAO) CreateDefaultIfNotExists(ctx context.Context, task *model.TaskDefinition) error {
	return dao.db.WithContext(ctx).
		Where("task_type = ?", task.TaskType).
		FirstOrCreate(task).Error
}
