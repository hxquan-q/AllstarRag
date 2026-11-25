package handler

// T03 TDD 红阶段：云之家登录 HTTP 层（AC-16）。
//
// 红阶段契约（实现方对齐）：
//   NewYunzhijiaAuthHandler(*config.YunzhijiaSSOConfig, interfaces.YunzhijiaLoginService) *YunzhijiaAuthHandler
//   (*YunzhijiaAuthHandler).Login(*gin.Context)
//   路由由 router.RegisterYunzhijiaAuthRoutes 注册（见 router 包用例）。
//
// HTTP 状态约定：配置缺失或 enabled=false → 503（不调服务、不签发 token）；
// 空/空白/异常超长 ticket → 400；服务返回 Success=false → 401；
// 服务返回 error → 500（统一错误 envelope）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type yzjT03LoginStub struct {
	resp    *types.LoginResponse
	err     error
	calls   int
	tickets []string
}

func (s *yzjT03LoginStub) Login(_ context.Context, ticket string) (*types.LoginResponse, error) {
	s.calls++
	s.tickets = append(s.tickets, ticket)
	return s.resp, s.err
}

var _ interfaces.YunzhijiaLoginService = (*yzjT03LoginStub)(nil)

func yzjT03HandlerSuccessResponse() *types.LoginResponse {
	return &types.LoginResponse{
		Success: true,
		User: &types.User{
			ID:       "yzj-user-placeholder",
			Username: "测试员工甲",
			Email:    "yzj_openid-placeholder@yunzhijia.local",
		},
		ActiveTenant: &types.Tenant{ID: 10024, Name: "测试空间"},
		Memberships: []types.Membership{{
			TenantID: 10024,
			Role:     types.TenantRoleViewer,
		}},
		Token:        "access-token-placeholder",
		RefreshToken: "refresh-token-placeholder",
		Capabilities: &types.LoginCapabilities{
			CanCreateTenant: false,
			AccessRevoked:   false,
		},
	}
}

// yzjT03EnabledConfig 返回一份能通过 Validate 的完整配置。
// handler 在入口对配置做全量校验（配置非法即 503），夹具必须给齐必填项；
// R2 起 Validate 也校验 base_url（scheme/host/无 query/fragment），
// 夹具必须提供合法地址，否则所有用例只会拿到 503。
func yzjT03EnabledConfig() *config.YunzhijiaSSOConfig {
	return &config.YunzhijiaSSOConfig{
		Enabled:             true,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/@asd_rd/api/public/employees",
		TicketUserPath:      "/@asd_rd/api/public/ticket/user",
		AppID:               "placeholder-app-id",
		TenantDefault:       10024,
		TenantHR:            10022,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            100,
		HTTPTimeoutSeconds:  10,
	}
}

func newYZJT03HandlerEngine(
	t *testing.T, cfg *config.YunzhijiaSSOConfig, svc interfaces.YunzhijiaLoginService,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	h := NewYunzhijiaAuthHandler(cfg, svc)
	r.POST("/api/v1/auth/yunzhijia/login", h.Login)
	return r
}

func yzjT03PostLogin(t *testing.T, r *gin.Engine, remoteAddr, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/yunzhijia/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestYunzhijiaT03HandlerLoginSuccessShape(t *testing.T) {
	stub := &yzjT03LoginStub{resp: yzjT03HandlerSuccessResponse()}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)
	const ticket = "ticket-placeholder-handler-001"

	w := yzjT03PostLogin(t, r, "198.51.100.10:41000", `{"ticket":"  `+ticket+`  "}`)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	require.Equal(t, 1, stub.calls)
	require.Len(t, stub.tickets, 1)
	assert.Equal(t, ticket, stub.tickets[0], "handler 应先 trim 空白再交给服务")

	var got dto.AuthLoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Success)
	assert.NotEmpty(t, got.Token)
	assert.NotEmpty(t, got.RefreshToken)
	require.NotNil(t, got.User)
	assert.Equal(t, "yzj-user-placeholder", got.User.ID)
	require.NotNil(t, got.ActiveTenant)
	assert.Equal(t, uint64(10024), got.ActiveTenant.ID)
	require.Len(t, got.Memberships, 1)
	assert.Equal(t, types.TenantRoleViewer, got.Memberships[0].Role)
	require.NotNil(t, got.Capabilities)
	assert.False(t, got.Capabilities.CanCreateTenant)
	assert.NotContains(t, w.Body.String(), ticket, "响应不得回显 ticket")
}

