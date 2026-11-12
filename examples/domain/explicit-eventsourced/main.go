// 本示例以业务用例组合授权与显式 ES 提交工具，不使用反射聚合或聚合快照。
package main

import (
	"context"
	"fmt"
	appes "gochen/app/eventsourced"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/auth"
	"gochen/domain"
	es "gochen/domain/eventsourced"
	"gochen/errors"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen/uuid"
	"log"
	"math"
	"strconv"
)

type accountState struct{ Balance int64 }
type deposited struct {
	Amount int64 `json:"amount"`
}

func (*deposited) EventType() string { return "MoneyDeposited" }

type withdrawn struct {
	Amount int64 `json:"amount"`
}

func (*withdrawn) EventType() string { return "MoneyWithdrawn" }

func reduce(state accountState, event domain.IDomainEvent) (accountState, error) {
	switch e := event.(type) {
	case *deposited:
		state.Balance += e.Amount
	case *withdrawn:
		state.Balance -= e.Amount
	default:
		return accountState{}, errors.NewCode(errors.InvalidInput, "unknown account event")
	}
	return state, nil
}

// Deposit 和 Withdraw 携带调用方读到的版本，防止重新加载后掩盖过期命令。
type Deposit struct {
	AccountID       int64
	ExpectedVersion uint64
	Amount          int64
}

// Withdraw 表示一次提款意图。
type Withdraw struct {
	AccountID       int64
	ExpectedVersion uint64
	Amount          int64
}

// IAccountAuthorizer 按账户和动作校验调用者；返回 nil 才允许访问该完整事件流。
// 多租户实现还须绑定租户边界，不能仅验证全局动作权限。
type IAccountAuthorizer interface {
	AuthorizeAccount(context.Context, int64, string) error
}
type demoAccountAuthorizer struct{}

func (demoAccountAuthorizer) AuthorizeAccount(ctx context.Context, id int64, action string) error {
	principal, err := auth.RequirePrincipal(ctx)
	if err != nil {
		return err
	}
	// 示例以固定账户所有权代替业务权限数据源，生产环境应显式注入真实策略。
	if principal.SubjectID != "demo-user" || id != 888 || (action != "read" && action != "deposit" && action != "withdraw") {
		return errors.NewCode(errors.Forbidden, "account access denied").WithContext("account_id", strconv.FormatInt(id, 10))
	}
	return nil
}

type accountService struct {
	repo       *appes.StateRepository[accountState, domain.IDomainEvent, int64]
	authorizer IAccountAuthorizer
}

func (s *accountService) read(ctx context.Context, id int64) (*appes.State[accountState, int64], error) {
	if err := s.authorizer.AuthorizeAccount(ctx, id, "read"); err != nil {
		return nil, err
	}
	return s.repo.Load(ctx, id)
}
func (s *accountService) deposit(ctx context.Context, cmd Deposit) (*appes.State[accountState, int64], error) {
	return s.execute(ctx, cmd.AccountID, cmd.ExpectedVersion, "deposit", func(_ context.Context, state accountState) ([]domain.IDomainEvent, error) {
		if cmd.Amount <= 0 || state.Balance > math.MaxInt64-cmd.Amount {
			return nil, errors.NewCode(errors.Validation, "deposit must be positive and fit balance")
		}
		return []domain.IDomainEvent{&deposited{Amount: cmd.Amount}}, nil
	})
}
func (s *accountService) withdraw(ctx context.Context, cmd Withdraw) (*appes.State[accountState, int64], error) {
	return s.execute(ctx, cmd.AccountID, cmd.ExpectedVersion, "withdraw", func(_ context.Context, state accountState) ([]domain.IDomainEvent, error) {
		if cmd.Amount <= 0 || cmd.Amount > state.Balance {
			return nil, errors.NewCode(errors.Validation, "invalid withdrawal")
		}
		return []domain.IDomainEvent{&withdrawn{Amount: cmd.Amount}}, nil
	})
}
func (s *accountService) execute(ctx context.Context, id int64, expected uint64, action string, decide func(context.Context, accountState) ([]domain.IDomainEvent, error)) (*appes.State[accountState, int64], error) {
	if err := s.authorizer.AuthorizeAccount(ctx, id, action); err != nil {
		return nil, err
	}
	base, err := s.repo.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if base.Version() != expected {
		return nil, errors.NewCode(errors.Concurrency, "command expected version is stale")
	}
	// AppendEvents 再以同一版本检查，覆盖 Load 与追加之间的并发变更；不自动重试。
	return s.repo.Execute(ctx, base, decide)
}
func newAccountService() (*accountService, error) {
	events := registry.NewRegistry()
	if err := events.Register("MoneyDeposited", func() any { return &deposited{} }); err != nil {
		return nil, err
	}
	if err := events.Register("MoneyWithdrawn", func() any { return &withdrawn{} }); err != nil {
		return nil, err
	}
	// 同一类型、JSON 载荷、schema registry 与存储可供传统聚合回放；绝不自动改写旧数据。
	// 显式路径完整回放，不读取/写入旧聚合快照；配置 SnapshotManager 会在装配时失败。
	stream, err := esstore.NewEventStream(esstore.DomainEventStoreOptions[int64]{AggregateType: "BankAccount", EventIDGenerator: uuid.NewGenerator(), EventStore: store.NewMemoryEventStore[int64](), EventRegistry: events, UpgraderRegistry: upcast.NewUpgraderRegistry(), PublishMode: esstore.PublishModePersistOnly})
	if err != nil {
		return nil, err
	}
	repo, err := appes.NewStateRepository(es.Model[accountState, domain.IDomainEvent]{Initial: func() accountState { return accountState{} }, Clone: func(s accountState) accountState { return s }, Reduce: reduce}, stream)
	if err != nil {
		return nil, err
	}
	return &accountService{repo: repo, authorizer: demoAccountAuthorizer{}}, nil
}
func main() {
	ctx, err := auth.WithPrincipal(context.Background(), auth.Principal{SubjectID: "demo-user"})
	must(err)
	service, err := newAccountService()
	must(err)
	funded, err := service.deposit(ctx, Deposit{AccountID: 888, ExpectedVersion: 0, Amount: 1000})
	must(err)
	paid, err := service.withdraw(ctx, Withdraw{AccountID: 888, ExpectedVersion: funded.Version(), Amount: 250})
	must(err)
	replayed, err := service.read(ctx, 888)
	must(err)
	fmt.Printf("balance=%d version=%d replay_matches=%t\n", paid.Value().Balance, paid.Version(), replayed.Value() == paid.Value())
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
