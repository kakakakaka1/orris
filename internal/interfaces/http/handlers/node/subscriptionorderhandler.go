package node

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/orris-inc/orris/internal/application/node/usecases"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/id"
	"github.com/orris-inc/orris/internal/shared/logger"
	"github.com/orris-inc/orris/internal/shared/utils"
)

// Use case interfaces for SubscriptionOrderHandler - enables unit testing with mocks.

type listSubscriptionOrderUseCase interface {
	Execute(ctx context.Context, cmd usecases.ListSubscriptionOrderCommand) ([]usecases.SubscriptionOrderItem, error)
}

type reorderSubscriptionOrderUseCase interface {
	Execute(ctx context.Context, cmd usecases.ReorderSubscriptionOrderCommand) error
}

// SubscriptionOrderHandler exposes the merged ordering of a resource group: its origin
// nodes and the system forward rules bound to it, in the order a subscription renders
// them. Both kinds share one sort_order sequence, so an origin node can be positioned
// between forwarded ones.
type SubscriptionOrderHandler struct {
	listUC    listSubscriptionOrderUseCase
	reorderUC reorderSubscriptionOrderUseCase
	logger    logger.Interface
}

// NewSubscriptionOrderHandler creates a new SubscriptionOrderHandler.
func NewSubscriptionOrderHandler(
	listUC listSubscriptionOrderUseCase,
	reorderUC reorderSubscriptionOrderUseCase,
	logger logger.Interface,
) *SubscriptionOrderHandler {
	return &SubscriptionOrderHandler{
		listUC:    listUC,
		reorderUC: reorderUC,
		logger:    logger,
	}
}

// SubscriptionOrderItemResponse is one positioned entry of a subscription.
type SubscriptionOrderItemResponse struct {
	Type      string `json:"type" example:"origin" description:"origin for a directly delivered node, forward for a forward rule"`
	ID        string `json:"id" example:"node_abc123"`
	Name      string `json:"name" example:"HK-01"`
	SortOrder int    `json:"sort_order" example:"100"`
}

// SubscriptionOrderItemRequest positions a single entry.
type SubscriptionOrderItemRequest struct {
	Type      string `json:"type" binding:"required,oneof=origin forward" example:"origin"`
	ID        string `json:"id" binding:"required" example:"node_abc123"`
	SortOrder int    `json:"sort_order" binding:"gte=0" example:"100"`
}

// ReorderSubscriptionOrderRequest carries the new ordering.
type ReorderSubscriptionOrderRequest struct {
	Items []SubscriptionOrderItemRequest `json:"items" binding:"required,min=1,dive"`
}

// GetOrder handles GET /nodes/subscription-order?group_id=rg_xxx
func (h *SubscriptionOrderHandler) GetOrder(c *gin.Context) {
	groupSID := c.Query("group_id")
	if groupSID == "" {
		utils.ErrorResponseWithError(c, errors.NewValidationError("group_id is required"))
		return
	}

	if err := id.ValidatePrefix(groupSID, id.PrefixResourceGroup); err != nil {
		h.logger.Warnw("invalid group_id format", "group_id", groupSID, "error", err, "ip", c.ClientIP())
		utils.ErrorResponseWithError(c, errors.NewValidationError("invalid group_id format, expected rg_xxxxx"))
		return
	}

	items, err := h.listUC.Execute(c.Request.Context(), usecases.ListSubscriptionOrderCommand{
		GroupSID: groupSID,
	})
	if err != nil {
		utils.ErrorResponseWithError(c, err)
		return
	}

	response := make([]SubscriptionOrderItemResponse, len(items))
	for i, item := range items {
		response[i] = SubscriptionOrderItemResponse{
			Type:      string(item.Type),
			ID:        item.SID,
			Name:      item.Name,
			SortOrder: item.SortOrder,
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Subscription order retrieved successfully", response)
}

// UpdateOrder handles PATCH /nodes/subscription-order
func (h *SubscriptionOrderHandler) UpdateOrder(c *gin.Context) {
	var req ReorderSubscriptionOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warnw("invalid request body for reorder subscription order", "error", err, "ip", c.ClientIP())
		utils.ErrorResponseWithError(c, err)
		return
	}

	items := make([]usecases.SubscriptionOrderItem, len(req.Items))
	for i, item := range req.Items {
		itemType := usecases.SubscriptionOrderItemType(item.Type)

		// The SID prefix must match the declared kind, otherwise a node ID sent as a
		// forward rule would be reported as a missing rule.
		prefix := id.PrefixNode
		if itemType == usecases.SubscriptionOrderItemForward {
			prefix = id.PrefixForwardRule
		}
		if err := id.ValidatePrefix(item.ID, prefix); err != nil {
			h.logger.Warnw("invalid item id format", "id", item.ID, "type", item.Type, "error", err, "ip", c.ClientIP())
			utils.ErrorResponseWithError(c, errors.NewValidationError("invalid id format for type "+item.Type+", expected "+prefix+"_xxxxx"))
			return
		}

		items[i] = usecases.SubscriptionOrderItem{
			Type:      itemType,
			SID:       item.ID,
			SortOrder: item.SortOrder,
		}
	}

	if err := h.reorderUC.Execute(c.Request.Context(), usecases.ReorderSubscriptionOrderCommand{Items: items}); err != nil {
		utils.ErrorResponseWithError(c, err)
		return
	}

	utils.NoContentResponse(c)
}
