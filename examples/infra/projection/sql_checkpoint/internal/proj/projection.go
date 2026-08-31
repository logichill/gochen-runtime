package proj

import (
	"context"
	"time"

	"gochen/eventing"
	"gochen/eventing/projection"
	"gochen/messaging"
)

// OrderCreated 事件载荷。
type OrderCreated struct{ Amount int }

var total int

// GetTotal 返回当前读模型累计的订单金额。
func GetTotal() int { return total }

type orderAmountProjection struct{}

// NewOrderAmountProjection 创建一个把订单金额累加到内存读模型的投影。
func NewOrderAmountProjection() (projection.IProjection[int64], error) {
	return &orderAmountProjection{}, nil
}

func (p *orderAmountProjection) Name() string { return "order_amount_sql_checkpoint" }

func (p *orderAmountProjection) Handle(ctx context.Context, evt eventing.IEvent) error {
	_ = ctx
	payload, ok := messaging.PayloadAs[*OrderCreated](evt.GetPayload())
	if !ok || payload == nil {
		return nil
	}
	total += payload.Amount
	return nil
}

func (p *orderAmountProjection) HandleWithCheckpoint(ctx context.Context, evt eventing.IEvent, store projection.ICheckpointStore, checkpoint *projection.Checkpoint) error {
	if err := p.Handle(ctx, evt); err != nil {
		return err
	}
	if store != nil && checkpoint != nil {
		return store.Save(ctx, checkpoint)
	}
	return nil
}

func (p *orderAmountProjection) SupportedEventTypes() []string { return []string{"OrderCreated"} }

func (p *orderAmountProjection) Rebuild(ctx context.Context, events []eventing.Event[int64]) error {
	for i := range events {
		if err := p.Handle(ctx, &events[i]); err != nil {
			return err
		}
	}
	return nil
}

func (p *orderAmountProjection) Status() projection.ProjectionStatus {
	return projection.ProjectionStatus{
		Name:      p.Name(),
		Status:    "running",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}
