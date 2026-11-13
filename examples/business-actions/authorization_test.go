package main

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/repo"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain/spec"
	"gochen/errors"
)

// 两个普通 Application 加入同一真实 SQL 事务；不需要 Command 或可逃逸的 Grant。
func TestMultiResourcePreflightAndFormalDenialRollback(t *testing.T) {
	for _, stage := range []string{"preflight", "formal", "success"} {
		t.Run(stage, func(t *testing.T) {
			database, engine, err := openOrders(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := database.Exec(t.Context(), `CREATE TABLE inventory (id INTEGER PRIMARY KEY,version INTEGER NOT NULL,department_id INTEGER NOT NULL,status TEXT NOT NULL,reason TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(t.Context(), `INSERT INTO inventory SELECT * FROM orders WHERE id=101`); err != nil {
				t.Fatal(err)
			}
			ctx, err := demoContext(t.Context(), "approver")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			orders, orderRepo, orderAuth := newCoordinatedApplication(t, engine, "orders", func(context.Context, string) error { return nil })
			inventory, _, inventoryAuth := newCoordinatedApplication(t, engine, "inventory", func(context.Context, string) error {
				calls++
				if stage == "preflight" || (stage == "formal" && calls > 1) {
					return errors.NewCode(errors.Forbidden, "inventory revoked")
				}
				return nil
			})
			callbacks, afterCommit := 0, 0
			execute := func() error {
				// 明确的业务预检：全部通过之前不开始任何业务副作用。
				for i, a := range []auth.IAuthorizer{orderAuth, inventoryAuth} {
					resource := []string{"orders", "inventory"}[i]
					decision, err := a.Authorize(ctx, auth.Request{Resource: resource, Action: approveAction, IDs: []string{"101"}})
					if err != nil {
						return err
					}
					if !decision.Allowed {
						return errors.NewCode(errors.Forbidden, "preflight denied")
					}
				}
				return orderRepo.WithinTx(ctx, func(txCtx context.Context) error {
					if err := contextx.AppendAfterCommit(txCtx, func(context.Context) error { afterCommit++; return nil }); err != nil {
						return err
					}
					for _, application := range []*crud.Application[*Order, int64]{orders, inventory} {
						_, err := application.Execute(txCtx, approveAction, 101, func(_ context.Context, order *Order) error { callbacks++; return order.Approve("approved together") })
						if err != nil {
							return err
						}
					}
					return nil
				})
			}
			err = execute()
			if stage == "success" {
				if err != nil || callbacks != 2 || afterCommit != 1 {
					t.Fatalf("success: callbacks=%d commit=%d err=%v", callbacks, afterCommit, err)
				}
			} else {
				if !errors.Is(err, errors.Forbidden) || afterCommit != 0 {
					t.Fatalf("denial: callbacks=%d commit=%d err=%v", callbacks, afterCommit, err)
				}
				expected := 0
				if stage == "formal" {
					expected = 1
				}
				if callbacks != expected {
					t.Fatalf("callbacks=%d want %d", callbacks, expected)
				}
			}
			for _, table := range []string{"orders", "inventory"} {
				var status string
				var version uint64
				if err := database.QueryRow(ctx, "SELECT status,version FROM "+table+" WHERE id=101").Scan(&status, &version); err != nil {
					t.Fatal(err)
				}
				expected, expectedVersion := "pending", uint64(7)
				if stage == "success" {
					expected, expectedVersion = "approved", 8
				}
				if status != expected || version != expectedVersion {
					t.Fatalf("%s persisted %s/%d want %s/%d", table, status, version, expected, expectedVersion)
				}
			}
		})
	}
}

func newCoordinatedApplication(t *testing.T, engine orm.IOrm, resource string, check auth.ActionCheckerFunc) (*crud.Application[*Order, int64], *repo.Repo[*Order, int64], auth.IAuthorizer) {
	t.Helper()
	repository, err := repo.NewRepo[*Order, int64](engine, resource, repo.WithResourceKind[*Order, int64](resource))
	if err != nil {
		t.Fatal(err)
	}
	options := crud.Options[*Order, int64]{DeleteMode: crud.DeletePurge, Resource: resource, Actions: []auth.Action{{Name: approveAction, Kind: auth.KindUpdate}}}
	descriptor, err := crud.Describe(options, true)
	if err != nil {
		t.Fatal(err)
	}
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: departmentScope{}}}}}, auth.CompileOptions{Checker: check, Metadata: repository.ExecutionDescriptor().Metadata()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := crud.NewApplication(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	// 真实条件读取在执行器内，测试只在组合根保留原始仓储用于事务。
	if _, err := repository.Get(t.Context(), 101, spec.All()); err != nil {
		t.Fatal(err)
	}
	return application, repository, options.Authorizer
}

func TestApprovalPreservesCommittedDTOOnPostCommitFailure(t *testing.T) {
	database, engine, err := openOrders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	repository, err := repo.NewRepo[*Order, int64](engine, "orders", repo.WithResourceKind[*Order, int64]("order"))
	if err != nil {
		t.Fatal(err)
	}
	options := crud.Options[*Order, int64]{DeleteMode: crud.DeletePurge, Resource: "order", Actions: []auth.Action{{Name: approveAction, Kind: auth.KindUpdate}}, Hooks: crud.Hooks[*Order]{approveAction: crud.Stage[*Order]{PostCommit: func(context.Context, *Order) error { return errors.NewCode(errors.Dependency, "notification failed") }}}}
	descriptor, err := crud.Describe(options, true)
	if err != nil {
		t.Fatal(err)
	}
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: departmentScope{}}}}}, auth.CompileOptions{Checker: auth.ActionCheckerFunc(func(context.Context, string) error { return nil }), Metadata: repository.ExecutionDescriptor().Metadata()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := crud.NewApplication(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := demoContext(t.Context(), "approver")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&OrderApprover{orders: application}).Approve(ctx, ApproveOrder{ID: 101, ExpectedVersion: 7, Reason: "approved"})
	if !contextx.IsAfterCommitError(err) || result != (Approval{OrderID: 101, Version: 8, Status: "approved"}) {
		t.Fatalf("committed result lost: %+v %v", result, err)
	}
	stored, err := repository.Get(ctx, 101, spec.All())
	if err != nil || stored.Version != 8 || stored.Status != "approved" {
		t.Fatalf("commit lost: %+v %v", stored, err)
	}
}
