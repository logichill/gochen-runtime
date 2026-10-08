package quick

import (
	"gochen/app/eventsourced"
	"gochen/app/eventsourced/eventstore"
	deventsourced "gochen/domain/eventsourced"
)

// EventSourcedOptions 组合聚合定义与持久化配置。
// 聚合类型只在 Store.AggregateType 声明一次；发布模式与所有依赖均显式配置。
type EventSourcedOptions[T deventsourced.IEventSourcedAggregate[ID], ID comparable] struct {
	Store            eventstore.DomainEventStoreOptions[ID]
	Sample           T
	Factory          func(id ID) (T, error)
	MetadataRegistry *deventsourced.MetadataRegistry
}

// EventSourced 创建领域事件适配器和单聚合仓储，沿用底层构造期校验。
// Store.SnapshotManager 负责恢复；需要保存后创建快照时显式装配快照仓储装饰器。
func EventSourced[T deventsourced.IEventSourcedAggregate[ID], ID comparable](opts EventSourcedOptions[T, ID]) (*eventsourced.EventSourcedRepository[T, ID], error) {
	store, err := eventstore.NewDomainEventStore(opts.Store)
	if err != nil {
		return nil, err
	}
	return eventsourced.NewEventSourcedRepository(opts.repositoryOptions(store))
}

// AtomicEventSourced 创建支持多聚合原子保存的仓储。
// 仅接受 PersistOnly 和真正支持原子追加的后端；不会循环单项保存模拟事务。
func AtomicEventSourced[T deventsourced.IEventSourcedAggregate[ID], ID comparable](opts EventSourcedOptions[T, ID]) (deventsourced.IAtomicEventSourcedRepository[T, ID], error) {
	store, err := eventstore.NewAtomicDomainEventStore(opts.Store)
	if err != nil {
		return nil, err
	}
	return eventsourced.NewAtomicEventSourcedRepository(opts.repositoryOptions(store))
}

func (opts EventSourcedOptions[T, ID]) repositoryOptions(store deventsourced.IDomainEventStore[ID]) eventsourced.RepositoryOptions[T, ID] {
	return eventsourced.RepositoryOptions[T, ID]{
		AggregateType:    opts.Store.AggregateType,
		Sample:           opts.Sample,
		Factory:          opts.Factory,
		Store:            store,
		MetadataRegistry: opts.MetadataRegistry,
	}
}
