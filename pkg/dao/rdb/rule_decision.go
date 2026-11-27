package rdb

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// RuleDecisionDAO stores the calculations of the fixed formula of the resource analysis (see model.RuleDecision).
type RuleDecisionDAO struct{ db *gorm.DB }

func NewRuleDecisionDAO(db *gorm.DB) *RuleDecisionDAO { return &RuleDecisionDAO{db: db} }

func (dao *RuleDecisionDAO) Create(ctx context.Context, row *model.RuleDecision) error {
	return dao.db.WithContext(ctx).Create(row).Error
}

// Get is one calculation (gorm.ErrRecordNotFound when there is none).
func (dao *RuleDecisionDAO) Get(ctx context.Context, id uint) (*model.RuleDecision, error) {
	var row model.RuleDecision
	if err := dao.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Latest is the newest calculation of a module, or nil when it has none.
func (dao *RuleDecisionDAO) Latest(ctx context.Context, moduleID uint) (*model.RuleDecision, error) {
	var rows []*model.RuleDecision
	if err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("id DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// LatestOfAll is the newest calculation of every module that has one, by module id.
func (dao *RuleDecisionDAO) LatestOfAll(ctx context.Context) (map[uint]*model.RuleDecision, error) {
	var newest []uint
	if err := dao.db.WithContext(ctx).Model(&model.RuleDecision{}).Group("module_id").Pluck("MAX(id)", &newest).Error; err != nil {
		return nil, err
	}
	out := map[uint]*model.RuleDecision{}
	if len(newest) == 0 {
		return out, nil
	}
	var rows []*model.RuleDecision
	if err := dao.db.WithContext(ctx).Where("id IN ?", newest).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ModuleID] = r
	}
	return out, nil
}

// Touch says the calculation was made again and came out the same: it is the newest one still, as of now.
func (dao *RuleDecisionDAO) Touch(ctx context.Context, id uint, at time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.RuleDecision{}).Where("id = ?", id).Update("checked_at", at).Error
}

// List is the calculations of a module, newest first.
func (dao *RuleDecisionDAO) List(ctx context.Context, moduleID uint, page, pageSize int) ([]*model.RuleDecision, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.RuleDecision{}).Where("module_id = ?", moduleID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*model.RuleDecision
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Prune deletes the calculations older than the time (a few at a time), keeping the newest one of every module.
func (dao *RuleDecisionDAO) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	var ids []uint
	keep := dao.db.Model(&model.RuleDecision{}).Select("MAX(id)").Group("module_id")
	if err := dao.db.WithContext(ctx).Model(&model.RuleDecision{}).Where("created_at < ? AND id NOT IN (?)", before, keep).
		Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.RuleDecision{})
	return res.RowsAffected, res.Error
}

// DeleteOfModule removes what a module's calculations were (the module is deleted).
func (dao *RuleDecisionDAO) DeleteOfModule(ctx context.Context, moduleID uint) error {
	return dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Delete(&model.RuleDecision{}).Error
}

// RecommendationsOf are the recommendations that took the calculation, newest first (a few: the same one is taken every cycle).
func (dao *RuleDecisionDAO) RecommendationsOf(ctx context.Context, ruleID uint, limit int) ([]*model.Recommendation, error) {
	var rows []*model.Recommendation
	err := dao.db.WithContext(ctx).Where("rule_decision_id = ?", ruleID).Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ExecutionsFor are the actions of the operator on the recommendations of the module with the versions, newest first.
func (dao *RuleDecisionDAO) ExecutionsFor(ctx context.Context, moduleID uint, versions []int, limit int) ([]*model.ScalingExecution, error) {
	if len(versions) == 0 {
		return nil, nil
	}
	var rows []*model.ScalingExecution
	err := dao.db.WithContext(ctx).Where("module_id = ? AND decision_version IN ?", moduleID, versions).Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
