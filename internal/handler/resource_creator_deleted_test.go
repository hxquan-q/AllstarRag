package handler

// T25 TDD 红阶段：资源列表的创建者「已注销」必须由后端明确标识。
//
// 现状：KnowledgeBase / CustomAgent 只有 creator_name，前端无法区分
// 「创建者已墓碑化」与「老数据本来就没有 creator_name」，于是把两种情况
// 都渲染成「已注销用户」。本票要求列表响应新增 creator_deleted: bool，
// 由后端用一次批量查询判定 creator_id 是否指向 deleted_at 非空的用户。
//
// 契约（T25）：
//   - KnowledgeBase / CustomAgent 新增 CreatorDeleted（json: creator_deleted，
//     不带 omitempty，false 也要出现在响应里）；
//   - UserService 新增窄接口 GetDeletedUserIDs(ctx, ids) (map[string]struct{}, error)，
//     handler 用匿名接口断言可选调用，避免破坏既有 fake；
//   - 批量回填只允许 GetUsersByIDs 一次 + GetDeletedUserIDs 一次（只查
//     active 查询里缺失的 id），不得退化成逐行 GetUserByID。
//
// 本文件不引用 CreatorDeleted 字段的 Go 字面量，因此当前实现下可编译，
// 断言会在响应 JSON 缺字段 / 未发起删除批量查询时失败（预期红）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type creatorDeletedUserSvc struct {
	interfaces.UserService
	users        map[string]*types.User
	deleted      map[string]struct{}
	batchCalls   int
	deletedCalls int
	batchIDs     []string
	deletedIDs   []string
	perUserCalls int
}

