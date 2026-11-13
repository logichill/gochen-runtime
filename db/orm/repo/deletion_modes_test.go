package repo_test

import (
	"context"
	"fmt"
	repopkg "gochen-runtime/db/orm/repo"
	"gochen/app"
	appcrud "gochen/app/crud"
	"gochen/audit"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplicationDeletionModes(t *testing.T) {
	engine := newCapabilityEngine(t)
	ctx := t.Context()
	for _, sql := range []string{
		`CREATE TABLE ordinary_modes (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL, updated_by TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE soft_modes (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, deleted_at TIMESTAMP NULL, deleted_by TEXT NULL, name TEXT NOT NULL)`,
	} {
		if _, err := engine.Database().Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	ordinary, err := repopkg.NewRepo[*capabilityAuditEntity, int64](engine, "ordinary_modes")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := repopkg.NewRepo[*capabilityDeletionEntity, int64](engine, "soft_modes")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("ordinary", func(t *testing.T) {
		checkApplicationDeletionMode(t, ordinary, appcrud.DeletePurge, false, func(id int64) *capabilityAuditEntity {
			return &capabilityAuditEntity{Entity: crud.Entity[int64]{ID: id}, Name: "ordinary"}
		})
	})
	for index, mode := range []appcrud.DeleteMode{appcrud.DeletePurge, appcrud.DeleteSoft} {
		t.Run(fmt.Sprintf("soft mode %d", mode), func(t *testing.T) {
			checkApplicationDeletionMode(t, soft, mode, true, func(id int64) *capabilityDeletionEntity {
				return &capabilityDeletionEntity{Entity: crud.Entity[int64]{ID: int64(index*10) + id}, Name: "soft"}
			})
		})
	}
	if _, err = appcrud.NewApplication(ordinary, appcrud.Options[*capabilityAuditEntity, int64]{Resource: "ordinary", DeleteMode: appcrud.DeleteSoft, Authorizer: auth.Allow()}); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("ordinary entity accepted soft mode: %v", err)
	}
}

func checkApplicationDeletionMode[T domain.IEntity[int64]](t *testing.T, repository *repopkg.Repo[T, int64], mode appcrud.DeleteMode, softCapable bool, entity func(int64) T) {
	t.Helper()
	ctx, err := contextx.WithOperator(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	application, err := appcrud.NewApplication(repository, appcrud.Options[T, int64]{Resource: repository.ResourceKind(), DeleteMode: mode, EnableRestore: softCapable, EnableDeletedQuery: softCapable, Authorizer: auth.Allow()})
	must(err)
	first, active, historical := entity(1), entity(2), entity(3)
	for _, item := range []T{first, active, historical} {
		must(application.Create(ctx, item))
	}
	must(application.Delete(ctx, first.GetID()))
	if mode == appcrud.DeleteSoft {
		stored, err := repository.GetWithDeleted(ctx, first.GetID(), spec.All())
		must(err)
		if !any(stored).(domain.ISoftDeletable).IsDeleted() {
			t.Fatal("Delete did not mark soft state")
		}
		version := stored.GetVersion()
		if err := application.Delete(ctx, first.GetID()); !errors.Is(err, errors.NotFound) {
			t.Fatalf("repeated soft delete: %v", err)
		}
		stored, err = repository.GetWithDeleted(ctx, first.GetID(), spec.All())
		must(err)
		if !any(stored).(domain.ISoftDeletable).IsDeleted() || stored.GetVersion() != version {
			t.Fatal("repeated Delete changed or purged soft row")
		}
		items, err := application.ListDeleted(ctx, 0, 100)
		must(err)
		if len(items) != 1 {
			t.Fatalf("deleted rows=%d", len(items))
		}
		_, err = application.Restore(ctx, first.GetID())
		must(err)
		must(application.Delete(ctx, first.GetID()))
		must(application.Purge(ctx, first.GetID()))
	}
	if _, err := repository.GetWithDeleted(ctx, first.GetID(), spec.All()); !errors.Is(err, errors.NotFound) {
		t.Fatalf("physical deletion left row: %v", err)
	}
	must(application.Purge(ctx, active.GetID()))
	if _, err := repository.GetWithDeleted(ctx, active.GetID(), spec.All()); !errors.Is(err, errors.NotFound) {
		t.Fatalf("purge active left row: %v", err)
	}
	if softCapable {
		// 硬模式仍可显式恢复、查询和清除既有软删除记录。
		stored, err := repository.Get(ctx, historical.GetID(), spec.All())
		must(err)
		must(any(stored).(domain.ISoftDeletableBy).SoftDeleteBy("alice", time.Now()))
		must(repository.Update(ctx, stored, spec.All()))
		items, err := application.ListDeleted(ctx, 0, 100)
		must(err)
		if len(items) != 1 {
			t.Fatalf("historical rows=%d", len(items))
		}
		if mode == appcrud.DeletePurge {
			_, err = application.Restore(ctx, historical.GetID())
			must(err)
			stored, err = repository.Get(ctx, historical.GetID(), spec.All())
			must(err)
			must(any(stored).(domain.ISoftDeletableBy).SoftDeleteBy("alice", time.Now()))
			must(repository.Update(ctx, stored, spec.All()))
		}
		if mode == appcrud.DeletePurge {
			must(application.Delete(ctx, historical.GetID()))
		} else {
			must(application.Purge(ctx, historical.GetID()))
		}
		if _, err := repository.GetWithDeleted(ctx, historical.GetID(), spec.All()); !errors.Is(err, errors.NotFound) {
			t.Fatalf("purge soft row left data: %v", err)
		}
	}
}

type deletionWriteAuthorizer struct {
	action string
	calls  []string
}

func (*deletionWriteAuthorizer) Vocabulary() auth.Vocabulary { return auth.Vocabulary{} }
func (a *deletionWriteAuthorizer) Authorize(_ context.Context, request auth.Request) (auth.Decision, error) {
	a.calls = append(a.calls, request.Action)
	return auth.Decision{Allowed: request.Action == a.action, Spec: spec.All()}, nil
}

type deletionGuardValidator struct {
	mutate func(*capabilityCombinedEntity) error
	target int64
}

func (v deletionGuardValidator) Validate(value any) error {
	entity := value.(*capabilityCombinedEntity)
	if entity.ID == v.target {
		return v.mutate(entity)
	}
	return nil
}

type deletionGuardFixture struct {
	ctx        context.Context
	repository *repopkg.Repo[*capabilityCombinedEntity, int64]
	audit      *repopkg.AuditStore
}

func newDeletionGuardFixture(t *testing.T) *deletionGuardFixture {
	t.Helper()
	engine := newCapabilityEngine(t)
	ctx, err := contextx.WithOperator(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE deletion_guard (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL, updated_by TEXT NOT NULL, deleted_at TIMESTAMP NULL, deleted_by TEXT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE deletion_guard_records (id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL, timestamp TIMESTAMP NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL)`,
	} {
		if _, err = engine.Database().Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := repopkg.NewRepo[*capabilityCombinedEntity, int64](engine, "deletion_guard")
	if err != nil {
		t.Fatal(err)
	}
	store, err := repopkg.NewAuditStore(engine, "deletion_guard_records", &capabilityIDs{}, repository.AuditResource(nil))
	if err != nil {
		t.Fatal(err)
	}
	return &deletionGuardFixture{ctx: ctx, repository: repository, audit: store}
}
func (f *deletionGuardFixture) records(t *testing.T, id int64) []audit.AuditRecord {
	t.Helper()
	rows, err := f.audit.ListAuditRecords(audit.WithAuditResourceKind(f.ctx, "deletion_guard"), fmt.Sprint(id), 0, 100, spec.All())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func (f *deletionGuardFixture) seed(t *testing.T, attribution bool) map[int64]*capabilityCombinedEntity {
	t.Helper()
	original := make(map[int64]*capabilityCombinedEntity)
	for _, id := range []int64{1, 2} {
		entity := &capabilityCombinedEntity{Entity: crud.Entity[int64]{ID: id}, Name: fmt.Sprintf("original-%d", id)}
		if err := f.repository.Create(f.ctx, entity, spec.All()); err != nil {
			t.Fatal(err)
		}
		if attribution {
			actor := "historical"
			entity.DeletedBy = &actor
			if err := f.repository.Update(f.ctx, entity, spec.All()); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		original[id], err = f.repository.GetWithDeleted(f.ctx, id, spec.All())
		if err != nil {
			t.Fatal(err)
		}
	}
	return original
}
func (f *deletionGuardFixture) assertUnchanged(t *testing.T, expected map[int64]*capabilityCombinedEntity) {
	t.Helper()
	for id, before := range expected {
		after, err := f.repository.GetWithDeleted(f.ctx, id, spec.All())
		if err != nil {
			t.Fatal(err)
		}
		if after.IsDeleted() || !reflect.DeepEqual(before, after) {
			t.Fatalf("forbidden update changed persisted entity: before=%+v after=%+v", before, after)
		}
		if records := f.records(t, id); len(records) != 0 {
			t.Fatalf("forbidden update left audit: %+v", records)
		}
	}
}

func deletionGuardMutation(kind string) func(*capabilityCombinedEntity) error {
	return func(entity *capabilityCombinedEntity) error {
		entity.Name = "unauthorized mutation"
		switch kind {
		case "marker":
			return entity.SoftDeleteBy("attacker", time.Now())
		case "attribution":
			by := "attacker"
			entity.DeletedBy = &by
		case "attribution pointer":
			*entity.DeletedBy = "attacker"
		}
		return nil
	}
}

func TestApplicationUpdateCannotChangeDeletionState(t *testing.T) {
	for _, mode := range []appcrud.DeleteMode{appcrud.DeleteSoft, appcrud.DeletePurge} {
		for _, entry := range []string{"Update", "Execute", "ExecuteBulk", "Before Update", "Before ExecuteBulk", "Validator Update", "Validator ExecuteBulk"} {
			for _, mutation := range []string{"marker", "attribution", "attribution pointer"} {
				t.Run(fmt.Sprintf("mode %d/%s/%s", mode, entry, mutation), func(t *testing.T) {
					fixture := newDeletionGuardFixture(t)
					original := fixture.seed(t, mutation == "attribution pointer")
					mutate := deletionGuardMutation(mutation)
					policy := &deletionWriteAuthorizer{action: appcrud.ActionUpdate}
					options := appcrud.Options[*capabilityCombinedEntity, int64]{Resource: "deletion_guard", DeleteMode: mode, Authorizer: policy, Audit: fixture.audit}
					if strings.HasPrefix(entry, "Before") {
						target := int64(1)
						if strings.HasSuffix(entry, "ExecuteBulk") {
							target = 2
						}
						options.Hooks = appcrud.Hooks[*capabilityCombinedEntity]{appcrud.ActionUpdate: appcrud.Stage[*capabilityCombinedEntity]{Before: func(_ context.Context, item *capabilityCombinedEntity) error {
							if item.ID == target {
								return mutate(item)
							}
							return nil
						}}}
					}
					if strings.HasPrefix(entry, "Validator") {
						target := int64(1)
						if strings.HasSuffix(entry, "ExecuteBulk") {
							target = 2
						}
						options.Validator = deletionGuardValidator{mutate: mutate, target: target}
					}
					application, err := appcrud.NewApplication(fixture.repository, options)
					if err != nil {
						t.Fatal(err)
					}
					switch entry {
					case "Update":
						input, getErr := fixture.repository.Get(fixture.ctx, 1, spec.All())
						if getErr != nil {
							t.Fatal(getErr)
						}
						if err = mutate(input); err == nil {
							err = application.Update(fixture.ctx, input)
						}
					case "Execute":
						_, err = application.Execute(fixture.ctx, appcrud.ActionUpdate, 1, func(_ context.Context, item *capabilityCombinedEntity) error { return mutate(item) })
					case "ExecuteBulk":
						_, err = application.ExecuteBulk(fixture.ctx, appcrud.ActionUpdate, []int64{1, 2}, func(_ context.Context, item *capabilityCombinedEntity) error {
							item.Name = "first write must roll back"
							if item.ID == 2 {
								return mutate(item)
							}
							return nil
						})
					case "Before Update", "Validator Update":
						input, getErr := fixture.repository.Get(fixture.ctx, 1, spec.All())
						if getErr != nil {
							t.Fatal(getErr)
						}
						input.Name = "business update before rejection"
						err = application.Update(fixture.ctx, input)
					case "Before ExecuteBulk", "Validator ExecuteBulk":
						_, err = application.ExecuteBulk(fixture.ctx, appcrud.ActionUpdate, []int64{1, 2}, func(_ context.Context, item *capabilityCombinedEntity) error {
							item.Name = "first write must roll back"
							return nil
						})
					}
					if !errors.Is(err, errors.Forbidden) {
						t.Fatalf("deletion-state bypass: %s %s returned %v; actions=%v", entry, mutation, err, policy.calls)
					}
					for _, action := range policy.calls {
						if action != appcrud.ActionUpdate {
							t.Fatalf("unexpected permission: %v", policy.calls)
						}
					}
					fixture.assertUnchanged(t, original)
				})
			}
		}
	}
}

func TestApplicationCreationCannotSupplyDeletionState(t *testing.T) {
	for _, mode := range []appcrud.DeleteMode{appcrud.DeleteSoft, appcrud.DeletePurge} {
		for _, entry := range []string{"Create", "ExecuteNew", "ExecuteNewBulk", "Before Create", "Before ExecuteNewBulk", "Validator Create", "Validator ExecuteNewBulk"} {
			for _, mutation := range []string{"marker", "attribution"} {
				t.Run(fmt.Sprintf("mode %d/%s/%s", mode, entry, mutation), func(t *testing.T) {
					fixture := newDeletionGuardFixture(t)
					policy := &deletionWriteAuthorizer{action: appcrud.ActionCreate}
					mutate := deletionGuardMutation(mutation)
					options := appcrud.Options[*capabilityCombinedEntity, int64]{Resource: "deletion_guard", DeleteMode: mode, Authorizer: policy, Audit: fixture.audit}
					target := int64(1)
					if strings.HasSuffix(entry, "ExecuteNewBulk") {
						target = 2
					}
					if strings.HasPrefix(entry, "Before") {
						options.Hooks = appcrud.Hooks[*capabilityCombinedEntity]{appcrud.ActionCreate: appcrud.Stage[*capabilityCombinedEntity]{Before: func(_ context.Context, item *capabilityCombinedEntity) error {
							if item.ID == target {
								return mutate(item)
							}
							return nil
						}}}
					}
					if strings.HasPrefix(entry, "Validator") {
						options.Validator = deletionGuardValidator{mutate: mutate, target: target}
					}
					application, err := appcrud.NewApplication(fixture.repository, options)
					if err != nil {
						t.Fatal(err)
					}
					first := &capabilityCombinedEntity{Entity: crud.Entity[int64]{ID: 1}, Name: "first"}
					second := &capabilityCombinedEntity{Entity: crud.Entity[int64]{ID: 2}, Name: "second"}
					switch entry {
					case "Create":
						if err = mutate(first); err == nil {
							err = application.Create(fixture.ctx, first)
						}
					case "ExecuteNew":
						_, err = application.ExecuteNew(fixture.ctx, appcrud.ActionCreate, func(context.Context) (*capabilityCombinedEntity, error) { return first, mutate(first) })
					case "ExecuteNewBulk":
						_, err = application.ExecuteNewBulk(fixture.ctx, appcrud.ActionCreate, func(context.Context) ([]*capabilityCombinedEntity, error) {
							return []*capabilityCombinedEntity{first, second}, mutate(second)
						})
					case "Before Create", "Validator Create":
						err = application.Create(fixture.ctx, first)
					case "Before ExecuteNewBulk", "Validator ExecuteNewBulk":
						_, err = application.ExecuteNewBulk(fixture.ctx, appcrud.ActionCreate, func(context.Context) ([]*capabilityCombinedEntity, error) {
							return []*capabilityCombinedEntity{first, second}, nil
						})
					}
					if !errors.Is(err, errors.Forbidden) {
						t.Fatalf("birth-in-deletion state accepted: %s %s returned %v", entry, mutation, err)
					}
					for _, action := range policy.calls {
						if action != appcrud.ActionCreate {
							t.Fatalf("unexpected permission: %v", policy.calls)
						}
					}
					for _, id := range []int64{1, 2} {
						if _, getErr := fixture.repository.GetWithDeleted(fixture.ctx, id, spec.All()); !errors.Is(getErr, errors.NotFound) {
							t.Fatalf("rejected creation left row %d: %v", id, getErr)
						}
						if records := fixture.records(t, id); len(records) != 0 {
							t.Fatalf("rejected creation left audit: %+v", records)
						}
					}
				})
			}
		}
	}
}

func TestApplicationDeletionGuardPreservesAuthorizedWrites(t *testing.T) {
	fixture := newDeletionGuardFixture(t)
	fixture.seed(t, false)
	policy := &deletionWriteAuthorizer{action: appcrud.ActionUpdate}
	application, err := appcrud.NewApplication(fixture.repository, appcrud.Options[*capabilityCombinedEntity, int64]{Resource: "deletion_guard", DeleteMode: appcrud.DeletePurge, Authorizer: policy, Audit: fixture.audit})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = application.Execute(fixture.ctx, appcrud.ActionUpdate, 1, func(_ context.Context, item *capabilityCombinedEntity) error {
		item.Name = "legal callback"
		return nil
	})
	must(err)
	entity, err := fixture.repository.Get(fixture.ctx, 1, spec.All())
	must(err)
	entity.Name = "legal direct update"
	must(application.Update(fixture.ctx, entity))
	_, err = application.ExecuteBulk(fixture.ctx, appcrud.ActionUpdate, []int64{1, 2}, func(_ context.Context, item *capabilityCombinedEntity) error {
		item.Name = "legal bulk update"
		return nil
	})
	must(err)
	for _, id := range []int64{1, 2} {
		stored, err := fixture.repository.GetWithDeleted(fixture.ctx, id, spec.All())
		must(err)
		expected := uint64(1)
		if id == 1 {
			expected = 3
		}
		if stored.IsDeleted() || stored.DeletedAt != nil || stored.DeletedBy != nil || stored.Name != "legal bulk update" || stored.Version != expected {
			t.Fatalf("legal update failed: %+v", stored)
		}
		records := fixture.records(t, id)
		if len(records) != int(expected) {
			t.Fatalf("legal update audits=%d want=%d", len(records), expected)
		}
		for _, record := range records {
			if record.Action != appcrud.ActionUpdate {
				t.Fatalf("wrong legal update audit: %+v", record)
			}
		}
	}
	if err = application.Delete(fixture.ctx, 1); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("update-only role deleted: %v", err)
	}
	lifecycle, err := appcrud.NewApplication(fixture.repository, appcrud.Options[*capabilityCombinedEntity, int64]{Resource: "deletion_guard", DeleteMode: appcrud.DeleteSoft, EnableRestore: true, Authorizer: auth.Allow(), Audit: fixture.audit})
	must(err)
	must(lifecycle.Delete(fixture.ctx, 1))
	deleted, err := fixture.repository.GetWithDeleted(fixture.ctx, 1, spec.All())
	must(err)
	if !deleted.IsDeleted() {
		t.Fatal("dedicated Delete lost soft-delete capability")
	}
	_, err = lifecycle.Restore(fixture.ctx, 1)
	must(err)
	must(lifecycle.Purge(fixture.ctx, 1))
	if _, err = fixture.repository.GetWithDeleted(fixture.ctx, 1, spec.All()); !errors.Is(err, errors.NotFound) {
		t.Fatalf("dedicated Purge left row: %v", err)
	}
	records := fixture.records(t, 1)
	for _, action := range []string{appcrud.ActionDelete, appcrud.ActionRestore, appcrud.ActionPurge} {
		found := false
		for _, record := range records {
			if record.Action == action {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing dedicated lifecycle audit %s: %+v", action, records)
		}
	}
}

type validatorBoundaryEntity struct {
	crud.Entity[int64]
	domain.ChangeInfo
	domain.DeletionState
	Tenant         string `dim:"tenant"`
	Owner          *int64 `dim:"owner"`
	Classification string `dim:"classification"`
	Name           string
}

type validatorBoundaryValidator struct {
	target int64
	mutate func(*validatorBoundaryEntity) error
}

func (v validatorBoundaryValidator) Validate(value any) error {
	entity := value.(*validatorBoundaryEntity)
	if entity.ID == v.target {
		return v.mutate(entity)
	}
	return nil
}

type validatorBoundaryFixture struct {
	ctx        context.Context
	repository *repopkg.Repo[*validatorBoundaryEntity, int64]
	audit      *repopkg.AuditStore
}

func newValidatorBoundaryFixture(t *testing.T, deleted bool) (*validatorBoundaryFixture, map[int64]*validatorBoundaryEntity) {
	t.Helper()
	engine := newCapabilityEngine(t)
	ctx, err := contextx.WithOperator(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE validator_boundary (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL, updated_by TEXT NOT NULL, deleted_at TIMESTAMP NULL, deleted_by TEXT NULL, tenant TEXT NOT NULL, owner INTEGER NULL, classification TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE validator_boundary_records (id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL, timestamp TIMESTAMP NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL, tenant_snapshot TEXT NOT NULL, owner_snapshot INTEGER NULL, classification_snapshot TEXT NOT NULL)`,
	} {
		if _, err := engine.Database().Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := repopkg.NewRepo[*validatorBoundaryEntity, int64](engine, "validator_boundary", repopkg.WithResourceContract[*validatorBoundaryEntity, int64](app.ResourceContract{Mutable: []meta.Dimension{"classification"}, Assignable: []meta.Dimension{"owner"}}))
	if err != nil {
		t.Fatal(err)
	}
	store, err := repopkg.NewAuditStore(engine, "validator_boundary_records", &capabilityIDs{}, repository.AuditResource(map[meta.Dimension]string{"tenant": "tenant_snapshot", "owner": "owner_snapshot", "classification": "classification_snapshot"}))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &validatorBoundaryFixture{ctx: ctx, repository: repository, audit: store}
	original := make(map[int64]*validatorBoundaryEntity)
	for _, id := range []int64{1, 2} {
		owner := id
		entity := &validatorBoundaryEntity{Entity: crud.Entity[int64]{ID: id}, Tenant: "t1", Owner: &owner, Classification: "old", Name: fmt.Sprintf("original-%d", id)}
		if err := repository.Create(ctx, entity, spec.All()); err != nil {
			t.Fatal(err)
		}
		if deleted {
			if err := entity.SoftDeleteBy("alice", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := repository.Update(ctx, entity, spec.All()); err != nil {
				t.Fatal(err)
			}
		}
		original[id], err = repository.GetWithDeleted(ctx, id, spec.All())
		if err != nil {
			t.Fatal(err)
		}
	}
	return fixture, original
}

func (f *validatorBoundaryFixture) assertUnchanged(t *testing.T, expected map[int64]*validatorBoundaryEntity) {
	t.Helper()
	for id, before := range expected {
		after, err := f.repository.GetWithDeleted(f.ctx, id, spec.All())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("validator changed a persisted target: before=%+v after=%+v", before, after)
		}
		records, err := f.audit.ListAuditRecords(audit.WithAuditResourceKind(f.ctx, "validator_boundary"), fmt.Sprint(id), 0, 100, spec.All())
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 0 {
			t.Fatalf("validator failure left audit for %d: %+v", id, records)
		}
	}
}

func TestValidatorsCannotChangeAuthorizedWriteTargets(t *testing.T) {
	for _, mode := range []appcrud.DeleteMode{appcrud.DeleteSoft, appcrud.DeletePurge} {
		for _, entry := range []string{"Restore", "Update", "Execute", "ExecuteBulk"} {
			mutations := []string{"id", "version", "tenant", "owner pointer"}
			if entry == "Restore" {
				mutations = append(mutations, "redelete")
			}
			for _, mutation := range mutations {
				t.Run(fmt.Sprintf("mode %d/%s/%s", mode, entry, mutation), func(t *testing.T) {
					fixture, original := newValidatorBoundaryFixture(t, entry == "Restore")
					target, redirect := int64(1), int64(2)
					if entry == "ExecuteBulk" {
						target, redirect = 2, 1
					}
					action := appcrud.ActionUpdate
					if entry == "Restore" {
						action = appcrud.ActionRestore
					}
					policy := &deletionWriteAuthorizer{action: action}
					options := appcrud.Options[*validatorBoundaryEntity, int64]{Resource: "validator_boundary", DeleteMode: mode, EnableRestore: true, Authorizer: policy, Audit: fixture.audit, Validator: validatorBoundaryValidator{target: target, mutate: func(entity *validatorBoundaryEntity) error {
						switch mutation {
						case "id":
							entity.ID = redirect
						case "version":
							entity.Version++
						case "tenant":
							entity.Tenant = "other"
						case "owner pointer":
							*entity.Owner = 999
						case "redelete":
							return entity.SoftDeleteBy("attacker", time.Now())
						}
						return nil
					}}}
					application, err := appcrud.NewApplication(fixture.repository, options)
					if err != nil {
						t.Fatal(err)
					}
					switch entry {
					case "Restore":
						_, err = application.Restore(fixture.ctx, 1)
					case "Update":
						entity, getErr := fixture.repository.Get(fixture.ctx, 1, spec.All())
						if getErr != nil {
							t.Fatal(getErr)
						}
						entity.Name = "changed"
						err = application.Update(fixture.ctx, entity)
					case "Execute":
						_, err = application.Execute(fixture.ctx, appcrud.ActionUpdate, 1, func(_ context.Context, entity *validatorBoundaryEntity) error { entity.Name = "changed"; return nil })
					case "ExecuteBulk":
						_, err = application.ExecuteBulk(fixture.ctx, appcrud.ActionUpdate, []int64{1, 2}, func(_ context.Context, entity *validatorBoundaryEntity) error {
							entity.Name = "first write must roll back"
							return nil
						})
					}
					expectedCode := errors.Forbidden
					if entry == "Restore" && mutation == "redelete" {
						expectedCode = errors.Conflict
					}
					if !errors.Is(err, expectedCode) {
						t.Fatalf("validator boundary bypass: %s/%s err=%v actions=%v", entry, mutation, err, policy.calls)
					}
					if len(policy.calls) != 1 || policy.calls[0] != action {
						t.Fatalf("wrong authorization actions: %v", policy.calls)
					}
					fixture.assertUnchanged(t, original)
				})
			}
		}
	}
}

func TestValidatorGuardAllowsAuthorizedBusinessChanges(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%t", restore), func(t *testing.T) {
			fixture, original := newValidatorBoundaryFixture(t, restore)
			action := appcrud.ActionUpdate
			if restore {
				action = appcrud.ActionRestore
			}
			policy := &deletionWriteAuthorizer{action: action}
			application, err := appcrud.NewApplication(fixture.repository, appcrud.Options[*validatorBoundaryEntity, int64]{Resource: "validator_boundary", DeleteMode: appcrud.DeletePurge, EnableRestore: true, Authorizer: policy, Audit: fixture.audit, Validator: validatorBoundaryValidator{target: 1, mutate: func(entity *validatorBoundaryEntity) error {
				entity.Name = "validated business"
				if !restore {
					entity.Classification = "new"
				}
				return nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if restore {
				_, err = application.Restore(fixture.ctx, 1)
			} else {
				_, err = application.Execute(fixture.ctx, appcrud.ActionUpdate, 1, func(context.Context, *validatorBoundaryEntity) error { return nil })
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := fixture.repository.GetWithDeleted(fixture.ctx, 1, spec.All())
			if err != nil {
				t.Fatal(err)
			}
			if after.IsDeleted() || after.Name != "validated business" || after.Version != original[1].Version+1 {
				t.Fatalf("authorized mutation rejected: %+v", after)
			}
			if !restore && after.Classification != "new" {
				t.Fatal("mutable dimension was frozen")
			}
			other, err := fixture.repository.GetWithDeleted(fixture.ctx, 2, spec.All())
			if err != nil || !reflect.DeepEqual(other, original[2]) {
				t.Fatalf("other target changed: %v %+v", err, other)
			}
			records, err := fixture.audit.ListAuditRecords(audit.WithAuditResourceKind(fixture.ctx, "validator_boundary"), "1", 0, 100, spec.All())
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Action != action {
				t.Fatalf("wrong audit: %+v", records)
			}
		})
	}
}

type assignedOwnerAuthorizer struct {
	owner int64
	calls []string
}

func (*assignedOwnerAuthorizer) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Assignable: []meta.Dimension{"owner"}}
}
func (a *assignedOwnerAuthorizer) Authorize(_ context.Context, request auth.Request) (auth.Decision, error) {
	a.calls = append(a.calls, request.Action)
	return auth.Decision{Allowed: request.Action == appcrud.ActionCreate, Spec: spec.All(), Assign: []auth.Assignment{{Dimension: "owner", Value: &a.owner}}}, nil
}

func TestAuthoritativeCreationAssignmentSurvivesValidator(t *testing.T) {
	for _, mode := range []appcrud.DeleteMode{appcrud.DeleteSoft, appcrud.DeletePurge} {
		for _, entry := range []string{"Create", "ExecuteNew", "ExecuteNewBulk"} {
			for _, attack := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode %d/%s/attack=%t", mode, entry, attack), func(t *testing.T) {
					fixture, original := newValidatorBoundaryFixture(t, false)
					policy := &assignedOwnerAuthorizer{owner: 100}
					target := int64(3)
					if entry == "ExecuteNewBulk" {
						target = 4
					}
					application, err := appcrud.NewApplication(fixture.repository, appcrud.Options[*validatorBoundaryEntity, int64]{Resource: "validator_boundary", DeleteMode: mode, Authorizer: policy, Audit: fixture.audit, Validator: validatorBoundaryValidator{target: target, mutate: func(entity *validatorBoundaryEntity) error {
						entity.Name = "validated business"
						if attack {
							*entity.Owner = 999
						}
						return nil
					}}})
					if err != nil {
						t.Fatal(err)
					}
					first := &validatorBoundaryEntity{Entity: crud.Entity[int64]{ID: 3}, Tenant: "t1", Classification: "old", Name: "first"}
					second := &validatorBoundaryEntity{Entity: crud.Entity[int64]{ID: 4}, Tenant: "t1", Classification: "old", Name: "second"}
					switch entry {
					case "Create":
						err = application.Create(fixture.ctx, first)
					case "ExecuteNew":
						_, err = application.ExecuteNew(fixture.ctx, appcrud.ActionCreate, func(context.Context) (*validatorBoundaryEntity, error) { return first, nil })
					case "ExecuteNewBulk":
						_, err = application.ExecuteNewBulk(fixture.ctx, appcrud.ActionCreate, func(context.Context) ([]*validatorBoundaryEntity, error) {
							return []*validatorBoundaryEntity{first, second}, nil
						})
					}
					if len(policy.calls) != 1 || policy.calls[0] != appcrud.ActionCreate || policy.owner != 100 {
						t.Fatalf("authorization decision mutated: calls=%v owner=%d", policy.calls, policy.owner)
					}
					fixture.assertUnchanged(t, original)
					ids := []int64{3}
					if entry == "ExecuteNewBulk" {
						ids = append(ids, 4)
					}
					if attack {
						if !errors.Is(err, errors.Forbidden) {
							t.Fatalf("validator overrode authoritative assignment: %v", err)
						}
						for _, id := range ids {
							if _, getErr := fixture.repository.GetWithDeleted(fixture.ctx, id, spec.All()); !errors.Is(getErr, errors.NotFound) {
								t.Fatalf("failed assignment persisted row %d: %v", id, getErr)
							}
							records, listErr := fixture.audit.ListAuditRecords(audit.WithAuditResourceKind(fixture.ctx, "validator_boundary"), fmt.Sprint(id), 0, 100, spec.All())
							if listErr != nil || len(records) != 0 {
								t.Fatalf("failed assignment audit %d: %v %+v", id, listErr, records)
							}
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, id := range ids {
						entity, getErr := fixture.repository.GetWithDeleted(fixture.ctx, id, spec.All())
						if getErr != nil {
							t.Fatal(getErr)
						}
						if entity.Owner == nil || *entity.Owner != 100 {
							t.Fatalf("assignment lost: %+v", entity)
						}
						records, listErr := fixture.audit.ListAuditRecords(audit.WithAuditResourceKind(fixture.ctx, "validator_boundary"), fmt.Sprint(id), 0, 100, spec.All())
						if listErr != nil || len(records) != 1 || records[0].Action != appcrud.ActionCreate {
							t.Fatalf("creation audit %d: %v %+v", id, listErr, records)
						}
					}
				})
			}
		}
	}
}
