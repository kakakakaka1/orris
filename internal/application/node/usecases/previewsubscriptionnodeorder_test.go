package usecases

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orris-inc/orris/internal/domain/subscription"
	subvo "github.com/orris-inc/orris/internal/domain/subscription/valueobjects"
	"github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// nopLogger is a no-op logger for testing.
type nopLogger struct{}

func newNopLogger() logger.Interface { return &nopLogger{} }

func (l *nopLogger) Debug(msg string, args ...any)                   {}
func (l *nopLogger) Info(msg string, args ...any)                    {}
func (l *nopLogger) Warn(msg string, args ...any)                    {}
func (l *nopLogger) Error(msg string, args ...any)                   {}
func (l *nopLogger) Fatal(msg string, args ...any)                   {}
func (l *nopLogger) With(args ...any) logger.Interface               { return l }
func (l *nopLogger) Named(name string) logger.Interface              { return l }
func (l *nopLogger) Debugw(msg string, keysAndValues ...interface{}) {}
func (l *nopLogger) Infow(msg string, keysAndValues ...interface{})  {}
func (l *nopLogger) Warnw(msg string, keysAndValues ...interface{})  {}
func (l *nopLogger) Errorw(msg string, keysAndValues ...interface{}) {}
func (l *nopLogger) Fatalw(msg string, keysAndValues ...interface{}) {}

type stubSubscriptionLookup struct {
	sub *subscription.Subscription
}

func (s *stubSubscriptionLookup) GetBySID(_ context.Context, _ string) (*subscription.Subscription, error) {
	return s.sub, nil
}

type stubNodeRepository struct {
	nodes []*Node
	// token and mode record what the delivery path was asked for.
	token string
	mode  string
}

func (s *stubNodeRepository) GetBySubscriptionToken(_ context.Context, token string, mode string) ([]*Node, error) {
	s.token = token
	s.mode = mode
	return s.nodes, nil
}

func (s *stubNodeRepository) GetByTokenHash(_ context.Context, _ string) (NodeData, error) {
	return NodeData{}, nil
}

