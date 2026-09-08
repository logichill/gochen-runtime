// 演示安全能力渐进分层的 L0 → L1 → L2 → L3 → L4 全部档位
// 。
//
//	L0 Plain    —— 直接裸用 crud.Application，不 import 任何 security 包；
//	L1 Action   —— 用 app/security/action 装饰同一个 Application，只增加动作级权限校验；
//	L2 Isolated —— 在 **Repo 构造期显式声明**隔离，Application 层完全不变；
//	            —— 外加 L2 唯一的正门：跨隔离空间只读阀门（两道闸、只含读、必留痕）；
//	L3 Scoped   —— 资源授权在 Application 层判定，写约束经受控通道下推给仓储；
//	L4 Governed —— 治理外壳装饰 Authorizer（不是 Application）。
//
// 两个关键设计点：
//   - L1 装饰的是 crud.IApplication 这一**宽接口**，底层仓储的可选能力
//     （IQueryRepository / 批量 / 物理删除）不会因装饰而丢失；
//   - L2 **不用装饰器**——隔离是 Repo 的构造期显式选项。窄接口
//     （domain/crud.IRepository 只有 4 个方法）上做装饰会丢掉可选能力接口，
//     所以改成显式声明。
//
// 判定规则不必自己写：L1 的 IActionChecker 与 L3 的 IEvaluator 都有默认实现
// （runtime/host/authz 的 NewActionChecker / NewEvaluator）。
// 本例 L1 段刻意手写一个 checker 展示契约有多小，L3 段则直接用默认判定器。
package main

