package rdb

import (
	"context"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// AlgorithmDAO stores the platform-wide setting of each catalog algorithm.
type AlgorithmDAO struct {
	db *gorm.DB
}

func NewAlgorithmDAO(db *gorm.DB) *AlgorithmDAO { return &AlgorithmDAO{db: db} }

// Ensure creates the config row of every catalog algorithm that has none, so the catalog and the configs never
// drift apart. enabledByDefault says which new ones start enabled. It is cheap when nothing is missing.
func (dao *AlgorithmDAO) Ensure(ctx context.Context, ids []string, enabledByDefault func(id string) bool) error {
	var have []string
	if err := dao.db.WithContext(ctx).Model(&model.AlgorithmConfig{}).Pluck("algorithm_id", &have).Error; err != nil {
		return err
	}
	seen := make(map[string]bool, len(have))
	for _, id := range have {
		seen[id] = true
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		c := &model.AlgorithmConfig{AlgorithmID: id, Weight: 1}
		if err := dao.db.WithContext(ctx).Create(c).Error; err != nil {
			return err
		}
		if enabledByDefault != nil && enabledByDefault(id) {
			if err := dao.db.WithContext(ctx).Model(c).Update("enabled", true).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// Get returns the config of a catalog algorithm.
func (dao *AlgorithmDAO) Get(ctx context.Context, algorithmID string) (*model.AlgorithmConfig, error) {
	var c model.AlgorithmConfig
	if err := dao.db.WithContext(ctx).Where("algorithm_id = ?", algorithmID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// All returns every config, ordered by algorithm id.
func (dao *AlgorithmDAO) All(ctx context.Context) ([]*model.AlgorithmConfig, error) {
	var list []*model.AlgorithmConfig
	err := dao.db.WithContext(ctx).Order("algorithm_id ASC").Find(&list).Error
	return list, err
}

// Enabled returns the enabled configs, ordered by algorithm id.
func (dao *AlgorithmDAO) Enabled(ctx context.Context) ([]*model.AlgorithmConfig, error) {
	var list []*model.AlgorithmConfig
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("algorithm_id ASC").Find(&list).Error
	return list, err
}

// Save stores a config.
func (dao *AlgorithmDAO) Save(ctx context.Context, c *model.AlgorithmConfig) error {
	return dao.db.WithContext(ctx).Save(c).Error
}

// Custom returns the algorithms an administrator created: formula algorithms and variants.
func (dao *AlgorithmDAO) Custom(ctx context.Context) ([]*model.AlgorithmConfig, error) {
	var list []*model.AlgorithmConfig
	err := dao.db.WithContext(ctx).Where("formula <> '' OR base <> ''").Order("algorithm_id ASC").Find(&list).Error
	return list, err
}

// Create stores a new config; the unique key refuses an id that is taken.
func (dao *AlgorithmDAO) Create(ctx context.Context, c *model.AlgorithmConfig) error {
	return dao.db.WithContext(ctx).Create(c).Error
}

// Delete removes a config for good: a custom algorithm leaves no row behind, so its id can be used again.
func (dao *AlgorithmDAO) Delete(ctx context.Context, algorithmID string) error {
	return dao.db.WithContext(ctx).Where("algorithm_id = ?", algorithmID).Delete(&model.AlgorithmConfig{}).Error
}
