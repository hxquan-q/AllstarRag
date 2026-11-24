// Package yunzhijia 是云之家 SSO 的外部 HTTP 客户端：
// ticket 换姓名，以及在职/离职员工分页列表。
//
// 只使用标准库；配置由调用方注入，不读环境变量。
// ticket 是凭证，只拼进请求路径，绝不进入错误字符串或日志。
package yunzhijia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
)

const (
	// DefaultTimeout 是单次外部请求的默认超时。
	DefaultTimeout = 10 * time.Second
	// DefaultMaxBodyBytes 是响应体读取上限（8 MiB），防止超大响应耗尽内存。
	DefaultMaxBodyBytes int64 = 8 << 20

	ticketSuccessCode = 200
	ticketSuccessMsg  = "成功"
)

// Client 是云之家外部接口的抽象，便于上层替换与测试。
type Client interface {
	// QueryUserName 用 ticket 换取员工姓名。ticket 无效/过期返回 ErrInvalidTicket。
	QueryUserName(ctx context.Context, ticket string) (string, error)
	// ListEmployees 拉取一页员工。status 只能是 active 或 inactive。
	ListEmployees(ctx context.Context, status Status, page, pageSize int) (*EmployeePage, error)
}

// Options 是客户端需要的最小配置。
type Options struct {
	// BaseURL 是云之家服务根地址（scheme + host）。
	BaseURL string
	// TicketUserPath 是 ticket 换姓名接口路径。
	TicketUserPath string
	// EmployeePath 是员工分页接口路径。
	EmployeePath string
	// Timeout 是单次请求超时，<=0 时用 DefaultTimeout。
	Timeout time.Duration
	// MaxBodyBytes 是响应体上限，<=0 时用 DefaultMaxBodyBytes。
	MaxBodyBytes int64
}

// HTTPClient 是 Client 的标准库实现。
type HTTPClient struct {
	httpClient      *http.Client
	ticketURLPrefix string
	employeeBaseURL *url.URL
	maxBodyBytes    int64
}

var _ Client = (*HTTPClient)(nil)

// NewClient 校验配置并构造客户端。BaseURL/两个接口路径缺失或非法时返回错误。
func NewClient(opts Options) (*HTTPClient, error) {
	base, err := parseBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	ticketBaseURL, err := joinURLPath(base, opts.TicketUserPath)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: invalid ticket_user_path: %w", err)
	}
	employeeBaseURL, err := joinURLPath(base, opts.EmployeePath)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: invalid employee_path: %w", err)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &HTTPClient{
		httpClient: &http.Client{
			Timeout:       timeout,
			CheckRedirect: rejectRedirect,
		},
		ticketURLPrefix: ticketBaseURL.String(),
		employeeBaseURL: employeeBaseURL,
		maxBodyBytes:    maxBodyBytes,
	}, nil
}

// NewClientFromConfig 从 T01 的配置段构造客户端。是否启用由调用方判断。
func NewClientFromConfig(cfg *config.YunzhijiaSSOConfig) (*HTTPClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("yunzhijia: nil config")
	}
	return NewClient(Options{
		BaseURL:        cfg.BaseURL,
		TicketUserPath: cfg.TicketUserPath,
		EmployeePath:   cfg.EmployeePath,
		Timeout:        time.Duration(cfg.HTTPTimeoutSeconds) * time.Second,
	})
}

// QueryUserName 调用 queryUserName/{ticket} 换取姓名。
// 无效 ticket 也可能返回 HTTP 200，因此必须校验 JSON 的 code/msg/data。
func (c *HTTPClient) QueryUserName(ctx context.Context, ticket string) (string, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return "", fmt.Errorf("%w: empty ticket", ErrInvalidTicket)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, c.ticketURL(ticket), nil,
	)
	if err != nil {
		// 不透传 err：url.Error 会带完整请求 URL（含 ticket）。
		return "", fmt.Errorf("%w: build ticket request failed", ErrUpstream)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrUpstream, c.scrub(err.Error(), ticket))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: ticket endpoint returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	body, err := c.readBody(resp.Body)
	if err != nil {
		return "", err
	}

	var payload ticketResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("%w: decode ticket response: %s", ErrContract, c.scrub(err.Error(), ticket))
	}
	if payload.Code != ticketSuccessCode || payload.Msg != ticketSuccessMsg {
		return "", fmt.Errorf("%w: response code=%d", ErrInvalidTicket, payload.Code)
	}
	name := strings.TrimSpace(payload.Data)
	if name == "" {
		return "", fmt.Errorf("%w: empty name", ErrInvalidTicket)
	}
	return name, nil
}

