package yunzhijia

// T03 验收条款的基线用例：HTTP 200 + code=200 + msg=成功 + data 为空/空白
// 必须按无效 ticket 处理，不能当作契约错误放行。该行为由 T02 客户端实现，
// 在 T03 红阶段应保持绿色（用于证明测试环境本身可执行）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryUserNameEmptyDataIsInvalidTicket(t *testing.T) {
	for _, data := range []string{"", "   "} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"msg":"成功","data":"` + data + `"}`))
		}))

		client, err := NewClient(Options{
			BaseURL:        srv.URL,
			TicketUserPath: "/queryUserName",
			EmployeePath:   "/employees",
		})
		if err != nil {
			srv.Close()
			t.Fatalf("NewClient: %v", err)
		}

		_, err = client.QueryUserName(context.Background(), "ticket-placeholder-empty")
		srv.Close()
		if !errors.Is(err, ErrInvalidTicket) {
			t.Fatalf("data=%q: err = %v, want ErrInvalidTicket", data, err)
		}
	}
}
