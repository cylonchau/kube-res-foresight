package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestTaskDefinitionDAO_ListEnabled(t *testing.T) {
	db, mock := mustSetupMockDB()
	dao := NewTaskDefinitionDAO(db)
	ctx := context.Background()

	rows := sqlmock.NewRows([]string{"id", "task_type", "enabled"}).
		AddRow(1, "sync", true)
	mock.ExpectQuery("^SELECT .* FROM `task_definition` WHERE .*enabled.*=.*").
		WithArgs(true).
		WillReturnRows(rows)

	tasks, err := dao.ListEnabled(ctx)
	if err != nil {
		t.Fatalf("ListEnabled failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
}

func TestTaskDefinitionDAO_UpdateRunTimes(t *testing.T) {
	db, mock := mustSetupMockDB()
	dao := NewTaskDefinitionDAO(db)
	ctx := context.Background()
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectExec("^UPDATE `task_definition` SET .* WHERE .*id.*=.*").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := dao.UpdateRunTimes(ctx, 1, now, now)
	if err != nil {
		t.Fatalf("UpdateRunTimes failed: %v", err)
	}
}
