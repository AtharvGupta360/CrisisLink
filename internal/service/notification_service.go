package service

import (
	"context"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
)

// NotificationService is a thin read layer over the notifications the consumer
// produces. The write side happens in the consumer's inbox transaction (P21).
type NotificationService struct {
	repo *repository.NotificationRepository
}

func NewNotificationService(repo *repository.NotificationRepository) *NotificationService {
	return &NotificationService{repo: repo}
}

func (s *NotificationService) ListRecent(ctx context.Context, limit int) ([]models.Notification, error) {
	return s.repo.ListRecent(ctx, limit)
}
