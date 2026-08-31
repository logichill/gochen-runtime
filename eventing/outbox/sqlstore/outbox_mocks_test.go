package sqlstore

import (
	"context"
	"time"

	"gochen/eventing"
	"gochen/eventing/outbox"
)

type MockOutboxRepository struct{}

func (m *MockOutboxRepository) SaveWithEvents(ctx context.Context, aggregateID int64, events []eventing.Event[int64]) error {
	return nil
}

func (m *MockOutboxRepository) ClaimPendingEntries(ctx context.Context, limit int) ([]outbox.OutboxEntry[int64], error) {
	return nil, nil
}

func (m *MockOutboxRepository) MarkAsPublished(ctx context.Context, entryID int64, claimToken string) error {
	return nil
}

func (m *MockOutboxRepository) MarkAsFailed(ctx context.Context, entryID int64, claimToken string, errorMsg string, nextRetryAt time.Time) error {
	return nil
}

func (m *MockOutboxRepository) RenewClaim(ctx context.Context, entryID int64, claimToken string) error {
	return nil
}

func (m *MockOutboxRepository) DeletePublished(ctx context.Context, olderThan time.Time) error {
	return nil
}
