package model

func init() {
	RegisterModel(&UserContact{})
}

// UserContact is one way to reach a user: a type (email, phone, whatsapp, telegram, wechat, ... any name an administrator
// uses) and its value. The contacts are rows, not columns, so a new kind of contact needs no schema change and a user can have
// several of the same kind.
type UserContact struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UserID uint   `gorm:"not null;index;uniqueIndex:idx_user_contact,priority:1" json:"-"`
	Type   string `gorm:"type:varchar(32);not null;uniqueIndex:idx_user_contact,priority:2" json:"type"`
	Value  string `gorm:"type:varchar(190);not null;uniqueIndex:idx_user_contact,priority:3" json:"value"`
}

func (UserContact) TableName() string { return "user_contact" }
