package config

import (
	"testing"

	"gochen/testkit/require"
)

func TestProviderLoad_ValidatesTags(t *testing.T) {
	type nested struct {
		Mode string `yaml:"mode" validate:"required,oneof=debug release test"`
	}
	type root struct {
		Port   int    `yaml:"port" validate:"min=1,max=65535"`
		Path   string `yaml:"path" validate:"required,startswith=/,nospace"`
		Nested nested `yaml:"nested"`
	}

	provider, err := NewProvider()
	require.NoError(t, err)

	valid := &root{
		Port: 8080,
		Path: "/api/v1",
		Nested: nested{
			Mode: "debug",
		},
	}
	require.NoError(t, provider.Load("", valid))

	invalid := &root{
		Port: 0,
		Path: "bad path",
		Nested: nested{
			Mode: "broken",
		},
	}
	err = provider.Load("", invalid)
	require.Error(t, err)
}
