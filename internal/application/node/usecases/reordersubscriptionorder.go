package usecases

import (
	"context"
	"fmt"

	"github.com/orris-inc/orris/internal/domain/forward"
	"github.com/orris-inc/orris/internal/domain/node"
	"github.com/orris-inc/orris/internal/shared/db"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// ReorderSubscriptionOrderCommand carries the new positions. Only Type, SID and
// SortOrder are read; Name is ignored so callers can echo back a listing unchanged.
type ReorderSubscriptionOrderCommand struct {
	Items []SubscriptionOrderItem
}

// ReorderSubscriptionOrderUseCase writes new positions for origin nodes and system
// forward rules in one transaction, so a subscription never renders a half-applied order.
//
// Both kinds are written to the same value space: nodes.sort_order and
// forward_rules.sort_order are compared directly when the subscription is generated.
type ReorderSubscriptionOrderUseCase struct {
	nodeRepo    node.NodeRepository
	forwardRepo forward.Repository
	txMgr       *db.TransactionManager
	logger      logger.Interface
}

// NewReorderSubscriptionOrderUseCase creates a new ReorderSubscriptionOrderUseCase.
func NewReorderSubscriptionOrderUseCase(
	nodeRepo node.NodeRepository,
	forwardRepo forward.Repository,
	txMgr *db.TransactionManager,
	logger logger.Interface,
) *ReorderSubscriptionOrderUseCase {
	return &ReorderSubscriptionOrderUseCase{
		nodeRepo:    nodeRepo,
		forwardRepo: forwardRepo,
		txMgr:       txMgr,
		logger:      logger,
	}
}

// splitOrderItems validates the requested positions and splits them by kind.
//
// A SID that appears twice under the same kind is rejected rather than resolved, since
// the caller would otherwise silently get whichever position was written last. The same
// SID under both kinds is fine: a node and a forward rule are separate entries.
func splitOrderItems(items []SubscriptionOrderItem) (nodeSortOrders, ruleSortOrders map[string]int, err error) {
	if len(items) == 0 {
		return nil, nil, errors.NewValidationError("items is required")
	}

	nodeSortOrders = make(map[string]int, len(items))
	ruleSortOrders = make(map[string]int, len(items))

	for _, item := range items {
		if item.SID == "" {
			return nil, nil, errors.NewValidationError("id is required for each item")
		}

		switch item.Type {
		case SubscriptionOrderItemOrigin:
			if _, seen := nodeSortOrders[item.SID]; seen {
				return nil, nil, errors.NewValidationError(fmt.Sprintf("duplicate item: %s", item.SID))
			}
			nodeSortOrders[item.SID] = item.SortOrder
		case SubscriptionOrderItemForward:
			if _, seen := ruleSortOrders[item.SID]; seen {
				return nil, nil, errors.NewValidationError(fmt.Sprintf("duplicate item: %s", item.SID))
			}
			ruleSortOrders[item.SID] = item.SortOrder
		default:
			return nil, nil, errors.NewValidationError(fmt.Sprintf("invalid type: %s, expected origin or forward", item.Type))
		}
	}

	return nodeSortOrders, ruleSortOrders, nil
}

// Execute applies the new ordering.
func (uc *ReorderSubscriptionOrderUseCase) Execute(ctx context.Context, cmd ReorderSubscriptionOrderCommand) error {
	nodeSortOrders, ruleSortOrders, err := splitOrderItems(cmd.Items)
	if err != nil {
		return err
	}

	uc.logger.Infow("executing reorder subscription order use case",
		"origin_count", len(nodeSortOrders),
		"forward_count", len(ruleSortOrders),
	)

	return uc.txMgr.RunInTransaction(ctx, func(txCtx context.Context) error {
		nodeOrders, err := uc.resolveNodeOrders(txCtx, nodeSortOrders)
		if err != nil {
			return err
		}

		ruleOrders, err := uc.resolveRuleOrders(txCtx, ruleSortOrders)
		if err != nil {
			return err
		}

		if err := uc.nodeRepo.UpdateSortOrders(txCtx, nodeOrders); err != nil {
			uc.logger.Errorw("failed to update node sort orders", "error", err)
			return fmt.Errorf("failed to update node sort orders: %w", err)
		}

		if err := uc.forwardRepo.UpdateSortOrders(txCtx, ruleOrders); err != nil {
			uc.logger.Errorw("failed to update forward rule sort orders", "error", err)
			return fmt.Errorf("failed to update forward rule sort orders: %w", err)
		}

		uc.logger.Infow("subscription order updated successfully",
			"origin_count", len(nodeOrders),
			"forward_count", len(ruleOrders),
		)
		return nil
	})
}

// resolveNodeOrders maps node SIDs to internal IDs, failing on any unknown SID.
func (uc *ReorderSubscriptionOrderUseCase) resolveNodeOrders(ctx context.Context, sortOrders map[string]int) (map[uint]int, error) {
	orders := make(map[uint]int, len(sortOrders))
	if len(sortOrders) == 0 {
		return orders, nil
	}

	sids := make([]string, 0, len(sortOrders))
	for sid := range sortOrders {
		sids = append(sids, sid)
	}

	nodes, err := uc.nodeRepo.GetBySIDs(ctx, sids)
	if err != nil {
		uc.logger.Errorw("failed to get nodes by SIDs", "count", len(sids), "error", err)
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	found := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		orders[n.ID()] = sortOrders[n.SID()]
		found[n.SID()] = true
	}

	for _, sid := range sids {
		if !found[sid] {
			return nil, errors.NewNotFoundError("node", sid)
		}
	}

	return orders, nil
}

// resolveRuleOrders maps forward rule SIDs to internal IDs, failing on any unknown SID.
func (uc *ReorderSubscriptionOrderUseCase) resolveRuleOrders(ctx context.Context, sortOrders map[string]int) (map[uint]int, error) {
	orders := make(map[uint]int, len(sortOrders))
	if len(sortOrders) == 0 {
		return orders, nil
	}

	sids := make([]string, 0, len(sortOrders))
	for sid := range sortOrders {
		sids = append(sids, sid)
	}

	rulesMap, err := uc.forwardRepo.GetBySIDs(ctx, sids)
	if err != nil {
		uc.logger.Errorw("failed to get forward rules by SIDs", "count", len(sids), "error", err)
		return nil, fmt.Errorf("failed to get forward rules: %w", err)
	}

	for _, sid := range sids {
		rule, ok := rulesMap[sid]
		if !ok {
			return nil, errors.NewNotFoundError("forward rule", sid)
		}
		orders[rule.ID()] = sortOrders[sid]
	}

	return orders, nil
}
