package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NotifyTemplateDAO struct {
	db *gorm.DB
}

func NewNotifyTemplateDAO(db *gorm.DB) *NotifyTemplateDAO {
	return &NotifyTemplateDAO{db: db}
}

func (dao *NotifyTemplateDAO) GetByKey(ctx context.Context, key string) (*model.NotifyTemplate, error) {
	var tmpl model.NotifyTemplate
	if err := dao.db.WithContext(ctx).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).First(&tmpl).Error; err != nil {
		return nil, err
	}
	return &tmpl, nil
}

// ListPaginated lists templates, optionally filtered by a keyword matched
// against name or key — same page/page_size/keyword shape as every other
// admin list endpoint in this codebase.
func (dao *NotifyTemplateDAO) ListPaginated(ctx context.Context, page, pageSize int, keyword string) ([]model.NotifyTemplate, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := dao.db.WithContext(ctx).Model(&model.NotifyTemplate{})
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where(clause.Or(
			clause.Like{Column: clause.Column{Name: "name"}, Value: like},
			clause.Like{Column: clause.Column{Name: "key"}, Value: like},
		))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var templates []model.NotifyTemplate
	err := query.Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&templates).Error
	return templates, total, err
}

// Update edits an existing template's editable fields by key. Templates are
// seeded (never admin-created), so there is deliberately no Create/Delete —
// only the fixed, known set of keys registered in migration.EnsureDefaultNotifyTemplates.
func (dao *NotifyTemplateDAO) Update(ctx context.Context, key string, titleTemplate, bodyTemplate, buttonsJSON *string, enabled *bool) error {
	updates := map[string]interface{}{}
	if titleTemplate != nil {
		updates["title_template"] = *titleTemplate
	}
	if bodyTemplate != nil {
		updates["body_template"] = *bodyTemplate
	}
	if buttonsJSON != nil {
		updates["buttons_json"] = *buttonsJSON
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if len(updates) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Model(&model.NotifyTemplate{}).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Updates(updates).Error
}
