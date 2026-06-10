package usecases

import (
	"context"
	"fmt"

	"github.com/orris-inc/orris/internal/domain/subscription"
	apperrors "github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

type RevokeSubscriptionTokenCommand struct {
	// SubscriptionID is the owner-verified subscription the token must belong to.
	SubscriptionID uint
	TokenID        uint
}

type RevokeSubscriptionTokenUseCase struct {
	tokenRepo subscription.SubscriptionTokenRepository
	logger    logger.Interface
}

func NewRevokeSubscriptionTokenUseCase(
	tokenRepo subscription.SubscriptionTokenRepository,
	logger logger.Interface,
) *RevokeSubscriptionTokenUseCase {
	return &RevokeSubscriptionTokenUseCase{
		tokenRepo: tokenRepo,
		logger:    logger,
	}
}

func (uc *RevokeSubscriptionTokenUseCase) Execute(ctx context.Context, cmd RevokeSubscriptionTokenCommand) error {
	token, err := uc.tokenRepo.GetByID(ctx, cmd.TokenID)
	if err != nil {
		uc.logger.Errorw("failed to get token", "error", err, "token_id", cmd.TokenID)
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Authorization: the token must belong to the owner-verified subscription.
	// Without this check any authenticated user owning any subscription could
	// revoke another user's tokens by supplying an arbitrary (enumerable) token ID.
	if token.SubscriptionID() != cmd.SubscriptionID {
		uc.logger.Warnw("subscription token does not belong to subscription",
			"token_id", cmd.TokenID,
			"token_subscription_id", token.SubscriptionID(),
			"request_subscription_id", cmd.SubscriptionID,
		)
		return apperrors.NewNotFoundError("subscription token not found")
	}

	if err := token.Revoke(); err != nil {
		uc.logger.Warnw("failed to revoke token", "error", err, "token_id", cmd.TokenID)
		return err
	}

	if err := uc.tokenRepo.Update(ctx, token); err != nil {
		uc.logger.Errorw("failed to update token", "error", err, "token_id", cmd.TokenID)
		return fmt.Errorf("failed to update token: %w", err)
	}

	uc.logger.Infow("token revoked successfully",
		"token_id", cmd.TokenID,
		"subscription_id", token.SubscriptionID(),
	)

	return nil
}
