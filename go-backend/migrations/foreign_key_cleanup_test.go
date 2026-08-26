package migrations

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRemoveLegacyForeignKeysOnConnectionDropsKnownObjects(t *testing.T) {
	db, mock := newMigrationTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn(): %v", err)
	}
	defer connection.Close()

	for _, foreignKey := range legacyForeignKeys {
		mock.ExpectQuery(regexp.QuoteMeta(foreignKeyExistsQuery)).
			WithArgs(foreignKey.table, foreignKey.name).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta(dropForeignKeySQL(foreignKey))).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, index := range legacyRelationshipIndexes {
		mock.ExpectQuery(regexp.QuoteMeta(indexExistsQuery)).
			WithArgs(index.table, index.name).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta(dropIndexSQL(index))).WillReturnResult(sqlmock.NewResult(0, 0))
	}

	if err := removeLegacyForeignKeysOnConnection(context.Background(), connection); err != nil {
		t.Fatalf("removeLegacyForeignKeysOnConnection() error = %v", err)
	}
	assertMigrationExpectations(t, mock)
}

func expectNoLegacyRelationshipObjects(mock sqlmock.Sqlmock) {
	for _, foreignKey := range legacyForeignKeys {
		mock.ExpectQuery(regexp.QuoteMeta(foreignKeyExistsQuery)).
			WithArgs(foreignKey.table, foreignKey.name).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
	for _, index := range legacyRelationshipIndexes {
		mock.ExpectQuery(regexp.QuoteMeta(indexExistsQuery)).
			WithArgs(index.table, index.name).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
}