func newTestSubscription(t *testing.T, linkToken string, status subvo.SubscriptionStatus) *subscription.Subscription {
	t.Helper()

	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	sub, err := subscription.ReconstructSubscriptionWithParams(subscription.SubscriptionReconstructParams{
		ID:                 1,
		UserID:             7,
		PlanID:             3,
		SubjectType:        "user",
		SubjectID:          7,
		SID:                "sub_test123",
		UUID:               "0d1f1b6c-2d5f-4a0b-9d0e-5f6a7b8c9d0e",
		LinkToken:          linkToken,
		Status:             status,
		StartDate:          now,
		EndDate:            now.AddDate(0, 1, 0),
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		Version:            1,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	require.NoError(t, err)

	return sub
}

// The preview must hand back the delivery order untouched, including the interleaving of
// direct and forwarded entries that the shared sort_order sequence produces.
func TestPreviewSubscriptionNodeOrderPreservesDeliveryOrder(t *testing.T) {
	nodeRepo := &stubNodeRepository{nodes: []*Node{
		{
			Name: "HK relay", Protocol: "trojan", ServerAddress: "1.1.1.1", SubscriptionPort: 443,
			SortOrder: 100, EntryType: SubscriptionOrderItemForward, EntrySID: "fr_aaa",
			EntryScope: SubscriptionEntryScopeSystem, NodeSID: "node_hk",
		},
		{
			Name: "JP direct", Protocol: "shadowsocks", ServerAddress: "2.2.2.2", SubscriptionPort: 8388,
			SortOrder: 200, EntryType: SubscriptionOrderItemOrigin, EntrySID: "node_jp", NodeSID: "node_jp",
		},
		{
			Name: "My relay", Protocol: "trojan", ServerAddress: "3.3.3.3", SubscriptionPort: 8443,
			SortOrder: 300, EntryType: SubscriptionOrderItemForward, EntrySID: "fr_bbb",
			EntryScope: SubscriptionEntryScopeUser, NodeSID: "node_hk",
		},
	}}
	uc := NewPreviewSubscriptionNodeOrderUseCase(
		&stubSubscriptionLookup{sub: newTestSubscription(t, "link_token_value", subvo.StatusActive)},
		nodeRepo,
		newNopLogger(),
	)

	result, err := uc.Execute(context.Background(), PreviewSubscriptionNodeOrderCommand{
		SubscriptionSID: "sub_test123",
	})

	require.NoError(t, err)
	assert.Equal(t, "sub_test123", result.SubscriptionSID)
	assert.Equal(t, string(subvo.StatusActive), result.Status)
	// An empty mode must reach the delivery path as "all", the subscription default.
	assert.Equal(t, NodeModeAll, result.Mode)
	assert.Equal(t, NodeModeAll, nodeRepo.mode)
	assert.Equal(t, "link_token_value", nodeRepo.token)

	require.Len(t, result.Entries, 3)
	assert.Equal(t, []int{1, 2, 3}, []int{result.Entries[0].Position, result.Entries[1].Position, result.Entries[2].Position})
	assert.Equal(t, []string{"fr_aaa", "node_jp", "fr_bbb"},
		[]string{result.Entries[0].SID, result.Entries[1].SID, result.Entries[2].SID})
	// A direct node sits between two forwarded entries, which is the whole point of the
	// shared sort_order sequence.
	assert.Equal(t, SubscriptionOrderItemOrigin, result.Entries[1].Type)
	assert.Equal(t, SubscriptionEntryScopeUser, result.Entries[2].Scope)
	assert.Empty(t, result.Entries[1].Scope)
	// Forwarded entries keep the underlying node so both HK entries are traceable.
	assert.Equal(t, "node_hk", result.Entries[0].NodeSID)
	assert.Equal(t, "node_hk", result.Entries[2].NodeSID)
	assert.Equal(t, uint16(8388), result.Entries[1].Port)
}

// A non-active subscription delivers nothing. The preview must still succeed and report
// the status and mode, otherwise the admin UI cannot explain why the list is empty.
func TestPreviewSubscriptionNodeOrderReportsStatusForEmptyDelivery(t *testing.T) {
	nodeRepo := &stubNodeRepository{nodes: nil}
	uc := NewPreviewSubscriptionNodeOrderUseCase(
		&stubSubscriptionLookup{sub: newTestSubscription(t, "link_token_value", subvo.StatusSuspended)},
		nodeRepo,
		newNopLogger(),
	)

	result, err := uc.Execute(context.Background(), PreviewSubscriptionNodeOrderCommand{
		SubscriptionSID: "sub_test123",
		Mode:            NodeModeForward,
	})

	require.NoError(t, err)
	assert.NotNil(t, result.Entries, "an empty delivery must serialize as [] rather than null")
	assert.Empty(t, result.Entries)
	assert.Equal(t, string(subvo.StatusSuspended), result.Status)
	assert.Equal(t, NodeModeForward, result.Mode)
	assert.Equal(t, NodeModeForward, nodeRepo.mode)
}

func TestPreviewSubscriptionNodeOrderRejectsInvalidInput(t *testing.T) {
	uc := NewPreviewSubscriptionNodeOrderUseCase(
		&stubSubscriptionLookup{sub: newTestSubscription(t, "link_token_value", subvo.StatusActive)},
		&stubNodeRepository{},
		newNopLogger(),
	)

	tests := []struct {
		name string
		cmd  PreviewSubscriptionNodeOrderCommand
	}{
		{name: "missing subscription id", cmd: PreviewSubscriptionNodeOrderCommand{Mode: NodeModeAll}},
		{name: "unknown mode", cmd: PreviewSubscriptionNodeOrderCommand{SubscriptionSID: "sub_test123", Mode: "direct"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.Execute(context.Background(), tt.cmd)

			require.Error(t, err)
			assert.True(t, errors.IsValidationError(err), "expected a validation error, got %v", err)
		})
	}
}

func TestPreviewSubscriptionNodeOrderMissingSubscription(t *testing.T) {
	uc := NewPreviewSubscriptionNodeOrderUseCase(
		&stubSubscriptionLookup{sub: nil},
		&stubNodeRepository{},
		newNopLogger(),
	)

	_, err := uc.Execute(context.Background(), PreviewSubscriptionNodeOrderCommand{SubscriptionSID: "sub_missing"})

	require.Error(t, err)
	assert.True(t, errors.IsNotFoundError(err), "expected a not found error, got %v", err)
}
