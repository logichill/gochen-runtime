package sqlstore

import (
	"context"

	"gochen-runtime/internal/dbsavepoint"
	"gochen/db"
)

func executeOutboxRecoverableStatement(ctx context.Context, database db.IDatabase, prefix string, fn func() error) error {
	return dbsavepoint.ExecuteRecoverableStatement(ctx, database, "outbox_"+prefix, fn)
}
