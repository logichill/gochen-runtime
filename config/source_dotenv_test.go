package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDotEnvSource_Load(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nAPP_NAME=demo-app\nLOG_LEVEL='debug'\nAUTH_SECRET=\"abc123\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	source := NewDotEnvSource(path)
	settings, err := source.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := settings["app.name"]; got != "demo-app" {
		t.Fatalf("app.name = %#v, want demo-app", got)
	}
	if got := settings["log.level"]; got != "debug" {
		t.Fatalf("log.level = %#v, want debug", got)
	}
	if got := settings["auth.secret"]; got != "abc123" {
		t.Fatalf("auth.secret = %#v, want abc123", got)
	}
}

func TestDotEnvSource_LoadOptionalMissing(t *testing.T) {
	source := NewDotEnvSource(filepath.Join(t.TempDir(), ".env"), WithDotEnvOptional())
	settings, err := source.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(settings) != 0 {
		t.Fatalf("expected empty settings, got %#v", settings)
	}
}

func TestDotEnvSource_LoadWithPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "ERP_HTTP_HOST=127.0.0.1\nAUTH_SECRET=ignored\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	source := NewDotEnvSource(path, WithDotEnvPrefix("ERP_"))
	settings, err := source.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := settings["http.host"]; got != "127.0.0.1" {
		t.Fatalf("http.host = %#v, want 127.0.0.1", got)
	}
	if _, exists := settings["auth.secret"]; exists {
		t.Fatal("expected AUTH_SECRET to be filtered by prefix")
	}
}

func TestLoadDotEnvIntoEnv_PreservesExplicitEmptyValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "APP_NAME=from-dotenv\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	t.Setenv("APP_NAME", "")
	if err := LoadDotEnvIntoEnv(path, false); err != nil {
		t.Fatalf("LoadDotEnvIntoEnv() error = %v", err)
	}
	if got, exists := os.LookupEnv("APP_NAME"); !exists || got != "" {
		t.Fatalf("APP_NAME = (%q, %v), want empty explicit env", got, exists)
	}
}
