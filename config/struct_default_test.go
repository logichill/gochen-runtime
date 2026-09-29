package config

import (
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func loadDefaultsForTest(t *testing.T, target any) {
	t.Helper()
	provider, err := NewProvider()
	require.NoError(t, err)
	require.NoError(t, provider.Load("", target))
}

func TestProviderLoad_AppliesTagDefaults(t *testing.T) {
	type nested struct {
		Mode    string        `default:"release"`
		Timeout time.Duration `default:"30s"`
		Headers []string      `default:"A,B"`
	}
	type root struct {
		Name   string  `default:"demo"`
		Port   int     `default:"8080"`
		Debug  bool    `default:"true"`
		Nested *nested `default:"{}"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.Equal(t, "demo", cfg.Name)
	assert.Equal(t, 8080, cfg.Port)
	assert.True(t, cfg.Debug)
	if assert.NotNil(t, cfg.Nested) {
		assert.Equal(t, "release", cfg.Nested.Mode)
		assert.Equal(t, 30*time.Second, cfg.Nested.Timeout)
		assert.Equal(t, []string{"A", "B"}, cfg.Nested.Headers)
	}
}

func TestProviderLoad_TagDefaultsDoNotOverrideExistingValue(t *testing.T) {
	type nested struct {
		Mode string `default:"release"`
	}
	type root struct {
		Name   string  `default:"demo"`
		Nested *nested `default:"{}"`
	}

	cfg := &root{
		Name:   "custom",
		Nested: &nested{Mode: "debug"},
	}
	loadDefaultsForTest(t, cfg)

	assert.Equal(t, "custom", cfg.Name)
	assert.Equal(t, "debug", cfg.Nested.Mode)
}

func TestProviderLoad_TagDefaultEmptyStringSlice(t *testing.T) {
	type root struct {
		Headers []string `default:"[]"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.NotNil(t, cfg.Headers)
	assert.Empty(t, cfg.Headers)
}

func TestProviderLoad_TagDefaultNamedStringSlice(t *testing.T) {
	type headerName string
	type headerList []headerName
	type stringList []string
	type root struct {
		Headers    stringList `default:"A,B"`
		HeaderKeys headerList `default:"Trace-ID,Request-ID"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.Equal(t, stringList{"A", "B"}, cfg.Headers)
	assert.Equal(t, headerList{"Trace-ID", "Request-ID"}, cfg.HeaderKeys)
}

func TestProviderLoad_TagDefaultNumericSlices(t *testing.T) {
	type intList []int
	type root struct {
		Ports        intList   `default:"8080,8081"`
		Weights      []float64 `default:"0.25,0.75"`
		EmptyWeights []float64 `default:"[]"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.Equal(t, intList{8080, 8081}, cfg.Ports)
	assert.Equal(t, []float64{0.25, 0.75}, cfg.Weights)
	assert.NotNil(t, cfg.EmptyWeights)
	assert.Empty(t, cfg.EmptyWeights)
}

func TestProviderLoad_InvalidTagDefaultNumericSlicesKeepZeroValue(t *testing.T) {
	type root struct {
		Ports   []int     `default:"8080,abc,8081"`
		Weights []float64 `default:"0.25,+Inf,0.75"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.Nil(t, cfg.Ports)
	assert.Nil(t, cfg.Weights)
}

func TestProviderLoad_TagDefaultScalarPointers(t *testing.T) {
	type root struct {
		Limit   *int           `default:"10"`
		Timeout *time.Duration `default:"2s"`
		Ratio   *float64       `default:"0.75"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	if assert.NotNil(t, cfg.Limit) {
		assert.Equal(t, 10, *cfg.Limit)
	}
	if assert.NotNil(t, cfg.Timeout) {
		assert.Equal(t, 2*time.Second, *cfg.Timeout)
	}
	if assert.NotNil(t, cfg.Ratio) {
		assert.Equal(t, 0.75, *cfg.Ratio)
	}
}

func TestProviderLoad_TagDefaultsRejectNonFiniteFloat(t *testing.T) {
	type root struct {
		Ratio       float64  `default:"NaN"`
		PointerRate *float64 `default:"+Inf"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)

	assert.Zero(t, cfg.Ratio)
	assert.Nil(t, cfg.PointerRate)
}

func TestProviderLoad_SkipsHiddenRecursiveDefaults(t *testing.T) {
	type node struct {
		Next  *node  `config:"-" default:"{}"`
		Value string `config:"-" default:"hidden"`
	}
	type child struct {
		Value string `yaml:"value" default:"child"`
	}
	type root struct {
		Hidden *node `config:"-" default:"{}"`
		Left   child `yaml:"left"`
		Right  child `yaml:"right"`
	}

	cfg := &root{}
	loadDefaultsForTest(t, cfg)
	assert.Nil(t, cfg.Hidden)
	assert.Equal(t, "child", cfg.Left.Value)
	assert.Equal(t, "child", cfg.Right.Value)
}
