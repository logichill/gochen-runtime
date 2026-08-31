// 演示 L3 资源授权：主体在什么数据范围内、能对哪条资源做什么。
//
// 关键点：
//   - 判定发生在 PDP（scoped.IAuthorizer），返回结构化 Decision；
//   - allow 决策被投影为 WriteConstraint，经受控通道交给 Repo 合并进原子 WHERE；
//   - 范围外的资源直接判 deny，而不是"查出来再过滤"。
package main

import (
	"context"
	"log"

	"gochen/auth/scoped"
)

const (
	readAction  = "document:api:read"
	writeAction = "document:api:write"
)

type document struct {
	ID             string
	TenantID       string
	ManagedScopeID int64
}

func main() {
	log.SetFlags(0)

	target := &document{ID: "doc-1", TenantID: "tenant-a", ManagedScopeID: 1001}
	authorizer := mustAuthorizer()

	// 主体可见范围：仅 1001。
	ctx := mustContext(scoped.WithDataScope(context.Background(), scoped.Filtered(1001)))

	readDecision := mustDecision(authorizer.Authorize(ctx, readAction, target))
	log.Printf("read:  effect=%s resources=%d", readDecision.Effect, len(readDecision.AuthorizedResources))

	// allow 决策 → 写约束：这份约束随后经受控通道交给 Repo。
	if resource, ok := readDecision.WriteConstraint().Single(); ok {
		log.Printf("       constraint kind=%s scope=%d", resource.Kind, resource.ManagedScopeID)
	}

	// 同一主体缺少 write 动作 → 拒绝。
	writeDecision := mustDecision(authorizer.Authorize(ctx, writeAction, target))
	log.Printf("write: effect=%s reason=%s", writeDecision.Effect, writeDecision.ReasonCode)

	// 范围外资源 → 拒绝。
	outside := &document{ID: "doc-2", TenantID: "tenant-a", ManagedScopeID: 2002}
	outsideDecision := mustDecision(authorizer.Authorize(ctx, readAction, outside))
	log.Printf("out:   effect=%s reason=%s", outsideDecision.Effect, outsideDecision.ReasonCode)

	// 三态 DataScope：DenyAll 一律拒绝，且它是 DataScope 的零值（fail-closed 默认）。
	denyCtx := mustContext(scoped.WithDataScope(context.Background(), scoped.DenyAll()))
	denyDecision := mustDecision(authorizer.Authorize(denyCtx, readAction, target))
	log.Printf("deny:  effect=%s reason=%s", denyDecision.Effect, denyDecision.ReasonCode)
}

// mustAuthorizer 组装一个最小 PDP：解析资源 → 按动作与数据范围判定。
func mustAuthorizer() scoped.IAuthorizer {
	resolver := scoped.TypedResourceResolver[*document](func(doc *document) (scoped.Resource, bool) {
		if doc == nil {
			return scoped.Resource{}, false
		}
		return scoped.Resource{
			Kind:           "document",
			ID:             doc.ID,
			TenantID:       doc.TenantID,
			ManagedScopeID: doc.ManagedScopeID,
		}, true
	})

	// 本示例的主体只被授予 read。
	granted := map[string]bool{readAction: true}

	evaluator := scoped.EvaluatorFunc(func(ctx context.Context, action string, resources []scoped.Resource) (scoped.Decision, error) {
		if !granted[action] {
			return scoped.Deny(scoped.ReasonActionDenied), nil
		}
		scope, err := scoped.ResolveDataScope(ctx, nil)
		if err != nil {
			return scoped.Deny(scoped.ReasonOutOfScope), nil
		}
		for _, resource := range resources {
			if !scope.Contains(resource.ManagedScopeID) {
				return scoped.Deny(scoped.ReasonOutOfScope), nil
			}
		}
		return scoped.Allow(resources...), nil
	})

	authorizer, err := scoped.NewAuthorizer(resolver, evaluator)
	if err != nil {
		log.Fatal(err)
	}
	return authorizer
}

func mustContext(ctx context.Context, err error) context.Context {
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}

func mustDecision(decision scoped.Decision, err error) scoped.Decision {
	if err != nil {
		log.Fatal(err)
	}
	return decision
}
