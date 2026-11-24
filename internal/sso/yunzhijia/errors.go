package yunzhijia

import "errors"

// 外部客户端的三类错误。调用方用 errors.Is 区分处理：
//   - ErrInvalidTicket：ticket 无效/过期（业务失败），提示用户从云之家重新打开；
//   - ErrUpstream：网络、超时、重定向、非 200 等上游故障，提示稍后再试；
//   - ErrContract：HTTP 成功但响应结构不符合约定，属于上游契约错误。
//
// 所有错误文本都不得包含 ticket 或带 ticket 的完整 URL。
var (
	ErrInvalidTicket = errors.New("yunzhijia: invalid or expired ticket")
	ErrUpstream      = errors.New("yunzhijia: upstream request failed")
	ErrContract      = errors.New("yunzhijia: upstream response violates contract")
)
