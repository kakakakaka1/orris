package usecases

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildRuleSortOrdersAdminWritesRequestedValues(t *testing.T) {
	// An admin orders rules against the whole sequence, origin nodes included, so the
	// requested values are persisted as-is.
	resolved := []resolvedRuleOrder{
		{ruleID: 1, current: 500, requested: 100},
		{ruleID: 2, current: 600, requested: 200},
	}

	got := buildRuleSortOrders(resolved, false)

	assert.Equal(t, map[uint]int{1: 100, 2: 200}, got)
}

func TestBuildRuleSortOrdersScopedKeepsOccupiedPositions(t *testing.T) {
	// A user's drag-and-drop UI only sees their own rules and sends dense positions.
	// Writing 1..N verbatim would drag the whole set ahead of every origin node, so the
	// rules must swap places within the positions they already hold.
	resolved := []resolvedRuleOrder{
		{ruleID: 1, current: 700, requested: 3},
		{ruleID: 2, current: 800, requested: 1},
		{ruleID: 3, current: 900, requested: 2},
	}

	got := buildRuleSortOrders(resolved, true)

	// Requested order is 2, 3, 1 — mapped onto the sorted slots 700, 800, 900.
	assert.Equal(t, map[uint]int{2: 700, 3: 800, 1: 900}, got)
}

func TestBuildRuleSortOrdersScopedIgnoresRequestedMagnitude(t *testing.T) {
	// Only the relative order of the requested values matters for a scoped caller.
	dense := []resolvedRuleOrder{
		{ruleID: 1, current: 700, requested: 1},
		{ruleID: 2, current: 800, requested: 0},
	}
	sparse := []resolvedRuleOrder{
		{ruleID: 1, current: 700, requested: 5000},
		{ruleID: 2, current: 800, requested: 42},
	}

	assert.Equal(t, buildRuleSortOrders(dense, true), buildRuleSortOrders(sparse, true))
}

func TestBuildRuleSortOrdersScopedWithUnsortedCurrentValues(t *testing.T) {
	// Slots are collected in sorted order regardless of the order rules arrive in.
	resolved := []resolvedRuleOrder{
		{ruleID: 1, current: 900, requested: 1},
		{ruleID: 2, current: 700, requested: 2},
		{ruleID: 3, current: 800, requested: 3},
	}

	got := buildRuleSortOrders(resolved, true)

	assert.Equal(t, map[uint]int{1: 700, 2: 800, 3: 900}, got)
}

func TestBuildRuleSortOrdersScopedSingleRuleStaysPut(t *testing.T) {
	resolved := []resolvedRuleOrder{{ruleID: 1, current: 700, requested: 1}}

	got := buildRuleSortOrders(resolved, true)

	assert.Equal(t, map[uint]int{1: 700}, got)
}

func TestBuildRuleSortOrdersEmpty(t *testing.T) {
	assert.Empty(t, buildRuleSortOrders(nil, true))
	assert.Empty(t, buildRuleSortOrders(nil, false))
}
