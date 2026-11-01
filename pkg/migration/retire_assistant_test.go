package migration

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func assistantDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.AssistantConnection{}))
	return db
}

func put(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: key}).Error)
}

// The connection settings of the assistant used to be system parameters. A whole one becomes a connection (the stored key is carried over
// as it is), and the parameters are removed; only the switch stays.
func TestTheConnectionSettingsThatLivedInTheSystemParametersBecomeAConnection(t *testing.T) {
	db := assistantDB(t)
	put(t, db, "assistant.enabled", "true")
	put(t, db, "assistant.endpoint", "https://llm.example.com/v1")
	put(t, db, "assistant.model", "m1")
	put(t, db, "assistant.api_key", "ENCRYPTED-AS-STORED")
	put(t, db, "assistant.request_timeout_seconds", "90")
	put(t, db, "assistant.max_tool_rounds", "99") // outside the range: the default
	put(t, db, "assistant.max_reply_tokens", "2000")

	said, err := retireAssistantParameters(db)
	require.NoError(t, err)
	require.Contains(t, said, "connection")
	var conns []model.AssistantConnection
	require.NoError(t, db.Find(&conns).Error)
	require.Len(t, conns, 1)
	c := conns[0]
	require.Equal(t, "default", c.Name)
	require.Equal(t, "https://llm.example.com/v1", c.Endpoint)
	require.Equal(t, "m1", c.Model)
	require.Equal(t, "ENCRYPTED-AS-STORED", c.APIKey, "the key is carried over as it is stored")
	require.True(t, c.Enabled)
	require.Equal(t, 90, c.TimeoutSeconds)
	require.Equal(t, 6, c.MaxToolRounds)
	require.Equal(t, 2000, c.MaxReplyTokens)

	var keys []string
	require.NoError(t, db.Unscoped().Model(&model.SysConfig{}).Where("config_key LIKE ?", "assistant.%").Pluck("config_key", &keys).Error)
	require.Equal(t, []string{"assistant.enabled"}, keys, "only the switch is left")

	said, err = retireAssistantParameters(db)
	require.NoError(t, err)
	require.Empty(t, said, "nothing is done twice")
	var n int64
	require.NoError(t, db.Model(&model.AssistantConnection{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
}

func TestAnIncompleteSetOfRetiredParametersIsRemovedWithoutInventingAConnection(t *testing.T) {
	db := assistantDB(t)
	put(t, db, "assistant.endpoint", "https://llm.example.com/v1")
	put(t, db, "assistant.model", "")
	put(t, db, "assistant.api_key", "")
	said, err := retireAssistantParameters(db)
	require.NoError(t, err)
	require.NotEmpty(t, said)
	var n int64
	require.NoError(t, db.Model(&model.AssistantConnection{}).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, db.Model(&model.SysConfig{}).Count(&n).Error)
	require.Zero(t, n)

	// an administrator who already made a connection keeps it: the old values do not become a second one
	db = assistantDB(t)
	require.NoError(t, db.Create(&model.AssistantConnection{Name: "mine", Endpoint: "https://a/v1", Model: "m", APIKey: "k", Enabled: true}).Error)
	put(t, db, "assistant.endpoint", "https://old/v1")
	put(t, db, "assistant.model", "old")
	put(t, db, "assistant.api_key", "old-key")
	_, err = retireAssistantParameters(db)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AssistantConnection{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
}
