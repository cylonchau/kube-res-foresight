package rdb

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"gorm.io/gorm"
	"net"
	"regexp"
	"strings"
	"time"
)

// streamName covers Redis keys and Kafka topics: letters, digits and . _ : -
var streamName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,127}$`)

type TelegramForwardChannelDAO struct{ db *gorm.DB }

func NewTelegramForwardChannelDAO(db *gorm.DB) *TelegramForwardChannelDAO {
	return &TelegramForwardChannelDAO{db: db}
}

type TelegramForwardChannelFilter struct {
	Keyword   string
	Transport string
	Consumer  string
	Enabled   *bool
}

// ChannelInput is a channel as the API receives it: the secrets are write-only.
type ChannelInput struct {
	model.TelegramForwardChannel
	RedisPassword string
	KafkaPassword string
	// ClearSecrets removes the stored passwords.
	ClearSecrets bool
}

// Validate normalizes and checks a channel. It says what is missing for the channel to
// be traceable end to end: where it writes, who consumes it, what handles the message.
func (d *TelegramForwardChannelDAO) Validate(c *model.TelegramForwardChannel) error {
	c.Name = strings.TrimSpace(c.Name)
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	c.RedisAddr = strings.TrimSpace(c.RedisAddr)
	c.Stream = strings.TrimSpace(c.Stream)
	c.Brokers = strings.TrimSpace(c.Brokers)
	c.Topic = strings.TrimSpace(c.Topic)
	c.KafkaUsername = strings.TrimSpace(c.KafkaUsername)
	c.ConsumerKind = strings.ToLower(strings.TrimSpace(c.ConsumerKind))
	c.ConsumerRef = strings.TrimSpace(c.ConsumerRef)
	c.ConsumerGroup = strings.TrimSpace(c.ConsumerGroup)
	if c.Format = strings.TrimSpace(c.Format); c.Format == "" {
		c.Format = model.ChannelFormatV1
	}
	if c.Name == "" {
		return invalidInput("the channel needs a name")
	}
	if c.Format != model.ChannelFormatV1 {
		return invalidInput("format must be " + model.ChannelFormatV1)
	}
	switch c.ConsumerKind {
	case model.ChannelConsumerJob, model.ChannelConsumerWorkflow:
		if c.ConsumerRef == "" {
			return invalidInput("the consumer needs a job type or a workflow key to hand the message to")
		}
	case model.ChannelConsumerExternal:
		if c.Transport == model.ChannelTransportDirect {
			return invalidInput("a direct channel has no broker for an outside consumer to read; use a Redis stream or a Kafka topic")
		}
		c.ConsumerRef = ""
	default:
		return invalidInput("consumer must be job, workflow or external")
	}
	switch c.Transport {
	case model.ChannelTransportDirect:
		c.RedisAddr, c.Stream, c.Brokers, c.Topic, c.ConsumerGroup = "", "", "", "", ""
	case model.ChannelTransportRedis:
		if !streamName.MatchString(c.Stream) {
			return invalidInput("a Redis stream needs a stream name (letters, digits and . _ : -)")
		}
		if c.ConsumerGroup == "" {
			return invalidInput("a Redis stream needs the consumer group that reads it")
		}
		if c.RedisAddr != "" {
			if _, _, err := net.SplitHostPort(c.RedisAddr); err != nil {
				return invalidInput("the Redis address must be host:port (empty uses the platform Redis)")
			}
		}
		if c.RedisDB < 0 || c.RedisDB > 15 {
			return invalidInput("the Redis database must be between 0 and 15")
		}
		c.Brokers, c.Topic = "", ""
	case model.ChannelTransportKafka:
		if c.Brokers == "" {
			return invalidInput("a Kafka channel needs its brokers (host:port, comma separated)")
		}
		for _, broker := range strings.Split(c.Brokers, ",") {
			if _, _, err := net.SplitHostPort(strings.TrimSpace(broker)); err != nil {
				return invalidInput(fmt.Sprintf("broker %q must be host:port", strings.TrimSpace(broker)))
			}
		}
		if !streamName.MatchString(c.Topic) {
			return invalidInput("a Kafka channel needs a topic name (letters, digits and . _ -)")
		}
		if c.ConsumerGroup == "" {
			return invalidInput("a Kafka channel needs the consumer group that reads it")
		}
		c.RedisAddr, c.Stream = "", ""
	default:
		return invalidInput("transport must be direct, redis_stream or kafka")
	}
	return nil
}

// checkConsumer makes sure the module a channel hands messages to exists.
func (d *TelegramForwardChannelDAO) checkConsumer(ctx context.Context, c *model.TelegramForwardChannel) error {
	if c.ConsumerKind != model.ChannelConsumerWorkflow {
		return nil
	}
	var n int64
	if err := d.db.WithContext(ctx).Model(&model.WorkflowDef{}).Where("workflow_key = ?", c.ConsumerRef).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return invalidInput(fmt.Sprintf("workflow %q does not exist", c.ConsumerRef))
	}
	return nil
}

func (d *TelegramForwardChannelDAO) applySecrets(c *model.TelegramForwardChannel, in ChannelInput) error {
	if in.ClearSecrets {
		c.RedisPasswordEnc, c.KafkaPasswordEnc = "", ""
	}
	if in.RedisPassword != "" {
		enc, err := cryptoutils.EncryptValue(in.RedisPassword)
		if err != nil {
			return err
		}
		c.RedisPasswordEnc = enc
	}
	if in.KafkaPassword != "" {
		enc, err := cryptoutils.EncryptValue(in.KafkaPassword)
		if err != nil {
			return err
		}
		c.KafkaPasswordEnc = enc
	}
	return nil
}

// Passwords returns the decrypted passwords of a channel.
func (d *TelegramForwardChannelDAO) Passwords(c *model.TelegramForwardChannel) (redisPassword, kafkaPassword string, err error) {
	if c.RedisPasswordEnc != "" {
		if redisPassword, err = cryptoutils.DecryptValue(c.RedisPasswordEnc); err != nil {
			return "", "", err
		}
	}
	if c.KafkaPasswordEnc != "" {
		if kafkaPassword, err = cryptoutils.DecryptValue(c.KafkaPasswordEnc); err != nil {
			return "", "", err
		}
	}
	return redisPassword, kafkaPassword, nil
}

func (d *TelegramForwardChannelDAO) Create(ctx context.Context, in ChannelInput) (*model.TelegramForwardChannel, error) {
	c := in.TelegramForwardChannel
	c.ID = 0
	if err := d.Validate(&c); err != nil {
		return nil, err
	}
	if err := d.checkConsumer(ctx, &c); err != nil {
		return nil, err
	}
	if err := d.applySecrets(&c, in); err != nil {
		return nil, err
	}
	enabled, consumer := c.Enabled, c.ConsumerEnabled
	if err := d.db.WithContext(ctx).Create(&c).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, invalidInput("a channel with this name already exists")
		}
		return nil, err
	}
	// Both flags have a database default of true: store the requested values explicitly.
	if !enabled || !consumer {
		c.Enabled, c.ConsumerEnabled = enabled, consumer
		if err := d.db.WithContext(ctx).Model(&c).Updates(map[string]any{"enabled": enabled, "consumer_enabled": consumer}).Error; err != nil {
			return nil, err
		}
	}
	d.mark(&c)
	return &c, nil
}

func (d *TelegramForwardChannelDAO) mark(c *model.TelegramForwardChannel) {
	c.HasRedisPassword, c.HasKafkaPassword = c.RedisPasswordEnc != "", c.KafkaPasswordEnc != ""
}

// Update replaces a channel with the merged result of the stored row and the input.
func (d *TelegramForwardChannelDAO) Update(ctx context.Context, id int64, in ChannelInput) (*model.TelegramForwardChannel, error) {
	stored, err := d.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	c := in.TelegramForwardChannel
	c.ID, c.CreatedAt = stored.ID, stored.CreatedAt
	c.RedisPasswordEnc, c.KafkaPasswordEnc = stored.RedisPasswordEnc, stored.KafkaPasswordEnc
	c.Produced, c.Consumed, c.LastProducedAt, c.LastConsumedAt, c.LastError = stored.Produced, stored.Consumed, stored.LastProducedAt, stored.LastConsumedAt, ""
	if err := d.Validate(&c); err != nil {
		return nil, err
	}
	if err := d.checkConsumer(ctx, &c); err != nil {
		return nil, err
	}
	if err := d.applySecrets(&c, in); err != nil {
		return nil, err
	}
	if err := d.db.WithContext(ctx).Save(&c).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, invalidInput("a channel with this name already exists")
		}
		return nil, err
	}
	d.mark(&c)
	return &c, nil
}

func (d *TelegramForwardChannelDAO) Get(ctx context.Context, id int64) (*model.TelegramForwardChannel, error) {
	var c model.TelegramForwardChannel
	if err := d.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	d.mark(&c)
	return &c, nil
}

// Many returns the channels with the given ids, keyed by id.
func (d *TelegramForwardChannelDAO) Many(ctx context.Context, ids []int64) (map[int64]model.TelegramForwardChannel, error) {
	out := map[int64]model.TelegramForwardChannel{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []model.TelegramForwardChannel
	if err := d.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

func (d *TelegramForwardChannelDAO) List(ctx context.Context, page, pageSize int, f TelegramForwardChannelFilter) ([]model.TelegramForwardChannel, int64, error) {
	page, pageSize = pageBounds(page, pageSize)
	db := d.db.WithContext(ctx).Model(&model.TelegramForwardChannel{})
	if f.Keyword != "" {
		db = db.Where("name LIKE ? OR stream LIKE ? OR topic LIKE ? OR consumer_ref LIKE ?", like(f.Keyword), like(f.Keyword), like(f.Keyword), like(f.Keyword))
	}
	if f.Transport != "" {
		db = db.Where("transport = ?", f.Transport)
	}
	if f.Consumer != "" {
		db = db.Where("consumer_kind = ?", f.Consumer)
	}
	if f.Enabled != nil {
		db = db.Where("enabled = ?", *f.Enabled)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.TelegramForwardChannel
	if err := db.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	counts := map[int64]int64{}
	var usage []struct {
		ChannelID int64
		N         int64
	}
	if err := d.db.WithContext(ctx).Model(&model.TelegramForwardRule{}).Select("channel_id, COUNT(*) AS n").Where("channel_id > 0").Group("channel_id").Scan(&usage).Error; err == nil {
		for _, u := range usage {
			counts[u.ChannelID] = u.N
		}
	}
	for i := range rows {
		d.mark(&rows[i])
		rows[i].Rules = counts[rows[i].ID]
	}
	return rows, total, nil
}

// ListActive returns the enabled channels, for the forwarder and the consumers.
func (d *TelegramForwardChannelDAO) ListActive(ctx context.Context) ([]model.TelegramForwardChannel, error) {
	var rows []model.TelegramForwardChannel
	err := d.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Find(&rows).Error
	return rows, err
}

// ErrChannelInUse means rules still write to the channel.
var ErrChannelInUse = errors.New("rules still write to this channel; point them elsewhere or delete them first")

func (d *TelegramForwardChannelDAO) Delete(ctx context.Context, ids []int64) (int64, error) {
	var used int64
	if err := d.db.WithContext(ctx).Model(&model.TelegramForwardRule{}).Where("channel_id IN ?", ids).Count(&used).Error; err != nil {
		return 0, err
	}
	if used > 0 {
		return 0, invalidInput(ErrChannelInUse.Error())
	}
	res := d.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.TelegramForwardChannel{})
	return res.RowsAffected, res.Error
}

func (d *TelegramForwardChannelDAO) SetEnabled(ctx context.Context, ids []int64, enabled bool) (int64, error) {
	res := d.db.WithContext(ctx).Model(&model.TelegramForwardChannel{}).Where("id IN ?", ids).UpdateColumn("enabled", enabled)
	return res.RowsAffected, res.Error
}

// RecordProduced / RecordConsumed / RecordError keep the counters the channel page shows. They leave
// updated_at alone: a changed updated_at is how a running consumer learns its channel was edited.
func (d *TelegramForwardChannelDAO) RecordProduced(ctx context.Context, id int64) {
	now := time.Now()
	_ = d.db.WithContext(ctx).Model(&model.TelegramForwardChannel{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"produced": gorm.Expr("produced + 1"), "last_produced_at": &now, "last_error": ""}).Error
}

func (d *TelegramForwardChannelDAO) RecordConsumed(ctx context.Context, id int64) {
	now := time.Now()
	_ = d.db.WithContext(ctx).Model(&model.TelegramForwardChannel{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"consumed": gorm.Expr("consumed + 1"), "last_consumed_at": &now}).Error
}

func (d *TelegramForwardChannelDAO) RecordError(ctx context.Context, id int64, message string) {
	_ = d.db.WithContext(ctx).Model(&model.TelegramForwardChannel{}).Where("id = ?", id).UpdateColumn("last_error", trimTo(message, 500)).Error
}
