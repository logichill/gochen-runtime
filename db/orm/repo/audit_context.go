package repo

import (
	"context"

	"gochen/domain/audited"
)

func auditResourceKindFromContext(ctx context.Context) string {
	return audited.AuditResourceKind(ctx)
}
