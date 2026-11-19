package store

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// the statements of a request are added to its span, whatever the operation, so the diagnostics can say how much of a slow route is the database
func TestTheStatementsOfARequestAreAddedToItsSpan(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:spanstore?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	StartDiagnostics(db, sqlDB)
	type thing struct {
		ID   uint
		Name string
	}
	require.NoError(t, db.AutoMigrate(&thing{}))

	ctx, span := reqstat.WithSpan(context.Background())
	tx := db.WithContext(ctx)
	require.NoError(t, tx.Create(&thing{Name: "a"}).Error)
	var got []thing
	require.NoError(t, tx.Find(&got).Error)
	require.NoError(t, tx.Model(&thing{}).Where("name = ?", "a").Update("name", "b").Error)
	require.NoError(t, tx.Where("name = ?", "b").Delete(&thing{}).Error)
	_, calls, _, _ := span.Totals()
	assert.EqualValues(t, 4, calls, "an insert, a select, an update and a delete")

	before := calls
	require.NoError(t, db.Find(&got).Error) // no request: nothing is added, nothing breaks
	_, after, _, _ := span.Totals()
	assert.Equal(t, before, after)
}
