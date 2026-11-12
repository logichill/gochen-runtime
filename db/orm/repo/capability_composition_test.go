package repo_test

import (
	"context"
	repopkg "gochen-runtime/db/orm/repo"
	appaudit "gochen/app/audit"
	appcrud "gochen/app/crud"
	appdeletion "gochen/app/deletion"
	"gochen/audit"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"testing"
	"time"
)

func newCapabilityEngine(t *testing.T) orm.IOrm { return newEngine(t) }

// capabilityAuditEntity 的修改归因不携带任何删除字段。
type capabilityAuditEntity struct {
	crud.Entity[int64]
	domain.ChangeInfo
	Name string `json:"name"`
}

// capabilityDeletionEntity 的回收站能力不携带任何修改归因字段。
type capabilityDeletionEntity struct {
	crud.Entity[int64]
	domain.DeletionState
	Name string `json:"name"`
}
type capabilityCombinedEntity struct {
	crud.Entity[int64]
	domain.ChangeInfo
	domain.DeletionState
	Name string `json:"name"`
}
type capabilityIDs struct{ next int64 }

func (g *capabilityIDs) Next() (int64, error) { g.next++; return g.next, nil }

func TestIndependentPersistenceCapabilities(t *testing.T) {
	engine := newCapabilityEngine(t)
	ctx, err := contextx.WithOperator(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`CREATE TABLE capability_audit (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL, updated_by TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE capability_deletion (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, deleted_at TIMESTAMP NULL, deleted_by TEXT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE capability_combined (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL, updated_by TEXT NOT NULL, deleted_at TIMESTAMP NULL, deleted_by TEXT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE capability_records (id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL, timestamp TIMESTAMP NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL)`,
	} {
		_, err := engine.Database().Exec(ctx, sql)
		must(err)
	}
	auditRepo, err := repopkg.NewRepo[*capabilityAuditEntity, int64](engine, "capability_audit")
	must(err)
	deletionRepo, err := repopkg.NewRepo[*capabilityDeletionEntity, int64](engine, "capability_deletion")
	must(err)
	combinedRepo, err := repopkg.NewRepo[*capabilityCombinedEntity, int64](engine, "capability_combined")
	must(err)
	store, err := repopkg.NewAuditStore(engine, "capability_records", &capabilityIDs{}, auditRepo.AuditResource(nil), combinedRepo.AuditResource(nil))
	must(err)
	auditOnly := &capabilityAuditEntity{Entity: crud.Entity[int64]{ID: 1}, Name: "audit only"}
	must(auditRepo.Create(ctx, auditOnly, spec.All()))
	if auditOnly.CreatedBy != "alice" {
		t.Fatalf("missing mutation actor: %+v", auditOnly)
	}
	metadata, err := meta.Describe[*capabilityAuditEntity]()
	must(err)
	snapshot, err := audit.CaptureDimensions(metadata, auditOnly)
	must(err)
	_, err = store.SaveAuditRecord(ctx, audit.AuditRecord{ResourceKind: "capability_audit", EntityID: "1", Action: "create", Operator: "alice", OperationID: "create-1", Dimensions: snapshot})
	must(err)
	historyCtx := audit.WithAuditResourceKind(ctx, "capability_audit")
	records, err := store.ListAuditRecords(historyCtx, "1", 0, 10, spec.All())
	must(err)
	if len(records) != 1 {
		t.Fatalf("audit-only records=%d", len(records))
	}
	// 无软删除字段也能保留完整历史，物理删除后历史仍然可读。
	must(auditRepo.Delete(ctx, auditOnly, spec.All()))
	records, err = store.ListAuditRecords(historyCtx, "1", 0, 10, spec.All())
	must(err)
	if len(records) != 1 {
		t.Fatal("audit incorrectly depends on live entity")
	}
	deletionOnly := &capabilityDeletionEntity{Entity: crud.Entity[int64]{ID: 1}, Name: "deletion only"}
	must(deletionOnly.SoftDeleteBy("untrusted", time.Now()))
	must(deletionRepo.Create(ctx, deletionOnly, spec.All()))
	if deletionOnly.IsDeleted() {
		t.Fatal("create retained client deletion state")
	}
	must(deletionOnly.SoftDeleteBy("alice", time.Now()))
	must(deletionRepo.Update(ctx, deletionOnly, spec.All()))
	_, err = deletionRepo.Get(ctx, 1, spec.All())
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("deleted row visible: %v", err)
	}
	recovered, err := deletionRepo.GetWithDeleted(ctx, 1, spec.All())
	must(err)
	if recovered.DeletedBy == nil || *recovered.DeletedBy != "alice" {
		t.Fatal("deletion actor was coupled to change fields")
	}
	must(recovered.Restore())
	must(deletionRepo.Restore(ctx, recovered, spec.All()))
	must(deletionRepo.Purge(ctx, recovered, spec.All()))
	combined := &capabilityCombinedEntity{Entity: crud.Entity[int64]{ID: 1}, Name: "combined"}
	must(combinedRepo.Create(ctx, combined, spec.All()))
	combinedMetadata, err := meta.Describe[*capabilityCombinedEntity]()
	must(err)
	combinedSnapshot, err := audit.CaptureDimensions(combinedMetadata, combined)
	must(err)
	abort := errors.NewCode(errors.Conflict, "abort write and audit together")
	err = combinedRepo.WithinTx(ctx, func(txCtx context.Context) error {
		entity, err := combinedRepo.Get(txCtx, 1, spec.All())
		if err != nil {
			return err
		}
		if err = entity.SoftDeleteBy("alice", time.Now()); err != nil {
			return err
		}
		if err = combinedRepo.Update(txCtx, entity, spec.All()); err != nil {
			return err
		}
		if _, err = store.SaveAuditRecord(txCtx, audit.AuditRecord{ResourceKind: "capability_combined", EntityID: "1", Action: "delete", Operator: "alice", OperationID: "delete-1", Dimensions: combinedSnapshot}); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, errors.Conflict) {
		t.Fatalf("rollback error=%v", err)
	}
	after, err := combinedRepo.Get(ctx, 1, spec.All())
	must(err)
	if after.IsDeleted() || after.Version != 0 {
		t.Fatalf("rollback left mutation: %+v", after)
	}
	records, err = store.ListAuditRecords(audit.WithAuditResourceKind(ctx, "capability_combined"), "1", 0, 10, spec.All())
	must(err)
	if len(records) != 0 {
		t.Fatal("rollback left audit records")
	}
	// 应用装配同样不把审计和回收站绑定在一起。
	auditApp, err := appcrud.NewApplication[*capabilityAuditEntity, int64](auditRepo, appcrud.Options[*capabilityAuditEntity, int64]{Resource: "capability_audit", Authorizer: auth.Allow(), Audit: store})
	must(err)
	must(auditApp.Create(ctx, &capabilityAuditEntity{Entity: crud.Entity[int64]{ID: 2}, Name: "via application"}))
	reader, err := appaudit.NewReader[int64](store, auditRepo, appaudit.Options{Resource: "capability_audit", Authorizer: auth.Allow()})
	must(err)
	records, err = reader.AuditTrail(ctx, 2, 0, 10)
	must(err)
	if len(records) != 1 {
		t.Fatalf("application audit records=%d", len(records))
	}
	must(auditApp.Delete(ctx, 2))
	records, err = reader.AuditTrail(ctx, 2, 0, 10)
	must(err)
	if len(records) != 2 {
		t.Fatal("history reader lost physically deleted entity")
	}
	deletionApp, err := appdeletion.NewApplication[*capabilityDeletionEntity, int64](deletionRepo, appdeletion.Options[*capabilityDeletionEntity, int64]{Options: appcrud.Options[*capabilityDeletionEntity, int64]{Resource: "capability_deletion", Authorizer: auth.Allow()}})
	must(err)
	must(deletionRepo.Create(ctx, &capabilityDeletionEntity{Entity: crud.Entity[int64]{ID: 2}, Name: "no audit store"}, spec.All()))
	must(deletionApp.Delete(ctx, 2))
	_, err = deletionApp.Restore(ctx, 2)
	must(err)
	must(deletionApp.Delete(ctx, 2))
	must(deletionApp.Purge(ctx, 2))
	failingStore := &capabilityFailingAudit{AuditStore: store}
	combinedApp, err := appdeletion.NewApplication[*capabilityCombinedEntity, int64](combinedRepo, appdeletion.Options[*capabilityCombinedEntity, int64]{Options: appcrud.Options[*capabilityCombinedEntity, int64]{Resource: "capability_combined", Authorizer: auth.Allow(), Audit: failingStore}})
	must(err)
	err = combinedApp.Delete(ctx, 1)
	if !errors.Is(err, errors.Conflict) {
		t.Fatalf("audit failure=%v", err)
	}
	after, err = combinedRepo.Get(ctx, 1, spec.All())
	must(err)
	if after.IsDeleted() || after.Version != 0 {
		t.Fatal("application did not roll back deletion after audit failure")
	}
	records, err = store.ListAuditRecords(audit.WithAuditResourceKind(ctx, "capability_combined"), "1", 0, 10, spec.All())
	must(err)
	if len(records) != 0 {
		t.Fatal("application did not roll back written audit record")
	}

}

// capabilityFailingAudit 验证已写入审计后的失败也能回滚同源事务。
type capabilityFailingAudit struct{ *repopkg.AuditStore }

func (s *capabilityFailingAudit) SaveAuditRecord(ctx context.Context, record audit.AuditRecord) (int64, error) {
	id, err := s.AuditStore.SaveAuditRecord(ctx, record)
	if err != nil {
		return 0, err
	}
	return id, errors.NewCode(errors.Conflict, "injected audit failure")
}
func (s *capabilityFailingAudit) SaveAuditRecords(ctx context.Context, records []audit.AuditRecord) error {
	if err := s.AuditStore.SaveAuditRecords(ctx, records); err != nil {
		return err
	}
	return errors.NewCode(errors.Conflict, "injected audit failure")
}
