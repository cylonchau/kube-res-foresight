package timezone

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTheDefaultZoneIsUTCPlusEightAndAnotherCanBeNamed(t *testing.T) {
	t.Setenv(Env, "")
	_, offset := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC).In(Location()).Zone()
	assert.Equal(t, 8*3600, offset)
	_, winter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).In(Location()).Zone()
	assert.Equal(t, 8*3600, winter, "no daylight saving")
	assert.Equal(t, Name, NameOf())

	t.Setenv(Env, "America/New_York")
	assert.Equal(t, "America/New_York", NameOf())
	t.Setenv(Env, "Not/AZone")
	_, offset = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC).In(Location()).Zone()
	assert.Equal(t, 8*3600, offset, "a name that is no zone falls back to UTC+8")
}

func TestApplyMakesTheZoneTheLocalZoneAndALocalTimeIsShownInIt(t *testing.T) {
	t.Setenv(Env, "")
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	Apply()
	utc := time.Date(2026, 10, 29, 0, 58, 0, 0, time.UTC)
	assert.Equal(t, "2026-10-29 08:58:00", utc.Local().Format("2006-01-02 15:04:05"))
	parsed, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-29 08:58", time.Local)
	assert.NoError(t, err)
	assert.True(t, parsed.Equal(utc), "a time typed without an offset is UTC+8")
}
