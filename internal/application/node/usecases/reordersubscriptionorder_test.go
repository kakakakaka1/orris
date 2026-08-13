package usecases

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orris-inc/orris/internal/shared/errors"
)

func TestSplitOrderItemsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		items []SubscriptionOrderItem
	}{
		{
			name:  "no items",
			items: nil,
		},
		{
			name:  "missing id",
			items: []SubscriptionOrderItem{{Type: SubscriptionOrderItemOrigin, SortOrder: 100}},
		},
		{
			name:  "unknown type",
			items: []SubscriptionOrderItem{{Type: "external", SID: "node_abc", SortOrder: 100}},
		},
		{
			name:  "empty type",
			items: []SubscriptionOrderItem{{SID: "node_abc", SortOrder: 100}},
		},
		{
			// Otherwise the caller silently gets whichever position was written last.
			name: "duplicate node",
			items: []SubscriptionOrderItem{
				{Type: SubscriptionOrderItemOrigin, SID: "node_abc", SortOrder: 100},
				{Type: SubscriptionOrderItemOrigin, SID: "node_abc", SortOrder: 200},
			},
		},
		{
			name: "duplicate forward rule",
			items: []SubscriptionOrderItem{
				{Type: SubscriptionOrderItemForward, SID: "fr_abc", SortOrder: 100},
				{Type: SubscriptionOrderItemForward, SID: "fr_abc", SortOrder: 200},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := splitOrderItems(tt.items)

			require.Error(t, err)
			assert.True(t, errors.IsValidationError(err), "expected a validation error, got %v", err)
		})
	}
}

func TestSplitOrderItemsSplitsByKind(t *testing.T) {
	nodeOrders, ruleOrders, err := splitOrderItems([]SubscriptionOrderItem{
		{Type: SubscriptionOrderItemOrigin, SID: "node_a", SortOrder: 100},
		{Type: SubscriptionOrderItemForward, SID: "fr_a", SortOrder: 200},
		{Type: SubscriptionOrderItemOrigin, SID: "node_b", SortOrder: 300},
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]int{"node_a": 100, "node_b": 300}, nodeOrders)
	assert.Equal(t, map[string]int{"fr_a": 200}, ruleOrders)
}

// A node and a forward rule are distinct entries even when their SIDs collide, so the
// same string under the two kinds must not trip duplicate detection.
func TestSplitOrderItemsAllowsSameSIDAcrossKinds(t *testing.T) {
	nodeOrders, ruleOrders, err := splitOrderItems([]SubscriptionOrderItem{
		{Type: SubscriptionOrderItemOrigin, SID: "abc", SortOrder: 100},
		{Type: SubscriptionOrderItemForward, SID: "abc", SortOrder: 200},
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]int{"abc": 100}, nodeOrders)
	assert.Equal(t, map[string]int{"abc": 200}, ruleOrders)
}
