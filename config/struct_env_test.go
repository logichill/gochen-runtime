package config

import (
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func loadProcessEnvironmentForTest(t *testing.T, target any) error {
	t.Helper()
	provider, err := NewProvider(WithEnvSource(""))
	require.NoError(t, err)
	return provider.Load("", target)
}

func TestProviderLoad_BindsEnvironmentTags(t *testing.T) {
	type childConfig struct {
		TableName string        `yaml:"table_name" env:"EVENT_STORE_TABLE"`
		TTL       time.Duration `yaml:"ttl"`
		Enabled   bool          `yaml:"enabled"`
		Headers   []string      `yaml:"headers"`
	}
	type rootConfig struct {
		Logging struct {
			Level string `yaml:"level"`
		} `yaml:"logging" envprefix:"LOG"`
		App struct {
			Environment string `yaml:"environment" env:"APP_ENV"`
		} `yaml:"app"`
		Event struct {
			Store childConfig `yaml:"store"`
		} `yaml:"event"`
	}

	cfg := &rootConfig{}
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("APP_ENV", "production")
	t.Setenv("EVENT_STORE_TABLE", "events")
	t.Setenv("EVENT_STORE_TTL", "5m")
	t.Setenv("EVENT_STORE_ENABLED", "true")
	t.Setenv("EVENT_STORE_HEADERS", "A,B")

	require.NoError(t, loadProcessEnvironmentForTest(t, cfg))

	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "production", cfg.App.Environment)
	assert.Equal(t, "events", cfg.Event.Store.TableName)
	assert.Equal(t, 5*time.Minute, cfg.Event.Store.TTL)
	assert.True(t, cfg.Event.Store.Enabled)
	assert.Equal(t, []string{"A", "B"}, cfg.Event.Store.Headers)
}

func TestProviderLoad_BindsEnvironmentScalarPointers(t *testing.T) {
	type rootConfig struct {
		Limit   *int           `yaml:"limit"`
		Timeout *time.Duration `yaml:"timeout"`
		Ratio   *float64       `yaml:"ratio"`
	}

	cfg := &rootConfig{}
	t.Setenv("LIMIT", "10")
	t.Setenv("TIMEOUT", "2s")
	t.Setenv("RATIO", "0.75")

	require.NoError(t, loadProcessEnvironmentForTest(t, cfg))

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

func TestProviderLoad_RejectsNonFiniteEnvironmentFloat(t *testing.T) {
	type rootConfig struct {
		Ratio       float64  `yaml:"ratio"`
		PointerRate *float64 `yaml:"pointer_rate"`
	}

	cfg := &rootConfig{Ratio: 0.5}
	t.Setenv("RATIO", "NaN")
	t.Setenv("POINTER_RATE", "-Inf")

	err := loadProcessEnvironmentForTest(t, cfg)
	require.Error(t, err)
	assert.Equal(t, 0.5, cfg.Ratio)
	assert.Nil(t, cfg.PointerRate)
}

func TestProviderLoad_BindsEnvironmentNamedStringSlice(t *testing.T) {
	type headerName string
	type headerList []headerName
	type stringList []string
	type rootConfig struct {
		Headers    stringList `yaml:"headers"`
		HeaderKeys headerList `yaml:"header_keys"`
	}

	cfg := &rootConfig{}
	t.Setenv("HEADERS", "A,B")
	t.Setenv("HEADER_KEYS", "Trace-ID,Request-ID")

	require.NoError(t, loadProcessEnvironmentForTest(t, cfg))

	assert.Equal(t, stringList{"A", "B"}, cfg.Headers)
	assert.Equal(t, headerList{"Trace-ID", "Request-ID"}, cfg.HeaderKeys)
}

func TestProviderLoad_BindsEnvironmentNumericSlices(t *testing.T) {
	type intList []int
	type rootConfig struct {
		Ports   intList   `yaml:"ports"`
		Weights []float64 `yaml:"weights"`
	}

	cfg := &rootConfig{}
	t.Setenv("PORTS", "8080,8081")
	t.Setenv("WEIGHTS", "0.25,0.75")

	require.NoError(t, loadProcessEnvironmentForTest(t, cfg))

	assert.Equal(t, intList{8080, 8081}, cfg.Ports)
	assert.Equal(t, []float64{0.25, 0.75}, cfg.Weights)
}

func TestProviderLoad_InvalidEnvironmentNumericSlicesReturnError(t *testing.T) {
	type rootConfig struct {
		Ports   []int     `yaml:"ports"`
		Weights []float64 `yaml:"weights"`
	}

	cfg := &rootConfig{
		Ports:   []int{7000},
		Weights: []float64{0.5},
	}
	t.Setenv("PORTS", "8080,abc,8081")
	t.Setenv("WEIGHTS", "0.25,NaN,0.75")

	err := loadProcessEnvironmentForTest(t, cfg)
	require.Error(t, err)
	assert.Equal(t, []int{7000}, cfg.Ports)
	assert.Equal(t, []float64{0.5}, cfg.Weights)
}
