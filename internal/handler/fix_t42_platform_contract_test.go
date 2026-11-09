package handler

// T42 TDD 红阶段：平台接口请求体契约（GPT 复核 #1/#4）。
//
// 票面验收 1（03-tickets/T42.md）：
//   - POST /system/admin/users/:user_id/ban 与 /unban 允许完全不带请求体，
//     等价于 {"reason":""}，handler 继续调用 service（200）；
//   - 非法 JSON（如 `{bad json`）仍 400，且不得触达 service；
//   - purge 的请求体仍必填（confirm_email 必填），空 body 仍 400——
//     防实现把“空 body 容忍”扩大成所有平台接口。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// t42PostNoBody 发送完全没有请求体的 POST：不传 body，也不设置
// Content-Type，复刻“客户端按 Swagger 可选 body 不传”的真实请求。
func t42PostNoBody(t *testing.T, r http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// H1：无 body 的平台 ban 必须成功，reason 为空串。
func TestT42PlatformBanAcceptsEmptyBody(t *testing.T) {
	fake := &t35FakePlatformService{
		banFn: func(_ context.Context, targetUserID, reason string) (*types.User, error) {
			require.Equal(t, "u-target", targetUserID)
			require.Equal(t, "", reason, "absent body must behave like an empty reason")
			return t35TargetUser(false), nil
		},
	}
	h := NewUserManagementHandler(fake, nil)
	w := t42PostNoBody(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/ban")

	require.Equal(t, http.StatusOK, w.Code,
		"an absent request body must not be treated as malformed JSON")
	require.Equal(t, 1, fake.banCalls,
		"platform ban without a body must still reach the service")
	require.Equal(t, "", fake.lastReason)
}

// H2：无 body 的平台 unban 必须成功，reason 为空串。
func TestT42PlatformUnbanAcceptsEmptyBody(t *testing.T) {
	fake := &t35FakePlatformService{
		unbanFn: func(_ context.Context, targetUserID, reason string) (*types.User, error) {
			require.Equal(t, "u-target", targetUserID)
			require.Equal(t, "", reason, "absent body must behave like an empty reason")
			return t35TargetUser(true), nil
		},
	}
	h := NewUserManagementHandler(fake, nil)
	w := t42PostNoBody(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/unban")

	require.Equal(t, http.StatusOK, w.Code,
		"an absent request body must not be treated as malformed JSON")
	require.Equal(t, 1, fake.unbanCalls,
		"platform unban without a body must still reach the service")
	require.Equal(t, "", fake.lastReason)
}

// H3：提供但非法的 JSON 仍必须 400，且不得触达 service。
func TestT42PlatformBanUnbanKeepRejectingMalformedJSON(t *testing.T) {
	fake := &t35FakePlatformService{
		banFn: func(context.Context, string, string) (*types.User, error) {
			return t35TargetUser(false), nil
		},
		unbanFn: func(context.Context, string, string) (*types.User, error) {
			return t35TargetUser(true), nil
		},
	}
	h := NewUserManagementHandler(fake, nil)
	engine := t35HandlerEngine(h)

	for _, action := range []string{"ban", "unban"} {
		w := t35PostHandler(t, engine,
			"/api/v1/system/admin/users/u-target/"+action, `{bad json`)
		require.Equal(t, http.StatusBadRequest, w.Code,
			"malformed JSON must stay 400 on platform %s", action)
	}
	require.Zero(t, fake.banCalls+fake.unbanCalls,
		"malformed JSON must not reach the service")
}

// H4：purge 的 request body 仍是必填（confirm_email），空 body 必须 400。
func TestT42PlatformPurgeStillRequiresBody(t *testing.T) {
	fake := &t35FakePlatformService{
		purgeFn: func(context.Context, string, string, string) error { return nil },
	}
	h := NewUserManagementHandler(fake, nil)
	w := t42PostNoBody(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/purge")

	require.Equal(t, http.StatusBadRequest, w.Code,
		"purge carries confirm_email and its body must stay required")
	require.Zero(t, fake.purgeCalls,
		"body-less purge must not reach the service")
}
