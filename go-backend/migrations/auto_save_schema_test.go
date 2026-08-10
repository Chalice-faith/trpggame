package migrations

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEnsureAutoSaveUniquenessLeavesCompleteSchemaUntouched(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectMigrationLock(mock, 1)
	expectAutoSaveColumn(mock, true, "STORED GENERATED", "case when `is_auto` then `round_number` else NULL end")
	expectAutoSaveIndex(mock, true)
	expectMigrationUnlock(mock)

	if err := EnsureAutoSaveUniqueness(context.Background(), db); err != nil {
		t.Fatalf("EnsureAutoSaveUniqueness() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func TestEnsureAutoSaveUniquenessAddsMissingColumnAndIndex(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectMigrationLock(mock, 1)
	expectAutoSaveColumn(mock, false, "", "")
	expectAutoSaveIndex(mock, false)
	mock.ExpectExec(regexp.QuoteMeta(addAutoSaveColumnSQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(addAutoSaveIndexSQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectMigrationUnlock(mock)

	if err := EnsureAutoSaveUniqueness(context.Background(), db); err != nil {
		t.Fatalf("EnsureAutoSaveUniqueness() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func TestEnsureAutoSaveUniquenessAddsOnlyMissingIndex(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectMigrationLock(mock, 1)
	expectAutoSaveColumn(mock, true, "STORED GENERATED", "case when is_auto then round_number else null end")
	expectAutoSaveIndex(mock, false)
	mock.ExpectExec(regexp.QuoteMeta(addAutoSaveIndexSQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectMigrationUnlock(mock)

	if err := EnsureAutoSaveUniqueness(context.Background(), db); err != nil {
		t.Fatalf("EnsureAutoSaveUniqueness() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func TestEnsureAutoSaveUniquenessRejectsIncompatibleSchema(t *testing.T) {
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{"column", func(mock sqlmock.Sqlmock) {
			expectAutoSaveColumn(mock, true, "DEFAULT_GENERATED", "round_number")
		}},
		{"index", func(mock sqlmock.Sqlmock) {
			expectAutoSaveColumn(mock, true, "STORED GENERATED", "case when is_auto then round_number else null end")
			mock.ExpectQuery(regexp.QuoteMeta(autoSaveIndexQuery)).WillReturnRows(
				sqlmock.NewRows([]string{"NON_UNIQUE", "SEQ_IN_INDEX", "COLUMN_NAME"}).
					AddRow(1, 1, "room_id"),
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newMigrationTestDB(t)
			expectMigrationLock(mock, 1)
			test.expect(mock)
			expectMigrationUnlock(mock)
			if err := EnsureAutoSaveUniqueness(context.Background(), db); err == nil {
				t.Fatal("incompatible schema was accepted")
			}
			assertMigrationExpectations(t, mock)
		})
	}
}

func TestEnsureAutoSaveUniquenessRequiresMigrationLock(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectMigrationLock(mock, 0)
	if err := EnsureAutoSaveUniqueness(context.Background(), db); err == nil {
		t.Fatal("unavailable migration lock was accepted")
	}
	assertMigrationExpectations(t, mock)
}

func newMigrationTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(
		mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open GORM database: %v", err)
	}
	return db, mock
}

func expectMigrationLock(mock sqlmock.Sqlmock, value int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, 10)")).
		WithArgs(autoSaveMigrationLock).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(value))
}

func expectMigrationUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(autoSaveMigrationLock).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

func expectAutoSaveColumn(mock sqlmock.Sqlmock, exists bool, extra, expression string) {
	rows := sqlmock.NewRows([]string{"EXTRA", "GENERATION_EXPRESSION"})
	if exists {
		rows.AddRow(extra, expression)
	}
	mock.ExpectQuery(regexp.QuoteMeta(autoSaveColumnQuery)).WillReturnRows(rows)
}

func expectAutoSaveIndex(mock sqlmock.Sqlmock, exists bool) {
	rows := sqlmock.NewRows([]string{"NON_UNIQUE", "SEQ_IN_INDEX", "COLUMN_NAME"})
	if exists {
		rows.AddRow(0, 1, "room_id").AddRow(0, 2, "auto_round_number")
	}
	mock.ExpectQuery(regexp.QuoteMeta(autoSaveIndexQuery)).WillReturnRows(rows)
}

func assertMigrationExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
