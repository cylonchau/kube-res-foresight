package api

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestForwardChannelsAreValidatedManagedAndNeverShowTheirPasswords(t *testing.T) {
	router, _ := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	create := func(body map[string]any) int {
		return performJSON(t, router, http.MethodPost, "/telegram/admin/forward-channels", "", body).Code
	}
	id := makeChannel(t, router, map[string]any{
		"name": "stream", "transport": "redis_stream", "stream": "tg:task", "consumer_group": "task", "consumer_kind": "job", "consumer_ref": "llm_rating",
		"redis_password": "hunter2",
	})
	for name, body := range map[string]map[string]any{
		"no name":       {"transport": "direct", "consumer_kind": "job", "consumer_ref": "x"},
		"no stream":     {"name": "a", "transport": "redis_stream", "consumer_group": "g", "consumer_kind": "external"},
		"no group":      {"name": "b", "transport": "kafka", "brokers": "k:9092", "topic": "t", "consumer_kind": "external"},
		"no consumer":   {"name": "c", "transport": "direct"},
		"duplicate":     {"name": "stream", "transport": "direct", "consumer_kind": "job", "consumer_ref": "x"},
		"bad transport": {"name": "d", "transport": "pigeon", "consumer_kind": "external"},
	} {
		assert.Equal(t, http.StatusBadRequest, create(body), name)
	}

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/forward-channels?transport=redis_stream", "", nil)
	list, total := decodeList[model.TelegramForwardChannel](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.True(t, list[0].HasRedisPassword)
	assert.NotContains(t, rec.Body.String(), "hunter2")
	assert.NotContains(t, rec.Body.String(), "redis_password_enc")

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/forward-channels/%d", id), "", map[string]any{
		"name": "stream", "transport": "redis_stream", "stream": "tg:task2", "consumer_group": "task", "consumer_kind": "job", "consumer_ref": "llm_rating",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/forward-channels/batch-enable", "", map[string]any{"ids": []int64{id}, "enabled": false})
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/forward-channels?enabled=false", "", nil)
	_, total = decodeList[model.TelegramForwardChannel](t, rec.Body.Bytes())
	assert.EqualValues(t, 1, total)

	// A channel rules write to cannot be deleted from under them.
	post := performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules", "", map[string]any{"name": "r", "target_kind": "channel", "channel_id": id})
	require.Equal(t, http.StatusOK, post.Code, post.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/forward-channels/batch-delete", "", map[string]any{"ids": []int64{id}})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	denied, _ := telegramClientAPIRouter(t, []string{"VIEWER"}, time.Second)
	assert.Equal(t, http.StatusForbidden, performJSON(t, denied, http.MethodGet, "/telegram/admin/forward-channels", "", nil).Code)
}
