package model

import (
	"fmt"
	"strings"
	"time"
)

func init() { RegisterModel(&ModuleTrigger{}) }

// Actions of a metric trigger on the replicas.
const (
	// TriggerScaleUp asks for more replicas than the workload runs now (by a number or by a share of what runs).
	TriggerScaleUp = "scale_up"
	// TriggerScaleDown asks for fewer. It is the weakest of the three: it never acts against a scale-up, a protection (OOM, JVM heap) or what the forecast wants.
	TriggerScaleDown = "scale_down"
	// TriggerSet asks for a number of replicas.
	TriggerSet = "set"
)

// TriggerActions lists the actions.
var TriggerActions = []string{TriggerScaleUp, TriggerScaleDown, TriggerSet}

// Units of the amount of a trigger that scales: a number of replicas, or a percentage of the replicas that run.
const (
	TriggerUnitReplicas = "replicas"
	TriggerUnitPercent  = "percent"
)

// Comparisons of a trigger: the value of the metric against the threshold.
const (
	TriggerGT  = "gt"
	TriggerGTE = "gte"
	TriggerLT  = "lt"
	TriggerLTE = "lte"
)

// How the samples of the window of a trigger are reduced to one value.
const (
	TriggerStatLast = "last"
	TriggerStatAvg  = "avg"
	TriggerStatMax  = "max"
	TriggerStatMin  = "min"
)

// TriggerJVMHeap is the metric of a trigger that is no source of the module but a figure the platform works out: the heap used over the heap maximum (see
// the JVM pressure of the module). It needs the sources jvm_heap_used and jvm_heap_max.
const TriggerJVMHeap = "jvm_heap_pressure"

// TriggerRuleGap is the other metric of a trigger that is no source of the module: the replicas the newest Resource Assessment (the fixed formula of the real use) needs, less the
// replicas that run: positive when the formula wants more, negative when it wants fewer. It is read from the stored calculation, so the history is not asked for again.
const TriggerRuleGap = "rule_replicas_gap"

// DefaultTriggerWindowMinutes is the window of a trigger that sets none.
const DefaultTriggerWindowMinutes = 15

