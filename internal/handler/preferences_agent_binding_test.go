package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 本文件是整体回归（testing 相位）补的绑定偏好测试：T01 把该测试留给测试者，
// T06 只收口前端。覆盖 AC-1（按工作区分键与 PATCH 语义）、AC-2（整批校验）、
// AC-2.1（内建 agent 可绑定）。

type bindingAgentService struct {
	interfaces.CustomAgentService
	agents map[uint64]map[string]*types.CustomAgent
	calls  []string
}

func (s *bindingAgentService) GetAgentByID(ctx context.Context, id string) (*types.CustomAgent, error) {
	tenantID, _ := types.TenantIDFromContext(ctx)
	s.calls = append(s.calls, fmt.Sprintf("%d/%s", tenantID, id))
	if agent, ok := s.agents[tenantID][id]; ok {
		return agent, nil
	}
	return nil, fmt.Errorf("agent %q not found", id)
}

type bindingDisabledRepo struct {
	interfaces.TenantDisabledSharedAgentRepository
	byTenant map[uint64][]string
}

func (r *bindingDisabledRepo) ListDisabledOwnAgentIDs(_ context.Context, tenantID uint64) ([]string, error) {
	return r.byTenant[tenantID], nil
}

type bindingUserService struct {
	interfaces.UserService
	user      *types.User
	calls     int
	updatedID string
	patch     types.UserPreferences
}

func (s *bindingUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return s.user, nil
}

func (s *bindingUserService) UpdateUserPreferences(
	_ context.Context, id string, patch types.UserPreferences,
) (types.UserPreferences, error) {
	s.calls++
	s.updatedID = id
	s.patch = patch
	return patch, nil
}

func newBindingHandler(
	tenantAgents map[uint64][]string,
	disabled map[uint64][]string,
	prefs types.UserPreferences,
) (*AuthHandler, *bindingUserService, *bindingAgentService) {
	agentSvc := &bindingAgentService{agents: map[uint64]map[string]*types.CustomAgent{}}
	for tenant, ids := range tenantAgents {
		agentSvc.agents[tenant] = map[string]*types.CustomAgent{}
		for _, id := range ids {
			agentSvc.agents[tenant][id] = &types.CustomAgent{ID: id, TenantID: tenant}
		}
	}
	users := &bindingUserService{user: &types.User{ID: "u-1", Preferences: prefs}}
	h := &AuthHandler{
		userService:        users,
		customAgentService: agentSvc,
		disabledAgentRepo:  &bindingDisabledRepo{byTenant: disabled},
	}
	return h, users, agentSvc
}

func tenantContext(tenantID uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
}

func putPreferences(
	t *testing.T, h *AuthHandler, tenantID *uint64, body string,
) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("PUT", "/auth/me/preferences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tenantID != nil {
		req = req.WithContext(tenantContext(*tenantID))
	}
	c.Request = req
	h.UpdateMyPreferences(c)
	return c, w
}

func TestValidateBoundAgentIDsAcceptsBuiltinAndCustomAgents(t *testing.T) {
	h, _, agents := newBindingHandler(
		map[uint64][]string{7: {"builtin-quick-answer", "custom-a"}}, nil, types.UserPreferences{})

	got, err := h.validateBoundAgentIDs(
		tenantContext(7), 7, []string{"builtin-quick-answer", "custom-a"})

	require.NoError(t, err, "AC-2.1：内建 agent 必须允许绑定")
	require.Equal(t, []string{"builtin-quick-answer", "custom-a"}, got)
	require.Equal(t, []string{"7/builtin-quick-answer", "7/custom-a"}, agents.calls,
		"校验必须查当前活动工作区的 agent")
}

func TestValidateBoundAgentIDsDedupesKeepingFirstOrder(t *testing.T) {
	h, _, _ := newBindingHandler(
		map[uint64][]string{7: {"a", "b"}}, nil, types.UserPreferences{})

	got, err := h.validateBoundAgentIDs(tenantContext(7), 7, []string{"b", "a", "b", "a"})

	require.NoError(t, err)
	require.Equal(t, []string{"b", "a"}, got, "重复 id 去重且保留首次出现顺序")
}

