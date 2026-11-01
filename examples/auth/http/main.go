// 演示 HTTP 路由级 Action 快速拦截（Fast-Deny）。
//
// 重要定位：本中间件**不是安全边界**。
// 真正的授权发生在 Application 层（`auth.IAuthorizer`），
// 因此绕过 HTTP 直接调用 Application 同样会被拦截。
// 这里只是把"明显无权"的请求在进入业务编排前挡掉，省掉后续开销。
package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	restaction "gochen-runtime/api/rest/action"
	"gochen-runtime/http/nethttp"

	authaction "gochen/auth/action"
	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
)

const reportReadAction = "report:api:read"

// grantChecker 是一个最小 IActionChecker：按 operator 查它被授予的动作码。
type grantChecker struct {
	grants map[string][]string
}

func (c *grantChecker) RequireAction(ctx context.Context, code string) error {
	operator := contextx.Operator(ctx)
	if operator == "" {
		return errors.NewCode(errors.Unauthorized, "operator is required").
			WithContext("action", code)
	}
	for _, granted := range c.grants[operator] {
		if authaction.PatternMatches(granted, code) {
			return nil
		}
	}
	return errors.NewCode(errors.Forbidden, "action denied").
		WithContext("operator", operator).
		WithContext("action", code)
}

func main() {
	checker := &grantChecker{grants: map[string][]string{
		"analyst": {"report:api:read"},
		"intern":  {"report:api:list"},
	}}

	server := nethttp.NewServer(nil)
	group := server.Group("/api")
	group.Use(demoAuthentication())
	fastDeny, err := restaction.FastDeny(checker, reportReadAction)
	if err != nil {
		log.Fatal(err)
	}
	group.Use(fastDeny)
	group.GET("/reports", func(ctx httpx.IContext) error {
		operator := contextx.Operator(ctx.RequestContext())
		return ctx.String(http.StatusOK, "operator="+operator)
	})

	// analyst 有 report:api:read → 放行
	allowed := do(server, "analyst")
	if allowed.Code != http.StatusOK {
		log.Fatalf("authorized response: status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	// intern 只有 report:api:list → 在中间件层就被挡掉
	denied := do(server, "intern")
	if denied.Code != http.StatusForbidden {
		log.Fatalf("denied response: status=%d body=%s", denied.Code, denied.Body.String())
	}

	// 匿名请求 → 未认证
	anonymous := do(server, "")
	if anonymous.Code != http.StatusUnauthorized {
		log.Fatalf("anonymous response: status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}

	log.Printf("fast-deny: analyst=%d %q; intern=%d; anonymous=%d",
		allowed.Code, strings.TrimSpace(allowed.Body.String()), denied.Code, anonymous.Code)
}

func do(server *nethttp.Server, operator string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/reports", nil)
	if operator != "" {
		request.Header.Set("X-Operator", operator)
	}
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// demoAuthentication 模拟 Transport 的 AuthN 中间件：
// 从 Header 解析身份并写入 contextx，安全层只消费不解析。
func demoAuthentication() httpx.Middleware {
	return func(ctx httpx.IContext, next func() error) error {
		operator := strings.TrimSpace(ctx.Header("X-Operator"))
		if operator == "" {
			return next()
		}
		requestContext := ctx.RequestContext()
		if requestContext == nil {
			return errors.NewCode(errors.Internal, "request context is missing")
		}
		bound, err := contextx.WithOperator(requestContext, operator)
		if err != nil {
			return err
		}
		ctx.SetContext(requestContext.WithContext(bound))
		return next()
	}
}
