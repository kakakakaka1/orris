package usecases

import (
	"context"
	"fmt"

	"github.com/orris-inc/orris/internal/domain/subscription"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// SubscriptionLookup resolves a subscription by its Stripe-style ID.
//
// Only the read side of subscription.SubscriptionRepository is needed here; the narrow
// interface keeps this use case testable without a subscription repository.
type SubscriptionLookup interface {
	GetBySID(ctx context.Context, sid string) (*subscription.Subscription, error)
}

// SubscriptionNodeEntry is one entry of a rendered subscription, in delivery order.
//
// Type and SID together identify the entry: the same node is delivered once directly and
// again behind every forward rule that targets it, so NodeSID alone is ambiguous.
type SubscriptionNodeEntry struct {
	// Position is the 1-based index in the delivered list.
	Position int
	Type     SubscriptionOrderItemType
	// SID is the node SID for origin entries, the forward rule SID for forward ones.
	SID string
	// Scope is "system" or "user" for forward entries, empty for origin entries.
	Scope string
	// NodeSID is the node carrying the traffic; equals SID for origin entries.
	NodeSID       string
	Name          string
	SortOrder     int
	Protocol      string
	ServerAddress string
	Port          uint16
}

// PreviewSubscriptionNodeOrderCommand selects the subscription to render.
type PreviewSubscriptionNodeOrderCommand struct {
	SubscriptionSID string
	// Mode is "all", "origin" or "forward"; empty means "all", matching the
	// subscription endpoints.
	Mode string
}

// PreviewSubscriptionNodeOrderResult is the rendered order plus the context needed to
// read it: an inactive subscription delivers nothing, and the mode decides which kinds
// of entry are included at all.
type PreviewSubscriptionNodeOrderResult struct {
	SubscriptionSID string
	Status          string
	Mode            string
	Entries         []SubscriptionNodeEntry
}

// PreviewSubscriptionNodeOrderUseCase returns the entries one subscription delivers, in
// the exact order its subscription link renders them.
//
// Unlike ListSubscriptionOrderUseCase's per-resource-group view, this covers every
// group the plan maps to, includes the subscriber's own forward rules, and applies the
// same active-node filtering as the subscription link — it reads the delivery path itself
// rather than reproducing it, so a preview cannot drift from what clients receive.
type PreviewSubscriptionNodeOrderUseCase struct {
	subscriptionRepo SubscriptionLookup
	nodeRepo         NodeRepository
	logger           logger.Interface
}

// NewPreviewSubscriptionNodeOrderUseCase creates a new PreviewSubscriptionNodeOrderUseCase.
func NewPreviewSubscriptionNodeOrderUseCase(
	subscriptionRepo SubscriptionLookup,
	nodeRepo NodeRepository,
	logger logger.Interface,
) *PreviewSubscriptionNodeOrderUseCase {
	return &PreviewSubscriptionNodeOrderUseCase{
		subscriptionRepo: subscriptionRepo,
		nodeRepo:         nodeRepo,
		logger:           logger,
	}
}

// Execute renders the subscription's delivery order.
func (uc *PreviewSubscriptionNodeOrderUseCase) Execute(
	ctx context.Context,
	cmd PreviewSubscriptionNodeOrderCommand,
) (*PreviewSubscriptionNodeOrderResult, error) {
	if cmd.SubscriptionSID == "" {
		return nil, errors.NewValidationError("subscription id is required")
	}

	mode := cmd.Mode
	if mode == "" {
		mode = NodeModeAll
	}
	if mode != NodeModeAll && mode != NodeModeOrigin && mode != NodeModeForward {
		return nil, errors.NewValidationError("invalid mode, expected all, origin or forward")
	}

	sub, err := uc.subscriptionRepo.GetBySID(ctx, cmd.SubscriptionSID)
	if err != nil {
		uc.logger.Errorw("failed to get subscription", "subscription_sid", cmd.SubscriptionSID, "error", err)
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}
	if sub == nil {
		return nil, errors.NewNotFoundError("subscription", cmd.SubscriptionSID)
	}

	result := &PreviewSubscriptionNodeOrderResult{
		SubscriptionSID: sub.SID(),
		Status:          string(sub.Status()),
		Mode:            mode,
		Entries:         []SubscriptionNodeEntry{},
	}

	// A subscription without a link token has nothing to pull from, and the delivery
	// path is keyed by that token.
	if sub.LinkToken() == "" {
		uc.logger.Warnw("subscription has no link token", "subscription_sid", sub.SID())
		return result, nil
	}

	// Reuse the subscription delivery path: it filters inactive nodes, resolves every
	// resource group of the plan, and merges the subscriber's own forward rules. Note
	// that a non-active subscription delivers nothing, which is why Status is reported.
	nodes, err := uc.nodeRepo.GetBySubscriptionToken(ctx, sub.LinkToken(), mode)
	if err != nil {
		uc.logger.Errorw("failed to get subscription nodes", "subscription_sid", sub.SID(), "error", err)
		return nil, fmt.Errorf("failed to get subscription nodes: %w", err)
	}

	entries := make([]SubscriptionNodeEntry, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		entries = append(entries, SubscriptionNodeEntry{
			Position:      len(entries) + 1,
			Type:          n.EntryType,
			SID:           n.EntrySID,
			Scope:         n.EntryScope,
			NodeSID:       n.NodeSID,
			Name:          n.Name,
			SortOrder:     n.SortOrder,
			Protocol:      n.Protocol,
			ServerAddress: n.ServerAddress,
			Port:          n.SubscriptionPort,
		})
	}
	result.Entries = entries

	uc.logger.Debugw("previewed subscription node order",
		"subscription_sid", sub.SID(),
		"status", result.Status,
		"mode", mode,
		"entry_count", len(entries),
	)

	return result, nil
}
