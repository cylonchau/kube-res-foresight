package model

import "time"

func init() {
	RegisterModel(&AssistantConversation{})
	RegisterModel(&AssistantMessage{})
}

// AssistantConversation is a conversation of one user with the AI assistant, kept so the user can come back to it. Only its owner reads it:
// no page or API of the platform lists the conversations of another user. It is deleted by its owner, or by the hourly clean-up when it has
// been quiet longer than the system parameter assistant.conversation_retention_days.
type AssistantConversation struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the time of the last message: the list is ordered by it and the clean-up goes by it.
	UpdatedAt time.Time `gorm:"index" json:"updated_at"`

	OwnerID uint `gorm:"not null;index" json:"-"`
	// Title is the first question, cut short, until the user renames the conversation.
	Title        string `gorm:"type:varchar(120);not null;default:''" json:"title"`
	MessageCount int    `gorm:"not null;default:0" json:"message_count"`
}

func (AssistantConversation) TableName() string { return "assistant_conversation" }

// AssistantMessage is one message of a conversation, as the user saw it.
type AssistantMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`

	ConversationID uint   `gorm:"not null;index" json:"-"`
	Role           string `gorm:"type:varchar(10);not null" json:"role"`
	Content        string `gorm:"type:text;not null" json:"content"`
	// Tools and Attachments are the JSON of what the assistant read and the files it offered, kept so a restored answer shows where it came from.
	// Context is the JSON of the page and the object the user was looking at when they asked (the user's messages only): a restored conversation knows what "this module" was.
	Context     string `gorm:"type:text" json:"-"`
	Tools       string `gorm:"type:text" json:"-"`
	Attachments string `gorm:"type:text" json:"-"`
	// Failed marks the reason a question could not be answered: it is shown, and never sent back to the model.
	Failed bool `gorm:"not null;default:false" json:"failed"`
}

func (AssistantMessage) TableName() string { return "assistant_message" }
