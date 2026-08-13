package subscription

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	nodeUsecases "github.com/orris-inc/orris/internal/application/node/usecases"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/id"
	"github.com/orris-inc/orris/internal/shared/logger"
	"github.com/orris-inc/orris/internal/shared/utils"
)

// Use case interface for NodeOrderHandler - enables unit testing with mocks.
type previewSubscriptionNodeOrderUseCase interface {
	Execute(ctx context.Context, cmd nodeUsecases.PreviewSubscriptionNodeOrderCommand) (*nodeUsecases.PreviewSubscriptionNodeOrderResult, error)
}

// NodeOrderHandler exposes what one subscription actually delivers, in the order its
// subscription link renders it.
//
// This is the whole sequence, unlike the per-resource-group ordering endpoints: it spans
// every resource group the plan maps to, includes the subscriber's own forward rules, and
// applies the same active-node filtering as the link itself.
type NodeOrderHandler struct {
	previewUC previewSubscriptionNodeOrderUseCase
	logger    logger.Interface
}

// NewNodeOrderHandler creates a new NodeOrderHandler.
func NewNodeOrderHandler(
	previewUC previewSubscriptionNodeOrderUseCase,
	logger logger.Interface,
) *NodeOrderHandler {
	return &NodeOrderHandler{
		previewUC: previewUC,
		logger:    logger,
	}
}

// SubscriptionNodeOrderEntryResponse is one delivered entry of a subscription.
type SubscriptionNodeOrderEntryResponse struct {
	Position int    `json:"position" example:"1"`
	Type     string `json:"type" example:"forward" description:"origin for a directly delivered node, forward for a forward rule"`
	ID       string `json:"id" example:"fr_abc123" description:"node ID when type is origin, forward rule ID when forward"`
	Scope    string `json:"scope,omitempty" example:"system" description:"system or user, only set for forward entries"`
	NodeID   string `json:"node_id" example:"node_abc123" description:"the node carrying the traffic"`
	Name     string `json:"name" example:"HK relay"`
	// SortOrder is the position in the shared sequence that produced this ordering.
	SortOrder     int    `json:"sort_order" example:"100"`
	Protocol      string `json:"protocol" example:"trojan"`
	ServerAddress string `json:"server_address" example:"1.2.3.4"`
	Port          uint16 `json:"port" example:"443"`
}

// SubscriptionNodeOrderResponse is a subscription's delivered order.
type SubscriptionNodeOrderResponse struct {
	SubscriptionID string `json:"subscription_id" example:"sub_abc123"`
	// Status explains an empty list: only active subscriptions deliver nodes.
	Status string                               `json:"status" example:"active"`
	Mode   string                               `json:"mode" example:"all"`
	Total  int                                  `json:"total" example:"3"`
	Items  []SubscriptionNodeOrderEntryResponse `json:"items"`
}

// GetNodeOrder handles GET /admin/subscriptions/:id/node-order?mode=all
func (h *NodeOrderHandler) GetNodeOrder(c *gin.Context) {
	subscriptionSID := c.Param("id")

	// Only accept the Stripe-style SID, as every other admin subscription route does,
	// so numeric IDs cannot be enumerated.
	if err := id.ValidatePrefix(subscriptionSID, id.PrefixSubscription); err != nil {
		h.logger.Warnw("invalid subscription id format", "subscription_id", subscriptionSID, "error", err, "ip", c.ClientIP())
		utils.ErrorResponseWithError(c, errors.NewValidationError("invalid subscription ID format, expected sub_xxxxx"))
		return
	}

	result, err := h.previewUC.Execute(c.Request.Context(), nodeUsecases.PreviewSubscriptionNodeOrderCommand{
		SubscriptionSID: subscriptionSID,
		Mode:            c.Query("mode"),
	})
	if err != nil {
		utils.ErrorResponseWithError(c, err)
		return
	}

	items := make([]SubscriptionNodeOrderEntryResponse, len(result.Entries))
	for i, entry := range result.Entries {
		items[i] = SubscriptionNodeOrderEntryResponse{
			Position:      entry.Position,
			Type:          string(entry.Type),
			ID:            entry.SID,
			Scope:         entry.Scope,
			NodeID:        entry.NodeSID,
			Name:          entry.Name,
			SortOrder:     entry.SortOrder,
			Protocol:      entry.Protocol,
			ServerAddress: entry.ServerAddress,
			Port:          entry.Port,
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Subscription node order retrieved successfully", SubscriptionNodeOrderResponse{
		SubscriptionID: result.SubscriptionSID,
		Status:         result.Status,
		Mode:           result.Mode,
		Total:          len(items),
		Items:          items,
	})
}
