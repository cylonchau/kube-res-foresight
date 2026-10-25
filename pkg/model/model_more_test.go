package model

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestModelLookupNamesAndDisplayNames(t *testing.T) {
	assert.NotNil(t, GetModelByName("User"))
	assert.Nil(t, GetModelByName("Missing"))
	assert.Equal(t, "", DisplayName(nil))
	assert.Equal(t, "Nick", DisplayName(&User{Username: "user", NickName: "Nick"}))
	assert.Equal(t, "user", DisplayName(&User{Username: "user"}))
	assert.Equal(t, "job", (Job{}).TableName())
	assert.Equal(t, "login_audit_log", (LoginAuditLog{}).TableName())
	assert.Equal(t, "login_failure_record", (LoginFailureRecord{}).TableName())
	assert.Equal(t, "login_captcha_challenge", (LoginCaptchaChallenge{}).TableName())
	assert.Equal(t, "operation_log", (OperationLog{}).TableName())
	assert.Equal(t, "refresh_token_session", (RefreshTokenSession{}).TableName())
	assert.Equal(t, "secret_key", (SecretKey{}).TableName())
	assert.Equal(t, "sys_config", (SysConfig{}).TableName())
	assert.Equal(t, "task_definition", (TaskDefinition{}).TableName())
}
