package service

import (
	"context"
	"errors"
	"time"
)

// registrationQuotaRepo 是注册名额预占/释放的窄接口。
// 不并入中央 interfaces.TenantInvitationRepository，避免既有测试桩
// 显式实现全部方法时因接口扩张而编译失败。
type registrationQuotaRepo interface {
	ReserveRegistrationQuota(ctx context.Context, id uint64, now time.Time) (bool, error)
	ReleaseRegistrationQuota(ctx context.Context, id uint64) error
}

// shareLinkOptionsRepo 是共享链接创建后补写选项的窄接口。
// 与名额预占分开断言：只包一层记录/装饰的调用方可以只实现
// Reserve/Release，不必连带实现 ApplyShareLinkOptions。
type shareLinkOptionsRepo interface {
	ApplyShareLinkOptions(ctx context.Context, id uint64, maxRegistrations *int, expiresAt *time.Time) error
}

// errRegistrationQuotaUnsupported 表示注入的仓储没有实现配额方法。
// 真实环境始终使用仓储实现；出现该错误说明装配有误，直接透出。
var errRegistrationQuotaUnsupported = errors.New("租户邀请仓储不支持注册配额")

// quotaRepo 通过类型断言取出窄接口，避免扩展中央仓储接口。
func (s *tenantInvitationService) quotaRepo() (registrationQuotaRepo, error) {
	repo, ok := s.repo.(registrationQuotaRepo)
	if !ok {
		return nil, errRegistrationQuotaUnsupported
	}
	return repo, nil
}

// shareLinkRepo 通过类型断言取出补写选项的窄接口。
func (s *tenantInvitationService) shareLinkRepo() (shareLinkOptionsRepo, error) {
	repo, ok := s.repo.(shareLinkOptionsRepo)
	if !ok {
		return nil, errRegistrationQuotaUnsupported
	}
	return repo, nil
}

// ReserveRegistrationQuota 原子预占一个注册名额。
// 未抢到名额（满额 / 过期 / 已撤销）返回 ErrInvitationTokenInvalid，
// 由 handler 统一映射为 410，不区分具体原因。
func (s *tenantInvitationService) ReserveRegistrationQuota(
	ctx context.Context,
	invID uint64,
) error {
	repo, err := s.quotaRepo()
	if err != nil {
		return err
	}
	ok, err := repo.ReserveRegistrationQuota(ctx, invID, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvitationTokenInvalid
	}
	return nil
}

// ReleaseRegistrationQuota 在注册失败时退回已预占的名额。
func (s *tenantInvitationService) ReleaseRegistrationQuota(
	ctx context.Context,
	invID uint64,
) error {
	repo, err := s.quotaRepo()
	if err != nil {
		return err
	}
	return repo.ReleaseRegistrationQuota(ctx, invID)
}

// ApplyShareLinkOptions 在共享链接创建后补写配额与有效期。
// 传 nil 的字段不更新。
func (s *tenantInvitationService) ApplyShareLinkOptions(
	ctx context.Context,
	invID uint64,
	maxRegistrations *int,
	expiresAt *time.Time,
) error {
	repo, err := s.shareLinkRepo()
	if err != nil {
		return err
	}
	return repo.ApplyShareLinkOptions(ctx, invID, maxRegistrations, expiresAt)
}
