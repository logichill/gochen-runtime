package config

import (
	"os"
	"path/filepath"
	"testing"

	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func TestResolveConfigFilePath_UsesEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "custom.yaml")
	require.NoError(t, os.WriteFile(envPath, []byte("name: env\n"), 0o644))

	t.Setenv("CONFIG_FILE", envPath)

	got, err := ResolveConfigFilePath("CONFIG_FILE", "config.yaml")
	require.NoError(t, err)
	assert.Equal(t, envPath, got)
}

func TestResolveConfigFilePath_UsesFirstExistingCandidate(t *testing.T) {
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWD) }()
	require.NoError(t, os.Chdir(tmpDir))

	require.NoError(t, os.WriteFile("config.yaml", []byte("name: local\n"), 0o644))

	got, err := ResolveConfigFilePath("CONFIG_FILE", "missing.yaml", "config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "config.yaml", got)
}

func TestResolveConfigFilePath_ReturnsErrorWhenMissing(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")

	_, err := ResolveConfigFilePath("CONFIG_FILE", "missing.yaml")
	require.Error(t, err)
}

func TestProviderLoad_YAMLSource(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("app:\n  name: demo\n"), 0o644))

	var cfg struct {
		App struct {
			Name string `yaml:"name"`
		} `yaml:"app"`
	}

	provider, err := NewProvider(WithYAMLSource(path))
	require.NoError(t, err)
	err = provider.Load("", &cfg)
	require.NoError(t, err)
	assert.Equal(t, "demo", cfg.App.Name)
}
