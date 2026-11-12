// 演示 L4 治理：把策略快照落到 SQL 存储，并以 Authorizer Decorator 装配。
//
// 关键点：
//   - 治理装饰的是 IAuthorizer 而不是 Application，不使用时零编译依赖；
//   - 快照获取失败默认 fail-closed（拒绝），成功后传播解析器版本；
//   - TTL 缓存减少回源；实际评估绑定和请求内版本固定由策略接入方保证。
package main

import (
	"context"
	"log"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/auth/sqlstore"
	"gochen-runtime/db/sql/stdsql"

	"gochen/auth"

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
	must(store.SavePolicySnapshot(ctx, auth.PolicySnapshot{
		Key:       "default",
		Version:   "policy-v7",
		Timestamp: time.Now(),
		Metadata:  map[string]any{"source": "sql"},
	}))

	snapshot, err := auth.NewStoreBackedSnapshot(store, "default", time.Minute)
	must(err)

	// 基础 PDP：本示例一律放行，便于突出治理层的行为。
	base := mustAuthorizer()

	// L4 装配：Metrics(Log(Snapshot(base)))，顺序由 governance.Wrap 固化。
	var recorded []auth.AuthzLogEntry
	governed, err := auth.New(auth.Options{Policy: base, Governance: auth.GovernanceOptions{Snapshot: snapshot, Audit: auditFunc(func(entry auth.AuthzLogEntry) error {
		recorded = append(recorded, entry)
		return nil
	}), Metrics: auth.AuthzMetricsFunc(
		func(_ context.Context, action, effect string, latency time.Duration) {
			log.Printf("metric: action=%s effect=%s latency<=%s", action, effect, latency.Round(time.Millisecond))
		})}})
	must(err)

	decision, err := governed.Authorize(ctx, auth.Request{Resource: "report", Action: "read", Permission: "report:api:read", IDs: []string{"r-1"}})
	must(err)

	log.Printf("decision: allowed=%t snapshot=%s", decision.Allowed, decision.SnapshotVersion)
	if decision.SnapshotVersion != "policy-v7" {
		log.Fatalf("unexpected snapshot version: %q", decision.SnapshotVersion)
	}
	if len(recorded) != 1 || recorded[0].SnapshotVersion != "policy-v7" {
		log.Fatalf("audit entry missing snapshot version: %#v", recorded)
	}
	log.Printf("audit: action=%s effect=%s snapshot=%s resources=%d",
		recorded[0].Action, recorded[0].Effect, recorded[0].SnapshotVersion, len(recorded[0].Resources))

	// 第二次判定命中 TTL 缓存，复用同一策略版本。
	second, err := governed.Authorize(ctx, auth.Request{Resource: "report", Action: "read", Permission: "report:api:read", IDs: []string{"r-2"}})
	must(err)
	log.Printf("second decision reuses snapshot=%v", second.SnapshotVersion == decision.SnapshotVersion)

	// 快照不可用时默认 fail-closed。
	broken, err := auth.New(auth.Options{Policy: base, Governance: auth.GovernanceOptions{Snapshot: auth.PolicySnapshotFunc(func(context.Context) (auth.PolicySnapshot, error) {
		return auth.PolicySnapshot{}, errors.NewCode(errors.ServiceUnavailable, "snapshot store down")
	})}})
	must(err)
	_, err = broken.Authorize(ctx, auth.Request{Resource: "report", Action: "read", Permission: "report:api:read"})
	log.Printf("snapshot unavailable -> forbidden=%v", errors.Is(err, errors.Forbidden))
}

func mustAuthorizer() auth.IPolicy { return auth.AllowPolicy() }

// auditFunc 把函数适配成 IAuthzAuditStore。
type auditFunc func(entry auth.AuthzLogEntry) error

func (f auditFunc) SaveAuthzLogEntry(_ context.Context, entry auth.AuthzLogEntry) error {
	return f(entry)
}

func (f auditFunc) ListAuthzLogEntries(context.Context, auth.AuthzLogEntryType, int) ([]auth.AuthzLogEntry, error) {
	return nil, nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
