package service

import (
	"context"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// OutboxService is a thin read layer over the outbox for ops/inspection. The
// write side lives inside the domain transactions (the repositories); the relay
// that publishes events is P20.
type OutboxService struct {
	repo *repository.OutboxRepository
}

func NewOutboxService(repo *repository.OutboxRepository) *OutboxService {
	return &OutboxService{repo: repo}
}

func (s *OutboxService) ListRecent(ctx context.Context, limit int) ([]models.OutboxEvent, error) {
	return s.repo.ListRecent(ctx, limit)
}
