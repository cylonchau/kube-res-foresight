package rdb

import (
	"context"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// AssistantConversationDAO stores the conversations of the AI assistant. Every read and change is bound to the owner: a conversation of
// another user is not found, whatever its id.
type AssistantConversationDAO struct {
	db *gorm.DB
}

func NewAssistantConversationDAO(db *gorm.DB) *AssistantConversationDAO {
	return &AssistantConversationDAO{db: db}
}

// Create starts a conversation.
func (dao *AssistantConversationDAO) Create(ctx context.Context, c *model.AssistantConversation) error {
	return dao.db.WithContext(ctx).Create(c).Error
}

// Count is how many conversations a user has.
func (dao *AssistantConversationDAO) Count(ctx context.Context, owner uint) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AssistantConversation{}).Where("owner_id = ?", owner).Count(&n).Error
	return n, err
}

// Get returns a conversation of the owner; gorm.ErrRecordNotFound when it is not theirs or not there.
func (dao *AssistantConversationDAO) Get(ctx context.Context, owner, id uint) (*model.AssistantConversation, error) {
	var c model.AssistantConversation
	if err := dao.db.WithContext(ctx).Where("id = ? AND owner_id = ?", id, owner).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// List returns the conversations of the owner, the latest first. A keyword matches the title.
func (dao *AssistantConversationDAO) List(ctx context.Context, owner uint, keyword string, page, pageSize int) ([]*model.AssistantConversation, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantConversation{}).Where("owner_id = ?", owner)
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("title LIKE ?", "%"+kw+"%")
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
	var list []*model.AssistantConversation
	err := q.Order("updated_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// Rename sets the title of a conversation of the owner.
func (dao *AssistantConversationDAO) Rename(ctx context.Context, owner, id uint, title string) error {
	res := dao.db.WithContext(ctx).Model(&model.AssistantConversation{}).Where("id = ? AND owner_id = ?", id, owner).UpdateColumn("title", title)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Messages returns the last limit messages of a conversation of the owner, oldest first.
func (dao *AssistantConversationDAO) Messages(ctx context.Context, owner, id uint, limit int) ([]*model.AssistantMessage, error) {
	if _, err := dao.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	var list []*model.AssistantMessage
	if err := dao.db.WithContext(ctx).Where("conversation_id = ?", id).Order("id DESC").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, nil
}

// Append adds messages to a conversation of the owner and moves its last-message time to at. A title is set when the conversation has none.
func (dao *AssistantConversationDAO) Append(ctx context.Context, owner, id uint, at time.Time, title string, msgs ...*model.AssistantMessage) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c model.AssistantConversation
		if err := tx.Where("id = ? AND owner_id = ?", id, owner).First(&c).Error; err != nil {
			return err
		}
		for _, m := range msgs {
			m.ConversationID, m.CreatedAt = id, at
		}
		if len(msgs) > 0 {
			if err := tx.Create(&msgs).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"updated_at": at, "message_count": c.MessageCount + len(msgs)}
		if c.Title == "" && title != "" {
			updates["title"] = title
		}
		return tx.Model(&model.AssistantConversation{}).Where("id = ?", id).UpdateColumns(updates).Error
	})
}

// Delete removes conversations of the owner with their messages; ids of conversations that are not theirs are ignored. It returns how many went.
func (dao *AssistantConversationDAO) Delete(ctx context.Context, owner uint, ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var deleted int64
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mine []uint
		if err := tx.Model(&model.AssistantConversation{}).Where("owner_id = ? AND id IN ?", owner, ids).Pluck("id", &mine).Error; err != nil {
			return err
		}
		if len(mine) == 0 {
			return nil
		}
		if err := tx.Where("conversation_id IN ?", mine).Delete(&model.AssistantMessage{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", mine).Delete(&model.AssistantConversation{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

// PruneBefore deletes the conversations whose last message is older than cutoff, with their messages, at most limit of them in one call (so one
// call is a short transaction); it returns how many went. The clean-up calls it until it returns less than limit.
func (dao *AssistantConversationDAO) PruneBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var deleted int64
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old []uint
		if err := tx.Model(&model.AssistantConversation{}).Where("updated_at < ?", cutoff).Order("updated_at").Limit(limit).Pluck("id", &old).Error; err != nil {
			return err
		}
		if len(old) == 0 {
			return nil
		}
		if err := tx.Where("conversation_id IN ?", old).Delete(&model.AssistantMessage{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", old).Delete(&model.AssistantConversation{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}
