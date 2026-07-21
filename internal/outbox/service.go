package outbox

import (
	"context"
)

// OutboxService is a thin read layer over the outbox for ops/inspection. The
// write side lives inside the domain transactions (the repositories); the relay
// that publishes events is P20.
type OutboxService struct {
	repo *OutboxRepository
}

func NewOutboxService(repo *OutboxRepository) *OutboxService {
	return &OutboxService{repo: repo}
}

func (s *OutboxService) ListRecent(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return s.repo.ListRecent(ctx, limit)
}

// ListDead returns dead-lettered events — the ones that exhausted their retry
// budget and now need a human.
func (s *OutboxService) ListDead(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return s.repo.ListDead(ctx, limit)
}

// PendingLag is the outbox backlog: unpublished, not-yet-dead events. The number
// to alarm on — a climbing lag means downstream consumers are silently stale.
func (s *OutboxService) PendingLag(ctx context.Context) (int, error) {
	return s.repo.PendingCount(ctx)
}
