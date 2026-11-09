package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

// T41 TDD 红阶段（service 层）：注销与消息写入并发围栏（GPT 复核 #10）。
//
// 假定契约（03-tickets/T41.md + 队长派单）：
//   - 消息创建必须与 session 存在性检查处于同一事务，并在事务内锁定/复核
//     目标 session；session 所属用户已墓碑化/封禁时拒绝写入；
//   - 消息先提交时，purge 的删除覆盖它；purge 先完成时，旧请求恢复后
//     写入必须失败，不能留下孤儿消息；
//   - purge 删除 sessions/messages 使用同一锁序：先锁 users，再锁 sessions，
//     最后删 messages/sessions（SQLite 用空更新取得写锁，与 lockPurgeTarget
//     的既有做法一致）；
//   - IndexMessageToKB 在写 knowledge 前复核消息仍存活；
//   - 正常聊天路径不回归。
//
// 本文件只驱动服务层公开行为；仓储新契约
// MessageRepository.CreateMessageInSessionTx(
//     ctx, message, tenantID, ownerID) (*types.Message, error)
// 由 fix_t41 仓储测试直接覆盖。服务层用“可控暂停点”的仓储替身制造确定性
// 交错：旧实现会在 session 检查之后、插入之前被暂停，purge 完成后恢复仍
// 插入孤儿消息（预期红）。

// t41CreateMessageInSessionTx 是本轮给②钉死的仓储契约。服务层替身与仓储
// 测试共用该形状；实现缺失时服务替身会在恢复后返回明确的“缺契约”错误。
type t41CreateMessageInSessionTx interface {
	CreateMessageInSessionTx(
		ctx context.Context, message *types.Message, tenantID uint64, ownerID string,
	) (*types.Message, error)
}

// t41NewMessageService 只注入消息/会话仓储，其余依赖留空；CreateMessage
// 与 IndexMessageToKB 都不需要它们（后者用注入的 knowService 记录调用）。
func t41NewMessageService(
	env *umTestEnv, repo interfaces.MessageRepository, know interfaces.KnowledgeService,
) interfaces.MessageService {
	if repo == nil {
		repo = apprepo.NewMessageRepository(env.db)
	}
	return NewMessageService(repo, apprepo.NewSessionRepository(env.db), nil, nil, know, nil, nil)
}

