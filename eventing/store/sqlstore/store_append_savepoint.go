package sqlstore

import (
	"context"

	"gochen-runtime/internal/dbsavepoint"
	"gochen/db"
)

func executeRecoverableStatement(ctx context.Context, database db.IDatabase, prefix string, fn func() error) error {
	return dbsavepoint.ExecuteRecoverableStatement(ctx, database, prefix, fn)
}
