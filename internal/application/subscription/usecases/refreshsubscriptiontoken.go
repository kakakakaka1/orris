package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/orris-inc/orris/internal/domain/subscription"
	apperrors "github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

type RefreshSubscriptionTokenCommand struct {
	// SubscriptionID is the owner-verified subscription the token must belong to.
	SubscriptionID uint
	OldTokenID     uint
}

type RefreshSubscriptionTokenResult struct {
	NewToken  string     `json:"new_token"`
	TokenID   uint       `json:"token_id"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type RefreshSubscriptionTokenUseCase struct {
	tokenRepo        subscription.SubscriptionTokenRepository
	subscriptionRepo subscription.SubscriptionRepository
	tokenGenerator   TokenGenerator
	logger           logger.Interface
}

func NewRefreshSubscriptionTokenUseCase(
	tokenRepo subscription.SubscriptionTokenRepository,
	subscriptionRepo subscription.SubscriptionRepository,
	tokenGenerator TokenGenerator,
	logger logger.Interface,
) *RefreshSubscriptionTokenUseCase {
	return &RefreshSubscriptionTokenUseCase{
		tokenRepo:        tokenRepo,
		subscriptionRepo: subscriptionRepo,
		tokenGenerator:   tokenGenerator,
		logger:           logger,
	}
}

func (uc *RefreshSubscriptionTokenUseCase) Execute(ctx context.Context, cmd RefreshSubscriptionTokenCommand) (*RefreshSubscriptionTokenResult, error) {
	oldToken, err := uc.tokenRepo.GetByID(ctx, cmd.OldTokenID)
	if err != nil {
		uc.logger.Errorw("failed to get old token", "error", err, "token_id", cmd.OldTokenID)
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Authorization: the token must belong to the owner-verified subscription.
	// Without this check any authenticated user owning any subscription could mint a
	// fresh valid token for another user's subscription by supplying an arbitrary
	// (enumerable) token ID, enabling full takeover of the victim's subscription feed.
	if oldToken.SubscriptionID() != cmd.SubscriptionID {
		uc.logger.Warnw("subscription token does not belong to subscription",
			"token_id", cmd.OldTokenID,
			"token_subscription_id", oldToken.SubscriptionID(),
			"request_subscription_id", cmd.SubscriptionID,
		)
		return nil, apperrors.NewNotFoundError("subscription token not found")
	}

	if !oldToken.IsValid() {
		return nil, fmt.Errorf("old token is invalid or expired")
	}

	sub, err := uc.subscriptionRepo.GetByID(ctx, oldToken.SubscriptionID())
	if err != nil {
		uc.logger.Errorw("failed to get subscription", "error", err, "subscription_id", oldToken.SubscriptionID())
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}

	if !sub.IsActive() {
		return nil, fmt.Errorf("subscription is not active")
	}

	plainToken, hashedToken, err := uc.tokenGenerator.Generate("sk")
	if err != nil {
		uc.logger.Errorw("failed to generate new token", "error", err)
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	prefix := plainToken[:8]

	newToken, err := subscription.NewSubscriptionToken(
		oldToken.SubscriptionID(),
		oldToken.Name(),
		hashedToken,
		prefix,
		oldToken.Scope(),
		oldToken.ExpiresAt(),
	)
	if err != nil {
		uc.logger.Errorw("failed to create new token entity", "error", err)
		return nil, fmt.Errorf("failed to create token: %w", err)
	}

	// Create new token first to ensure user always has a valid token
	// This prevents data inconsistency where old token is revoked but new token creation fails
	if err := uc.tokenRepo.Create(ctx, newToken); err != nil {
		uc.logger.Errorw("failed to persist new token", "error", err)
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	// Revoke old token after new token is successfully created
	// Brief window of two valid tokens is acceptable to prevent service disruption
	if err := oldToken.Revoke(); err != nil {
		uc.logger.Warnw("failed to revoke old token, new token still valid",
			"error", err,
			"old_token_id", cmd.OldTokenID,
			"new_token_id", newToken.ID(),
		)
		// Continue - new token is valid, old token revocation is best-effort
	} else {
		if err := uc.tokenRepo.Update(ctx, oldToken); err != nil {
			uc.logger.Warnw("failed to update old token status, new token still valid",
				"error", err,
				"old_token_id", cmd.OldTokenID,
			)
			// Continue - new token is valid, old token status update is best-effort
		}
	}

	uc.logger.Debugw("token refreshed successfully",
		"old_token_id", cmd.OldTokenID,
		"new_token_id", newToken.ID(),
		"subscription_id", oldToken.SubscriptionID(),
	)

	return &RefreshSubscriptionTokenResult{
		NewToken:  plainToken,
		TokenID:   newToken.ID(),
		ExpiresAt: newToken.ExpiresAt(),
	}, nil
}
