package rdb

import (
	"context"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// AssistantAuditDAO stores the audit records of the AI assistant.
type AssistantAuditDAO struct {
	db *gorm.DB
}

func NewAssistantAuditDAO(db *gorm.DB) *AssistantAuditDAO { return &AssistantAuditDAO{db: db} }

// Create keeps one record.
func (dao *AssistantAuditDAO) Create(ctx context.Context, a *model.AssistantAudit) error {
	return dao.db.WithContext(ctx).Create(a).Error
}

// AuditFilter narrows the list of records.
type AuditFilter struct {
	// Keyword matches the user name, the model and the question.
	Keyword string
	Status  string
	Source  string
	// Refused lists only the questions in which the model asked for a tool the user may not use.
	Refused bool
	From    *time.Time
	To      *time.Time
}

// List returns the records, newest first.
func (dao *AssistantAuditDAO) List(ctx context.Context, f AuditFilter, page, pageSize int) ([]*model.AssistantAudit, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantAudit{})
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("user_name LIKE ? OR model LIKE ? OR question LIKE ?", like, like, like)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Source != "" {
		q = q.Where("source = ?", f.Source)
	}
	if f.Refused {
		q = q.Where("refused <> '' AND refused <> '[]'")
	}
	if f.From != nil {
		q = q.Where("created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("created_at < ?", *f.To)
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
	var list []*model.AssistantAudit
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// Get returns one record, with its question, answer and steps.
func (dao *AssistantAuditDAO) Get(ctx context.Context, id uint) (*model.AssistantAudit, error) {
	var a model.AssistantAudit
	if err := dao.db.WithContext(ctx).First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// PruneBefore deletes at most limit records older than cutoff and returns how many went.
func (dao *AssistantAuditDAO) PruneBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var ids []uint
	if err := dao.db.WithContext(ctx).Model(&model.AssistantAudit{}).Where("created_at < ?", cutoff).Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.AssistantAudit{})
	return res.RowsAffected, res.Error
}
