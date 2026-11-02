// 演示安全能力在 v5.1 契约下的分层装配。
//
//	L0 Plain    —— auth.Allow()，完全放行；
//	L1 Action   —— auth/action.RBAC(checker)，只校验动作码；
//	L2 Isolated —— 隔离在 Repo 构造期显式声明，Application 层零感知；
//	L3 Scoped   —— auth/scoped.New(checker, resolver)，动作 + 行级规约与赋值；
//	L4 Governed —— governance.Wrap 装饰 PDP，留痕、快照与指标。
//
// 与旧版的关键差异：授权不再是包裹 Application 的装饰器，而是构造
// Application 时显式传入的 auth.IAuthorizer。装配点只有一个，能力边界
// 由授权器与仓储选项共同决定。
package main

import (
	"context"
	"gochen-runtime/host/authz"
	"gochen/app"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"log"
	"time"

	authaction "gochen/auth/action"
	"gochen/auth/governance"

	authscoped "gochen/auth/scoped"
	"gochen/contextx"
	"gochen/domain"

	domcrud "gochen/domain/crud"
	"gochen/errors"
	"gochen/testkit"
)

// Order 是一个最普通的 CRUD 实体——不实现任何安全接口。
type Order struct {
	domcrud.Entity[int64]
	Customer string `json:"customer"`
	Amount   int64  `json:"amount"`
}

func (o *Order) Validate() error { return nil }

// operatorChecker 是一个最小 action.IActionChecker 实现：
// 从 contextx 读取当前 operator，再查它被授予的动作码。
type operatorChecker struct {
	grants map[string][]string // operator -> 已授予的权限码（支持整段通配 *）
}

func (c *operatorChecker) RequireAction(ctx context.Context, code string) error {
	operator := contextx.Operator(ctx)
	if operator == "" {
		return errors.NewCode(errors.Unauthorized, "operator is required").
			WithContext("action", code)
	}
	for _, granted := range c.grants[operator] {
		if authaction.PatternMatches(granted, code) {
			return nil
		}
	}
	return errors.NewCode(errors.Forbidden, "action denied").
		WithContext("operator", operator).
		WithContext("action", code)
}

func newApp[T domain.IEntity[ID], ID comparable](
	repo crud.Repository[T, ID],
	authorizer auth.IAuthorizer,
	resource string,
) *crud.Application[T, ID] {
	app, err := crud.NewApplication[T, ID](repo, crud.Options[T, ID]{
		Resource:   resource,
		Authorizer: authorizer,
	})
	if err != nil {
		log.Fatal(err)
	}
	return app
}

