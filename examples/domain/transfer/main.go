// 演示维度归属迁移（两阶段 Transfer）：一次调用、两侧授权、双记录审计。
//
// 与普通写用例的差别：
//   - 转出与接收各自独立判定，且都发生在事务之外，仓储只在两阶段都通过后才开工；
//   - 转出侧只给出「来源可见」条件，接收侧给出权威目标值，客户端 targets 仅是意图；
//   - 迁移在一个事务内推进版本，并写入共享 OperationID 的 transfer_out / transfer_in。
//
// 权限落点：Application 在执行入口调用授权器，隔离与维度写入能力由仓储构造期声明。
package main

import (
	"context"
	"fmt"
	"log"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	repopkg "gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"

	"gochen/app"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/app/operation"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db"
	"gochen/domain/audited"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/gen/snowflake"
)

// Ticket 是带归属维度的工单。
//
//   - tenant 是隔离空间，由仓储权威盖戳；
//   - department 与 region 是可迁移的归属维度，region 为可空维度；
//   - title 是普通业务字段，不参与授权。
type Ticket struct {
	audited.AuditedEntity[int64]
	Tenant     string  `json:"tenant" dim:"tenant"`
	Department int64   `json:"department" dim:"department"`
	Region     *string `json:"region" dim:"region"`
	Title      string  `json:"title"`
}

// Validate 满足 domain.IValidatable；本示例不额外施加业务校验。
func (t *Ticket) Validate() error { return nil }

// ticketContract 声明资源的隔离、可赋值与可迁移维度。
//
// 普通更新默认保护全部维度，只有进入 Transferable 的维度才允许经迁移用例改写。
func ticketContract() app.ResourceContract {
	return app.ResourceContract{
		Isolation:    meta.ByDimension("tenant"),
		Assignable:   []meta.Dimension{"department", "region"},
		Transferable: []meta.Dimension{"department", "region"},
	}
}

// ticketPolicy 是两阶段授权器。
//
// 转出判定只回答「当前主体能否迁走这条单据」，接收判定回答「目标方是否收下」并输出权威归属。
type ticketPolicy struct {
	inScope     int64 // 调用方当前可见的部门
	denyReceive bool
	trace       []string
}

func (p *ticketPolicy) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{
		Refs:       []meta.Ref{meta.ByDimension("department"), meta.ByDimension("region")},
		Assignable: []meta.Dimension{"department", "region"},
	}
}

func (p *ticketPolicy) Authorize(ctx context.Context, request auth.Request) (auth.Decision, error) {
	_, inTransaction := contextx.TxLifecycleFromContext(ctx)
	p.trace = append(p.trace, fmt.Sprintf("%s(tx=%t)", request.Action, inTransaction))

	switch request.Action {
	case "move":
		return auth.Decision{
			Allowed: true,
			Reason:  "转出范围已确认",
			Spec:    spec.Eq(meta.ByDimension("department"), p.inScope),
		}, nil
	case "receive":
		if p.denyReceive {
			return auth.Decision{Reason: "接收部门已停用"}, nil
		}
		// 接收方按自身规则给出权威目标值：客户端没有提到的 region 也在此落定。
		return auth.Decision{
			Allowed: true,
			Reason:  "接收部门接受转派",
			Spec:    spec.Eq(meta.ByDimension("department"), int64(2)),
			Assign: []auth.Assignment{
				{Dimension: "department", Value: int64(2)},
				{Dimension: "region", Value: "north"},
			},
		}, nil
	}
	return auth.Decision{Allowed: true, Spec: spec.All()}, nil
}

