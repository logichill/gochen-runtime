package repo_test

import (
	"context"
	"database/sql"
	repopkg "gochen-runtime/db/orm/repo"
	"gochen/app"
	appcrud "gochen/app/crud"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/testkit/contracttest"
	"testing"
)

type entryTransferPolicy struct{ calls int }

func (p *entryTransferPolicy) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Assignable: []meta.Dimension{"department"}}
}
func (p *entryTransferPolicy) Authorize(_ context.Context, request auth.Request) (auth.Decision, error) {
	p.calls++
	decision := auth.Decision{Allowed: true, Spec: spec.All()}
	if request.Action == "receive" {
		decision.Assign = request.Targets
	}
	return decision, nil
}

func TestApplicationTransferRejectsRawAndBoundORMTransactionsBeforeAuthorization(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "context_session", true: "repository_session"}[bound], func(t *testing.T) {
			r, engine := newDimensionSQL(t)
			ctx, err := contextx.WithTenantID(context.Background(), "t1")
			requireNoError(t, err, "tenant")
			entity := &contracttest.Document{Department: 1}
			requireNoError(t, r.Create(ctx, entity, spec.All()), "create")
			session, err := engine.BeginTx(ctx, nil)
			requireNoError(t, err, "raw transaction")
			defer session.Rollback()
			if bound {
				r, err = repopkg.NewRepo[*contracttest.Document, int64](session, "documents",
					repopkg.WithResourceContract[*contracttest.Document, int64](contracttest.Contract()),
					repopkg.WithIsolation[*contracttest.Document, int64](repopkg.IsolationCols{Column: "tenant"}))
				requireNoError(t, err, "bound repository")
			} else {
				ctx, err = orm.WithTxSession(ctx, session, true)
				requireNoError(t, err, "inject raw session")
			}
			policy := &entryTransferPolicy{}
			application, err := appcrud.NewApplication(r, appcrud.Options[*contracttest.Document, int64]{DeleteMode: appcrud.DeleteSoft, Resource: "document", Authorizer: policy,
				Actions: []auth.Action{{Name: "move", Kind: auth.KindTransfer, ReceiveAction: "receive", Dimensions: []meta.Dimension{"department"}}, {Name: "receive", Kind: auth.KindTransfer}}})
			requireNoError(t, err, "application")
			_, err = application.Transfer(ctx, "move", entity.ID, entity.Version, []auth.Assignment{{Dimension: "department", Value: 2}})
			requireCode(t, err, errors.FailedPrecondition, "reject ambient transaction")
			if policy.calls != 0 {
				t.Fatalf("authorization inside transaction: calls=%d", policy.calls)
			}
			stored, err := r.Get(ctx, entity.ID, spec.All())
			requireNoError(t, err, "read unchanged state")
			if stored.Department != 1 || stored.Version != entity.Version {
				t.Fatalf("transfer wrote state: %+v", stored)
			}
		})
	}
}

func TestTransferContractCannotDisableSQLVersioning(t *testing.T) {
	_, err := repopkg.NewRepo[*specItem, int64](newEngine(t), "items",
		repopkg.WithResourceContract[*specItem, int64](app.ResourceContract{Transferable: []meta.Dimension{"owner"}}),
		repopkg.WithoutVersioning[*specItem, int64]())
	requireCode(t, err, errors.InvalidInput, "transfer declaration requires optimistic versioning")
}

func TestTransferReadbackRejectsStorageDrift(t *testing.T) {
	for _, update := range []string{"department = 99", "region = 'drift'", "version = version + 1", "name = 'denied'"} {
		t.Run(update, func(t *testing.T) {
			r, engine := newDimensionSQL(t)
			ctx, err := contextx.WithTenantID(context.Background(), "t1")
			requireNoError(t, err, "tenant")
			entity := &contracttest.Document{Department: 1, Name: "allowed"}
			requireNoError(t, r.Create(ctx, entity, spec.All()), "create")
			execSQL(t, engine, "CREATE TRIGGER storage_drift AFTER UPDATE ON documents BEGIN UPDATE documents SET "+update+" WHERE id = NEW.id; END")
			err = r.WithinTx(ctx, func(txCtx context.Context) error {
				_, err := r.Transfer(txCtx, entity.ID, entity.Version, []crud.DimensionChange{{Dimension: "department", Value: 2}},
					spec.Eq(meta.ByDimension("department"), 1), spec.Eq(meta.ByField("Name"), "allowed"))
				return err
			})
			requireCode(t, err, errors.Internal, "actual state verification")
			stored, err := r.Get(ctx, entity.ID, spec.All())
			requireNoError(t, err, "read rollback")
			if stored.Department != 1 || stored.Region != nil || stored.Name != "allowed" || stored.Version != entity.Version {
				t.Fatalf("drift remained after rollback: %+v", stored)
			}
		})
	}
}

