// 演示 L4 治理：把策略快照落到 SQL 存储，并以 Authorizer Decorator 装配。
//
// 关键点：
//   - 治理装饰的是 IAuthorizer 而不是 Application，不使用时零编译依赖；
//   - 快照获取失败默认 fail-closed（拒绝），保证判定依据始终一致；
//   - 同一 TTL 内复用同一策略版本，避免判定依据在请求中途漂移。
package main

import (
	"context"
	"log"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/auth/sqlstore"
	"gochen-runtime/db/sql/stdsql"

	"gochen/auth/governance"
	"gochen/auth/scoped"
	"gochen/db"
	"gochen/errors"
)

func main() {
	log.SetFlags(0)
	ctx := context.Background()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	must(err)
	defer func() { must(database.Close()) }()

	_, err = database.Exec(ctx, "CREATE TABLE authz_policy_snapshots ("+
		"snapshot_key TEXT PRIMARY KEY,"+
		"version TEXT NOT NULL,"+
		"timestamp DATETIME NOT NULL,"+
		"metadata TEXT NOT NULL"+
		")")
	must(err)

	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	must(err)
	must(store.SavePolicySnapshot(ctx, governance.PolicySnapshot{
		Key:       "default",
		Version:   "policy-v7",
		Timestamp: time.Now(),
		Metadata:  map[string]any{"source": "sql"},
	}))

	snapshot, err := governance.NewStoreBackedSnapshot(store, "default", time.Minute)
	must(err)

	// 基础 PDP：本示例一律放行，便于突出治理层的行为。
	base := mustAuthorizer()

	// L4 装配：Metrics(Log(Snapshot(base)))，顺序由 governance.Wrap 固化。
	var recorded []governance.AuthzLogEntry
	governed, err := governance.Wrap(base,
		governance.WithSnapshot(snapshot),
		governance.WithAudit(auditFunc(func(entry governance.AuthzLogEntry) error {
			recorded = append(recorded, entry)
			return nil
		})),
		governance.WithMetrics(governance.AuthzMetricsFunc(
			func(_ context.Context, action, effect string, latency time.Duration) {
				log.Printf("metric: action=%s effect=%s latency<=%s", action, effect, latency.Round(time.Millisecond))
			})),
	)
	must(err)

	decision, err := governed.Authorize(ctx, "report:api:read",
		scoped.Resource{Kind: "report", ID: "r-1", ManagedScopeID: 1001})
	must(err)

	log.Printf("decision: effect=%s snapshot=%s", decision.Effect, decision.SnapshotVersion)
	if decision.SnapshotVersion != "policy-v7" {
		log.Fatalf("unexpected snapshot version: %q", decision.SnapshotVersion)
	}
	if len(recorded) != 1 || recorded[0].SnapshotVersion != "policy-v7" {
		log.Fatalf("audit entry missing snapshot version: %#v", recorded)
	}
	log.Printf("audit: action=%s effect=%s snapshot=%s resources=%d",
		recorded[0].Action, recorded[0].Effect, recorded[0].SnapshotVersion, len(recorded[0].Resources))

	// 第二次判定命中 TTL 缓存，复用同一策略版本。
	second, err := governed.Authorize(ctx, "report:api:read",
		scoped.Resource{Kind: "report", ID: "r-2", ManagedScopeID: 1001})
	must(err)
	log.Printf("second decision reuses snapshot=%v", second.SnapshotVersion == decision.SnapshotVersion)

	// 快照不可用时默认 fail-closed。
	broken, err := governance.Wrap(base, governance.WithSnapshot(
		governance.PolicySnapshotFunc(func(context.Context) (governance.PolicySnapshot, error) {
			return governance.PolicySnapshot{}, errors.NewCode(errors.ServiceUnavailable, "snapshot store down")
		})))
	must(err)
	_, err = broken.Authorize(ctx, "report:api:read")
	log.Printf("snapshot unavailable -> forbidden=%v", errors.Is(err, errors.Forbidden))
}

func mustAuthorizer() scoped.IAuthorizer {
	authorizer, err := scoped.NewAuthorizer(nil, scoped.EvaluatorFunc(
		func(_ context.Context, _ string, resources []scoped.Resource) (scoped.Decision, error) {
			return scoped.Allow(resources...), nil
		}))
	must(err)
	return authorizer
}

// auditFunc 把函数适配成 IAuthzAuditStore。
type auditFunc func(entry governance.AuthzLogEntry) error

func (f auditFunc) SaveAuthzLogEntry(_ context.Context, entry governance.AuthzLogEntry) error {
	return f(entry)
}

func (f auditFunc) ListAuthzLogEntries(context.Context, governance.AuthzLogEntryType, int) ([]governance.AuthzLogEntry, error) {
	return nil, nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