// newTicketApp 装配工单应用：迁移动作声明接收动作与可写维度。
func newTicketApp(tickets *repopkg.Repo[*Ticket, int64], audits *repopkg.AuditStore, policy auth.IAuthorizer) *appaudited.Application[*Ticket, int64] {
	application, err := appaudited.NewApplication[*Ticket, int64](tickets, audits, appaudited.Options[*Ticket, int64]{
		Options: appcrud.Options[*Ticket, int64]{
			Resource:   "ticket",
			Authorizer: policy,
			Actions: []auth.Action{
				{Name: "move", Kind: auth.KindTransfer, ReceiveAction: "receive", Dimensions: []meta.Dimension{"department", "region"}},
				{Name: "receive", Kind: auth.KindTransfer},
			},
		},
	})
	must(err)
	return application
}

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

	// 业务仓储：隔离键与维度能力在构造期声明，Application 只消费既定契约。
	tickets, err := repopkg.NewRepo[*Ticket, int64](engine, "tickets",
		repopkg.WithIDGenerator[*Ticket, int64](repoIDs),
		repopkg.WithResourceContract[*Ticket, int64](ticketContract()),
		repopkg.WithIsolation[*Ticket, int64](repopkg.IsolationCols{Column: "tenant"}),
		repopkg.WithResourceKind[*Ticket, int64]("ticket"),
	)
	must(err)

	// 审计存储复用同一份执行描述符与维度列，构造期核对实际表结构。
	auditConfig := tickets.AuditResource(map[meta.Dimension]string{
		"tenant": "tenant_id", "department": "department_id", "region": "region_code",
	})
	auditConfig.IsolationColumn = "tenant_id"
	audits, err := repopkg.NewAuditStore(engine, "audit_records", auditIDs, auditConfig)
	must(err)

	policy := &ticketPolicy{inScope: 1}
	ticketApp := newTicketApp(tickets, audits, policy)

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	must(err)
	ctx, err = contextx.WithOperator(ctx, "alice")
	must(err)

	// ---------- 1. 在部门 1 建立工单 ----------
	ticket := &Ticket{Department: 1, Title: "打印机故障"}
	must(ticketApp.Create(withOperation(ctx, "op-create-0001"), ticket))
	log.Printf("[1] 建单 id=%d department=%d region=%v version=%d",
		ticket.ID, ticket.Department, deref(ticket.Region), ticket.Version)

	// ---------- 2. 两阶段授权后的迁移 ----------
	// 客户端提交的 targets 只是意图：只声明目标部门，region 由接收方权威落定。
	intent := []auth.Assignment{{Dimension: "department", Value: int64(2)}}
	policy.trace = nil
	move := withOperation(ctx, "op-transfer-0001")
	moved, err := ticketApp.Transfer(move, "move", ticket.ID, ticket.Version, intent)
	must(err)
	log.Printf("[2] 迁移完成 department=%d region=%q version=%d（客户端意图仅含 department）",
		moved.Department, deref(moved.Region), moved.Version)
	log.Printf("[2] 判定轨迹 %v（两侧均在事务外完成判定）", policy.trace)

	// ---------- 3. 双记录审计共享同一操作编号 ----------
	readCtx := audited.WithAuditResourceKind(ctx, "ticket")
	entityID := fmt.Sprint(ticket.ID)
	source, err := audits.ListAuditRecords(readCtx, entityID, 0, 10, spec.Eq(meta.ByDimension("department"), int64(1)))
	must(err)
	target, err := audits.ListAuditRecords(readCtx, entityID, 0, 10, spec.Eq(meta.ByDimension("department"), int64(2)))
	must(err)
	log.Printf("[3] 写入时归属为部门 1 的历史记录: %s", describe(source))
	log.Printf("[3] 写入时归属为部门 2 的历史记录: %s", describe(target))
	log.Printf("[3] 两侧共享 operation=%q，配对键为 (operation_id, resource_kind, entity_id, action)",
		sharedOperationID(source, target))

	// ---------- 4. 同一操作重复迁移：操作内实体只能迁移一次 ----------
	_, err = ticketApp.Transfer(move, "move", ticket.ID, moved.Version, intent)
	log.Printf("[4] 同一操作重复迁移 -> invalid-input=%v (%v)", errors.Is(err, errors.InvalidInput), err)

	// ---------- 5. 转出条件不再满足：范围内不可见按 NotFound 处理 ----------
	_, err = ticketApp.Transfer(withOperation(ctx, "op-transfer-0002"), "move", ticket.ID, moved.Version, intent)
	log.Printf("[5] 单据已不在调用方范围内 -> notfound=%v (%v)", errors.Is(err, errors.NotFound), err)

	// ---------- 6. 新操作携带旧版本：版本协议拒绝 ----------
	policy.inScope = 2
	_, err = ticketApp.Transfer(withOperation(ctx, "op-transfer-0003"), "move", ticket.ID, ticket.Version, intent)
	log.Printf("[6] 新操作 + 旧版本 -> concurrency=%v (%v)", errors.Is(err, errors.Concurrency), err)

	// ---------- 7. 无实际变化：版本当前，但目标值等于现状 ----------
	_, err = ticketApp.Transfer(withOperation(ctx, "op-transfer-0004"), "move", ticket.ID, moved.Version, intent)
	log.Printf("[7] 目标值等于现状 -> invalid-input=%v (%v)", errors.Is(err, errors.InvalidInput), err)
	policy.inScope = 1

	// ---------- 8. 接收方拒绝：转出侧不写库 ----------
	policy.denyReceive = true
	_, err = ticketApp.Transfer(withOperation(ctx, "op-transfer-0005"), "move", ticket.ID, moved.Version, intent)
	log.Printf("[8] 接收方拒绝 -> forbidden=%v (%v)", errors.Is(err, errors.Forbidden), err)
	stored, err := tickets.Get(ctx, ticket.ID, spec.All())
	must(err)
	policy.denyReceive = false
	log.Printf("[8] 库中状态保持 department=%d region=%q version=%d",
		stored.Department, deref(stored.Region), stored.Version)

	// ---------- 9. 未声明的维度：动作维度白名单拒绝 ----------
	_, err = ticketApp.Transfer(withOperation(ctx, "op-transfer-0006"), "move", ticket.ID, stored.Version,
		[]auth.Assignment{{Dimension: "tenant", Value: "tenant-b"}})
	log.Printf("[9] 迁移隔离维度 -> invalid-input=%v (%v)", errors.Is(err, errors.InvalidInput), err)
}