// ModuleTrigger is a metric trigger of a module (one of its strategies): while the value of a metric stands on the wrong side of a threshold, the replicas are
// raised, lowered or set. The strategies of a module (the forecast, the periods, the triggers and the fixed count) are combined by fixed rules; see decision.ApplyTriggers
// and the module form. Nothing a trigger asks for goes beyond the replica bounds of the module.
type ModuleTrigger struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"false"`

	ModuleID uint   `gorm:"not null;uniqueIndex:idx_module_trigger,priority:1" json:"module_id" column_setting:"true"`
	Name     string `gorm:"type:varchar(100);not null;uniqueIndex:idx_module_trigger,priority:2" json:"name" column_setting:"true"`
	Enabled  bool   `gorm:"not null" json:"enabled" column_setting:"true"`
	// Priority orders the triggers that ask for different replica counts at once: the higher wins (see decision.ApplyTriggers).
	Priority int `gorm:"not null;default:0" json:"priority" column_setting:"true"`
	// Weight is how much the trigger counts when triggers that scale out and triggers that scale in fire together (see decision.ApplyTriggers): the larger total of
	// weight x how far the metric is past its threshold decides. 1 = counts as one; a metric that is trusted more has a larger weight.
	Weight float64 `gorm:"not null;default:1" json:"weight" column_setting:"true"`
	// Metric is the key of a metric source of the module, or TriggerJVMHeap.
	Metric string `gorm:"type:varchar(32);not null" json:"metric" column_setting:"true"`
	// Stat reduces the samples of the last WindowMinutes to the value that is compared: last, avg, max or min.
	Stat          string  `gorm:"type:varchar(8);not null;default:'avg'" json:"stat" column_setting:"true"`
	WindowMinutes int     `gorm:"not null;default:15" json:"window_minutes" column_setting:"true"`
	Operator      string  `gorm:"type:varchar(4);not null;default:'gt'" json:"operator" column_setting:"true"`
	Threshold     float64 `gorm:"not null;default:0" json:"threshold" column_setting:"true"`
	// Action is what the trigger does; Unit and Amount say how much: scale_up and scale_down add or remove Amount replicas (Unit replicas) or Amount percent of the
	// replicas that run (Unit percent), set takes Amount as the replica count.
	Action string  `gorm:"type:varchar(12);not null;default:'scale_up'" json:"action" column_setting:"true"`
	Unit   string  `gorm:"type:varchar(8);not null;default:'replicas'" json:"unit" column_setting:"true"`
	Amount float64 `gorm:"not null;default:1" json:"amount" column_setting:"true"`
	// The time the trigger is in force: Weekdays (a bit mask, bit n = time.Weekday(n); 0 = every day) between StartMinute and EndMinute of the day in the zone of the
	// module (equal minutes = all day).
	Weekdays    int    `gorm:"not null;default:0" json:"weekdays" column_setting:"true"`
	StartMinute int    `gorm:"not null;default:0" json:"start_minute" column_setting:"true"`
	EndMinute   int    `gorm:"not null;default:0" json:"end_minute" column_setting:"true"`
	Remark      string `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`
}

func (ModuleTrigger) TableName() string { return "module_trigger" }

// Validate checks the trigger and fills its defaults.
func (t *ModuleTrigger) Validate() error {
	t.Name, t.Metric = strings.TrimSpace(t.Name), strings.TrimSpace(t.Metric)
	if t.Name == "" {
		return fmt.Errorf("trigger name is required")
	}
	if t.Metric == "" {
		return fmt.Errorf("trigger metric is required")
	}
	if t.Weight == 0 {
		t.Weight = 1
	}
	if t.Weight < 0.1 || t.Weight > 100 {
		return fmt.Errorf("weight must be between 0.1 and 100")
	}
	if t.Stat == "" {
		t.Stat = TriggerStatAvg
	}
	if !oneOf(t.Stat, []string{TriggerStatLast, TriggerStatAvg, TriggerStatMax, TriggerStatMin}) {
		return fmt.Errorf("stat must be last, avg, max or min")
	}
	if t.WindowMinutes == 0 {
		t.WindowMinutes = DefaultTriggerWindowMinutes
	}
	if t.WindowMinutes < 1 || t.WindowMinutes > 24*60 {
		return fmt.Errorf("window_minutes must be between 1 and 1440")
	}
	if t.Operator == "" {
		t.Operator = TriggerGT
	}
	if !oneOf(t.Operator, []string{TriggerGT, TriggerGTE, TriggerLT, TriggerLTE}) {
		return fmt.Errorf("operator must be gt, gte, lt or lte")
	}
	if t.Action == "" {
		t.Action = TriggerScaleUp
	}
	if !oneOf(t.Action, TriggerActions) {
		return fmt.Errorf("action must be one of %v", TriggerActions)
	}
	if t.Unit == "" {
		t.Unit = TriggerUnitReplicas
	}
	if !oneOf(t.Unit, []string{TriggerUnitReplicas, TriggerUnitPercent}) {
		return fmt.Errorf("unit must be replicas or percent")
	}
	if t.Action == TriggerSet {
		t.Unit = TriggerUnitReplicas
	}
	if t.Amount <= 0 || t.Amount > 100000 {
		return fmt.Errorf("amount must be positive")
	}
	if t.Action == TriggerSet && t.Amount != float64(int(t.Amount)) {
		return fmt.Errorf("the replicas a trigger sets must be a whole number")
	}
	if t.Unit == TriggerUnitPercent && t.Amount > 1000 {
		return fmt.Errorf("a percentage must not exceed 1000")
	}
	if t.Weekdays < 0 || t.Weekdays > 127 {
		return fmt.Errorf("weekdays must be a bit mask between 0 and 127")
	}
	if t.StartMinute < 0 || t.StartMinute > 1439 || t.EndMinute < 0 || t.EndMinute > 1439 {
		return fmt.Errorf("start and end must be minutes of the day (0-1439)")
	}
	if t.Weekdays > 0 && t.StartMinute == t.EndMinute {
		t.StartMinute, t.EndMinute = 0, 0 // the whole day
	}
	return nil
}
