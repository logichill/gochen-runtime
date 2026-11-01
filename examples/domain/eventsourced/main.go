package main

import (
	"context"
	"fmt"
	"log"

	"gochen-runtime/quick"
	appes "gochen/app/eventsourced"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	deventsourced "gochen/domain/eventsourced"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen/uuid"
)

// 1. 领域事件定义
type MoneyDeposited struct {
	Amount int64 `json:"amount"`
}

func (e *MoneyDeposited) EventType() string { return "MoneyDeposited" }

type MoneyWithdrawn struct {
	Amount int64 `json:"amount"`
}

func (e *MoneyWithdrawn) EventType() string { return "MoneyWithdrawn" }

// 2. 事件溯源聚合根
type BankAccount struct {
	*deventsourced.EventSourcedAggregate[int64]
	Balance int64 `json:"balance"`
}

func NewBankAccount(reg *deventsourced.MetadataRegistry, id int64) *BankAccount {
	acc := &BankAccount{}
	agg, err := deventsourced.InitAggregate[int64](reg, acc, id, "BankAccount")
	if err != nil {
		panic(err)
	}
	acc.EventSourcedAggregate = agg
	return acc
}

// 领域逻辑与事件应用
func (a *BankAccount) Deposit(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("deposit amount must be positive, got: %d", amount)
	}
	return a.ApplyAndRecord(&MoneyDeposited{Amount: amount})
}

func (a *BankAccount) Withdraw(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("withdraw amount must be positive, got: %d", amount)
	}
	if a.Balance < amount {
		return fmt.Errorf("insufficient balance: current %d, required %d", a.Balance, amount)
	}
	return a.ApplyAndRecord(&MoneyWithdrawn{Amount: amount})
}

// 反射路由方法
func (a *BankAccount) ApplyMoneyDeposited(evt *MoneyDeposited) {
	a.Balance += evt.Amount
}

func (a *BankAccount) ApplyMoneyWithdrawn(evt *MoneyWithdrawn) {
	a.Balance -= evt.Amount
}

func main() {
	ctx, err := contextx.WithOperator(context.Background(), "demo-user")
	if err != nil {
		log.Fatalf("set operator failed: %v", err)
	}
	fmt.Println("=== Gochen Event Sourcing 示例 ===")

	// 1. 初始化注册表与存储
	metaReg := deventsourced.NewMetadataRegistry()
	eventReg := registry.NewRegistry()
	upgraders := upcast.NewUpgraderRegistry()

	_ = eventReg.Register("MoneyDeposited", func() any { return &MoneyDeposited{} })
	_ = eventReg.Register("MoneyWithdrawn", func() any { return &MoneyWithdrawn{} })

	memStore := store.NewMemoryEventStore[int64]()

	repo, err := quick.EventSourced(quick.EventSourcedOptions[*BankAccount, int64]{
		Store: esstore.DomainEventStoreOptions[int64]{
			PublishMode:      esstore.PublishModePersistOnly,
			AggregateType:    "BankAccount",
			EventStore:       memStore,
			EventIDGenerator: uuid.NewGenerator(),
			EventRegistry:    eventReg,
			UpgraderRegistry: upgraders,
		},
		Sample: &BankAccount{},
		Factory: appes.AdaptAggregateFactory(func(id int64) *BankAccount {
			return NewBankAccount(metaReg, id)
		}),
		MetadataRegistry: metaReg,
	})
	if err != nil {
		log.Fatalf("create repository failed: %v", err)
	}

	// 2. 初始化事件溯源应用：授权、匹配、命令后校验与审计统一由 Application 编排
	app, err := appes.NewApplication[*BankAccount, int64](repo, appes.Options[*BankAccount, int64]{
		Resource:   "bank.account",
		Authorizer: auth.Allow(),
		Actions: []auth.Action{
			{Name: "deposit", Permission: "bank.account:api:deposit", Kind: auth.KindUpdate},
			{Name: "withdraw", Permission: "bank.account:api:withdraw", Kind: auth.KindUpdate},
		},
		History: memStore,
	})
	if err != nil {
		log.Fatalf("create application failed: %v", err)
	}

	// 3. 执行一系列命令
	accountID := int64(888)

	fmt.Printf("\n[1] 开户并存入 1000 元...\n")
	_, err = app.ExecuteNew(ctx, appes.ActionCreate, func(ctx context.Context) (*BankAccount, error) {
		acc := NewBankAccount(metaReg, accountID)
		return acc, acc.Deposit(1000)
	}, appes.WithReason("initial funding"))
	if err != nil {
		log.Fatalf("create account failed: %v", err)
	}

	fmt.Printf("[2] 消费 350 元...\n")
	_, err = app.Execute(ctx, "withdraw", accountID, func(ctx context.Context, acc *BankAccount) error {
		return acc.Withdraw(350)
	})
	if err != nil {
		log.Fatalf("withdraw failed: %v", err)
	}

	fmt.Printf("[3] 存入 200 元...\n")
	_, err = app.Execute(ctx, "deposit", accountID, func(ctx context.Context, acc *BankAccount) error {
		return acc.Deposit(200)
	})
	if err != nil {
		log.Fatalf("deposit failed: %v", err)
	}

	// 4. 重放并验证聚合状态
	acc, err := app.Get(ctx, accountID)
	if err != nil {
		log.Fatalf("get aggregate failed: %v", err)
	}

	fmt.Printf("\n=== 重构聚合状态 ===\n")
	fmt.Printf("账户 ID: %d\n", acc.GetID())
	fmt.Printf("当前余额: %d (预期: 850)\n", acc.Balance)
	fmt.Printf("当前版本: %d (预期: 3)\n", acc.GetVersion())

	// 5. 审计轨迹：操作人、动作、原因与关联 ID 均来自事件元数据
	entries, err := app.AuditTrail(ctx, accountID, 0, 100)
	if err != nil {
		log.Fatalf("load audit trail failed: %v", err)
	}
	fmt.Printf("\n=== 审计轨迹 (%d 条事件) ===\n", len(entries))
	for _, entry := range entries {
		fmt.Printf("  [%d] Type=%-15s Operator=%-9s Action=%-8s Reason=%s\n",
			entry.Version, entry.EventType, entry.ActorID, entry.Action, entry.Reason)
	}

	// 6. 验证领域规则拦截（余额不足）
	fmt.Printf("\n[4] 尝试透支 1000 元 (预期触发业务规则拦截)...\n")
	_, err = app.Execute(ctx, "withdraw", accountID, func(ctx context.Context, acc *BankAccount) error {
		return acc.Withdraw(1000)
	})
	if err != nil {
		fmt.Printf("  -> 成功拦截: %v\n", err)
	} else {
		log.Fatalf("expected overdraft error, but got success")
	}

	// 避免 unused import 警告
	var _ domain.IDomainEvent = (*MoneyDeposited)(nil)

	fmt.Println("\nEvent Sourcing 示例执行完成！")
}
