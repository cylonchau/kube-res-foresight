package rdb

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPermissionDAO_GetAll(t *testing.T) {
	db, mock, err := setupMockDB()
	assert.NoError(t, err)
	dao := NewPermissionDAO(db)
	ctx := context.Background()

	rows := sqlmock.NewRows([]string{"id", "name", "code", "type"}).
		AddRow(1, "View User", "system:user:view", "MENU").
		AddRow(2, "Create User", "system:user:create", "BUTTON")

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `permissions` WHERE `permissions`.`deleted_at` IS NULL ORDER BY sort asc")).
		WillReturnRows(rows)

	perms, err := dao.GetAll(ctx)
	assert.NoError(t, err)
	assert.Len(t, perms, 2)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionDAO_GetByType(t *testing.T) {
	db, mock, err := setupMockDB()
	assert.NoError(t, err)
	dao := NewPermissionDAO(db)
	ctx := context.Background()

	rows := sqlmock.NewRows([]string{"id", "name", "code", "type"}).
		AddRow(1, "View User", "system:user:view", "MENU")

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `permissions` WHERE (type = ? AND status = ?) AND `permissions`.`deleted_at` IS NULL ORDER BY sort asc")).
		WithArgs("MENU", true).
		WillReturnRows(rows)

	perms, err := dao.GetByType(ctx, "MENU")
	assert.NoError(t, err)
	assert.Len(t, perms, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionDAO_Create(t *testing.T) {
	db, mock, err := setupMockDB()
	assert.NoError(t, err)
	dao := NewPermissionDAO(db)
	ctx := context.Background()

	perm := &model.Permission{
		Name: "Test Permission",
		Code: "test:perm",
		Type: "MENU",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `permissions`")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = dao.Create(ctx, perm)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionDAO_Delete(t *testing.T) {
	db, mock, err := setupMockDB()
	assert.NoError(t, err)
	dao := NewPermissionDAO(db)
	ctx := context.Background()

	mock.ExpectBegin()
	// GORM SOFT Delete uses UPDATE
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `permissions` SET `deleted_at`=? WHERE `permissions`.`id` = ? AND `permissions`.`deleted_at` IS NULL")).
		WithArgs(sqlmock.AnyArg(), 1).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = dao.Delete(ctx, 1)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
