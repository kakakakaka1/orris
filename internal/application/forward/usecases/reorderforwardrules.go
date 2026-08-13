package usecases

import (
	"context"
	"fmt"
	"sort"

	"github.com/orris-inc/orris/internal/domain/forward"
	"github.com/orris-inc/orris/internal/shared/db"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// RuleOrder represents a single rule's sort order.
type RuleOrder struct {
	RuleSID   string
	SortOrder int
}

// resolvedRuleOrder pairs a rule's existing position with the requested one.
type resolvedRuleOrder struct {
	ruleID    uint
	current   int
	requested int
}

// buildRuleSortOrders turns requested positions into sort_order values to persist.
//
// forward_rules.sort_order shares one value space with nodes.sort_order — the
// subscription is rendered by merging both kinds on that single key — so what a caller
// is allowed to write depends on how much of the sequence it can see.
//
// Admin callers order rules against the whole sequence, origin nodes included, so their
// values are written through unchanged.
//
// Scoped callers (a user, or a subscription owner) only ever see their own rules. A
// drag-and-drop UI therefore sends dense positions like 1..N, and writing those verbatim
// would drag the entire set ahead of every origin node. Instead the rules keep the exact
// set of positions they already occupy and merely swap places among themselves; where
// that block sits relative to origin nodes stays an administrative decision.
func buildRuleSortOrders(resolved []resolvedRuleOrder, scoped bool) map[uint]int {
	orders := make(map[uint]int, len(resolved))

	if !scoped {
		for _, r := range resolved {
			orders[r.ruleID] = r.requested
		}
		return orders
	}

	slots := make([]int, len(resolved))
	for i, r := range resolved {
		slots[i] = r.current
	}
	sort.Ints(slots)

	ordered := make([]resolvedRuleOrder, len(resolved))
	copy(ordered, resolved)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].requested < ordered[j].requested
	})

	for i, r := range ordered {
		orders[r.ruleID] = slots[i]
	}

	return orders
}

// ReorderForwardRulesCommand represents the input for reordering forward rules.
type ReorderForwardRulesCommand struct {
	RuleOrders     []RuleOrder
	UserID         *uint // optional: if set, only reorder rules owned by this user
	SubscriptionID *uint // optional: if set, only reorder rules belonging to this subscription
}

// ReorderForwardRulesUseCase handles batch reordering of forward rules.
type ReorderForwardRulesUseCase struct {
	repo   forward.Repository
	txMgr  *db.TransactionManager
	logger logger.Interface
}

// NewReorderForwardRulesUseCase creates a new ReorderForwardRulesUseCase.
func NewReorderForwardRulesUseCase(
	repo forward.Repository,
	txMgr *db.TransactionManager,
	logger logger.Interface,
) *ReorderForwardRulesUseCase {
	return &ReorderForwardRulesUseCase{
		repo:   repo,
		txMgr:  txMgr,
		logger: logger,
	}
}

// Execute reorders multiple forward rules.
func (uc *ReorderForwardRulesUseCase) Execute(ctx context.Context, cmd ReorderForwardRulesCommand) error {
	if len(cmd.RuleOrders) == 0 {
		return errors.NewValidationError("rule_orders is required")
	}

	uc.logger.Infow("executing reorder forward rules use case", "count", len(cmd.RuleOrders))

	// Collect rule SIDs for batch query
	sids := make([]string, 0, len(cmd.RuleOrders))
	for _, order := range cmd.RuleOrders {
		if order.RuleSID == "" {
			return errors.NewValidationError("rule_id is required for each item")
		}
		sids = append(sids, order.RuleSID)
	}

	// Run in transaction to ensure atomicity
	return uc.txMgr.RunInTransaction(ctx, func(txCtx context.Context) error {
		// Batch query all rules
		rulesMap, err := uc.repo.GetBySIDs(txCtx, sids)
		if err != nil {
			uc.logger.Errorw("failed to get rules by SIDs", "count", len(sids), "error", err)
			return fmt.Errorf("failed to get rules: %w", err)
		}

		// Resolve rules and validate ownership
		resolved := make([]resolvedRuleOrder, 0, len(cmd.RuleOrders))
		for _, order := range cmd.RuleOrders {
			rule, exists := rulesMap[order.RuleSID]
			if !exists {
				return errors.NewNotFoundError("forward rule", order.RuleSID)
			}

			// If user ID is specified, verify ownership
			if cmd.UserID != nil {
				ruleUserID := rule.UserID()
				if ruleUserID == nil || *ruleUserID != *cmd.UserID {
					uc.logger.Warnw("user attempted to reorder rule they don't own",
						"user_id", *cmd.UserID,
						"rule_sid", order.RuleSID,
						"rule_owner", ruleUserID,
					)
					return errors.NewForbiddenError("cannot reorder this rule")
				}
			}

			// If subscription ID is specified, verify rule belongs to this subscription
			if cmd.SubscriptionID != nil {
				ruleSubscriptionID := rule.SubscriptionID()
				if ruleSubscriptionID == nil || *ruleSubscriptionID != *cmd.SubscriptionID {
					uc.logger.Warnw("user attempted to reorder rule from another subscription",
						"subscription_id", *cmd.SubscriptionID,
						"rule_sid", order.RuleSID,
						"rule_subscription", ruleSubscriptionID,
					)
					return errors.NewForbiddenError("cannot reorder this rule")
				}
			}

			resolved = append(resolved, resolvedRuleOrder{
				ruleID:    rule.ID(),
				current:   rule.SortOrder(),
				requested: order.SortOrder,
			})
		}

		// Scoped callers only see their own rules, so their requested values are
		// relative and must be mapped back onto the positions they already hold.
		scoped := cmd.UserID != nil || cmd.SubscriptionID != nil
		ruleOrders := buildRuleSortOrders(resolved, scoped)

		// Update sort orders in batch
		if err := uc.repo.UpdateSortOrders(txCtx, ruleOrders); err != nil {
			uc.logger.Errorw("failed to update sort orders", "error", err)
			return fmt.Errorf("failed to update sort orders: %w", err)
		}

		uc.logger.Infow("forward rules reordered successfully", "count", len(cmd.RuleOrders))
		return nil
	})
}