import (
	"context"
	"log"
	"time"

	secaction "gochen/app/security/action"
	secscoped "gochen/app/security/scoped"

	"gochen-runtime/host/authz"
	"gochen/app/crud"
	authaction "gochen/auth/action"
	"gochen/auth/governance"
	authscoped "gochen/auth/scoped"
	"gochen/contextx"
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

// operatorChecker 是一个最小 IActionChecker 实现：
// 从 contextx 读取当前 operator，再查它被授予的动作码。
//
// 这里手写是为了展示 IActionChecker 的契约有多小——一个方法，且主体语义
// 完全由实现方决定（本例的主体就是一个 operator 字符串，没有 Principal）。
//
// 若主体语义就是 host/authz.Principal，**不要照抄这段**：直接用
// authz.NewActionChecker() 即可，规则与 L3 默认判定器的动作校验完全一致
// （见下面 demoScoped 的用法）。
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

func newPlainApp() (crud.IApplication[*Order, int64], error) {
	repo := testkit.NewMemoryRepository[*Order, int64](testkit.NewInt64Sequence(1))
	return crud.NewApplication[*Order, int64](repo, nil, nil)
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

	// ---------- L0 Plain：零安全依赖，直接裸用 ----------
	plain, err := newPlainApp()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if err := plain.Create(ctx, &Order{Customer: "alice", Amount: 100}); err != nil {
		log.Fatal(err)
	}
	count, err := plain.Count(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L0 plain: created without any authz, count=%d", count)

	// ---------- L1 Action：同一个 Application，加一层动作级校验 ----------
	base, err := newPlainApp()
	if err != nil {
		log.Fatal(err)
	}
	checker := &operatorChecker{grants: map[string][]string{
		"alice": {"order:api:*"},    // alice 可以做 order 的一切
		"bob":   {"order:api:read"}, // bob 只能读
	}}
	policy := authaction.OperationPolicy{
		Create: "order:api:create",
		Read:   "order:api:read",
		Update: "order:api:update",
		Delete: "order:api:delete",
		List:   "order:api:list",
	}
	secured, err := secaction.New[*Order, int64](base, checker, policy)
	if err != nil {
		log.Fatal(err)
	}

	// alice 有 order:api:* → 放行
	order := &Order{Customer: "alice", Amount: 250}
	if err := secured.Create(mustContext("alice"), order); err != nil {
		log.Fatal(err)
	}
	log.Printf("L1 action: alice create allowed, id=%d", order.GetID())

	// bob 只有 read → create 被拒
	err = secured.Create(mustContext("bob"), &Order{Customer: "bob", Amount: 10})
	log.Printf("L1 action: bob create -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	// bob 读取被放行
	got, err := secured.Get(mustContext("bob"), order.GetID())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L1 action: bob get allowed, customer=%s", got.Customer)

	// 无 operator（未认证）→ 拒绝，且拒绝发生在 Application 层，
	// 绕过 HTTP 直接调用同样拦截。
	err = secured.Create(context.Background(), &Order{Customer: "anon"})
	log.Printf("L1 action: anonymous create -> unauthorized=%v", errors.Is(err, errors.Unauthorized))

	// 装饰不丢失底层可选能力：IQueryRepository 断言仍然成立。
	_, ok := secured.Repository().(domcrud.IQueryRepository[*Order, int64])
	log.Printf("L1 action: optional IQueryRepository preserved=%v", ok)

	// ---------- L2 Isolated：Repo 构造期显式声明，Application 代码零改动 ----------
	demoIsolation()

	// ---------- L2 的唯一正门：跨隔离空间只读阀门 ----------
	demoCrossIsolation()

	// ---------- L3 Scoped + L4 Governed ----------
	demoScoped()
}

// TenantOrder 是租户隔离实体——嵌入 TenantEntity 即实现 ITenantEntity。
type TenantOrder struct {
	domcrud.TenantEntity[int64]
	Customer string `json:"customer"`
}

func (o *TenantOrder) Validate() error { return nil }

func demoIsolation() {
	// 隔离在**构造期**显式启用；未声明即完全不启用（无隐式字段名推断）。
	repo := testkit.NewMemoryRepository[*TenantOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryIsolation[*TenantOrder, int64](),
	)

	// 注意：Application 的构造与调用方式与 L0 完全一致——业务层对隔离零感知。
	app, err := crud.NewApplication[*TenantOrder, int64](repo, nil, nil)
	if err != nil {
		log.Fatal(err)
	}

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
	_, err = app.Get(ctxB, orderA.GetID())
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

// reconciliationAudit 是一个最小 ICrossIsolationAudit：真实系统会落库或进指标
// （SQL 侧的等价接口是 ormrepo.ICrossIsolationAudit，可转接到 auth/governance）。
//
// 返回错误即中止本次读取——留痕是开闸的对价，不是旁路观测。
type reconciliationAudit struct {
	reads []testkit.CrossIsolationRead
}

func (a *reconciliationAudit) RecordCrossIsolationRead(_ context.Context, read testkit.CrossIsolationRead) error {
	a.reads = append(a.reads, read)
	return nil
}

// demoCrossIsolation 演示 L2 唯一的正门：跨隔离空间只读阀门。
//
// 为什么需要它：对账、计费汇总、跨隔离空间报表、数据迁移与平台巡检客观存在。
// 没有正门时，这些需求最省事的实现是"同一张表再建一个不声明隔离的仓储"——
// 它构造期不报错、运行期不留痕、形态与普通仓储完全一致，而且读写通吃。
// 也就是说 fail-closed 并没有消灭开闸行为，只是把它挤到了看不见的地方。
//
// 阀门把这条路换成一条刺眼的路：两道闸、只含读、必然留痕。
func demoCrossIsolation() {
	audit := &reconciliationAudit{}
	repo := testkit.NewMemoryRepository[*TenantOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryIsolation[*TenantOrder, int64](),
		// 第一道闸（构造期）：声明这张表有被开闸的资格，并交出留痕出口。
		// audit 传 nil 会在构造期直接失败——无法留痕的阀门与"干脆不声明隔离"没区别。
		testkit.WithMemoryCrossIsolationReads[*TenantOrder, int64](audit),
	)
	app, err := crud.NewApplication[*TenantOrder, int64](repo, nil, nil)
	if err != nil {
		log.Fatal(err)
	}

	if err := app.Create(mustTenantContext("tenant-a"), &TenantOrder{Customer: "alice"}); err != nil {
		log.Fatal(err)
	}
	if err := app.Create(mustTenantContext("tenant-b"), &TenantOrder{Customer: "bob"}); err != nil {
		log.Fatal(err)
	}

	// 最重要的一条：声明了阀门，普通请求（没有凭据）依然严格隔离。
	// 阀门不是"这个仓储从此敞开"，而是"这个仓储允许被开闸"。
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

	// 只含读：开着闸的写入直接被拒，且错误说清"开闸不含写权限"。
	// 不这么做的话，一次本以为是全局的批处理会静默只写中其中一个隔离空间。
	err = app.Create(reconCtx, &TenantOrder{Customer: "eve"})
	log.Printf("L2 valve: write under an open valve -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	// 缺构造期声明：同一份凭据落到没声明阀门的仓储上完全惰性，
	// 因此凭据泄漏到无关请求上不会把别的表也打开。
	strictApp, err := crud.NewApplication[*TenantOrder, int64](
		testkit.NewMemoryRepository[*TenantOrder, int64](
			testkit.NewInt64Sequence(1),
			testkit.WithMemoryIsolation[*TenantOrder, int64](),
		), nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	_, err = strictApp.Count(reconCtx)
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
//
// 真实系统里这一步由 Transport 的 AuthN 中间件完成（解析 token → 写入 Principal），
// 其中 ActiveScopeID 是登录时**显式激活**的身份，不靠推断——见
// Core docs/architecture/layered-authz.md「隔离、范围与主体」。
func mustPrincipalContext(activeScopeID int64, permissions ...string) context.Context {
	ctx, err := authz.WithPrincipal(context.Background(), authz.Principal{
		SubjectID:     9,
		ActiveScopeID: activeScopeID,
		Permissions:   permissions,
	})
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

// ---------- L3 Scoped / L4 Governed ----------

// ScopedOrder 是带数据范围的实体。
type ScopedOrder struct {
	domcrud.Entity[int64]
	Customer string `json:"customer"`
}

func (o *ScopedOrder) Validate() error { return nil }

// demoScoped 演示 L3：资源授权在 Application 层完成，写约束经受控通道下推到仓储。
//
// 三个关键事实：
//   - 仓储必须在构造期显式声明范围能力，未声明的仓储参与 L3 装配会启动期报错；
//   - 装饰器判定通过后把约束写入一次性派生 ctx，基础 Application 全程无感知；
//   - 绕过装饰器直接用裸 Application 写入，仓储取不到约束会 fail-closed 拒绝。
func demoScoped() {
	repo := testkit.NewMemoryRepository[*ScopedOrder, int64](
		testkit.NewInt64Sequence(1),
		testkit.WithMemoryScope[*ScopedOrder, int64]("order"),
	)
	plainApp, err := crud.NewApplication[*ScopedOrder, int64](repo, nil, nil)
	if err != nil {
		log.Fatal(err)
	}

	// PDP：直接用框架自带的默认判定器，不必手写 EvaluatorFunc。
	//
	// 它的规则就是"动作码 + 资源 managed scope 归属"，覆盖 RBAC + 数据范围这条
	// 最常见路径：主体从 ctx 里的 host/authz.Principal 取，可见范围默认按
	// Principal.ActiveScopeID 推导。接 OPA / Casbin / SpiceDB 时把 IEvaluator
	// 整个换掉即可，下面的 Authorizer / 治理 / Application 装配一行都不用动。
	evaluator := authz.NewEvaluator()
	resolver := authscoped.ResourceResolverFunc(func(target any) (authscoped.Resource, bool) {
		order, ok := target.(*ScopedOrder)
		if !ok {
			return authscoped.Resource{}, false
		}
		// 目标范围由解析器**显式**给出：默认判定器不会拿"当前唯一可见的那个范围"
		// 替业务填上——那等于框架替业务决定数据落在哪儿。
		return authscoped.Resource{Kind: "order", ID: "1", ManagedScopeID: 101, Revision: "0"}, order != nil
	})
	authorizer, err := authscoped.NewAuthorizer(resolver, evaluator)
	if err != nil {
		log.Fatal(err)
	}

	// L4：治理外壳装饰的是 Authorizer，不是 Application。不启用时完全不参与编译。
	//
	// 三层固化为 Metrics(Log(Snapshot(base)))：快照最内层先固定策略版本，
	// 留痕居中记录已带版本的最终决策，指标最外层统计含全部治理开销的真实延迟。
	audit := &memoryAuthzAudit{}
	var recordedLatencies int
	governed, err := governance.Wrap(authorizer,
		governance.WithSnapshot(governance.PolicySnapshotFunc(
			func(context.Context) (governance.PolicySnapshot, error) {
				return governance.PolicySnapshot{Key: "order-policy", Version: "v42"}, nil
			})),
		governance.WithAudit(audit),
		governance.WithMetrics(governance.AuthzMetricsFunc(
			func(context.Context, string, string, time.Duration) { recordedLatencies++ })),
	)
	if err != nil {
		log.Fatal(err)
	}

	securedApp, err := secscoped.New[*ScopedOrder, int64](plainApp, governed, secscoped.Config{
		EntityType: "order",
		Policy: authaction.OperationPolicy{
			Create: "order:api:create",
			Read:   "order:api:read",
			Update: "order:api:update",
			Delete: "order:api:delete",
			List:   "order:api:list",
		},
		ScopeResolver: authscoped.DataScopeResolverFunc(func(context.Context) (authscoped.DataScope, error) {
			return authscoped.Filtered(101), nil
		}),
	})
	if err != nil {
		log.Fatal(err)
	}

	order := &ScopedOrder{Customer: "carol"}
	if err := securedApp.Create(mustPrincipalContext(101, "order:api:*"), order); err != nil {
		log.Fatal(err)
	}
	log.Printf("L3 scoped: authorized create ok, id=%d", order.GetID())

	// 换一个主体：动作码一模一样，但它工作在别的范围里 → 越界被拒。
	// 这正是 L3 与 L1 的分界——L1 只问"能不能做这个动作"，L3 还要问"对这条资源"。
	err = securedApp.Create(mustPrincipalContext(202, "order:api:*"), &ScopedOrder{Customer: "dave"})
	log.Printf("L3 scoped: out-of-scope create -> forbidden=%v", errors.Is(err, errors.Forbidden))

	// 列表读没有具体目标：判据退化为"能不能列这类资源"，可见性随后由 DataScope 界定。
	// 默认判定器在这里返回的是 allow + **空**授权资源清单，绝不伪造一个占位资源
	// 来凑满 IsAllowed——那正是要防的"空 allow 通行证"。
	total, err := securedApp.Count(mustPrincipalContext(101, "order:api:*"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("L3 scoped: list read allowed by action, visible count=%d", total)

	// 未认证：ctx 里没有 Principal → 判定为 principal_missing。
	err = securedApp.Create(context.Background(), &ScopedOrder{Customer: "anon"})
	log.Printf("L3 scoped: anonymous create -> forbidden=%v", errors.Is(err, errors.Forbidden))

	// 绕过 PEP 直接用裸 Application：仓储取不到约束 → fail-closed。
	err = plainApp.Create(context.Background(), &ScopedOrder{Customer: "mallory"})
	log.Printf("L3 scoped: bypassing the PEP -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	// 可选能力不因装饰而丢失：批量写仍可被探测到，且同样受授权保护。
	_, batchOK := any(securedApp).(crud.IBatchWriter[*ScopedOrder, int64])
	log.Printf("L3 scoped: optional IBatchWriter preserved=%v", batchOK)

	// L4 治理确实生效：每次判定都留了痕、带上了策略快照版本、上报了指标。
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

// memoryAuthzAudit 是一个最小 IAuthzAuditStore：真实系统会落库（见 runtime/auth/sqlstore）。
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
