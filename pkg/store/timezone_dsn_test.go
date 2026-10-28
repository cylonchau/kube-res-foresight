package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTheMySQLConnectionIsToldTheZoneOfThePlatform(t *testing.T) {
	dsn := mysqlDSN("u", "p", "db", 3306, "fcst", "Asia/Shanghai")
	assert.Equal(t, "u:p@tcp(db:3306)/fcst?charset=utf8mb4&parseTime=True&loc=Asia%2FShanghai&time_zone=%27%2B08%3A00%27", dsn)
	assert.Equal(t, "+08:00", mysqlOffset("Asia/Shanghai"))
	assert.Equal(t, "-04:00", mysqlOffset("America/Caracas"))
	assert.Equal(t, "+05:30", mysqlOffset("Asia/Kolkata"))
	assert.Equal(t, "+08:00", mysqlOffset("Not/AZone"))
}
