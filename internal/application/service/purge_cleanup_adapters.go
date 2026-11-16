package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// purgeVectorTarget 是 worker 传给向量引擎适配器的 KB 绑定信息：
// DeleteByKnowledgeIDList 的参数里没有 KB，只能经上下文带过去。
type purgeVectorTarget struct {
	TenantID      uint64
	KnowledgeBase string
	VectorStoreID *string
}

type purgeVectorTargetContextKey struct{}

func withPurgeVectorTarget(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, vectorStoreID *string,
) context.Context {
	return context.WithValue(ctx, purgeVectorTargetContextKey{}, purgeVectorTarget{
		TenantID:      tenantID,
		KnowledgeBase: knowledgeBaseID,
		VectorStoreID: vectorStoreID,
	})
}

// purgeVectorEngineAdapter 用同步删除路径同一套工厂解析 KB 绑定的向量库
// （含租户 ownership 校验），再执行按 knowledge 删除。
type purgeVectorEngineAdapter struct {
	registry  interfaces.RetrieveEngineRegistry
	ownership retriever.TenantStoreOwnership
	tenants   interfaces.TenantService
}

// NewPurgeVectorEngineAdapter 构造生产向量引擎适配器。
func NewPurgeVectorEngineAdapter(
	registry interfaces.RetrieveEngineRegistry,
	ownership retriever.TenantStoreOwnership,
	tenants interfaces.TenantService,
) PurgeVectorEngine {
	return &purgeVectorEngineAdapter{
		registry: registry, ownership: ownership, tenants: tenants,
	}
}

func (a *purgeVectorEngineAdapter) DeleteByKnowledgeIDList(
	ctx context.Context, knowledgeIDList []string, dimension int, knowledgeType string,
) error {
	target, ok := ctx.Value(purgeVectorTargetContextKey{}).(purgeVectorTarget)
	if !ok || target.TenantID == 0 || target.KnowledgeBase == "" {
		return fmt.Errorf("purge vector cleanup: missing KB/tenant context")
	}
	// KB 未绑定具体向量库时，工厂从 ctx 的 TenantInfo 取工作区生效引擎。
	if target.VectorStoreID == nil || strings.TrimSpace(*target.VectorStoreID) == "" {
		tenant, err := a.tenants.GetTenantByID(ctx, target.TenantID)
		if err != nil {
			return fmt.Errorf("resolve purge tenant %d: %w", target.TenantID, err)
		}
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	}
	engine, err := retriever.CreateRetrieveEngineForKB(
		ctx, a.registry, a.ownership, target.TenantID, target.VectorStoreID)
	if err != nil {
		return fmt.Errorf("resolve purge vector engine for KB %s: %w", target.KnowledgeBase, err)
	}
	return engine.DeleteByKnowledgeIDList(ctx, knowledgeIDList, dimension, knowledgeType)
}

// temporaryDocumentReleaser 把临时附件服务的资源释放语义包装成 worker 的
// 窄接口，避免扩大 interfaces.TemporaryDocumentService。
type temporaryDocumentReleaser struct {
	catalog interfaces.ResourceCatalog
	files   interfaces.FileService
}

// NewTemporaryDocumentReleaser 构造附件资源释放适配器。
func NewTemporaryDocumentReleaser(
	catalog interfaces.ResourceCatalog, files interfaces.FileService,
) PurgeAttachmentReleaser {
	return &temporaryDocumentReleaser{catalog: catalog, files: files}
}

func (r *temporaryDocumentReleaser) ReleaseResource(
	ctx context.Context, documentID, ref string,
) error {
	return releaseTemporaryDocumentResource(ctx, r.catalog, r.files, documentID, ref)
}
