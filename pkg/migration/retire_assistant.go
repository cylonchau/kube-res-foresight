package migration

import (
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// retiredAssistantKeys were the connection settings of the AI assistant while they lived in the system parameters. They are a connection
// now (the menu AI assistant); only the switch stays a parameter.
var retiredAssistantKeys = []string{"assistant.endpoint", "assistant.model", "assistant.api_key",
	"assistant.request_timeout_seconds", "assistant.max_tool_rounds", "assistant.max_reply_tokens"}

// retireAssistantParameters turns the retired parameters into a connection (when they held a whole one and there is no connection yet:
// the key is already stored encrypted and is carried over as it is) and removes them. It reports what it did, or "" when there was nothing.
func retireAssistantParameters(db *gorm.DB) (string, error) {
	var rows []model.SysConfig
	if err := db.Unscoped().Where("config_key IN ?", retiredAssistantKeys).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	values := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.DeletedAt.Valid {
			continue
		}
		values[r.ConfigKey] = strings.TrimSpace(r.ConfigValue)
	}
	said := "removed the assistant connection settings from the system parameters"
	var existing int64
	if err := db.Model(&model.AssistantConnection{}).Count(&existing).Error; err != nil {
		return "", err
	}
	if values["assistant.endpoint"] != "" && values["assistant.model"] != "" && values["assistant.api_key"] != "" && existing == 0 {
		number := func(key string, lo, hi, fallback int) int {
			n, err := strconv.Atoi(values[key])
			if err != nil || n < lo || n > hi {
				return fallback
			}
			return n
		}
		conn := &model.AssistantConnection{Name: "default", Endpoint: values["assistant.endpoint"], Model: values["assistant.model"], APIKey: values["assistant.api_key"], Enabled: true,
			TimeoutSeconds: number("assistant.request_timeout_seconds", assistant.MinTimeoutSeconds, assistant.MaxTimeoutSeconds, assistant.DefaultTimeoutSeconds),
			MaxToolRounds:  number("assistant.max_tool_rounds", assistant.MinMaxToolRounds, assistant.MaxMaxToolRounds, assistant.DefaultMaxToolRounds),
			MaxReplyTokens: number("assistant.max_reply_tokens", assistant.MinMaxReplyTokens, assistant.MaxMaxReplyTokens, assistant.DefaultMaxReplyTokens),
			Remark:         "moved from the system parameters"}
		if err := db.Create(conn).Error; err != nil {
			return "", err
		}
		said = "moved the assistant connection settings from the system parameters into the connection \"default\""
	}
	if err := db.Unscoped().Where("config_key IN ?", retiredAssistantKeys).Delete(&model.SysConfig{}).Error; err != nil {
		return "", err
	}
	return said, nil
}