func TestValidateBoundAgentIDsRejectsEmptyString(t *testing.T) {
	h, _, _ := newBindingHandler(
		map[uint64][]string{7: {"a"}}, nil, types.UserPreferences{})

	_, err := h.validateBoundAgentIDs(tenantContext(7), 7, []string{"a", ""})

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestValidateBoundAgentIDsRejectsMoreThanTwenty(t *testing.T) {
	ids := make([]string, 21)
	allowed := make([]string, 21)
	for i := range ids {
		ids[i] = fmt.Sprintf("agent-%02d", i)
		allowed[i] = ids[i]
	}
	h, _, _ := newBindingHandler(map[uint64][]string{7: allowed}, nil, types.UserPreferences{})

	_, err := h.validateBoundAgentIDs(tenantContext(7), 7, ids)

	require.Error(t, err)
	require.Contains(t, err.Error(), "more than 20")
}

func TestValidateBoundAgentIDsRejectsInternalAgents(t *testing.T) {
	internal := []string{
		types.BuiltinWikiFixerID,
		types.BuiltinSkillInstallerID,
		types.BuiltinWeKnoraAssistantID,
	}
	for _, id := range internal {
		h, _, _ := newBindingHandler(map[uint64][]string{7: {id}}, nil, types.UserPreferences{})
		_, err := h.validateBoundAgentIDs(tenantContext(7), 7, []string{id})
		require.Error(t, err, "内部 agent %s 必须拒绝", id)
		require.Contains(t, err.Error(), "is internal")
	}
}

func TestValidateBoundAgentIDsRejectsCrossTenantAndUnknown(t *testing.T) {
	// agent 只存在于工作区 8；当前活动工作区是 7，必须拒绝，且错误信息不泄露来源工作区。
	h, _, _ := newBindingHandler(
		map[uint64][]string{8: {"other-tenant-agent"}}, nil, types.UserPreferences{})

	_, err := h.validateBoundAgentIDs(tenantContext(7), 7, []string{"other-tenant-agent"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "other-tenant-agent")
	require.NotContains(t, err.Error(), "workspace 8")
	require.NotContains(t, err.Error(), "tenant 8")
}

func TestValidateBoundAgentIDsRejectsDisabledAgent(t *testing.T) {
	h, _, _ := newBindingHandler(
		map[uint64][]string{7: {"custom-a"}},
		map[uint64][]string{7: {"custom-a"}},
		types.UserPreferences{})

	_, err := h.validateBoundAgentIDs(tenantContext(7), 7, []string{"custom-a"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "is disabled")
}

func TestValidateBoundAgentIDsUsesActiveWorkspaceNotContextHome(t *testing.T) {
	// ctx 里的租户（用户 home）与调用方给的活动工作区不一致时，
	// 必须按活动工作区查询；否则会校验到错误的工作区。
	h, _, agents := newBindingHandler(
		map[uint64][]string{8: {"workspace-8-agent"}}, nil, types.UserPreferences{})

	got, err := h.validateBoundAgentIDs(tenantContext(7), 8, []string{"workspace-8-agent"})

	require.NoError(t, err)
	require.Equal(t, []string{"workspace-8-agent"}, got)
	require.Equal(t, []string{"8/workspace-8-agent"}, agents.calls)
}

func TestUpdateMyPreferencesWritesOnlyActiveTenantBinding(t *testing.T) {
	prefs := types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {"a"}, "8": {"b"}},
	}
	h, users, _ := newBindingHandler(
		map[uint64][]string{7: {"c"}}, nil, prefs)

	c, w := putPreferences(t, h, uint64Ptr(7),
		`{"bound_agent_ids":["c"],"last_active_tenant_id":7}`)

	require.Equal(t, 200, w.Code)
	require.Empty(t, c.Errors)
	require.Equal(t, 1, users.calls)
	require.Equal(t, "u-1", users.updatedID)
	// 活动工作区 7 被整批覆盖；工作区 8 原样保留（AC-1 跨工作区不互相覆盖）。
	require.Equal(t, []string{"c"}, users.patch.BoundAgentIDsByTenant["7"])
	require.Equal(t, []string{"b"}, users.patch.BoundAgentIDsByTenant["8"])
	require.NotNil(t, users.patch.LastActiveTenantID)
	require.Equal(t, uint64(7), *users.patch.LastActiveTenantID)
	// 实现不得原地改写用户对象里的旧 map。
	require.Equal(t, []string{"a"}, prefs.BoundAgentIDsByTenant["7"])
}

func TestUpdateMyPreferencesEmptyArrayClearsOnlyActiveTenant(t *testing.T) {
	prefs := types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {"a"}, "8": {"b"}},
	}
	h, users, _ := newBindingHandler(nil, nil, prefs)

	c, w := putPreferences(t, h, uint64Ptr(7), `{"bound_agent_ids":[]}`)

	require.Equal(t, 200, w.Code)
	require.Empty(t, c.Errors)
	require.Equal(t, 1, users.calls)
	_, kept := users.patch.BoundAgentIDsByTenant["7"]
	require.False(t, kept, "空数组必须删除该工作区的键，不留空数组")
	require.Equal(t, []string{"b"}, users.patch.BoundAgentIDsByTenant["8"])
}