func (s *creatorDeletedUserSvc) GetUsersByIDs(
	_ context.Context, ids []string,
) (map[string]*types.User, error) {
	s.batchCalls++
	s.batchIDs = append([]string(nil), ids...)
	out := make(map[string]*types.User, len(ids))
	for _, id := range ids {
		if u, ok := s.users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

// GetDeletedUserIDs 是 T25 要求新增的窄接口。当前实现没有调用它，
// 所以 deletedCalls 恒为 0，本文件的相关断言会红。
func (s *creatorDeletedUserSvc) GetDeletedUserIDs(
	_ context.Context, ids []string,
) (map[string]struct{}, error) {
	s.deletedCalls++
	s.deletedIDs = append([]string(nil), ids...)
	out := make(map[string]struct{})
	for _, id := range ids {
		if _, ok := s.deleted[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

func (s *creatorDeletedUserSvc) GetUserByID(
	_ context.Context, id string,
) (*types.User, error) {
	s.perUserCalls++
	return s.users[id], nil
}

type stubAgentListService struct {
	interfaces.CustomAgentService
	agents []*types.CustomAgent
}

func (s *stubAgentListService) ListAgents(context.Context) ([]*types.CustomAgent, error) {
	return s.agents, nil
}

type stubDisabledOwnAgentRepo struct {
	interfaces.TenantDisabledSharedAgentRepository
}

func (s *stubDisabledOwnAgentRepo) ListDisabledOwnAgentIDs(
	_ context.Context, _ uint64,
) ([]string, error) {
	return nil, nil
}

func creatorListContext(c *gin.Context) {
	c.Set(types.TenantIDContextKey.String(), uint64(7))
	c.Set(types.UserIDContextKey.String(), "caller")
	c.Next()
}

func newCreatorKBListRouter(
	t *testing.T,
	kbs []*types.KnowledgeBase,
	users interfaces.UserService,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler(), creatorListContext)
	h := &KnowledgeBaseHandler{
		service:            &stubListKBService{kbs: kbs},
		vectorStoreService: &stubVectorStoreService{},
		userService:        users,
	}
	r.GET("/knowledge-bases", h.ListKnowledgeBases)
	return r
}

func newCreatorAgentListRouter(
	t *testing.T,
	agents []*types.CustomAgent,
	users interfaces.UserService,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler(), creatorListContext)
	h := &CustomAgentHandler{
		service:      &stubAgentListService{agents: agents},
		disabledRepo: &stubDisabledOwnAgentRepo{},
		userService:  users,
	}
	r.GET("/agents", h.ListAgents)
	return r
}

func decodeRows(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()
	var envelope struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if !envelope.Success {
		t.Fatalf("success=false body=%s", body)
	}
	return envelope.Data
}

func mustCreatorDeleted(t *testing.T, row map[string]interface{}, want bool, who string) {
	t.Helper()
	raw, ok := row["creator_deleted"]
	if !ok {
		t.Fatalf("%s: response row is missing creator_deleted; row=%v", who, row)
	}
	got, ok := raw.(bool)
	if !ok {
		t.Fatalf("%s: creator_deleted is %T, want bool", who, raw)
	}
	if got != want {
		t.Fatalf("%s: creator_deleted=%v, want %v (row=%v)", who, got, want, row)
	}
}

func TestListKnowledgeBasesMarksDeletedCreator(t *testing.T) {
	kbs := []*types.KnowledgeBase{
		{ID: "kb-active", Name: "active", TenantID: 7, CreatorID: "u-active"},
		{ID: "kb-deleted", Name: "deleted", TenantID: 7, CreatorID: "u-deleted"},
		{ID: "kb-absent", Name: "absent", TenantID: 7, CreatorID: "u-never-existed"},
		{ID: "kb-dup", Name: "dup", TenantID: 7, CreatorID: "u-active"},
	}
	users := &creatorDeletedUserSvc{
		users: map[string]*types.User{
			"u-active": {ID: "u-active", Username: "alice"},
		},
		deleted: map[string]struct{}{"u-deleted": {}},
	}

	w := httptest.NewRecorder()
	newCreatorKBListRouter(t, kbs, users).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/knowledge-bases", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 4 {
		t.Fatalf("rows=%d want 4", len(rows))
	}
	byID := map[string]map[string]interface{}{}
	for _, row := range rows {
		byID[row["id"].(string)] = row
	}
	mustCreatorDeleted(t, byID["kb-active"], false, "active creator")
	mustCreatorDeleted(t, byID["kb-deleted"], true, "tombstoned creator")
	mustCreatorDeleted(t, byID["kb-absent"], false, "never-existed creator")
	mustCreatorDeleted(t, byID["kb-dup"], false, "duplicate active creator")

	if byID["kb-active"]["creator_name"] != "alice" {
		t.Fatalf("active creator name=%v want alice", byID["kb-active"]["creator_name"])
	}
	if _, ok := byID["kb-deleted"]["creator_name"]; ok {
		t.Fatalf("tombstoned creator must not expose creator_name: %v", byID["kb-deleted"])
	}
}

func TestListKnowledgeBasesCreatorLookupIsBatched(t *testing.T) {
	kbs := []*types.KnowledgeBase{
		{ID: "kb-1", TenantID: 7, CreatorID: "u-active"},
		{ID: "kb-2", TenantID: 7, CreatorID: "u-active"},
		{ID: "kb-3", TenantID: 7, CreatorID: "u-deleted"},
		{ID: "kb-4", TenantID: 7, CreatorID: "u-missing"},
	}
	users := &creatorDeletedUserSvc{
		users:   map[string]*types.User{"u-active": {ID: "u-active", Username: "alice"}},
		deleted: map[string]struct{}{"u-deleted": {}},
	}

	w := httptest.NewRecorder()
	newCreatorKBListRouter(t, kbs, users).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/knowledge-bases", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	if users.batchCalls != 1 {
		t.Fatalf("GetUsersByIDs calls=%d want 1", users.batchCalls)
	}
	if users.deletedCalls != 1 {
		t.Fatalf("GetDeletedUserIDs calls=%d want 1 (only missing ids batch)", users.deletedCalls)
	}
	if users.perUserCalls != 0 {
		t.Fatalf("GetUserByID calls=%d want 0 (no N+1)", users.perUserCalls)
	}
	if len(users.deletedIDs) != 2 {
		t.Fatalf("GetDeletedUserIDs ids=%v want only the 2 ids missing from active lookup", users.deletedIDs)
	}
}

func TestListAgentsMarksDeletedCreator(t *testing.T) {
	agents := []*types.CustomAgent{
		{ID: "ag-active", Name: "active", CreatedBy: "u-active"},
		{ID: "ag-deleted", Name: "deleted", CreatedBy: "u-deleted"},
		{ID: "ag-builtin", Name: "builtin", IsBuiltin: true},
	}
	users := &creatorDeletedUserSvc{
		users:   map[string]*types.User{"u-active": {ID: "u-active", Username: "alice"}},
		deleted: map[string]struct{}{"u-deleted": {}},
	}

	w := httptest.NewRecorder()
	newCreatorAgentListRouter(t, agents, users).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/agents", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	byID := map[string]map[string]interface{}{}
	for _, row := range rows {
		byID[row["id"].(string)] = row
	}
	mustCreatorDeleted(t, byID["ag-active"], false, "active agent creator")
	mustCreatorDeleted(t, byID["ag-deleted"], true, "tombstoned agent creator")
	// Built-in agent has no creator; it must not be mislabelled as deleted.
	if got, _ := byID["ag-builtin"]["creator_deleted"].(bool); got {
		t.Fatalf("builtin agent must not be creator_deleted: %v", byID["ag-builtin"])
	}
	if users.perUserCalls != 0 {
		t.Fatalf("GetUserByID calls=%d want 0 (no N+1)", users.perUserCalls)
	}
}
