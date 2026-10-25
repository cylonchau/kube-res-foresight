package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// apiTestDB opens an isolated in-memory SQLite database with the identity tables the API tests share.
func apiTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}))
	return db
}

func decode(t *testing.T, body []byte, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(body, out))
}
