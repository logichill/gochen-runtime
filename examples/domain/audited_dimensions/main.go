// 演示维度化审计：审计事实保存在「写入时」的完整维度快照上。
//
// 三项能力在这里汇合：
//   - L1 动作权限 + L3 数据范围由同一个策略判定，创建归属由服务端赋值；
//   - 普通更新保护全部授权维度，客户端改写归属会被拒绝；
//   - 审计记录按写入时维度过滤读取，归属迁移不会改写历史事实。
//
// 权限落点：Application 在用例入口调用授权器；仓储执行隔离与条件写入；
// 审计存储按资源配置校验实际表列，并同时保存完整 JSON 与类型化维度列。
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	repopkg "gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/host/authz"

	"gochen/app"
	appcrud "gochen/app/crud"
	appaudit "gochen/app/crud/audit"
	"gochen/app/operation"
	"gochen/audit"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/gen/snowflake"
)

// Contract 是带归属维度的合同：tenant 为隔离空间，department 为授权维度。
type Contract struct {
	crud.TrackedEntity[int64]
	Tenant     string `json:"tenant" dim:"tenant"`
	Department int64  `json:"department" dim:"department"`
	Title      string `json:"title"`
	Amount     int64  `json:"amount"`
}

// Validate 满足 domain.IValidatable；本示例不额外施加业务校验。
func (c *Contract) Validate() error { return nil }

// contractContract 声明隔离、可赋值与可迁移维度。
func contractContract() app.ResourceContract {
	return app.ResourceContract{
		Isolation:    meta.ByDimension("tenant"),
		Assignable:   []meta.Dimension{"department"},
		Transferable: []meta.Dimension{"department"},
	}
}

// departmentScope 把调用方当前部门映射为 department 维度条件。
//
// 迁移动作按两阶段语义分别给出条件：转出侧只判定来源可见性，接收侧输出权威目标值。
type departmentScope struct{}

func (departmentScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{
		Refs:       []meta.Ref{meta.ByDimension("department")},
		Assignable: []meta.Dimension{"department"},
	}
}

func (departmentScope) ResolveSpec(ctx context.Context, request auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok || principal.ActiveScopeID <= 0 {
		return spec.Spec{}, nil, errors.NewCode(errors.Unauthorized, "active scope is required")
	}
	switch request.Action {
	case "move":
		return spec.Eq(meta.ByDimension("department"), principal.ActiveScopeID), nil, nil
	case "receive":
		return spec.Eq(meta.ByDimension("department"), int64(2)),
			[]auth.Assignment{{Dimension: "department", Value: int64(2)}}, nil
	}
	// 其余动作统一限定在当前部门，创建时由服务端落定归属。
	return spec.Eq(meta.ByDimension("department"), principal.ActiveScopeID),
		[]auth.Assignment{{Dimension: "department", Value: principal.ActiveScopeID}}, nil
}

// 调用方持有的动作权限：含独立的审计读取权限。
const operatorPermissions = "contract:api:create,contract:api:read,contract:api:update,contract:api:audit_read,contract:api:move,contract:api:receive"

