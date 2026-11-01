package rdb

import (
	"context"
	"strings"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

// AssistantConnectionDAO stores the language model connections of the AI assistant.
type AssistantConnectionDAO struct {
	db *gorm.DB
}

func NewAssistantConnectionDAO(db *gorm.DB) *AssistantConnectionDAO {
	return &AssistantConnectionDAO{db: db}
}

func (dao *AssistantConnectionDAO) Get(ctx context.Context, id uint) (*model.AssistantConnection, error) {
	var c model.AssistantConnection
	if err := dao.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// Default is the connection that answers when none is named: the enabled one marked as the default, else the oldest enabled one (a
// manual edit, or the deletion of the default, can leave none marked). Nil when nothing is enabled.
func (dao *AssistantConnectionDAO) Default(ctx context.Context) (*model.AssistantConnection, error) {
	var c model.AssistantConnection
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("is_default DESC, id ASC").Limit(1).Find(&c).Error
	if err != nil || c.ID == 0 {
		return nil, err
	}
	return &c, nil
}

// Count is how many connections there are.
func (dao *AssistantConnectionDAO) Count(ctx context.Context) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AssistantConnection{}).Count(&n).Error
	return n, err
}

// Options lists the enabled connections, for a selector.
func (dao *AssistantConnectionDAO) Options(ctx context.Context) ([]*model.AssistantConnection, error) {
	var list []*model.AssistantConnection
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("is_default DESC, id ASC").Find(&list).Error
	return list, err
}

// List returns the connections, newest first, filtered by a keyword (name, description, endpoint, model) and by whether they are enabled.
func (dao *AssistantConnectionDAO) List(ctx context.Context, keyword string, enabled *bool, page, pageSize int) ([]*model.AssistantConnection, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantConnection{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR remark LIKE ? OR endpoint LIKE ? OR model LIKE ?", like, like, like, like)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
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
	var list []*model.AssistantConnection
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

func nameTaken(tx *gorm.DB, table any, name string, exceptID uint) (bool, error) {
	var n int64
	q := tx.Model(table).Where("name = ?", name)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	err := q.Count(&n).Error
	return n > 0, err
}

// Save creates (ID 0) or updates a connection. The name is unique. Making one the default makes the others not: there is one default. It
// is one transaction, so a failure leaves the default as it was.
func (dao *AssistantConnectionDAO) Save(ctx context.Context, c *model.AssistantConnection) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if taken, err := nameTaken(tx, &model.AssistantConnection{}, c.Name, c.ID); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		if err := tx.Save(c).Error; err != nil {
			return err
		}
		if c.IsDefault {
			return tx.Model(&model.AssistantConnection{}).Where("id <> ?", c.ID).Update("is_default", false).Error
		}
		return nil
	})
}

// Delete removes connections for good; the skills that named one go back to the default model.
func (dao *AssistantConnectionDAO) Delete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AssistantSkill{}).Where("llm_id IN ?", ids).Update("llm_id", 0).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Delete(&model.AssistantConnection{}).Error
	})
}

// AssistantSkillDAO stores the skills of the AI assistant.
type AssistantSkillDAO struct {
	db *gorm.DB
}

func NewAssistantSkillDAO(db *gorm.DB) *AssistantSkillDAO { return &AssistantSkillDAO{db: db} }

func (dao *AssistantSkillDAO) Get(ctx context.Context, id uint) (*model.AssistantSkill, error) {
	var s model.AssistantSkill
	if err := dao.db.WithContext(ctx).First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// All returns every skill row, oldest first.
func (dao *AssistantSkillDAO) All(ctx context.Context) ([]*model.AssistantSkill, error) {
	var list []*model.AssistantSkill
	err := dao.db.WithContext(ctx).Order("id ASC").Find(&list).Error
	return list, err
}

// Save creates (ID 0) or updates a skill; the name is unique.
func (dao *AssistantSkillDAO) Save(ctx context.Context, s *model.AssistantSkill) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if taken, err := nameTaken(tx, &model.AssistantSkill{}, s.Name, s.ID); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		return tx.Save(s).Error
	})
}

// EnsureBuiltin gives each built-in skill its row (enabled, on the default model) when it has none, and keeps its words up to date; what an
// administrator changed (enabled, the model) is never touched.
func (dao *AssistantSkillDAO) EnsureBuiltin(ctx context.Context, builtin []*model.AssistantSkill) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, b := range builtin {
			var row model.AssistantSkill
			if err := tx.Where("name = ?", b.Name).Limit(1).Find(&row).Error; err != nil {
				return err
			}
			if row.ID == 0 {
				row = *b
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&row).Updates(map[string]any{"title": b.Title, "description": b.Description, "builtin": true, "arg_mode": b.ArgMode, "arg_hint": b.ArgHint}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete removes custom skills for good; a built-in one cannot be deleted.
func (dao *AssistantSkillDAO) Delete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("id IN ? AND builtin = ?", ids, false).Delete(&model.AssistantSkill{}).Error
}
