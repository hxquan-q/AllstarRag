package handler

// CP3 修复轮 1 复验（F3）：批量入口 skipped[].recover_at 与单文档 429 同口径
// = 窗口内最早计数 + 24h（±2min 容差）。复用 T04 的真实 sqlite 审计夹具与
// 真实 ReparseQuotaGate（red-stage 接线点已由②落地）。

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestCP3R1_BatchReplayRecoverAtIsEarliestPlus24h(t *testing.T) {
	const doc = "exhausted-doc"
	env := newReparseQuotaEnv(t, false, doc)
	// 三行用不同 age 落库，最早一行决定 recover_at。
	seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, 5*time.Hour)
	seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, 2*time.Hour)
	seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, time.Hour)

	w := env.post(t, "/knowledge/batch-reparse",
		`{"kb_id":"kb","ids":["`+doc+`"]}`)
	if w.Code != 200 {
		t.Fatalf("batch reparse status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			ReparseCount int `json:"reparse_count"`
			Skipped      []struct {
				KnowledgeID string `json:"knowledge_id"`
				Reason      string `json:"reason"`
				RecoverAt   string `json:"recover_at"`
			} `json:"skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode batch body: %v (body=%s)", err, w.Body.String())
	}
	if body.Data.ReparseCount != 0 {
		t.Fatalf("reparse_count=%d, want 0（文档已耗尽）", body.Data.ReparseCount)
	}
	if len(body.Data.Skipped) != 1 {
		t.Fatalf("skipped len=%d, want 1", len(body.Data.Skipped))
	}
	skip := body.Data.Skipped[0]
	if skip.KnowledgeID != doc || skip.Reason != "RETRY_LIMIT_EXHAUSTED" {
		t.Fatalf("skipped=%+v, want doc+RETRY_LIMIT_EXHAUSTED", skip)
	}
	recoverAt, err := time.Parse(time.RFC3339, skip.RecoverAt)
	if err != nil {
		t.Fatalf("recover_at=%q 不是 RFC3339: %v", skip.RecoverAt, err)
	}
	want := earliestReparseAudit(t, env.db, doc, 24*time.Hour).Add(24 * time.Hour)
	if diff := recoverAt.Sub(want); diff < -2*time.Minute || diff > 2*time.Minute {
		t.Fatalf("skipped.recover_at=%v, want≈%v（最早计数+24h，与单文档 429 同口径）", recoverAt, want)
	}
}
