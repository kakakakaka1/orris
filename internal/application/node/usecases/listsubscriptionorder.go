package usecases

import (
	"context"
	"fmt"
	"sort"

	"github.com/orris-inc/orris/internal/domain/forward"
	"github.com/orris-inc/orris/internal/domain/node"
	"github.com/orris-inc/orris/internal/domain/resource"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// SubscriptionOrderItemType distinguishes the two kinds of entry that make up a
// subscription. Both draw positions from one shared sort_order value space, which is
// what lets an origin node sit between forwarded ones.
type SubscriptionOrderItemType string

const (
	// SubscriptionOrderItemOrigin is a node delivered directly, ordered by nodes.sort_order.
	SubscriptionOrderItemOrigin SubscriptionOrderItemType = "origin"
	// SubscriptionOrderItemForward is a forward rule delivered as a node, ordered by
	// forward_rules.sort_order.
	SubscriptionOrderItemForward SubscriptionOrderItemType = "forward"
)

// SubscriptionOrderItem is one positioned entry in a subscription's merged ordering.
// SID refers to a node (node_xxx) or a forward rule (fr_xxx) according to Type, so the
// two fields together form the identity of an entry — a single node can appear once as
// an origin entry and again behind several forward rules.
type SubscriptionOrderItem struct {
	Type      SubscriptionOrderItemType
	SID       string
	Name      string
	SortOrder int
}

// ListSubscriptionOrderCommand selects the resource group whose ordering to read.
type ListSubscriptionOrderCommand struct {
	GroupSID string
}

// ListSubscriptionOrderUseCase returns the merged, ordered entries of a resource group:
// its origin nodes interleaved with the system forward rules bound to it.
//
// User-owned forward rules are deliberately excluded. They belong to individual users
// rather than to a resource group, and users position them through the forward rule
// reorder endpoints.
type ListSubscriptionOrderUseCase struct {
	nodeRepo     node.NodeRepository
	forwardRepo  forward.Repository
	resourceRepo resource.Repository
	logger       logger.Interface
}

// NewListSubscriptionOrderUseCase creates a new ListSubscriptionOrderUseCase.
func NewListSubscriptionOrderUseCase(
	nodeRepo node.NodeRepository,
	forwardRepo forward.Repository,
	resourceRepo resource.Repository,
	logger logger.Interface,
) *ListSubscriptionOrderUseCase {
	return &ListSubscriptionOrderUseCase{
		nodeRepo:     nodeRepo,
		forwardRepo:  forwardRepo,
		resourceRepo: resourceRepo,
		logger:       logger,
	}
}

// Execute returns the group's entries sorted the way the subscription renders them.
func (uc *ListSubscriptionOrderUseCase) Execute(ctx context.Context, cmd ListSubscriptionOrderCommand) ([]SubscriptionOrderItem, error) {
	if cmd.GroupSID == "" {
		return nil, errors.NewValidationError("group_id is required")
	}

	group, err := uc.resourceRepo.GetBySID(ctx, cmd.GroupSID)
	if err != nil {
		uc.logger.Errorw("failed to get resource group", "group_sid", cmd.GroupSID, "error", err)
		return nil, fmt.Errorf("failed to get resource group: %w", err)
	}
	if group == nil {
		return nil, errors.NewNotFoundError("resource group", cmd.GroupSID)
	}

	originItems, err := uc.originItems(ctx, group.ID())
	if err != nil {
		return nil, err
	}

	forwardItems, err := uc.forwardItems(ctx, group.ID())
	if err != nil {
		return nil, err
	}

	// Append origin before forward so that ties break the same way mergeBySortOrder
	// breaks them when the subscription is generated.
	items := make([]SubscriptionOrderItem, 0, len(originItems)+len(forwardItems))
	items = append(items, originItems...)
	items = append(items, forwardItems...)

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].SortOrder < items[j].SortOrder
	})

	uc.logger.Debugw("listed subscription order",
		"group_sid", cmd.GroupSID,
		"origin_count", len(originItems),
		"forward_count", len(forwardItems),
	)

	return items, nil
}

// originItems collects the group's directly delivered nodes.
func (uc *ListSubscriptionOrderUseCase) originItems(ctx context.Context, groupID uint) ([]SubscriptionOrderItem, error) {
	nodeIDs, err := uc.nodeRepo.GetIDsByGroupID(ctx, groupID)
	if err != nil {
		uc.logger.Errorw("failed to get node IDs by group", "group_id", groupID, "error", err)
		return nil, fmt.Errorf("failed to get nodes for group: %w", err)
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	nodes, err := uc.nodeRepo.GetByIDs(ctx, nodeIDs)
	if err != nil {
		uc.logger.Errorw("failed to get nodes by IDs", "group_id", groupID, "error", err)
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	items := make([]SubscriptionOrderItem, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, SubscriptionOrderItem{
			Type:      SubscriptionOrderItemOrigin,
			SID:       n.SID(),
			Name:      n.Name(),
			SortOrder: n.SortOrder(),
		})
	}

	return items, nil
}

// forwardItems collects the system forward rules bound to the group.
func (uc *ListSubscriptionOrderUseCase) forwardItems(ctx context.Context, groupID uint) ([]SubscriptionOrderItem, error) {
	rules, err := uc.forwardRepo.ListSystemRulesByGroupIDs(ctx, []uint{groupID})
	if err != nil {
		uc.logger.Errorw("failed to list system rules by group", "group_id", groupID, "error", err)
		return nil, fmt.Errorf("failed to get forward rules for group: %w", err)
	}

	items := make([]SubscriptionOrderItem, 0, len(rules))
	for _, rule := range rules {
		items = append(items, SubscriptionOrderItem{
			Type:      SubscriptionOrderItemForward,
			SID:       rule.SID(),
			Name:      rule.Name(),
			SortOrder: rule.SortOrder(),
		})
	}

	return items, nil
}