// t41PausingMessageRepo 在消息写入入口暂停，制造“请求已通过 session
// 检查 → purge 完成 → 请求恢复写入”的确定性交错。
type t41PausingMessageRepo struct {
	interfaces.MessageRepository

	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newT41PausingMessageRepo(inner interfaces.MessageRepository) *t41PausingMessageRepo {
	return &t41PausingMessageRepo{
		MessageRepository: inner,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (r *t41PausingMessageRepo) pause() {
	r.enteredOnce.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-time.After(10 * time.Second):
		// 兜底：用例失败时也不要让替身永久挂住。
	}
}

func (r *t41PausingMessageRepo) releaseNow() {
	r.releaseOnce.Do(func() { close(r.release) })
}

// CreateMessage 覆盖旧的两步路径：服务层先 Get 再插入，插入前暂停。
func (r *t41PausingMessageRepo) CreateMessage(
	ctx context.Context, message *types.Message,
) (*types.Message, error) {
	r.pause()
	return r.MessageRepository.CreateMessage(ctx, message)
}

// CreateMessageInSessionTx 覆盖新契约路径：线程安全的暂停后委托真实仓储；
// 真实仓储在事务内复核 session，purge 已删会话时必须失败。
func (r *t41PausingMessageRepo) CreateMessageInSessionTx(
	ctx context.Context, message *types.Message, tenantID uint64, ownerID string,
) (*types.Message, error) {
	r.pause()
	txRepo, ok := r.MessageRepository.(t41CreateMessageInSessionTx)
	if !ok {
		return nil, errors.New("MessageRepository 未实现 CreateMessageInSessionTx")
	}
	return txRepo.CreateMessageInSessionTx(ctx, message, tenantID, ownerID)
}

func t41CountMessages(t *testing.T, env *umTestEnv, sessionID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Unscoped().Model(&types.Message{}).
		Where("session_id = ?", sessionID).Count(&n).Error)
	return n
}

// TestT41MessageCreatePausedBeforeInsertFailsAfterPurge 覆盖验收 1：
// 请求通过 session 检查后暂停，purge 完成并提交，请求恢复后写入必须失败，
// 且库里不得留下孤儿消息。旧实现（Get 与插入不同事务、无锁复核）会成功
// 插入孤儿消息，因此本用例当前预期红。
func TestT41MessageCreatePausedBeforeInsertFailsAfterPurge(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-race", "target")

	repo := newT41PausingMessageRepo(apprepo.NewMessageRepository(env.db))
	t.Cleanup(repo.releaseNow)
	svc := t41NewMessageService(env, repo, nil)

	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)
	errCh := make(chan error, 1)
	go func() {
		_, err := svc.CreateMessage(ctx, &types.Message{
			SessionID: "s-race",
			Role:      "user",
			Content:   "private message racing with purge",
			RequestID: "req-t41-race",
		})
		errCh <- err
	}()

	select {
	case <-repo.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("CreateMessage 未到达消息写入暂停点：无法确认它已通过 session 检查")
	}

	// 请求停在写入前；此时注销完整提交（删会话/消息 + 墓碑化用户）。
	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t41 interleaving purge"))

	repo.releaseNow()
	select {
	case err := <-errCh:
		require.Error(t, err,
			"purge 先完成时，恢复的旧请求必须写入失败，不能返回成功")
	case <-time.After(10 * time.Second):
		t.Fatal("恢复后的 CreateMessage 没有返回")
	}

	require.Zero(t, t41CountMessages(t, env, "s-race"),
		"purge 完成后不得留下任何孤儿消息")
	var sessions int64
	require.NoError(t, env.db.Unscoped().Model(&types.Session{}).
		Where("id = ?", "s-race").Count(&sessions).Error)
	require.Zero(t, sessions, "purge 必须删除目标会话")
}

// TestT41MessageCommittedBeforePurgeIsDeleted 覆盖验收 2：
// 消息先提交，purge 后消息与其聊天历史索引都被删除，并留下清理任务。
// 这是既有 T33/T39 行为的保护网（当前预期 PASS）。
func TestT41MessageCommittedBeforePurgeIsDeleted(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-first", "target")
	t33SeedChatKnowledge(t, env, "k-t41-first", "kb-chat")

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)
	created, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-first",
		Role:      "user",
		Content:   "committed before purge",
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.NoError(t, env.db.Model(&types.Message{}).
		Where("id = ?", created.ID).Update("knowledge_id", "k-t41-first").Error)

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t41 commit first"))

	t33RequirePhysicalGone(t, env, &types.Message{}, created.ID)
	t33RequirePhysicalGone(t, env, &types.Knowledge{}, "k-t41-first")

	var tasks []types.TaskPendingOp
	require.NoError(t, env.db.
		Where("task_type = ? AND op = ?", "purge:cleanup", "knowledge").
		Find(&tasks).Error)
	require.NotEmpty(t, tasks, "purge 必须为聊天历史索引写入清理任务")
	found := false
	for _, task := range tasks {
		var payload t39KnowledgePayload
		require.NoError(t, json.Unmarshal(task.Payload, &payload))
		for _, id := range payload.KnowledgeIDs {
			if id == "k-t41-first" {
				found = true
			}
		}
	}
	require.True(t, found, "清理任务必须包含被删消息的 knowledge_id")
}

