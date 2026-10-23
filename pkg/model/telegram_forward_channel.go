package model

import "time"

// Where a forward channel writes a message, and who reads it from there.
const (
	// ChannelTransportDirect hands the message straight to the consumer's module: a row in the
	// job table, or a started workflow. There is no broker; the job scheduler or the workflow
	// engine is the consumer.
	ChannelTransportDirect = "direct"
	// ChannelTransportRedis appends the message to a Redis Stream; a consumer group reads it.
	ChannelTransportRedis = "redis_stream"
	// ChannelTransportKafka produces the message to a Kafka topic; a consumer group reads it.
	ChannelTransportKafka = "kafka"
)

// What reads a channel and where the message ends up.
const (
	ChannelConsumerJob      = "job"      // creates a job of type ConsumerRef (the Job module runs it)
	ChannelConsumerWorkflow = "workflow" // starts the workflow ConsumerRef (the Workflow engine runs it)
	// ChannelConsumerExternal means a process outside this system reads the channel with the
	// stated consumer group; the TG Agent runs no consumer for it.
	ChannelConsumerExternal = "external"
)

// ChannelFormatV1 is the only message format: the JSON of message.Message.
const ChannelFormatV1 = "telegram.message.v1"

// TelegramForwardChannel is one place the forwarder writes messages to, with everything needed
// to trace a message: where it is written (transport and address), who consumes it (the
// consumer and its group) and which module finally handles it. A forward rule points at a
// channel; one channel can serve many rules, and one message can go to many channels.
type TelegramForwardChannel struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`
	Name      string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"name" column_setting:"true"`
	Transport string    `gorm:"type:varchar(16);not null" json:"transport" column_setting:"true"`

	// Redis Stream. An empty address means the platform Redis of config.yaml.
	RedisAddr        string `gorm:"type:varchar(128);not null;default:''" json:"redis_addr" column_setting:"true"`
	RedisDB          int    `gorm:"not null;default:0" json:"redis_db" column_setting:"true"`
	RedisPasswordEnc string `gorm:"type:text" json:"-" column_setting:"false"`
	Stream           string `gorm:"type:varchar(128);not null;default:''" json:"stream" column_setting:"true"`

	// Kafka.
	Brokers          string `gorm:"type:varchar(512);not null;default:''" json:"brokers" column_setting:"true"`
	Topic            string `gorm:"type:varchar(128);not null;default:''" json:"topic" column_setting:"true"`
	KafkaUsername    string `gorm:"type:varchar(64);not null;default:''" json:"kafka_username" column_setting:"true"`
	KafkaPasswordEnc string `gorm:"type:text" json:"-" column_setting:"false"`
	KafkaTLS         bool   `gorm:"not null;default:false" json:"kafka_tls" column_setting:"true"`

	// Format of the message body written to the stream or topic.
	Format string `gorm:"type:varchar(32);not null;default:'telegram.message.v1'" json:"format" column_setting:"true"`

	// Consumer: who reads the channel and what it does with the message.
	ConsumerKind string `gorm:"type:varchar(12);not null" json:"consumer_kind" column_setting:"true"`
	ConsumerRef  string `gorm:"type:varchar(64);not null;default:''" json:"consumer_ref" column_setting:"true"` // job type or workflow key
	// ConsumerGroup is the Redis consumer group or Kafka group id the consumer reads with.
	ConsumerGroup string `gorm:"type:varchar(64);not null;default:''" json:"consumer_group" column_setting:"true"`
	// ConsumerEnabled switches the built-in consumer of the TG Agent on or off; the messages keep
	// queuing in the stream or topic while it is off.
	ConsumerEnabled bool `gorm:"not null;default:true" json:"consumer_enabled" column_setting:"true"`

	Enabled bool   `gorm:"not null;default:true;index" json:"enabled" column_setting:"true"`
	Remark  string `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`

	// Counters and the last failure, written by the TG Agent.
	Produced       uint64     `gorm:"not null;default:0" json:"produced" column_setting:"true"`
	Consumed       uint64     `gorm:"not null;default:0" json:"consumed" column_setting:"true"`
	LastProducedAt *time.Time `json:"last_produced_at" column_setting:"true"`
	LastConsumedAt *time.Time `json:"last_consumed_at" column_setting:"true"`
	LastError      string     `gorm:"type:text" json:"last_error" column_setting:"true"`

	HasRedisPassword bool `gorm:"-" json:"has_redis_password"`
	HasKafkaPassword bool `gorm:"-" json:"has_kafka_password"`
	// Rules is how many forward rules write to the channel.
	Rules int64 `gorm:"-" json:"rules"`
}

func (TelegramForwardChannel) TableName() string { return "telegram_forward_channel" }

func init() { RegisterModel(&TelegramForwardChannel{}) }
