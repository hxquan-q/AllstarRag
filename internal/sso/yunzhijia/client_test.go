package yunzhijia

// 整体回归补测：T02 外部客户端在票级阶段只有「空 data」基线用例，
// AC-17/§15.1 要求的成功、业务失败、HTTP 错误、超时、超大响应、
// 重定向、契约错误与员工分页解析在本轮补齐。全部使用 httptest，
// 不访问真实云之家，也不使用真实 ticket。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server, opts Options) *HTTPClient {
	t.Helper()
	opts.BaseURL = srv.URL
	if opts.TicketUserPath == "" {
		opts.TicketUserPath = "/queryUserName"
	}
	if opts.EmployeePath == "" {
		opts.EmployeePath = "/employees"
	}
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestQueryUserNameSuccessTrimsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queryUserName/"+"ticket-placeholder-ok" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(w, `{"code":200,"msg":"成功","data":"  张三  "}`)
	}))
	defer srv.Close()

	name, err := newTestClient(t, srv, Options{}).QueryUserName(context.Background(), "ticket-placeholder-ok")
	if err != nil {
		t.Fatalf("QueryUserName: %v", err)
	}
	if name != "张三" {
		t.Fatalf("name = %q, want 张三", name)
	}
}

func TestQueryUserNameBusinessFailureIsInvalidTicket(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "code not 200", body: `{"code":401,"msg":"成功","data":"张三"}`},
		{name: "msg not success", body: `{"code":200,"msg":"失败","data":"张三"}`},
		{name: "empty data", body: `{"code":200,"msg":"成功","data":""}`},
		{name: "blank data", body: `{"code":200,"msg":"成功","data":"   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.body)
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv, Options{}).QueryUserName(context.Background(), "ticket-placeholder-bad")
			if !errors.Is(err, ErrInvalidTicket) {
				t.Fatalf("err = %v, want ErrInvalidTicket", err)
			}
		})
	}
}

func TestQueryUserNameHTTPErrorIsUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{}).QueryUserName(context.Background(), "ticket-placeholder-http")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestQueryUserNameMalformedJSONIsContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"code":200,`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{}).QueryUserName(context.Background(), "ticket-placeholder-json")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("err = %v, want ErrContract", err)
	}
}

func TestQueryUserNameTimeoutIsUpstreamAndDoesNotLeakTicket(t *testing.T) {
	ticket := "ticket-placeholder-timeout-SECRET"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		writeJSON(w, `{"code":200,"msg":"成功","data":"张三"}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{Timeout: 20 * time.Millisecond}).
		QueryUserName(context.Background(), ticket)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), ticket) {
		t.Fatalf("error 文本泄露 ticket: %v", err)
	}
}

func TestQueryUserNameOversizedResponseIsContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"code":200,"msg":"成功","data":"一大段远超上限的响应内容"}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{MaxBodyBytes: 16}).
		QueryUserName(context.Background(), "ticket-placeholder-big")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("err = %v, want ErrContract", err)
	}
}

func TestQueryUserNameRejectsRedirectWithoutFollowing(t *testing.T) {
	var redirected bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/queryUserName/ticket-placeholder-redirect" {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		redirected = true
		writeJSON(w, `{"code":200,"msg":"成功","data":"张三"}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{}).QueryUserName(context.Background(), "ticket-placeholder-redirect")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if redirected {
		t.Fatal("客户端不应跟随重定向")
	}
}

func TestListEmployeesParsesPageAndEncodesQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("status"); got != "active" {
			t.Errorf("status = %q, want active", got)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page = %q, want 2", got)
		}
		if got := r.URL.Query().Get("page_size"); got != "500" {
			t.Errorf("page_size = %q, want 500", got)
		}
		writeJSON(w, `{"list":[{"open_id":" open-1 ","name":" 员工甲 ","department":"研发中心\\人力资源部","mobile":"13800000000"},{"open_id":"open-2","name":"员工乙","department":"","mobile":null}],"pagination":{"page":2,"page_size":500,"total":2}}`)
	}))
	defer srv.Close()

	page, err := newTestClient(t, srv, Options{}).ListEmployees(context.Background(), StatusActive, 2, 500)
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if page.Page != 2 || page.PageSize != 500 || page.Total != 2 {
		t.Fatalf("pagination = %+v", page)
	}
	if len(page.List) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(page.List))
	}
	if page.List[0].OpenID != "open-1" || page.List[0].Name != "员工甲" {
		t.Fatalf("first employee = %+v", page.List[0])
	}
	if page.List[0].Department != `研发中心\人力资源部` {
		t.Fatalf("department = %q", page.List[0].Department)
	}
	if page.List[1].Mobile != "" {
		t.Fatalf("mobile = %q, want empty", page.List[1].Mobile)
	}
}

func TestListEmployeesContractErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "invalid pagination", body: `{"list":[],"pagination":{"page":0,"page_size":500,"total":0}}`},
		{name: "empty list with total", body: `{"list":[],"pagination":{"page":1,"page_size":500,"total":10}}`},
		{name: "missing open_id", body: `{"list":[{"open_id":"  ","name":"员工甲"}],"pagination":{"page":1,"page_size":500,"total":1}}`},
		{name: "malformed json", body: `{"list":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.body)
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv, Options{}).ListEmployees(context.Background(), StatusInactive, 1, 500)
			if !errors.Is(err, ErrContract) {
				t.Fatalf("err = %v, want ErrContract", err)
			}
		})
	}
}

func TestListEmployeesHTTPErrorIsUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, Options{}).ListEmployees(context.Background(), StatusActive, 1, 500)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestNewClientRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{name: "empty base", opts: Options{TicketUserPath: "/t", EmployeePath: "/e"}},
		{name: "non http scheme", opts: Options{BaseURL: "ftp://example.com", TicketUserPath: "/t", EmployeePath: "/e"}},
		{name: "base with query", opts: Options{BaseURL: "https://example.com?x=1", TicketUserPath: "/t", EmployeePath: "/e"}},
		{name: "empty ticket path", opts: Options{BaseURL: "https://example.com", EmployeePath: "/e"}},
		{name: "path with query", opts: Options{BaseURL: "https://example.com", TicketUserPath: "/t?x=1", EmployeePath: "/e"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.opts); err == nil {
				t.Fatal("NewClient 应拒绝非法配置")
			}
		})
	}
}
