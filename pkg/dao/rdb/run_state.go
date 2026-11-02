package rdb

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// RunStateDAO stores what the scheduled pipeline remembers about a module between cycles.
type RunStateDAO struct{ db *gorm.DB }

func NewRunStateDAO(db *gorm.DB) *RunStateDAO { return &RunStateDAO{db: db} }

// DB exposes the handle the DAO was built on, for the services that build sibling DAOs from a module DAO.
func (dao *ModuleDAO) DB() *gorm.DB { return dao.db }

// RunState returns the remembered state of a module, or an empty one.
func (dao *RunStateDAO) RunState(ctx context.Context, moduleID uint) (*model.ModuleRunState, error) {
	var st model.ModuleRunState
	err := dao.db.WithContext(ctx).First(&st, moduleID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &model.ModuleRunState{ModuleID: moduleID}, nil
	}
	return &st, err
}

// SaveRunState writes the state of a module.
func (dao *RunStateDAO) SaveRunState(ctx context.Context, st *model.ModuleRunState) error {
	st.UpdatedAt = time.Now()
	return dao.db.WithContext(ctx).Save(st).Error
}
