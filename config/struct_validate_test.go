package config

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestProviderLoad_RejectsRecursiveConfigType(t *testing.T) {
	type node struct {
		Next *node `yaml:"next" default:"{}"`
	}
	provider, err := NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	var target node
	if err := provider.Load("", &target); err == nil {
		t.Fatal("expected recursive configuration type to be rejected")
	}
}

func TestValidateTaggedStruct_TerminatesOnObjectCycle(t *testing.T) {
	type node struct {
		Value string `yaml:"value" validate:"required"`
		Next  *node  `yaml:"next"`
	}

	root := &node{Value: "ok"}
	root.Next = root
	if err := validateTaggedStruct(root); err != nil {
		t.Fatalf("validateTaggedStruct: %v", err)
	}
}

func TestProviderLoad_AllowsHiddenRecursiveType(t *testing.T) {
	type node struct {
		Next *node `config:"-" default:"{}"`
	}
	type root struct {
		Hidden *node `config:"-" default:"{}"`
	}

	provider, err := NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &root{}
	if err := provider.Load("", cfg); err != nil {
		t.Fatalf("Load hidden recursive type: %v", err)
	}
	if cfg.Hidden != nil {
		t.Fatal("hidden recursive pointer was allocated")
	}
}

func TestProviderLoad_ValidationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		tag   string
		err   string
	}{
		{"unknown", -1, "required,minn=1", "unknown validation rule"},
		{"optional unknown", 0, "omitempty,minn=1", "unknown validation rule"},
		{"optional pointer unknown", (*int)(nil), "omitempty,minn=1", "unknown validation rule"},
		{"unsigned boundary", uint64(9007199254740992), "max=9007199254740992", ""},
		{"unsigned precision", uint64(9007199254740993), "max=9007199254740992", "greater than maximum"},
		{"signed precision", int64(-9007199254740993), "min=-9007199254740992", "less than minimum"},
		{"max uint64", uint64(math.MaxUint64), "max=18446744073709551615", ""},
		{"min int64", int64(math.MinInt64), "min=-9223372036854775808", ""},
		{"invalid integer limit", 1, "min=abc", "invalid integer limit"},
		{"overflow limit", uint64(1), "max=18446744073709551616", "invalid unsigned integer limit"},
		{"negative unsigned limit", uint64(1), "min=-1", "invalid unsigned integer limit"},
		{"nil min", (*int)(nil), "min=1", "min rule only supports numeric fields"},
		{"nil max", (*int)(nil), "max=1", "max rule only supports numeric fields"},
		{"nil required", (*int)(nil), "required,min=1", "required configuration missing"},
		{"nil optional", (*int)(nil), "omitempty,min=1", ""},
		{"nil optional string", (*string)(nil), "omitempty,startswith=/", ""},
		{"zero optional", 0, "omitempty,min=1", ""},
		{"invalid optional rule", 0, "omitempty,min=bad", "invalid integer limit"},
		{"wrong optional type", "", "omitempty,min=1", "min rule only supports numeric fields"},
		{"empty oneof", "", "omitempty,oneof=", "oneof rule requires"},
		{"duration units", time.Second, "min=500ms,max=2s", ""},
		{"duration nanos", time.Second, "min=1000000000", ""},
		{"duration out of range", time.Second, "max=500ms", "greater than maximum"},
		{"duration invalid", time.Second, "max=bad", "invalid duration limit"},
		{"finite float", 1.5, "min=0.5,max=2.5", ""},
		{"float below min", 0.25, "min=0.5", "less than minimum"},
		{"NaN value", math.NaN(), "max=1", "invalid float value"},
		{"infinite value", math.Inf(1), "max=1", "invalid float value"},
		{"NaN limit", 42.0, "max=NaN", "invalid float limit"},
		{"infinite limit", 42.0, "max=+Inf", "invalid float limit"},
		{"negative infinite limit", 42.0, "min=-Inf", "invalid float limit"},
		{"optional NaN limit", 0.0, "omitempty,max=NaN", "invalid float limit"},
		{"integer NaN limit", 42, "max=NaN", "invalid float limit"},
		{"integer infinite limit", 42, "max=+Inf", "invalid float limit"},
		{"zero optional unknown rule", 0, "omitempty,unknown_rule=1", "unknown validation rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewProvider()
			require.NoError(t, err)
			target := reflect.New(reflect.StructOf([]reflect.StructField{{
				Name: "Value", Type: reflect.TypeOf(tc.value),
				Tag: reflect.StructTag(fmt.Sprintf(`yaml:"value" validate:%q`, tc.tag)),
			}}))
			target.Elem().Field(0).Set(reflect.ValueOf(tc.value))
			err = provider.Load("", target.Interface())
			if tc.err == "" {
				require.NoError(t, err)
			} else if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("Load error = %v, want %q", err, tc.err)
			}
		})
	}
}
