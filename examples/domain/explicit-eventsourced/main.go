// 本示例以 StateApplication 组合统一授权与显式 ES，不使用反射聚合或聚合快照。
package main

import (
	"context"
	"fmt"
	appes "gochen/app/eventsourced"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/auth"
	"gochen/domain"
	es "gochen/domain/eventsourced"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen/uuid"
	"log"
	"math"
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

// accountView 仅描述授权事实；业务状态仍是普通 accountState。
type accountView struct {
	ID int64 `entity:"id"`
}
type demoAccountScope struct{}

func (demoAccountScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ID()}}
}
func (demoAccountScope) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, err := auth.RequirePrincipal(ctx)
	if err != nil {
		return spec.Spec{}, nil, err
	}
	// 演示以固定所有权替代业务权限数据源。真实多租户系统须注入租户范围与可信映射。
	if principal.SubjectID != "demo-user" {
		return spec.Spec{}, nil, errors.NewCode(errors.Forbidden, "account access denied")
	}
	return spec.Eq(meta.ID(), int64(888)), nil, nil
}

type accountService struct {
	application *appes.StateApplication[accountState, domain.IDomainEvent, int64]
}

func (s *accountService) read(ctx context.Context, id int64) (*appes.State[accountState, int64], error) {
	return s.application.Load(ctx, "read", id)
}
func (s *accountService) open(ctx context.Context, cmd Deposit) (*appes.State[accountState, int64], error) {
	return s.application.Execute(ctx, "open", cmd.AccountID, cmd.ExpectedVersion, func(_ context.Context, state accountState) ([]domain.IDomainEvent, error) {
		if cmd.Amount <= 0 {
			return nil, errors.NewCode(errors.Validation, "initial deposit must be positive")
		}
		return []domain.IDomainEvent{&deposited{Amount: cmd.Amount}}, nil
	})
}
func (s *accountService) deposit(ctx context.Context, cmd Deposit) (*appes.State[accountState, int64], error) {
	return s.application.Execute(ctx, "deposit", cmd.AccountID, cmd.ExpectedVersion, func(_ context.Context, state accountState) ([]domain.IDomainEvent, error) {
		if cmd.Amount <= 0 || state.Balance > math.MaxInt64-cmd.Amount {
			return nil, errors.NewCode(errors.Validation, "deposit must be positive and fit balance")
		}
		return []domain.IDomainEvent{&deposited{Amount: cmd.Amount}}, nil
	})
}
func (s *accountService) withdraw(ctx context.Context, cmd Withdraw) (*appes.State[accountState, int64], error) {
	return s.application.Execute(ctx, "withdraw", cmd.AccountID, cmd.ExpectedVersion, func(_ context.Context, state accountState) ([]domain.IDomainEvent, error) {
		if cmd.Amount <= 0 || cmd.Amount > state.Balance {
			return nil, errors.NewCode(errors.Validation, "invalid withdrawal")
		}
		return []domain.IDomainEvent{&withdrawn{Amount: cmd.Amount}}, nil
	})
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
	options := appes.StateOptions[accountState, int64, *accountView]{
		Resource: "account",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead}, {Name: "open", Kind: auth.KindCreate}, {Name: "deposit", Kind: auth.KindUpdate}, {Name: "withdraw", Kind: auth.KindUpdate}},
		View:     func(id int64, _ accountState) *accountView { return &accountView{ID: id} },
		Validate: func(state accountState) error {
			if state.Balance < 0 {
				return errors.NewCode(errors.Validation, "account balance cannot be negative")
			}
			return nil
		},
	}
	descriptor, err := appes.DescribeState(options)
	if err != nil {
		return nil, err
	}
	metadata, err := meta.Describe[*accountView]()
	if err != nil {
		return nil, err
	}
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: demoAccountScope{}}, System: auth.SystemCheckConstraints}}}, auth.CompileOptions{Metadata: metadata, Checker: auth.ActionCheckerFunc(func(ctx context.Context, _ string) error { _, err := auth.RequirePrincipal(ctx); return err })})
	if err != nil {
		return nil, err
	}
	application, err := appes.NewStateApplication(repo, options)
	if err != nil {
		return nil, err
	}
	return &accountService{application: application}, nil
}
func main() {
	ctx, err := auth.WithPrincipal(context.Background(), auth.Principal{SubjectID: "demo-user"})
	must(err)
	service, err := newAccountService()
	must(err)
	funded, err := service.open(ctx, Deposit{AccountID: 888, ExpectedVersion: 0, Amount: 1000})
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
