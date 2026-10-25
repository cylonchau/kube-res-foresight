package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"testing"
)

func TestUserColumnConfigDAOIsolatesUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.UserColumnConfig{}); err != nil {
		t.Fatal(err)
	}

	dao := NewUserColumnConfigDAO(db)
	ctx := context.Background()
	if err := dao.Save(ctx, 1, "system_user_manage", `[{"key":"name","width":180}]`); err != nil {
		t.Fatal(err)
	}
	if err := dao.Save(ctx, 2, "system_user_manage", `[{"key":"name","width":260}]`); err != nil {
		t.Fatal(err)
	}
	if err := dao.Save(ctx, 1, "system_user_manage", `[{"key":"name","width":200}]`); err != nil {
		t.Fatal(err)
	}

	userOne, err := dao.Get(ctx, 1, "system_user_manage")
	if err != nil {
		t.Fatal(err)
	}
	userTwo, err := dao.Get(ctx, 2, "system_user_manage")
	if err != nil {
		t.Fatal(err)
	}
	assert.Contains(t, userOne.Config, `"width":200`)
	assert.Contains(t, userTwo.Config, `"width":260`)
}
