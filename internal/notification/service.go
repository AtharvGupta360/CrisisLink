package notification

import (
	"context"
)

// NotificationService is a thin read layer over the notifications the consumer
// produces. The write side happens in the consumer's inbox transaction (P21).
type NotificationService struct {
	repo *NotificationRepository
}

func NewNotificationService(repo *NotificationRepository) *NotificationService {
	return &NotificationService{repo: repo}
}

func (s *NotificationService) ListRecent(ctx context.Context, limit int) ([]Notification, error) {
	return s.repo.ListRecent(ctx, limit)
}