// TestT41NormalMessageCreationStillWorks 覆盖验收 3（正常聊天路径）：
// 存活会话 + 存活用户下消息与 artifacts 正常落库。当前与实现后都应 PASS，
// 防止围栏把正常写入挡掉。
func TestT41NormalMessageCreationStillWorks(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-live", "target")

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)
	created, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-live",
		Role:      "assistant",
		Content:   "normal answer",
		Artifacts: types.MessageArtifacts{{
			URL:      "local://artifacts/normal.md",
			FileName: "normal.md",
			FileType: ".md",
			FileSize: 8,
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.NotEmpty(t, created.ID)

	var row types.Message
	require.NoError(t, env.db.Where("id = ?", created.ID).Take(&row).Error)
	require.Equal(t, "s-live", row.SessionID)
	require.Equal(t, "normal answer", row.Content)

	var artifacts int64
	require.NoError(t, env.db.Model(&types.MessageArtifactRecord{}).
		Where("message_id = ?", created.ID).Count(&artifacts).Error)
	require.EqualValues(t, 1, artifacts, "消息 artifacts 必须与消息同事务落库")
}

// TestT41CreateMessageRejectsMissingSession 覆盖 session 存在性检查：
// 会话已不存在（purge 后）时消息写入失败且零插入。当前实现靠外层 Get
// 也会 PASS，是保护网。
func TestT41CreateMessageRejectsMissingSession(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)
	_, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-missing",
		Role:      "user",
		Content:   "late",
	})
	require.Error(t, err)
	require.Zero(t, t41CountMessages(t, env, "s-missing"))
}

// TestT41CreateMessageRejectsForeignOwner 覆盖 owner 作用域：
// 会话属于其他账号时不得写入。当前实现靠 sessionRepo.Get 的 user 过滤
// 也会 PASS，是保护网。
func TestT41CreateMessageRejectsForeignOwner(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-other", "other")

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)
	_, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-other",
		Role:      "user",
		Content:   "cross-owner write",
	})
	require.Error(t, err)
	require.Zero(t, t41CountMessages(t, env, "s-other"))
}

// TestT41CreateMessageRejectsTombstonedOwner 覆盖事务内用户复核：
// 会话所属账号已墓碑化（会话行因故仍在）时消息写入必须失败。
// 旧实现只看会话存在性，会写入成功，因此当前预期红。
func TestT41CreateMessageRejectsTombstonedOwner(t *testing.T) {
	env := newT33PurgeEnv(t)
	owner := uuid.NewString()
	umSeedUser(t, env, owner, nil)
	t33SeedSession(t, env, "s-dead-owner", owner)
	require.NoError(t, env.db.Unscoped().Model(&types.User{}).
		Where("id = ?", owner).
		Updates(map[string]interface{}{"deleted_at": time.Now(), "is_active": false}).Error)

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx(owner, umTenantA, types.TenantRoleViewer, false)
	_, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-dead-owner",
		Role:      "user",
		Content:   "write into a tombstoned owner's session",
	})
	require.Error(t, err, "墓碑化账号的会话不得再接受消息写入")
	require.Zero(t, t41CountMessages(t, env, "s-dead-owner"))
}

// TestT41CreateMessageRejectsBannedOwner 覆盖事务内封禁复核：
// 会话所属账号已封禁（users.is_active=false，会话行保留）时消息写入必须
// 失败。旧实现会写入成功，因此当前预期红。
func TestT41CreateMessageRejectsBannedOwner(t *testing.T) {
	env := newT33PurgeEnv(t)
	owner := uuid.NewString()
	umSeedUser(t, env, owner, nil)
	t33SeedSession(t, env, "s-banned-owner", owner)
	require.NoError(t, env.db.Model(&types.User{}).
		Where("id = ?", owner).Update("is_active", false).Error)

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx(owner, umTenantA, types.TenantRoleViewer, false)
	_, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-banned-owner",
		Role:      "user",
		Content:   "write into a banned owner's session",
	})
	require.Error(t, err, "封禁账号的会话不得再接受消息写入")
	require.Zero(t, t41CountMessages(t, env, "s-banned-owner"))
}

