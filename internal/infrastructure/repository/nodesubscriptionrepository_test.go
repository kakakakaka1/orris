package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/orris-inc/orris/internal/application/node/usecases"
)

// orderedNode builds a subscription entry carrying only what the merge looks at.
func orderedNode(name string, sortOrder int) *usecases.Node {
	return &usecases.Node{Name: name, SortOrder: sortOrder}
}

func names(nodes []*usecases.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return out
}

func TestMergeBySortOrder(t *testing.T) {
	tests := []struct {
		name   string
		groups [][]*usecases.Node
		want   []string
	}{
		{
			name:   "no groups",
			groups: nil,
			want:   []string{},
		},
		{
			name:   "all groups empty",
			groups: [][]*usecases.Node{{}, {}},
			want:   []string{},
		},
		{
			name: "origin node placed between forwarded ones",
			groups: [][]*usecases.Node{
				{orderedNode("origin", 200)},
				{orderedNode("fwd-a", 100), orderedNode("fwd-b", 300)},
			},
			want: []string{"fwd-a", "origin", "fwd-b"},
		},
		{
			name: "equal sort orders keep group order",
			groups: [][]*usecases.Node{
				{orderedNode("origin-1", 0), orderedNode("origin-2", 0)},
				{orderedNode("system-fwd", 0)},
				{orderedNode("user-fwd", 0)},
			},
			want: []string{"origin-1", "origin-2", "system-fwd", "user-fwd"},
		},
		{
			name: "single group is returned in sort order",
			groups: [][]*usecases.Node{
				{orderedNode("b", 200), orderedNode("a", 100)},
			},
			want: []string{"a", "b"},
		},
		{
			name: "three groups fully interleave",
			groups: [][]*usecases.Node{
				{orderedNode("origin", 400)},
				{orderedNode("system-fwd", 100)},
				{orderedNode("user-fwd", 250)},
			},
			want: []string{"system-fwd", "user-fwd", "origin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeBySortOrder(tt.groups...)
			assert.Equal(t, tt.want, names(got))
		})
	}
}

// mergeBySortOrder must not write through to the caller's slices, which used to happen
// when the groups were combined with append(origin, forwarded...).
func TestMergeBySortOrderDoesNotAliasInput(t *testing.T) {
	origin := make([]*usecases.Node, 1, 4)
	origin[0] = orderedNode("origin", 100)
	forwarded := []*usecases.Node{orderedNode("fwd", 200)}

	merged := mergeBySortOrder(origin, forwarded)
	merged[0] = orderedNode("replaced", 0)

	assert.Equal(t, []string{"origin"}, names(origin))
	assert.Equal(t, []string{"fwd"}, names(forwarded))
}
