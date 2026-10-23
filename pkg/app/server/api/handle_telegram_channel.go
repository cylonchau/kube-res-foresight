package api

import (
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegramagent"
	"github.com/gin-gonic/gin"
)

func channelFrom(req apis.TelegramForwardChannelReq) rdb.ChannelInput {
	row := model.TelegramForwardChannel{
		Name: req.Name, Transport: req.Transport, RedisAddr: req.RedisAddr, RedisDB: req.RedisDB, Stream: req.Stream,
		Brokers: req.Brokers, Topic: req.Topic, KafkaUsername: req.KafkaUsername, KafkaTLS: req.KafkaTLS, Format: req.Format,
		ConsumerKind: req.ConsumerKind, ConsumerRef: req.ConsumerRef, ConsumerGroup: req.ConsumerGroup,
		Enabled: true, ConsumerEnabled: true, Remark: req.Remark,
	}
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	if req.ConsumerEnabled != nil {
		row.ConsumerEnabled = *req.ConsumerEnabled
	}
	return rdb.ChannelInput{TelegramForwardChannel: row, RedisPassword: req.RedisPassword, KafkaPassword: req.KafkaPassword, ClearSecrets: req.ClearSecrets}
}

func (r *TelegramBotRouter) ListChannels(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramForwardChannelListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.Channels.List(c.Request.Context(), int(req.Page), int(req.PageSize), rdb.TelegramForwardChannelFilter{
		Keyword: req.Keyword, Transport: req.Transport, Consumer: req.Consumer, Enabled: req.Enabled,
	})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (r *TelegramBotRouter) CreateChannel(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramForwardChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Channels.Create(c.Request.Context(), channelFrom(req))
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) UpdateChannel(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	var req apis.TelegramForwardChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Channels.Update(c.Request.Context(), id, channelFrom(req))
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) DeleteChannels(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Channels.Delete(c.Request.Context(), req.IDs)
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": n})
}

func (r *TelegramBotRouter) EnableChannels(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBatchEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Channels.SetEnabled(c.Request.Context(), req.IDs, req.Enabled)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"updated": n})
}

// TestChannel tries the broker of a channel and reports whether it can be reached.
func (r *TelegramBotRouter) TestChannel(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	ch, err := r.Channels.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	platform := func() config.RedisConfig {
		if config.CONFIG != nil {
			return config.CONFIG.Redis
		}
		return config.RedisConfig{}
	}
	if err := telegramagent.CheckChannel(c.Request.Context(), r.Channels, platform, *ch); err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"ok": true})
}
