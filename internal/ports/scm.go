package ports

import (
	"context"

	"github.com/slovx2/tyrs-hand/internal/domain"
)

type SCMProvider interface {
	Name() string
	VerifyWebhook(signature string, payload []byte) bool
	NormalizeWebhook(deliveryID, eventName string, payload []byte) (domain.NormalizedEvent, error)
	Permission(ctx context.Context, installationID int64, owner, repository, actor string) (string, error)
}
