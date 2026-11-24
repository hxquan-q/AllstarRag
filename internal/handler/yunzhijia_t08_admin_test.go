package handler

// T08 测试：管理端 handler 的第二层权限校验、解除 blocked 的状态映射
// 与单运行冲突的 409 映射（AC-14 / AC-16 / C4）。
//
// 路由守卫之外的独立校验在此验证：即使中间件被绕过，handler 仍拒绝
// 非系统管理员（对应 C4「前端/单层守卫不等于权限隔离」）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type yzjT08HandlerRunRepo struct {
	interfaces.YunzhijiaSyncRunRepository
}

func (r *yzjT08HandlerRunRepo) Latest(context.Context) (*types.YunzhijiaSyncRun, error) {
	return nil, nil
}

type yzjT08HandlerEmployeeRepo struct {
	interfaces.YunzhijiaEmployeeRepository
	unblockErr error
	unblocked  []string
}

func (r *yzjT08HandlerEmployeeRepo) Unblock(_ context.Context, openID string) error {
	if r.unblockErr != nil {
		return r.unblockErr
	}
	r.unblocked = append(r.unblocked, openID)
	return nil
}

func yzjT08HandlerEngine(
	t *testing.T, h *YunzhijiaAdminHandler, systemAdmin bool,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "actor-t08")
		if systemAdmin {
			ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.GET("/sync", h.GetSyncStatus)
	engine.POST("/sync/preview", h.PreviewSync)
	engine.POST("/sync", h.TriggerSync)
	engine.POST("/unblock", func(c *gin.Context) {
		c.Params = gin.Params{{Key: "open_id", Value: "open-placeholder"}}
		h.UnblockEmployee(c)
	})
	return engine
}

func yzjT08HandlerDo(
	t *testing.T, engine *gin.Engine, method, path string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func TestYunzhijiaT08HandlerRejectsNonSystemAdmin(t *testing.T) {
	// svc 故意为 nil：守卫必须在任何服务调用之前拒绝，否则会 panic。
	h := &YunzhijiaAdminHandler{}
	engine := yzjT08HandlerEngine(t, h, false)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/sync"},
		{http.MethodPost, "/sync/preview"},
		{http.MethodPost, "/sync"},
		{http.MethodPost, "/unblock"},
	} {
		w := yzjT08HandlerDo(t, engine, route.method, route.path)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s 非系统管理员应 403，实际 %d", route.method, route.path, w.Code)
		}
	}
}

func yzjT08UnblockHandler(unblockErr error) (*YunzhijiaAdminHandler, *yzjT08HandlerEmployeeRepo) {
	repo := &yzjT08HandlerEmployeeRepo{unblockErr: unblockErr}
	cfg := &config.YunzhijiaSSOConfig{Enabled: true}
	svc := service.NewYunzhijiaAdminService(
		cfg, nil, &yzjT08HandlerRunRepo{}, repo, nil,
	)
	return NewYunzhijiaAdminHandler(svc), repo
}

func TestYunzhijiaT08UnblockStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		unblockErr error
		wantCode   int
	}{
		{"success", nil, http.StatusOK},
		{"not-found-or-already-unblocked", repository.ErrYunzhijiaEmployeeNotFound, http.StatusNotFound},
		{"internal", errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := yzjT08UnblockHandler(tc.unblockErr)
			engine := yzjT08HandlerEngine(t, h, true)
			w := yzjT08HandlerDo(t, engine, http.MethodPost, "/unblock")
			if w.Code != tc.wantCode {
				t.Fatalf("状态码=%d，期望 %d，body=%s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestYunzhijiaT08UnblockRejectsInvalidOpenID(t *testing.T) {
	h, _ := yzjT08UnblockHandler(nil)
	engine := yzjT08HandlerEngine(t, h, true)
	engine.POST("/blank", func(c *gin.Context) {
		c.Params = gin.Params{{Key: "open_id", Value: "  "}}
		h.UnblockEmployee(c)
	})
	w := yzjT08HandlerDo(t, engine, http.MethodPost, "/blank")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空白 open_id 应 400，实际 %d", w.Code)
	}
}

// TestYunzhijiaT08SyncErrorMapping 验证单运行互斥映射为 409，
// 其他错误映射为 500。
func TestYunzhijiaT08SyncErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &YunzhijiaAdminHandler{}
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"running", service.ErrSyncRunning, http.StatusConflict},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/sync", nil)
			h.respondSyncError(c, "同步失败", tc.err)
			if w.Code != tc.wantCode {
				t.Fatalf("状态码=%d，期望 %d", w.Code, tc.wantCode)
			}
		})
	}
}
