package config

import (
	"testing"

	"gochen/httpx"
)

func TestBuild_DerivesSessionDefaultFromSecurityLayer(t *testing.T) {
	web := Build(WithSecurityLayer(httpx.SecurityLayerWeb))
	if web.AllowSession == nil || !*web.AllowSession {
		t.Fatal("expected Web security layer to allow sessions by default")
	}

	api := Build(WithSecurityLayer(httpx.SecurityLayerAPI))
	if api.AllowSession == nil || *api.AllowSession {
		t.Fatal("expected API security layer to deny sessions by default")
	}
}

func TestBuild_ExplicitSessionSettingOverridesSecurityLayer(t *testing.T) {
	cfg := Build(
		WithAllowSession(false),
		WithSecurityLayer(httpx.SecurityLayerWeb),
	)
	if cfg.AllowSession == nil || *cfg.AllowSession {
		t.Fatal("expected explicit session setting to override Web default")
	}
}

func TestWithModuleHTTPDefaultsAppliesToAllModuleGroups(t *testing.T) {
	middleware := func(httpx.IContext, func() error) error { return nil }
	cfg := Build(WithModuleHTTPDefaults(ModuleHTTPConfig{Middlewares: []httpx.Middleware{middleware}}))

	if len(cfg.ModuleHTTPDefaults.Middlewares) != 1 || cfg.ModuleHTTPDefaults.Middlewares[0] == nil {
		t.Fatalf("expected one default module middleware, got %#v", cfg.ModuleHTTPDefaults.Middlewares)
	}
}