func TestUpdateMyPreferencesMissingKeyLeavesAllTenantsUntouched(t *testing.T) {
	h, users, _ := newBindingHandler(nil, nil, types.UserPreferences{})

	c, w := putPreferences(t, h, uint64Ptr(7), `{"browser_search_instructions":"Use my engine"}`)

	require.Equal(t, 200, w.Code)
	require.Empty(t, c.Errors)
	require.Equal(t, 1, users.calls)
	require.Nil(t, users.patch.BoundAgentIDsByTenant,
		"请求不带 bound_agent_ids 时不得写入任何工作区")
	require.NotNil(t, users.patch.BrowserSearchInstructions)
	require.Equal(t, "Use my engine", *users.patch.BrowserSearchInstructions)
}

func TestUpdateMyPreferencesInvalidBatchWritesNothing(t *testing.T) {
	prefs := types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {"a"}},
	}
	h, users, _ := newBindingHandler(
		map[uint64][]string{7: {"a", "b"}}, nil, prefs)

	// 一批里混入内部 agent：整批拒绝，原绑定不得被清空（全有或全无）。
	c, _ := putPreferences(t, h, uint64Ptr(7),
		`{"bound_agent_ids":["b","`+types.BuiltinWikiFixerID+`"]}`)

	require.NotEmpty(t, c.Errors)
	require.Contains(t, c.Errors.Last().Err.Error(), "is internal")
	require.Zero(t, users.calls, "校验失败不得调用 UpdateUserPreferences")
	require.Equal(t, []string{"a"}, prefs.BoundAgentIDsByTenant["7"])
}

func TestUpdateMyPreferencesRequiresActiveWorkspace(t *testing.T) {
	h, users, _ := newBindingHandler(map[uint64][]string{7: {"a"}}, nil, types.UserPreferences{})

	c, _ := putPreferences(t, h, nil, `{"bound_agent_ids":["a"]}`)

	require.NotEmpty(t, c.Errors)
	require.Contains(t, c.Errors.Last().Err.Error(), "active workspace")
	require.Zero(t, users.calls)
}

func TestUserInfoExposesBoundAgentIDsByTenant(t *testing.T) {
	user := &types.User{
		ID: "u-1",
		Preferences: types.UserPreferences{
			BoundAgentIDsByTenant: map[string][]string{"7": {"agent-a"}},
		},
	}

	raw, err := json.Marshal(user.ToUserInfo())

	require.NoError(t, err)
	require.Contains(t, string(raw), `"bound_agent_ids_by_tenant"`)
	require.Contains(t, string(raw), `"7":["agent-a"]`)
}

func uint64Ptr(v uint64) *uint64 { return &v }
