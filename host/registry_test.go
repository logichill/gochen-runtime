package host_test

import (
	"testing"

	"gochen-runtime/host"
	"gochen/auth"
	"gochen/errors"
	"gochen/testkit/require"
)

type registryApp struct {
	descriptor auth.Descriptor
	allow      bool
}

func (a *registryApp) Descriptor() auth.Descriptor { return a.descriptor }
func (a *registryApp) UsesAllowAuthorizer() bool   { return a.allow }

func TestRegistryUnionsActionsAcrossApps(t *testing.T) {
	registry := host.NewRegistry()
	reader := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "list", Kind: auth.KindList}},
	}}
	writer := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "create", Kind: auth.KindCreate}},
	}}

	require.NoError(t, registry.Register(reader, writer))
	require.Equal(t, []string{"user"}, registry.Resources())

	actions := registry.Actions("user")
	require.Equal(t, 2, len(actions))
	require.Equal(t, auth.KindList, actions["list"].Kind)
	require.Equal(t, auth.KindCreate, actions["create"].Kind)
}

func TestRegistryAllowsIdenticalDuplicateAction(t *testing.T) {
	registry := host.NewRegistry()
	first := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead}},
	}}
	second := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead}},
	}}

	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))
	require.Equal(t, 1, len(registry.Actions("user")))
}

func TestRegistryRejectsActionKindConflict(t *testing.T) {
	registry := host.NewRegistry()
	first := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead}},
	}}
	second := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindUpdate}},
	}}

	require.NoError(t, registry.Register(first))
	err := registry.Register(second)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
}

func TestRegistryRejectsActionPermissionConflict(t *testing.T) {
	registry := host.NewRegistry()
	first := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead, Permission: "user:custom:read"}},
	}}
	second := &registryApp{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "read", Kind: auth.KindRead}},
	}}

	require.NoError(t, registry.Register(first))
	err := registry.Register(second)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
}

func TestRegistryTracksAllowResources(t *testing.T) {
	registry := host.NewRegistry()
	open := &registryApp{
		descriptor: auth.Descriptor{Resource: "audit_log", Actions: []auth.Action{{Name: "list", Kind: auth.KindList}}},
		allow:      true,
	}
	protected := &registryApp{
		descriptor: auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: "list", Kind: auth.KindList}}},
	}

	require.NoError(t, registry.Register(open, protected))
	require.Equal(t, []string{"audit_log"}, registry.AllowResources())
}

func TestRegistryRejectsInvalidInput(t *testing.T) {
	registry := host.NewRegistry()
	require.Error(t, registry.Register(nil))

	err := registry.Register(&registryApp{descriptor: auth.Descriptor{Resource: " "}})
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)

	var nilRegistry *host.Registry
	require.Error(t, nilRegistry.Register(&registryApp{}))
}

func TestRegistryFailedRegistrationLeavesStateUnchanged(t *testing.T) {
	registry := host.NewRegistry()
	reader := &registryApp{descriptor: auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: "read", Kind: auth.KindRead}}}}
	require.NoError(t, registry.Register(reader))
	failed := &registryApp{descriptor: auth.Descriptor{Resource: "user", Actions: []auth.Action{
		{Name: "create", Kind: auth.KindCreate},
		{Name: "read", Kind: auth.KindUpdate},
	}}}
	open := &registryApp{descriptor: auth.Descriptor{Resource: "item", Actions: []auth.Action{{Name: "list", Kind: auth.KindList}}}, allow: true}
	require.Error(t, registry.Register(open, failed))
	require.Equal(t, []string{"user"}, registry.Resources())
	require.Equal(t, 1, len(registry.Actions("user")))
	require.Equal(t, 0, len(registry.Actions("item")))
	require.Equal(t, 0, len(registry.AllowResources()))
	require.NoError(t, registry.Validate())
	// 失败的动作没有留下冲突状态，后续合法注册仍可成功。
	require.NoError(t, registry.Register(&registryApp{descriptor: auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: "create", Kind: auth.KindUpdate}}}}))
}
