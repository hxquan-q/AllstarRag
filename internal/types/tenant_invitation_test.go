package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestTenantInvitationIsRegistrationFull(t *testing.T) {
	require.False(t, (*TenantInvitation)(nil).IsRegistrationFull(), "nil 邀请不算满")

	unlimited := &TenantInvitation{RegisteredCount: 100}
	require.False(t, unlimited.IsRegistrationFull(), "MaxRegistrations 为 nil 表示不限")

	notFull := &TenantInvitation{MaxRegistrations: intPtr(5), RegisteredCount: 4}
	require.False(t, notFull.IsRegistrationFull())

	exactlyFull := &TenantInvitation{MaxRegistrations: intPtr(5), RegisteredCount: 5}
	require.True(t, exactlyFull.IsRegistrationFull())

	overFull := &TenantInvitation{MaxRegistrations: intPtr(5), RegisteredCount: 6}
	require.True(t, overFull.IsRegistrationFull(), "历史异常数据超过上限也算满")
}