// ListEmployees 拉取一页员工列表。
func (c *HTTPClient) ListEmployees(
	ctx context.Context, status Status, page, pageSize int,
) (*EmployeePage, error) {
	endpoint := c.employeeEndpoint(status, page, pageSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build employee request failed", ErrUpstream)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: employee endpoint returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	body, err := c.readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload employeeListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: decode employee list: %v", ErrContract, err)
	}
	return convertEmployeePage(payload)
}

// convertEmployeePage 校验分页字段并把上游记录转成对外类型。
func convertEmployeePage(payload employeeListResponse) (*EmployeePage, error) {
	p := payload.Pagination
	if p.Page < 1 || p.PageSize < 1 || p.Total < 0 {
		return nil, fmt.Errorf("%w: invalid pagination", ErrContract)
	}
	if len(payload.List) == 0 && p.Total > 0 {
		// 总量大于 0 却返回空页，说明数据不完整；不能据此判定离职。
		return nil, fmt.Errorf("%w: empty list with total %d", ErrContract, p.Total)
	}
	list := make([]Employee, 0, len(payload.List))
	for _, item := range payload.List {
		item.OpenID = strings.TrimSpace(item.OpenID)
		if item.OpenID == "" {
			return nil, fmt.Errorf("%w: employee without open_id", ErrContract)
		}
		item.Name = strings.TrimSpace(item.Name)
		list = append(list, Employee(item))
	}
	return &EmployeePage{
		List:     list,
		Page:     p.Page,
		PageSize: p.PageSize,
		Total:    p.Total,
	}, nil
}

func (c *HTTPClient) ticketURL(ticket string) string {
	return c.ticketURLPrefix + "/" + url.PathEscape(ticket)
}

func (c *HTTPClient) employeeEndpoint(status Status, page, pageSize int) string {
	u := *c.employeeBaseURL
	q := u.Query()
	q.Set("status", string(status))
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *HTTPClient) readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, c.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body", ErrUpstream)
	}
	if int64(len(body)) > c.maxBodyBytes {
		return nil, fmt.Errorf("%w: response body exceeds %d bytes", ErrContract, c.maxBodyBytes)
	}
	return body, nil
}

// scrub 把错误文本里可能出现的 ticket（含 URL 转义形式）替换掉。
func (c *HTTPClient) scrub(msg, ticket string) string {
	msg = strings.ReplaceAll(msg, ticket, "[ticket]")
	return strings.ReplaceAll(msg, url.PathEscape(ticket), "[ticket]")
}

// rejectRedirect 拒绝任何重定向：不跟随，直接把 3xx 响应交给调用方判定。
// 返回 ErrUseLastResponse 而不是 error，避免 http.Client 把带 ticket 的 URL
// 包进 url.Error。
func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func parseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("yunzhijia: base_url is empty")
	}
	base, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: invalid base_url")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("yunzhijia: base_url must use http or https")
	}
	if base.Host == "" {
		return nil, fmt.Errorf("yunzhijia: base_url has no host")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("yunzhijia: base_url must not contain query or fragment")
	}
	return base, nil
}

func joinURLPath(base *url.URL, rawPath string) (*url.URL, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return nil, fmt.Errorf("path is empty")
	}
	if strings.ContainsAny(rawPath, "?#") {
		return nil, fmt.Errorf("path must not contain query or fragment")
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(rawPath, "/")
	u.RawPath = ""
	return &u, nil
}
