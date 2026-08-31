package nethttp

import (
	"net/http"
	"strings"

	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
)

// TenantResolver 从标准库 HTTP 请求中解析当前 tenant。
type TenantResolver func(r *http.Request) (string, error)

// TrustedTenantHeaderResolver 从可信边界传入的 X-Tenant-ID 解析 tenant。
func TrustedTenantHeaderResolver(r *http.Request) (string, error) {
	if r == nil {
		return "", nil
	}
	rawTenantID := r.Header.Get(httpx.HeaderTenantID)
	tenantID := httpx.SanitizeIdentifierFromHeader(rawTenantID, 128)
	if strings.TrimSpace(rawTenantID) != "" && tenantID == "" {
		return "", errors.NewCode(errors.InvalidInput, "invalid tenant id")
	}
	return tenantID, nil
}

// TenantMiddleware 返回标准库 http.Handler 形态的租户提取中间件。
// resolver 负责解析可信 tenant；resolver 为 nil 时 fail-closed。
func TenantMiddleware(resolver TenantResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resolver == nil {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			tenantID, err := resolver(r)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}

			nextCtx, err := contextx.WithTenantID(r.Context(), tenantID)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
			if tenantID != "" {
				w.Header().Set(httpx.HeaderTenantID, tenantID)
			}
			next.ServeHTTP(w, r.WithContext(nextCtx))
		})
	}
}
