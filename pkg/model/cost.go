package model

import "time"

// CostPriceRule says what an hour of a node costs: the price an administrator typed in (the platform never guesses one). Which nodes it
// prices is picked by MatchKind: every node, the nodes with a label, or the nodes whose name matches a pattern. A node no rule matches is
// unpriced and is never counted as free. Rows are hard-deleted so the unique name is free again. See package cost.
type CostPriceRule struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name        string  `gorm:"type:varchar(100);not null;uniqueIndex" json:"name" column_setting:"true"`
	MatchKind   string  `gorm:"type:varchar(8);not null;default:'all'" json:"match_kind" column_setting:"true"`
	LabelKey    string  `gorm:"type:varchar(253);not null;default:''" json:"label_key" column_setting:"true"`
	LabelValue  string  `gorm:"type:varchar(253);not null;default:''" json:"label_value" column_setting:"true"`
	NamePattern string  `gorm:"type:varchar(253);not null;default:''" json:"name_pattern" column_setting:"true"`
	HourlyPrice float64 `gorm:"not null;default:0" json:"hourly_price" column_setting:"true"`
	Currency    string  `gorm:"type:varchar(8);not null" json:"currency" column_setting:"true"`
	Priority    int     `gorm:"not null;default:0" json:"priority" column_setting:"true"`
	Enabled     bool    `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
	Remark      string  `gorm:"type:varchar(255)" json:"remark" column_setting:"true"`
}

func (CostPriceRule) TableName() string { return "cost_price_rule" }