// withOperation 为一次业务操作绑定独立的操作编号。
//
// 同一操作内的同一实体只能迁移一次，重试应当复用原编号，新的迁移必须开新操作。
func withOperation(ctx context.Context, id string) context.Context {
	return operation.WithOperationID(ctx, id)
}

// describe 摘要打印审计记录的动作、操作编号与写入时维度快照。
func describe(records []audited.AuditRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, fmt.Sprintf("%s operation=%s dimensions=%v", record.Action, record.OperationID, record.Dimensions.Values()))
	}
	return out
}

// sharedOperationID 从两侧记录中取出共享的操作编号，任一侧缺失即返回空串。
func sharedOperationID(source, target []audited.AuditRecord) string {
	pair := func(records []audited.AuditRecord, action string) string {
		for _, record := range records {
			if record.Action == action {
				return record.OperationID
			}
		}
		return ""
	}
	out, in := pair(source, "transfer_out"), pair(target, "transfer_in")
	if out == "" || out != in {
		return ""
	}
	return out
}

// ensureSchema 建立业务表、审计表与迁移配对唯一索引。
//
// 配对索引让「一次操作恰好写出一条 transfer_out 与一条 transfer_in」成为存储层的硬约束。
func ensureSchema(ctx context.Context, database db.IDatabase) error {
	statements := []string{
		`CREATE TABLE tickets (
			id INTEGER PRIMARY KEY, version INTEGER NOT NULL,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			created_by TEXT, updated_by TEXT, deleted_by TEXT,
			tenant TEXT NOT NULL, department INTEGER NOT NULL, region TEXT, title TEXT NOT NULL)`,
		`CREATE TABLE audit_records (
			id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL,
			operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL,
			tenant_id TEXT NOT NULL, department_id INTEGER NOT NULL, region_code TEXT)`,
		`CREATE UNIQUE INDEX transfer_pair ON audit_records(operation_id, resource_kind, entity_id, action)
			WHERE action IN ('transfer_out', 'transfer_in')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func deref(value *string) string {
	if value == nil {
		return "<null>"
	}
	return *value
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