func TestYunzhijiaT03HandlerRejectsWhenDisabled(t *testing.T) {
	stub := &yzjT03LoginStub{resp: yzjT03HandlerSuccessResponse()}
	cases := map[string]*config.YunzhijiaSSOConfig{
		"disabled": {Enabled: false},
		"missing":  nil,
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			r := newYZJT03HandlerEngine(t, cfg, stub)
			w := yzjT03PostLogin(t, r, "198.51.100.16:41000", `{"ticket":"ticket-placeholder-disabled"}`)
			require.Equal(t, http.StatusServiceUnavailable, w.Code, "resp=%s", w.Body.String())

			var got dto.AuthLoginResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.False(t, got.Success)
			assert.Contains(t, got.Message, "系统暂不可用")
			assert.Empty(t, got.Token, "enabled=false 时不得签发 token")
			assert.Empty(t, got.RefreshToken)
			assert.Equal(t, 0, stub.calls, "enabled=false 时不应调用登录服务")
		})
	}
}

func TestYunzhijiaT03HandlerRejectsBlankTicket(t *testing.T) {
	stub := &yzjT03LoginStub{resp: yzjT03HandlerSuccessResponse()}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)

	for _, body := range []string{
		`{"ticket":""}`,
		`{"ticket":"   "}`,
		`{}`,
	} {
		w := yzjT03PostLogin(t, r, "198.51.100.11:41000", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s resp=%s", body, w.Body.String())
	}
	assert.Equal(t, 0, stub.calls, "空 ticket 不应进入登录服务")
}

func TestYunzhijiaT03HandlerRejectsOverlongTicket(t *testing.T) {
	stub := &yzjT03LoginStub{resp: yzjT03HandlerSuccessResponse()}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)

	overlong := strings.Repeat("a", 8192)
	body, err := json.Marshal(map[string]string{"ticket": overlong})
	require.NoError(t, err)
	w := yzjT03PostLogin(t, r, "198.51.100.12:41000", string(body))
	assert.Equal(t, http.StatusBadRequest, w.Code, "resp=%s", w.Body.String())
	assert.Equal(t, 0, stub.calls)
}

func TestYunzhijiaT03HandlerMalformedJSON(t *testing.T) {
	stub := &yzjT03LoginStub{resp: yzjT03HandlerSuccessResponse()}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)
	w := yzjT03PostLogin(t, r, "198.51.100.13:41000", `{"ticket":`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, 0, stub.calls)
}

func TestYunzhijiaT03HandlerBusinessRejectionIs401(t *testing.T) {
	stub := &yzjT03LoginStub{resp: &types.LoginResponse{
		Success: false,
		Message: "账号不可用，请联系管理员",
	}}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)

	w := yzjT03PostLogin(t, r, "198.51.100.14:41000", `{"ticket":"ticket-placeholder-rejected"}`)
	require.Equal(t, http.StatusUnauthorized, w.Code, "resp=%s", w.Body.String())

	var got dto.AuthLoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.False(t, got.Success)
	assert.Contains(t, got.Message, "账号不可用")
	assert.Empty(t, got.Token)
}

func TestYunzhijiaT03HandlerUnexpectedErrorIs500(t *testing.T) {
	stub := &yzjT03LoginStub{err: errors.New("simulated backend failure")}
	r := newYZJT03HandlerEngine(t, yzjT03EnabledConfig(), stub)

	w := yzjT03PostLogin(t, r, "198.51.100.15:41000", `{"ticket":"ticket-placeholder-error"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code, "resp=%s", w.Body.String())
}

func TestYunzhijiaT03SwaggerDeclaresLoginPath(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "swagger.json"))
	require.NoError(t, err)

	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	path, ok := doc.Paths["/auth/yunzhijia/login"]
	require.True(t, ok, "Swagger 必须声明 /auth/yunzhijia/login")
	post, ok := path["post"]
	require.True(t, ok, "Swagger 必须声明 POST /auth/yunzhijia/login")
	assert.Contains(t, post.Responses, "200")
	assert.Contains(t, post.Responses, "429", "登录入口必须声明限速响应")
}