// TestT41CreateMessageAllowsNonCanonicalOwner 保护 API/IM/embed 会话：
// 非规范 UUID 的 owner 没有 users 行，用户状态复核必须跳过，消息照常写入。
func TestT41CreateMessageAllowsNonCanonicalOwner(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedSession(t, env, "s-api", types.SessionOwnerAPITenantKeyPrefix+"10:1")

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx(types.SessionOwnerAPITenantKeyPrefix+"10:1", umTenantA, types.TenantRoleViewer, false)
	created, err := svc.CreateMessage(ctx, &types.Message{
		SessionID: "s-api",
		Role:      "user",
		Content:   "api session message",
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.EqualValues(t, 1, t41CountMessages(t, env, "s-api"))
}

// t41RecordingKnowledgeService 记录聊天历史索引创建次数。
type t41RecordingKnowledgeService struct {
	interfaces.KnowledgeService

	mu      sync.Mutex
	created []string
}

func (s *t41RecordingKnowledgeService) CreateKnowledgeFromPassage(
	_ context.Context, kbID string, _ []string, _ string,
) (*types.Knowledge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, kbID)
	return &types.Knowledge{ID: "k-t41-index", KnowledgeBaseID: kbID}, nil
}

func (s *t41RecordingKnowledgeService) createdCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created)
}

func t41ChatHistoryCtx() context.Context {
	ctx := contextWithTenant(&types.Tenant{ChatHistoryConfig: &types.ChatHistoryConfig{
		Enabled:          true,
		EmbeddingModelID: "emb-t41",
		KnowledgeBaseID:  "kb-chat",
	}})
	return context.WithValue(ctx, types.TenantIDContextKey, umTenantA)
}

// TestT41IndexMessageToKBAfterPurgeWritesNoIndex 覆盖索引围栏：
// 消息已被 purge 删除后，迟到的 IndexMessageToKB 不得再创建聊天历史索引，
// 否则会留下无法通过消息访问、却仍可被检索命中的孤儿文本。
// 旧实现直接创建 knowledge，因此当前预期红。
func TestT41IndexMessageToKBAfterPurgeWritesNoIndex(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-idx-dead", "target")
	t33SeedMessage(t, env, "m-idx-dead", "s-idx-dead", "assistant", "late answer", "", nil)

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t41 late index"))

	know := &t41RecordingKnowledgeService{}
	svc := t41NewMessageService(env, nil, know)
	svc.IndexMessageToKB(t41ChatHistoryCtx(), "late question", "late answer", "m-idx-dead", "s-idx-dead")

	require.Zero(t, know.createdCount(),
		"消息已删除时 IndexMessageToKB 不得创建聊天历史索引")
}

// TestT41IndexMessageToKBLiveMessageStillIndexes 保护网：
// 存活消息的索引路径不能被围栏误伤；创建成功后 knowledge_id 正常回写。
func TestT41IndexMessageToKBLiveMessageStillIndexes(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-idx-live", "target")
	t33SeedMessage(t, env, "m-idx-live", "s-idx-live", "assistant", "live answer", "", nil)

	know := &t41RecordingKnowledgeService{}
	svc := t41NewMessageService(env, nil, know)
	svc.IndexMessageToKB(t41ChatHistoryCtx(), "live question", "live answer", "m-idx-live", "s-idx-live")

	require.Equal(t, 1, know.createdCount(), "存活消息必须继续索引")
	var row types.Message
	require.NoError(t, env.db.Where("id = ?", "m-idx-live").Take(&row).Error)
	require.Equal(t, "k-t41-index", row.KnowledgeID,
		"索引成功后必须把 knowledge_id 回写到消息")
}

// t41SQLRecorder 记录 GORM 实际执行的 SQL 顺序，用于验证 purge 的锁序。
type t41SQLRecorder struct {
	logger.Interface

	mu   sync.Mutex
	sqls []string
}

func (r *t41SQLRecorder) Trace(
	_ context.Context, _ time.Time, fc func() (string, int64), _ error,
) {
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

func (r *t41SQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sqls...)
}

func t41FirstMatching(sqls []string, re *regexp.Regexp) int {
	for i, sql := range sqls {
		if re.MatchString(sql) {
			return i
		}
	}
	return -1
}