type interleavedSession struct {
	orm.IOrmSession
	interleave func(orm.IModel, context.Context) error
}

func (s interleavedSession) DatasourceID() string {
	id, _ := orm.DatasourceIDOf(s.IOrmSession)
	return id
}

func (s interleavedSession) Model(metadata *orm.ModelMeta) (orm.IModel, error) {
	model, err := s.IOrmSession.Model(metadata)
	if err != nil {
		return nil, err
	}
	return interleavedModel{IModel: model, IModelWithResult: model.(orm.IModelWithResult), interleave: s.interleave}, nil
}

type interleavedModel struct {
	orm.IModel
	orm.IModelWithResult
	interleave func(orm.IModel, context.Context) error
}

func (m interleavedModel) UpdateValuesWithResult(ctx context.Context, values map[string]any, options ...orm.QueryOption) (sql.Result, error) {
	if err := m.interleave(m.IModel, ctx); err != nil {
		return nil, err
	}
	return m.IModel.(orm.IModelWithResult).UpdateValuesWithResult(ctx, values, options...)
}

func TestTransferFinalVersionConditionRejectsInterleavedWrite(t *testing.T) {
	r, _ := newDimensionSQL(t)
	ctx, err := contextx.WithTenantID(context.Background(), "t1")
	requireNoError(t, err, "tenant")
	entity := &contracttest.Document{Department: 1}
	requireNoError(t, r.Create(ctx, entity, spec.All()), "create")
	err = r.WithinTx(ctx, func(txCtx context.Context) error {
		session, _, _ := orm.TxSessionFromContext(txCtx)
		wrapped := interleavedSession{IOrmSession: session, interleave: func(model orm.IModel, ctx context.Context) error {
			// 在来源读取与最终 UPDATE 之间插入遵守版本协议的写入。
			return model.UpdateValues(ctx, map[string]any{"version": entity.Version + 1}, orm.WithWhere("id = ?", entity.ID))
		}}
		txCtx, err := orm.WithTxSession(txCtx, wrapped, false)
		if err != nil {
			return err
		}
		_, err = r.Transfer(txCtx, entity.ID, entity.Version, []crud.DimensionChange{{Dimension: "department", Value: 2}}, spec.Eq(meta.ByDimension("department"), 1), spec.All())
		return err
	})
	requireCode(t, err, errors.Concurrency, "atomic version predicate")
	stored, err := r.Get(ctx, entity.ID, spec.All())
	requireNoError(t, err, "read rollback")
	if stored.Department != 1 || stored.Version != entity.Version {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestTransferProjectTargetConstraintAtPersistence(t *testing.T) {
	for _, invalidation := range []string{"DELETE FROM departments WHERE id = 2", "UPDATE departments SET enabled = 0 WHERE id = 2"} {
		t.Run(invalidation, func(t *testing.T) {
			r, engine := newDimensionSQL(t)
			ctx, err := contextx.WithTenantID(context.Background(), "t1")
			requireNoError(t, err, "tenant")
			entity := &contracttest.Document{Department: 1}
			requireNoError(t, r.Create(ctx, entity, spec.All()), "create")
			execSQL(t, engine, `CREATE TABLE departments (id INTEGER PRIMARY KEY, enabled BOOLEAN NOT NULL)`,
				`INSERT INTO departments VALUES (1, 1), (2, 1)`,
				`CREATE TRIGGER validate_target BEFORE UPDATE OF department ON documents WHEN NOT EXISTS (SELECT 1 FROM departments WHERE id = NEW.department AND enabled = 1) BEGIN SELECT RAISE(ABORT, 'target missing or disabled'); END`)
			// 模拟事务外接收授权完成后，目标被删除或停用。
			execSQL(t, engine, invalidation)
			err = r.WithinTx(ctx, func(txCtx context.Context) error {
				_, err := r.Transfer(txCtx, entity.ID, entity.Version, []crud.DimensionChange{{Dimension: "department", Value: 2}}, spec.All(), spec.All())
				return err
			})
			if err == nil {
				t.Fatal("persisted invalid target")
			}
			stored, err := r.Get(ctx, entity.ID, spec.All())
			requireNoError(t, err, "read rollback")
			if stored.Department != 1 || stored.Version != entity.Version {
				t.Fatalf("stored=%+v", stored)
			}
		})
	}
}