func main() {
	log.SetFlags(0)

	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{
		Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1,
	})
	must(err)
	defer func() { _ = database.Close() }()
	must(ensureSchema(context.Background(), database))

	engine, err := lite.New(database)
	must(err)
	repoIDs, err := snowflake.NewGenerator(1, 1)
	must(err)
	auditIDs, err := snowflake.NewGenerator(1, 2)
	must(err)

	contracts, err := repopkg.NewRepo[*Contract, int64](engine, "contracts",
		repopkg.WithIDGenerator[*Contract, int64](repoIDs),
		repopkg.WithResourceContract[*Contract, int64](contractContract()),
		repopkg.WithIsolation[*Contract, int64](repopkg.IsolationCols{Column: "tenant"}),
		repopkg.WithResourceKind[*Contract, int64]("contract"),
	)
	must(err)

	auditConfig := contracts.AuditResource(map[meta.Dimension]string{
		"tenant": "tenant_id", "department": "department_id",
	})
	auditConfig.IsolationColumn = "tenant_id"
	audits, err := repopkg.NewAuditStore(engine, "audit_records", auditIDs, auditConfig)
	must(err)

	options := appcrud.Options[*Contract, int64]{
		Resource: "contract", Audit: audits,
		Actions: []auth.Action{
			{Name: "move", Kind: auth.KindTransfer, ReceiveAction: "receive", Dimensions: []meta.Dimension{"department"}},
			{Name: "receive", Kind: auth.KindTransfer},
		},
	}
	descriptor, err := appcrud.Describe(options, true)
	must(err)
	options.Authorizer, err = auth.Compile(auth.Declaration{
		Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData,
		Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: departmentScope{}}}},
	}, auth.CompileOptions{Checker: authz.NewActionChecker(), Metadata: contracts.ExecutionDescriptor().Metadata()})
	must(err)
	contractApp, err := appcrud.NewApplication[*Contract, int64](contracts, options)
	must(err)

	historyOptions := appaudit.Options{Resource: "contract"}
	historyDescriptor, err := appaudit.Describe(historyOptions)
	must(err)
	historyOptions.Authorizer, err = auth.Compile(auth.Declaration{
		Descriptor: historyDescriptor, ResourceAccess: auth.ResourceAccessData,
		Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: departmentScope{}}}},
	}, auth.CompileOptions{Checker: authz.NewActionChecker(), Metadata: contracts.ExecutionDescriptor().Metadata()})
	must(err)
	historyReader, err := appaudit.NewReader[int64](audits, contracts, historyOptions)
	must(err)

	// 部门 1 的操作员。
	dept1 := mustIdentity("tenant-a", "alice", 1)

	// ---------- 1. 创建：归属由服务端赋值 ----------
	// 客户端预填了别的部门，与服务端赋值冲突即拒绝。
	if err := contractApp.Create(dept1, &Contract{Department: 9, Title: "预填归属"}); err != nil {
		log.Printf("[1] 预填别的归属 -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)
	}
	contract := &Contract{Title: "年度采购合同", Amount: 100000}
	must(contractApp.Create(dept1, contract))
	log.Printf("[1] 建单 id=%d tenant=%s department=%d version=%d",
		contract.ID, contract.Tenant, contract.Department, contract.Version)

	// ---------- 2. 普通更新：业务字段可写，授权维度受保护 ----------
	updated, err := contractApp.Execute(dept1, "update", contract.ID, func(_ context.Context, current *Contract) error {
		current.Amount = 120000
		return nil
	})
	must(err)
	log.Printf("[2] 更新金额成功 amount=%d version=%d", updated.Amount, updated.Version)

	_, err = contractApp.Execute(dept1, "update", contract.ID, func(_ context.Context, current *Contract) error {
		current.Department = 2
		return nil
	})
	log.Printf("[2] 借普通更新改写归属 -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)

	// ---------- 3. 审计读取：按写入时维度与隔离空间过滤 ----------
	readCtx := audit.WithAuditResourceKind(dept1, "contract")
	entityID := fmt.Sprint(contract.ID)
	snapshot, err := audits.ListAuditRecords(readCtx, entityID, 0, 10, spec.All())
	must(err)
	log.Printf("[3] 完整维度快照: %s", describe(snapshot))

	_, err = contractApp.Get(mustIdentity("tenant-b", "mallory", 1), contract.ID)
	log.Printf("[3] 另一租户读取该合同 -> notfound=%v（隔离继续生效）", errors.Is(err, errors.NotFound))

	// 审计过滤只接受实体标识与受保护维度，业务字段不可作为历史过滤条件。
	_, err = audits.ListAuditRecords(readCtx, entityID, 0, 10, spec.Eq(meta.ByField("Title"), "年度采购合同"))
	log.Printf("[3] 以业务字段过滤审计 -> internal=%v (%v)", errors.Is(err, errors.Internal), err)

	// ---------- 4. 归属迁移：历史事实留在写入时的那一侧 ----------
	moved, err := contractApp.Transfer(withOperation(dept1, "op-move-0001"), "move", contract.ID, updated.Version,
		[]auth.Assignment{{Dimension: "department", Value: int64(2)}})
	must(err)
	log.Printf("[4] 迁移到部门 2 version=%d", moved.Version)

	dept2 := mustIdentity("tenant-a", "carol", 2)
	before, err := historyReader.AuditTrail(dept2, contract.ID, 0, 10)
	must(err)
	log.Printf("[4] 部门 2 的调用方看到的轨迹: %s", actions(before))

	history, err := historyReader.AuditTrail(dept1, contract.ID, 0, 10)
	must(err)
	log.Printf("[4] 部门 1 的调用方看到的轨迹: %s", actions(history))
	log.Printf("[4] 两侧合计 %d 条，动作序列彼此互补：历史按写入时维度保留", len(before)+len(history))
}

// describe 摘要打印每条审计记录的写入时维度快照。
func describe(records []audit.AuditRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, fmt.Sprintf("%s@%v", record.Action, record.Dimensions.Values()))
	}
	return out
}

// actions 只列出轨迹中的动作名。
func actions(records []audit.AuditRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.Action)
	}
	return out
}

// mustIdentity 建立调用方身份：租户、主体、当前部门与动作权限。
func mustIdentity(tenant, operator string, department int64) context.Context {
	ctx, err := contextx.WithTenantID(context.Background(), tenant)
	must(err)
	ctx, err = contextx.WithOperator(ctx, operator)
	must(err)
	ctx, err = authz.WithPrincipal(ctx, authz.Principal{
		SubjectID:     operator,
		ActiveScopeID: department,
		Permissions:   splitPermissions(operatorPermissions),
	})
	must(err)
	return ctx
}

func withOperation(ctx context.Context, id string) context.Context {
	return operation.WithOperationID(ctx, id)
}

// splitPermissions 把逗号分隔的权限码归一为切片。
func splitPermissions(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func ensureSchema(ctx context.Context, database db.IDatabase) error {
	statements := []string{
		`CREATE TABLE contracts (
			id INTEGER PRIMARY KEY, version INTEGER NOT NULL,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			created_by TEXT, updated_by TEXT, deleted_by TEXT,
			tenant TEXT NOT NULL, department INTEGER NOT NULL, title TEXT NOT NULL, amount INTEGER NOT NULL)`,
		`CREATE TABLE audit_records (
			id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL,
			operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL,
			tenant_id TEXT NOT NULL, department_id INTEGER NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
