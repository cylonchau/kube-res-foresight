package job

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestExecuteNow_PersistsAndSucceeds(t *testing.T) {
	db, mock, _ := sqlmock.New()
	gormDB, _ := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{})
	jobDAO := rdb.NewJobDAO(gormDB)

	// Create
	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO `job`").WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectCommit()
	// MarkRunning
	mock.ExpectBegin()
	mock.ExpectExec("^UPDATE `job` SET .* WHERE .*id.*=.*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// MarkSuccess
	mock.ExpectBegin()
	mock.ExpectExec("^UPDATE `job` SET .* WHERE .*id.*=.*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j := &model.Job{JobType: "generic_task", TargetType: "resource", TargetID: 5, IdempotencyKey: "inline-test-1"}
	ran := false
	err := ExecuteNow(context.Background(), jobDAO, j, func(ctx context.Context, job *model.Job) error {
		ran = true
		if job.ID == 0 {
			t.Fatal("job passed to run() must have a real persisted ID")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteNow failed: %v", err)
	}
	if !ran {
		t.Fatal("run() was never called")
	}
	if j.ID != 42 {
		t.Fatalf("expected j.ID to be populated from the insert (42), got %d", j.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

func TestExecuteNow_PersistsAndReturnsErrorOnFailure(t *testing.T) {
	db, mock, _ := sqlmock.New()
	gormDB, _ := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{})
	jobDAO := rdb.NewJobDAO(gormDB)

	// Create
	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO `job`").WillReturnResult(sqlmock.NewResult(43, 1))
	mock.ExpectCommit()
	// MarkRunning
	mock.ExpectBegin()
	mock.ExpectExec("^UPDATE `job` SET .* WHERE .*id.*=.*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// retryOrFail -> ScheduleRetry (RetryCount 0, MaxRetry defaults to 3, so it retries not fails)
	mock.ExpectBegin()
	mock.ExpectExec("^UPDATE `job` SET .* WHERE .*id.*=.*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	wantErr := errors.New("publish TXT record failed")
	j := &model.Job{JobType: "generic_task", TargetType: "resource", TargetID: 5, IdempotencyKey: "inline-test-2"}
	err := ExecuteNow(context.Background(), jobDAO, j, func(ctx context.Context, job *model.Job) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected ExecuteNow to return the run() error so the caller's own flow can react, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}
