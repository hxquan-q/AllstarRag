package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
)

// CP3-FIX2 L1：LeaveTenant 收到服务层 ErrMembershipChanged（自助离开在
// 重试上限后仍持续竞争）时必须映射 409，不能落成 500。
func TestCP3Fix2LeaveTenantMembershipChangedMaps409(t *testing.T) {
	ms := &stubMemberService{
		remove: func(_ context.Context, userID string, tenantID uint64) error {
			if userID != "u-self" || tenantID != 1 {
				t.Fatalf("LeaveTenant must use caller id; got user=%s tenant=%d", userID, tenantID)
			}
			return service.ErrMembershipChanged
		},
	}
	h := newTestMemberHandler(ms, &stubMemberUserService{})

	w := doJSON(t, memberTestRouter(h), http.MethodPost, "/tenants/1/leave", nil, "u-self")
	if w.Code != http.StatusConflict {
		t.Fatalf("leave with ErrMembershipChanged must 409, got %d body=%s", w.Code, w.Body.String())
	}
}
