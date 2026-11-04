package docs_test

// 整体回归（AC-19 / C9）：邀请链接创建、lookup、注册接口的 Swagger 契约。
// 同时校验三份生成物（docs.go 内嵌、swagger.json、swagger.yaml），
// 防止只更新其中一份导致漂移。

import (
	"testing"
)

type inviteSwaggerProperty struct {
	Type    string   `json:"type" yaml:"type"`
	Minimum *float64 `json:"minimum" yaml:"minimum"`
	Maximum *float64 `json:"maximum" yaml:"maximum"`
}

type inviteSwaggerDefinition struct {
	Properties map[string]inviteSwaggerProperty `json:"properties" yaml:"properties"`
}

type inviteSwaggerOperation struct {
	Responses map[string]any `json:"responses" yaml:"responses"`
}

type inviteSwaggerSpec struct {
	Paths       map[string]map[string]inviteSwaggerOperation `json:"paths" yaml:"paths"`
	Definitions map[string]inviteSwaggerDefinition           `json:"definitions" yaml:"definitions"`
}

func TestInviteLinkSwaggerContract(t *testing.T) {
	const (
		createRequest  = "internal_handler.createInviteLinkRequest"
		invitationResp = "github_com_Tencent_WeKnora_internal_types.TenantInvitationResponse"
	)
	for _, tt := range swaggerDocuments() {
		t.Run(tt.name, func(t *testing.T) {
			var spec inviteSwaggerSpec
			if err := tt.parse(tt.loadSpec(t), &spec); err != nil {
				t.Fatalf("parse swagger: %v", err)
			}

			create, ok := spec.Paths["/tenants/{id}/invite-links"]["post"]
			if !ok {
				t.Fatal("缺少 POST /tenants/{id}/invite-links")
			}
			if _, ok := create.Responses["400"]; !ok {
				t.Error("创建接口缺少 400 响应（非法人数 / 过去时间）")
			}

			for _, method := range []string{"put", "patch"} {
				if _, ok := spec.Paths["/tenants/{id}/invite-links"][method]; ok {
					t.Errorf("共享链接不应文档化 %s 修改接口（创建后不可改）", method)
				}
			}

			req := spec.Definitions[createRequest].Properties
			if maxRegs, ok := req["max_registrations"]; !ok || maxRegs.Type != "integer" ||
				maxRegs.Minimum == nil || *maxRegs.Minimum != 1 ||
				maxRegs.Maximum == nil || *maxRegs.Maximum != 1000 {
				t.Errorf("创建请求 max_registrations 契约错误: %+v", maxRegs)
			}
			if _, ok := req["expires_at"]; !ok {
				t.Error("创建请求缺少 expires_at")
			}

			resp := spec.Definitions[invitationResp].Properties
			for _, field := range []string{"max_registrations", "registered_count"} {
				if _, ok := resp[field]; !ok {
					t.Errorf("TenantInvitationResponse 缺少 %s", field)
				}
			}

			if _, ok := spec.Paths["/auth/invitations/lookup"]["post"]; !ok {
				t.Fatal("缺少 POST /auth/invitations/lookup")
			}
			lookup := spec.Paths["/auth/invitations/lookup"]["post"]
			if _, ok := lookup.Responses["410"]; !ok {
				t.Error("lookup 缺少 410 响应（无效/过期/撤销/满额）")
			}

			register, ok := spec.Paths["/auth/register-by-invite"]["post"]
			if !ok {
				t.Fatal("缺少 POST /auth/register-by-invite")
			}
			for _, status := range []string{"409", "410"} {
				if _, ok := register.Responses[status]; !ok {
					t.Errorf("注册接口缺少 %s 响应", status)
				}
			}
		})
	}
}
