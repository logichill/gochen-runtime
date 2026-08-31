package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func TestProviderLoad_SourcePriority(t *testing.T) {
	type appConfig struct {
		Name string `yaml:"name" default:"default"`
	}
	type rootConfig struct {
		App appConfig `yaml:"app"`
	}

	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	dotenvPath := filepath.Join(tmpDir, ".env")
	require.NoError(t, os.WriteFile(yamlPath, []byte("app:\n  name: yaml\n"), 0o644))
	require.NoError(t, os.WriteFile(dotenvPath, []byte("PIPE_APP_NAME=dotenv\n"), 0o644))

	loadName := func(options ...ProviderOption) string {
		t.Helper()
		provider, err := NewProvider(options...)
		require.NoError(t, err)
		var cfg rootConfig
		require.NoError(t, provider.Load("", &cfg))
		return cfg.App.Name
	}

	assert.Equal(t, "default", loadName())
	assert.Equal(t, "yaml", loadName(WithYAMLSource(yamlPath)))
	assert.Equal(t, "dotenv", loadName(
		WithYAMLSource(yamlPath),
		WithDotEnvSource(dotenvPath, WithDotEnvPrefix("PIPE_")),
	))

	t.Setenv("PIPE_APP_NAME", "process")
	assert.Equal(t, "process", loadName(
		WithYAMLSource(yamlPath),
		WithDotEnvSource(dotenvPath, WithDotEnvPrefix("PIPE_")),
		WithEnvSource("PIPE_"),
	))
}

func TestProviderLoad_RunsEachStageOnce(t *testing.T) {
	type rootConfig struct {
		Mode string `yaml:"mode" default:"DEBUG" validate:"oneof=debug release"`
	}

	provider, err := NewProvider()
	require.NoError(t, err)
	postBindCalls := 0
	validationCalls := 0
	var cfg rootConfig
	require.NoError(t, provider.Load("", &cfg,
		WithPostBind(func(target any) error {
			postBindCalls++
			target.(*rootConfig).Mode = strings.ToLower(target.(*rootConfig).Mode)
			return nil
		}),
		WithValidation(func(target any) error {
			validationCalls++
			assert.Equal(t, "debug", target.(*rootConfig).Mode)
			return nil
		}),
	))

	assert.Equal(t, "debug", cfg.Mode)
	assert.Equal(t, 1, postBindCalls)
	assert.Equal(t, 1, validationCalls)
}

func TestProviderLoad_BindsPointerStructAndMap(t *testing.T) {
	type credentials struct {
		Token string `yaml:"token"`
	}
	type serviceConfig struct {
		Credentials *credentials      `yaml:"credentials"`
		Labels      map[string]string `yaml:"labels"`
	}
	type rootConfig struct {
		Service serviceConfig `yaml:"service"`
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`service:
  credentials:
    token: secret
  labels:
    environment: test
    region: cn
`), 0o644))
	provider, err := NewProvider(WithYAMLSource(path))
	require.NoError(t, err)

	var cfg rootConfig
	require.NoError(t, provider.Load("", &cfg))
	if assert.NotNil(t, cfg.Service.Credentials) {
		assert.Equal(t, "secret", cfg.Service.Credentials.Token)
	}
	assert.Equal(t, map[string]string{"environment": "test", "region": "cn"}, cfg.Service.Labels)
}

func TestProviderLoad_AppliesDefaultsToPointerStructCreatedBySourceBinding(t *testing.T) {
	type credentials struct {
		Token   string        `yaml:"token"`
		Timeout time.Duration `yaml:"timeout" default:"30s"`
	}
	type rootConfig struct {
		Credentials *credentials `yaml:"credentials"`
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("credentials:\n  token: secret\n"), 0o644))
	provider, err := NewProvider(WithYAMLSource(path))
	require.NoError(t, err)

	var cfg rootConfig
	require.NoError(t, provider.Load("", &cfg))
	if assert.NotNil(t, cfg.Credentials) {
		assert.Equal(t, "secret", cfg.Credentials.Token)
		assert.Equal(t, 30*time.Second, cfg.Credentials.Timeout)
	}
}

func TestProviderLoad_CanRequireExplicitEnvironmentTags(t *testing.T) {
	type databaseConfig struct {
		Host string `yaml:"host"`
	}
	type rootConfig struct {
		Name     string         `yaml:"name"`
		Secret   string         `yaml:"secret" env:"CUSTOM_SECRET"`
		Database databaseConfig `yaml:"database" envprefix:"DB"`
		Hidden   struct {
			Value string `yaml:"value"`
		} `yaml:"hidden" env:"-"`
	}

	t.Setenv("NAME", "implicit")
	t.Setenv("CUSTOM_SECRET", "secret")
	t.Setenv("DB_HOST", "database")
	t.Setenv("HIDDEN_VALUE", "hidden")
	provider, err := NewProvider(WithEnvSource(""))
	require.NoError(t, err)

	var cfg rootConfig
	require.NoError(t, provider.Load("", &cfg, WithImplicitEnvironment(false)))
	assert.Empty(t, cfg.Name)
	assert.Equal(t, "secret", cfg.Secret)
	assert.Equal(t, "database", cfg.Database.Host)
	assert.Empty(t, cfg.Hidden.Value)
}