func mustContext(operator string) context.Context {
	ctx, err := contextx.WithOperator(context.Background(), operator)
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

func main() {
	log.SetFlags(0)

	// ---------- L0 Plain：auth.Allow()，不校验任何动作 ----------
	plainRepo, repositoryErr := testkit.NewMemoryRepository[*Order, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	plain := newApp[*Order, int64](plainRepo, auth.Allow(), "order")
	ctx := context.Background()
	if err := plain.Create(ctx, &Order{Customer: "alice", Amount: 100}); err != nil {
		log.Fatal(err)
	}
	count, err := plain.Count(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L0 plain: created without any authz, count=%d", count)

	// ---------- L1 Action：只校验动作码 ----------
	checker := &operatorChecker{grants: map[string][]string{
		"alice": {"order:api:*"},    // alice 可以做 order 的一切
		"bob":   {"order:api:read"}, // bob 只能读
	}}
	rbac, err := authaction.RBAC(checker)
	if err != nil {
		log.Fatal(err)
	}
	rbacRepo, repositoryErr := testkit.NewMemoryRepository[*Order, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	secured := newApp[*Order, int64](rbacRepo, rbac, "order")

	order := &Order{Customer: "alice", Amount: 250}
	if err := secured.Create(mustContext("alice"), order); err != nil {
		log.Fatal(err)
	}
	log.Printf("L1 action: alice create allowed, id=%d", order.GetID())

	err = secured.Create(mustContext("bob"), &Order{Customer: "bob", Amount: 10})
	log.Printf("L1 action: bob create -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	got, err := secured.Get(mustContext("bob"), order.GetID())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L1 action: bob get allowed, customer=%s", got.Customer)

	// 未认证请求返回 Unauthorized，Application 保留授权器的错误码。
	err = secured.Create(context.Background(), &Order{Customer: "anon"})
	log.Printf("L1 action: anonymous create -> unauthorized=%v (%v)", errors.Is(err, errors.Unauthorized), err)

	// ---------- L2 Isolated：Repo 构造期显式声明，Application 代码零改动 ----------
	demoIsolation()

	// ---------- L2 的唯一正门：跨隔离空间只读阀门 ----------
	demoCrossIsolation()

	// ---------- L3 Scoped + L4 Governed ----------
	demoScoped()
}

// TenantOrder 是租户隔离实体——嵌入 TenantEntity 即实现 ITenantEntity。
type TenantOrder struct {
	auth.TenantEntity[int64]
	Customer string `json:"customer"`
}

func (o *TenantOrder) Validate() error { return nil }

func demoIsolation() {
	repo, repositoryErr := testkit.NewMemoryRepository[*TenantOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryIsolation[*TenantOrder, int64](),
	)
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	app := newApp[*TenantOrder, int64](repo, auth.Allow(), "tenant_order")

	ctxA, ctxB := mustTenantContext("tenant-a"), mustTenantContext("tenant-b")

	orderA := &TenantOrder{Customer: "alice"}
	if err := app.Create(ctxA, orderA); err != nil {
		log.Fatal(err)
	}
	log.Printf("L2 isolated: created under tenant-a, stamped tenant=%q", orderA.GetTenantID())

	if err := app.Create(ctxB, &TenantOrder{Customer: "bob"}); err != nil {
		log.Fatal(err)
	}

	// 跨租户读按 NotFound 处理——防探测，不泄露"存在但无权"。
	_, err := app.Get(ctxB, orderA.GetID())
	log.Printf("L2 isolated: tenant-b reading tenant-a row -> notfound=%v", errors.Is(err, errors.NotFound))

	countA, err := app.Count(ctxA)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L2 isolated: tenant-a sees %d row(s), not 2", countA)

	// Fail-Closed：无租户上下文一律拒绝，绝不退化为"看全部"。
	_, err = app.Count(context.Background())
	log.Printf("L2 isolated: no-tenant request -> forbidden=%v", errors.Is(err, errors.Forbidden))
}

func mustTenantContext(tenantID string) context.Context {
	ctx, err := contextx.WithTenantID(context.Background(), tenantID)
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

// ---------- L2 跨隔离只读阀门 ----------

// reconciliationAudit 是一个最小 ICrossIsolationAudit：真实系统会落库或进指标。
// 返回错误即中止本次读取——留痕是开闸的对价，不是旁路观测。
type reconciliationAudit struct {
	reads []testkit.CrossIsolationRead
}

func (a *reconciliationAudit) RecordCrossIsolationRead(_ context.Context, read testkit.CrossIsolationRead) error {
	a.reads = append(a.reads, read)
	return nil
}

func demoCrossIsolation() {
	audit := &reconciliationAudit{}
	repo, repositoryErr := testkit.NewMemoryRepository[*TenantOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryIsolation[*TenantOrder, int64](),
		// 第一道闸（构造期）：声明这张表有被开闸的资格，并交出留痕出口。
		testkit.WithMemoryCrossIsolationReads[*TenantOrder, int64](audit),
	)
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	app := newApp[*TenantOrder, int64](repo, auth.Allow(), "tenant_order")

	if err := app.Create(mustTenantContext("tenant-a"), &TenantOrder{Customer: "alice"}); err != nil {
		log.Fatal(err)
	}
	if err := app.Create(mustTenantContext("tenant-b"), &TenantOrder{Customer: "bob"}); err != nil {
		log.Fatal(err)
	}

	// 声明了阀门，普通请求（没有凭据）依然严格隔离。
	countA, err := app.Count(mustTenantContext("tenant-a"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L2 valve: ordinary request stays isolated, tenant-a sees %d row(s)", countA)

	// 第二道闸（请求期）：凭据必须带上"为什么"和"谁"，缺一即 InvalidInput。
	reconCtx := mustCrossIsolationContext("monthly_billing_reconciliation", "platform_cron_worker")

	total, err := app.Count(reconCtx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L2 valve: cross-isolation read spans all spaces, sees %d row(s)", total)
	log.Printf("L2 valve: audited %d read(s), reason=%q operator=%q",
		len(audit.reads), audit.reads[0].Reason, audit.reads[0].Operator)

	// 只含读：开着闸的写入直接被拒。
	err = app.Create(reconCtx, &TenantOrder{Customer: "eve"})
	log.Printf("L2 valve: write under an open valve -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	// 缺构造期声明：同一份凭据落到没声明阀门的仓储上完全惰性。
	strictRepo, err := testkit.NewMemoryRepository[*TenantOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryIsolation[*TenantOrder, int64](),
	)
	if err != nil {
		log.Fatal(err)
	}
	strict := newApp[*TenantOrder, int64](strictRepo, auth.Allow(), "tenant_order")
	_, err = strict.Count(reconCtx)
	log.Printf("L2 valve: credential alone is inert -> forbidden=%v", errors.Is(err, errors.Forbidden))
}

func mustCrossIsolationContext(reason, operator string) context.Context {
	ctx, err := contextx.WithCrossIsolation(context.Background(), contextx.CrossIsolation{
		Reason:   reason,
		Operator: operator,
	})
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

// mustPrincipalContext 构造 L3 判定所需的主体上下文。
func mustPrincipalContext(activeScopeID int64, permissions ...string) context.Context {
	ctx, err := authz.WithPrincipal(context.Background(), authz.Principal{
		SubjectID:     "9",
		ActiveScopeID: activeScopeID,
		Permissions:   permissions,
	})
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

// ---------- L3 Scoped / L4 Governed ----------

// ScopedOrder 是带数据范围的实体；ScopeID 通过 role 标签声明为范围字段。
type ScopedOrder struct {
	domcrud.Entity[int64]
	ScopeID  int64  `json:"scope_id" dim:"scope"`
	Customer string `json:"customer"`
}

func (o *ScopedOrder) Validate() error { return nil }

// scopedResolver 将激活范围映射为 scope 维度条件，并声明所需词汇。
type scopedResolver struct{}

func (scopedResolver) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension("scope")}, Assignable: []meta.Dimension{"scope"}}
}

func (scopedResolver) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok {
		return spec.Spec{}, nil, errors.NewCode(errors.Unauthorized, "principal is required")
	}
	// 读/改/删都只看当前激活范围；创建由服务端赋值落点，
	// 客户端预填范围冲突时由执行骨架拒绝。
	return spec.Eq(meta.ByDimension("scope"), principal.ActiveScopeID),
		[]auth.Assignment{{Dimension: "scope", Value: principal.ActiveScopeID}}, nil
}

// demoScoped 演示 L3：动作码 + 行级规约，写入范围由服务端赋值落定。
func demoScoped() {
	repo, repositoryErr := testkit.NewMemoryRepository[*ScopedOrder, int64](testkit.NewInt64Sequence(1), testkit.WithMemoryResourceContract[*ScopedOrder, int64](app.ResourceContract{Assignable: []meta.Dimension{"scope"}}))
	if repositoryErr != nil {
		panic(repositoryErr)
	}

	authorizer, err := authscoped.New(authz.NewActionChecker(), scopedResolver{})
	if err != nil {
		log.Fatal(err)
	}
	app := newApp[*ScopedOrder, int64](repo, authorizer, "scoped_order")

	order := &ScopedOrder{Customer: "carol"}
	if err := app.Create(mustPrincipalContext(101, "scoped_order:api:*"), order); err != nil {
		log.Fatal(err)
	}
	log.Printf("L3 scoped: authorized create ok, id=%d scope=%d", order.GetID(), order.ScopeID)

	// 客户端预填了别的范围 → 与服务端赋值冲突，拒绝。
	err = app.Create(mustPrincipalContext(202, "scoped_order:api:*"), &ScopedOrder{ScopeID: 101, Customer: "dave"})
	log.Printf("L3 scoped: conflicting scope create -> forbidden=%v", errors.Is(err, errors.Forbidden))

	// 列表读没有具体目标：动作通过后，可见性由规约过滤决定。
	total, err := app.Count(mustPrincipalContext(101, "scoped_order:api:*"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L3 scoped: list read allowed by action, visible count=%d", total)

	other, err := app.Count(mustPrincipalContext(202, "scoped_order:api:*"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L3 scoped: another active scope sees %d row(s)", other)

	// 未认证：ctx 里没有 Principal → 拒绝。
	err = app.Create(context.Background(), &ScopedOrder{Customer: "anon"})
	log.Printf("L3 scoped: anonymous create -> unauthorized=%v", errors.Is(err, errors.Unauthorized))

	demoGovernance()
}

// demoGovernance 演示 L4：治理包装统一的 auth.IAuthorizer。
func demoGovernance() {
	pdp, err := authscoped.New(authz.NewActionChecker(), scopedResolver{})
	if err != nil {
		log.Fatal(err)
	}

	audit := &memoryAuthzAudit{}
	var recordedLatencies int
	governed, err := governance.Wrap(pdp,
		governance.WithSnapshot(governance.PolicySnapshotFunc(
			func(context.Context) (governance.PolicySnapshot, error) {
				return governance.PolicySnapshot{Key: "scope-policy", Version: "v42"}, nil
			})),
		governance.WithAudit(audit),
		governance.WithMetrics(governance.AuthzMetricsFunc(
			func(context.Context, string, string, time.Duration) { recordedLatencies++ })),
	)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := governed.Authorize(mustPrincipalContext(101, "scoped_order:api:read"), auth.Request{Resource: "scoped_order", Action: "read", Permission: "scoped_order:api:read"}); err != nil {
		log.Fatalf("L4 governed: require failed: %v", err)
	}

	entries, err := audit.ListAuthzLogEntries(context.Background(), governance.AuthzLogEntryTypeDecision, 10)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L4 governed: audited %d decision(s), snapshot=%q, metrics reported=%d",
		len(entries), snapshotVersionOf(entries), recordedLatencies)
}

// snapshotVersionOf 取首条决策留痕上的策略快照版本。
func snapshotVersionOf(entries []governance.AuthzLogEntry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[0].SnapshotVersion
}

// memoryAuthzAudit 是一个最小 IAuthzAuditStore：真实系统会落库。
type memoryAuthzAudit struct {
	entries []governance.AuthzLogEntry
}

func (s *memoryAuthzAudit) SaveAuthzLogEntry(_ context.Context, entry governance.AuthzLogEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

func (s *memoryAuthzAudit) ListAuthzLogEntries(
	_ context.Context,
	entryType governance.AuthzLogEntryType,
	limit int,
) ([]governance.AuthzLogEntry, error) {
	out := make([]governance.AuthzLogEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.Type == entryType {
			out = append(out, entry)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}
