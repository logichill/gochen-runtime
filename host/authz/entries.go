package authz

import (
	"context"
	"gochen-runtime/host/internal/runtimeutil"
	"strings"
	"sync"

	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

// IEntrySource 枚举命令、Worker 或 CLI 注册表中的实际入口。
// 在调用 ValidateEntries 前完成注册，SealEntries 后新增处理器通过新实例重新校验。
type IEntrySource interface {
	Entries() []auth.Entry
	SealEntries() error
}

// EntryRegistry 汇总声明绑定，并与驱动独立枚举的实际入口交叉核对。
type EntryRegistry struct {
	mu              sync.Mutex
	bindings        []auth.EntryBinding
	sources         []IEntrySource
	report          []auth.EntryStatus
	sealed          bool
	modulesPrepared bool
	validated       bool
}

// NewEntryRegistry 连接独立入口注册源。
func NewEntryRegistry(sources ...IEntrySource) *EntryRegistry {
	return &EntryRegistry{sources: append([]IEntrySource(nil), sources...)}
}

// Bind 冻结并登记入口执行绑定。
func (r *EntryRegistry) Bind(binding auth.EntryBinding) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "entry registry cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.NewCode(errors.FailedPrecondition, "entry registry is sealed")
	}
	r.bindings = append(r.bindings, binding.Copy())
	return nil
}

// BindHTTP 按方法与驱动规范路径登记 HTTP 入口。
func (r *EntryRegistry) BindHTTP(method, path string, binding auth.EntryBinding) error {
	binding.Entry = auth.Entry{Kind: "http", Key: strings.ToUpper(strings.TrimSpace(method)) + " " + path}
	return r.Bind(binding)
}

// ValidateEntries 保留三分类报告；检查通过后冻结实际注册源和声明绑定。
func (r *EntryRegistry) ValidateEntries(_ context.Context, server httpx.IServer) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "entry registry cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validated = false
	var actual []auth.Entry
	bindings := append([]auth.EntryBinding(nil), r.bindings...)
	var routes httpx.IRouteSnapshot
	if server != nil {
		var ok bool
		routes, ok = httpx.UnwrapServer(server).(httpx.IRouteSnapshot)
		if !ok {
			return errors.NewCode(errors.InvalidInput, "HTTP driver must expose its actual route registry")
		}
		if err := routes.SealRoutes(); err != nil {
			return err
		}
		if registry, ok := server.(httpx.IRouteRegistry); ok && len(registry.RouteConflicts()) > 0 {
			return errors.NewCode(errors.InvalidInput, "duplicate HTTP entries cannot be authorized")
		}
		for _, route := range routes.Routes() {
			entry := auth.Entry{Kind: "http", Key: route.Method + " " + route.Path}
			actual = append(actual, entry)
			if route.AllowReason != "" {
				bindings = append(bindings, auth.EntryBinding{Entry: entry, ResourceAccess: auth.ResourceAccessNone, Source: "HTTP driver protocol handler", AllowReason: route.AllowReason})
			}
			if route.AliasMethod != "" {
				for _, binding := range r.bindings {
					if binding.Entry == (auth.Entry{Kind: "http", Key: route.AliasMethod + " " + route.Path}) {
						binding.Entry = entry
						bindings = append(bindings, binding)
					}
				}
			}
		}
	}
	for _, source := range r.sources {
		if runtimeutil.IsTypedNil(source) {
			return errors.NewCode(errors.InvalidInput, "entry source cannot be nil")
		}
		if err := source.SealEntries(); err != nil {
			return err
		}
		actual = append(actual, source.Entries()...)
	}
	var err error
	r.report, err = auth.ValidateEntries(actual, bindings)
	if err != nil {
		return err
	}
	r.sealed = true
	r.validated = true
	return nil
}

// ActivateEntries 在所有门禁通过后启用实际后台注册源。
func (r *EntryRegistry) ActivateEntries() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.validated {
		return errors.NewCode(errors.FailedPrecondition, "entry registry has not passed validation")
	}
	for _, source := range r.sources {
		if activator, ok := source.(interface{ ActivateEntries() error }); ok {
			if err := activator.ActivateEntries(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Report 返回最近一次核对的独立诊断副本。
func (r *EntryRegistry) Report() []auth.EntryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]auth.EntryStatus(nil), r.report...)
	for i := range out {
		out[i].EntryBinding = out[i].EntryBinding.Copy()
	}
	return out
}
