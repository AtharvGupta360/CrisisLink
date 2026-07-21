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
