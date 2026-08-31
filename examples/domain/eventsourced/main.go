package main

import (
	"context"
	"fmt"
	"log"

	appes "gochen/app/eventsourced"
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

// 3. 命令定义
type DepositCommand struct {
	AccountID int64
	Amount    int64
}

func (c *DepositCommand) AggregateID() int64 { return c.AccountID }

type WithdrawCommand struct {
	AccountID int64
	Amount    int64
}

func (c *WithdrawCommand) AggregateID() int64 { return c.AccountID }

func main() {
	ctx := context.Background()
	fmt.Println("=== Gochen Event Sourcing 示例 ===")

	// 1. 初始化注册表与存储
	metaReg := deventsourced.NewMetadataRegistry()
	eventReg := registry.NewRegistry()
	upgraders := upcast.NewUpgraderRegistry()

	_ = eventReg.Register("MoneyDeposited", func() any { return &MoneyDeposited{} })
	_ = eventReg.Register("MoneyWithdrawn", func() any { return &MoneyWithdrawn{} })

	memStore := store.NewMemoryEventStore[int64]()

	domainStore, err := appes.NewDomainEventStore(appes.DomainEventStoreOptions[*BankAccount, int64]{
		AggregateType:    "BankAccount",
		EventStore:       memStore,
		EventIDGenerator: uuid.NewGenerator(),
		EventRegistry:    eventReg,
		UpgraderRegistry: upgraders,
	})
	if err != nil {
		log.Fatalf("create domain store failed: %v", err)
	}

	repo, err := appes.NewEventSourcedRepository(appes.RepositoryOptions[*BankAccount, int64]{
		AggregateType: "BankAccount",
		Sample:        &BankAccount{},
		Factory: appes.AdaptAggregateFactory(func(id int64) *BankAccount {
			return NewBankAccount(metaReg, id)
		}),
		Store:            domainStore,
		MetadataRegistry: metaReg,
	})
	if err != nil {
		log.Fatalf("create repository failed: %v", err)
	}

	// 2. 初始化命令服务
	cmdService, err := appes.NewEventSourcedService[*BankAccount, int64](repo, nil)
	if err != nil {
		log.Fatalf("create command service failed: %v", err)
	}

	// 注册命令处理器
	_ = cmdService.RegisterCommandHandler(&DepositCommand{}, func(ctx context.Context, c appes.IEventSourcedCommand[int64], agg *BankAccount) error {
		cmd := c.(*DepositCommand)
		return agg.Deposit(cmd.Amount)
	})
	_ = cmdService.RegisterCommandHandler(&WithdrawCommand{}, func(ctx context.Context, c appes.IEventSourcedCommand[int64], agg *BankAccount) error {
		cmd := c.(*WithdrawCommand)
		return agg.Withdraw(cmd.Amount)
	})

	// 3. 执行一系列命令
	accountID := int64(888)

	fmt.Printf("\n[1] 存入 1000 元...\n")
	if err := cmdService.ExecuteCommand(ctx, &DepositCommand{AccountID: accountID, Amount: 1000}); err != nil {
		log.Fatalf("deposit failed: %v", err)
	}

	fmt.Printf("[2] 消费 350 元...\n")
	if err := cmdService.ExecuteCommand(ctx, &WithdrawCommand{AccountID: accountID, Amount: 350}); err != nil {
		log.Fatalf("withdraw failed: %v", err)
	}

	fmt.Printf("[3] 存入 200 元...\n")
	if err := cmdService.ExecuteCommand(ctx, &DepositCommand{AccountID: accountID, Amount: 200}); err != nil {
		log.Fatalf("deposit failed: %v", err)
	}

	// 4. 重放并验证聚合状态
	acc, err := repo.Get(ctx, accountID)
	if err != nil {
		log.Fatalf("get aggregate failed: %v", err)
	}

	fmt.Printf("\n=== 重构聚合状态 ===\n")
	fmt.Printf("账户 ID: %d\n", acc.GetID())
	fmt.Printf("当前余额: %d (预期: 850)\n", acc.Balance)
	fmt.Printf("当前版本: %d (预期: 3)\n", acc.GetVersion())

	// 5. 查看事件流
	events, err := memStore.LoadEvents(ctx, "BankAccount", accountID, 0)
	if err != nil {
		log.Fatalf("load events failed: %v", err)
	}
	fmt.Printf("\n=== 历史事件流 (%d 条事件) ===\n", len(events))
	for i, e := range events {
		fmt.Printf("  [%d] Version=%d Type=%-15s ID=%s\n", i+1, e.Version, e.Type, e.ID)
	}

	// 6. 验证领域规则拦截（余额不足）
	fmt.Printf("\n[4] 尝试透支 1000 元 (预期触发业务规则拦截)...\n")
	err = cmdService.ExecuteCommand(ctx, &WithdrawCommand{AccountID: accountID, Amount: 1000})
	if err != nil {
		fmt.Printf("  -> 成功拦截: %v\n", err)
	} else {
		log.Fatalf("expected overdraft error, but got success")
	}

	// 避免 unused import 警告
	var _ domain.IDomainEvent = (*MoneyDeposited)(nil)

	fmt.Println("\nEvent Sourcing 示例执行完成！")
}
