package migrations

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestApplyRunsPendingMigrationsInOrderAndRecordsChecksums(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectRunnerStart(mock, nil)
	for _, name := range orderedMigrationNames {
		body := mustMigrationBody(t, name)
		switch name {
		case "004_add_script_chunk_count.sql":
			mock.ExpectQuery(regexp.QuoteMeta(scriptChunkColumnQuery)).
				WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"}))
			mock.ExpectExec(regexp.QuoteMeta(string(body))).WillReturnResult(sqlmock.NewResult(0, 0))
		case "008_add_auto_save_uniqueness.sql":
			expectAutoSaveColumn(mock, false, "", "")
			expectAutoSaveIndex(mock, false)
			mock.ExpectExec(regexp.QuoteMeta(addAutoSaveColumnSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(regexp.QuoteMeta(addAutoSaveIndexSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
		default:
			mock.ExpectExec(regexp.QuoteMeta(string(body))).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		expectMigrationRecord(mock, name, body)
	}
	expectRunnerUnlock(mock)

	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func TestApplyBaselinesExistingUnrecordedIdempotentSchema(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	expectRunnerStart(mock, nil)
	for _, name := range orderedMigrationNames {
		body := mustMigrationBody(t, name)
		switch name {
		case "004_add_script_chunk_count.sql":
			mock.ExpectQuery(regexp.QuoteMeta(scriptChunkColumnQuery)).WillReturnRows(
				sqlmock.NewRows([]string{"DATA_TYPE", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"}).
					AddRow("int", "int unsigned", "NO", "0"),
			)
		case "008_add_auto_save_uniqueness.sql":
			expectAutoSaveColumn(mock, true, "STORED GENERATED", "case when is_auto then round_number else null end")
			expectAutoSaveIndex(mock, true)
		default:
			mock.ExpectExec(regexp.QuoteMeta(string(body))).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		expectMigrationRecord(mock, name, body)
	}
	expectRunnerUnlock(mock)

	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func TestApplySkipsRecordedMigrationsAndRejectsChecksumDrift(t *testing.T) {
	t.Run("all recorded", func(t *testing.T) {
		db, mock := newMigrationTestDB(t)
		records := make(map[string]string, len(orderedMigrationNames))
		for _, name := range orderedMigrationNames {
			records[name] = migrationChecksum(mustMigrationBody(t, name))
		}
		expectRunnerStart(mock, records)
		expectRunnerUnlock(mock)
		if err := Apply(context.Background(), db); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		assertMigrationExpectations(t, mock)
	})

	t.Run("checksum drift", func(t *testing.T) {
		db, mock := newMigrationTestDB(t)
		expectRunnerStart(mock, map[string]string{"001_create_users.sql": "wrong"})
		expectRunnerUnlock(mock)
		if err := Apply(context.Background(), db); err == nil {
			t.Fatal("checksum drift was accepted")
		}
		assertMigrationExpectations(t, mock)
	})
}

func TestApplyRequiresGlobalMigrationLock(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, 10)")).
		WithArgs(migrationLockName).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(0))
	if err := Apply(context.Background(), db); err == nil {
		t.Fatal("unavailable migration lock was accepted")
	}
	assertMigrationExpectations(t, mock)
}

func expectRunnerStart(mock sqlmock.Sqlmock, applied map[string]string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, 10)")).
		WithArgs(migrationLockName).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(createMigrationTableSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"version", "checksum"})
	for _, name := range orderedMigrationNames {
		if checksum, exists := applied[name]; exists {
			rows.AddRow(name, checksum)
		}
	}
	mock.ExpectQuery(regexp.QuoteMeta(listAppliedMigrationsSQL)).WillReturnRows(rows)
}

func expectMigrationRecord(mock sqlmock.Sqlmock, name string, body []byte) {
	mock.ExpectExec(regexp.QuoteMeta(recordMigrationSQL)).
		WithArgs(name, migrationChecksum(body)).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectRunnerUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(migrationLockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

func mustMigrationBody(t *testing.T, name string) []byte {
	t.Helper()
	body, err := migrationFiles.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return body
}

func migrationChecksum(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}