// TestT41PurgeLocksSessionsBeforeDeletingMessages 覆盖锁序契约：
// purge 必须先锁 users、再锁目标 sessions、最后才删 messages/sessions；
// 否则“消息写入先拿到 session 锁、purge 已删过 messages 再等 session”的
// 交错仍会留下孤儿消息。SQLite 沿用 lockPurgeTarget 的空更新取写锁做法，
// 因此用 SQL 记录断言 sessions 的 UPDATE 出现在 DELETE messages 之前。
// 当前 purge 没有 session 锁，预期红。
func TestT41PurgeLocksSessionsBeforeDeletingMessages(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-lock-order", "target")
	t33SeedMessage(t, env, "m-lock-order", "s-lock-order", "user", "existing message", "", nil)

	recorder := &t41SQLRecorder{Interface: logger.Default}
	env.db.Config.Logger = recorder

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t41 lock order"))

	sqls := recorder.snapshot()
	// GORM 在 SQLite 用反引号引用标识符（UPDATE `users` ...），PostgreSQL
	// 不引用；两种形式都要能识别。
	usersLock := t41FirstMatching(sqls, regexp.MustCompile(`(?i)update\s+[\x60"]?users[\x60"]?\s+set`))
	sessionsLock := t41FirstMatching(sqls, regexp.MustCompile(`(?i)update\s+[\x60"]?sessions[\x60"]?\s+set`))
	messagesDelete := t41FirstMatching(sqls, regexp.MustCompile(`(?i)delete\s+from\s+[\x60"]?messages[\x60"]?`))

	require.NotEqual(t, -1, usersLock,
		"purge 必须先锁 users 行；实际 SQL：%v", sqls)
	require.NotEqual(t, -1, sessionsLock,
		"purge 必须在事务内锁定目标 sessions（SQLite 用空更新取写锁）；实际 SQL：%v", sqls)
	require.NotEqual(t, -1, messagesDelete,
		"purge 必须删除 messages；实际 SQL：%v", sqls)
	require.Less(t, usersLock, sessionsLock,
		"锁序必须是 users → sessions；实际 SQL：%v", sqls)
	require.Less(t, sessionsLock, messagesDelete,
		"必须锁住 sessions 之后再删 messages，否则消息写入可与删除交错留下孤儿；实际 SQL：%v", sqls)
}

// TestT41MessageCreateAndPurgeDoNotDeadlock 覆盖验收 3（无锁等待死锁）：
// 消息写入与 purge 并发执行都必须在限时内返回；purge 成功后目标会话下不得
// 残留任何消息。SQLite 单写者会串行化两者，本用例只保证不死锁且终态干净。
func TestT41MessageCreateAndPurgeDoNotDeadlock(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)
	t33SeedSession(t, env, "s-deadlock", "target")

	svc := t41NewMessageService(env, nil, nil)
	ctx := umCallerCtx("target", umTenantA, types.TenantRoleViewer, false)

	start := make(chan struct{})
	createDone := make(chan error, 1)
	purgeDone := make(chan error, 1)
	go func() {
		<-start
		_, err := svc.CreateMessage(ctx, &types.Message{
			SessionID: "s-deadlock",
			Role:      "user",
			Content:   "concurrent with purge",
		})
		createDone <- err
	}()
	go func() {
		<-start
		purgeDone <- env.svc.PurgeUser(
			umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
			umTenantA, "target", "target@example.com", "t41 concurrency")
	}()
	close(start)

	deadline := time.After(20 * time.Second)
	var purgeErr error
	for pending := 2; pending > 0; {
		select {
		case <-createDone:
			pending--
		case err := <-purgeDone:
			purgeErr = err
			pending--
		case <-deadline:
			t.Fatal("消息写入与 purge 并发时发生死锁/超时")
		}
	}
	require.NoError(t, purgeErr, "并发的 purge 必须正常完成")
	require.Zero(t, t41CountMessages(t, env, "s-deadlock"),
		"purge 成功后目标会话下不得残留消息")
}
