package config

import (
	"os"
	"strings"

	"gochen/errors"
)

// EnvSource 环境变量配置源。
type EnvSource struct {
	prefix      string
	ignoreEmpty bool
}

// EnvSourceOption 环境变量配置源选项。
type EnvSourceOption func(*EnvSource)

func envKeyToConfigKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", ".")
	return k
}

// NewEnvSource 创建EnvSource。
func NewEnvSource(prefix string, opts ...EnvSourceOption) *EnvSource {
	source := &EnvSource{prefix: prefix}
	for _, opt := range opts {
		opt(source)
	}
	return source
}

// WithEnvIgnoreEmpty 忽略值为空的进程环境变量。
func WithEnvIgnoreEmpty() EnvSourceOption {
	return func(source *EnvSource) {
		source.ignoreEmpty = true
	}
}

// Load 加载数据。
func (s *EnvSource) Load() (map[string]any, error) {
	settings := make(map[string]any)

	// First pass: apply direct env vars (FOO=...) to settings.
	// Second pass: apply *_FILE vars (FOO_FILE=/path) only if FOO isn't set.
	fileVars := make(map[string]string)

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]
		if s.ignoreEmpty && value == "" {
			continue
		}

		// 检查前缀
		if s.prefix != "" && !strings.HasPrefix(key, s.prefix) {
			continue
		}

		// 移除前缀
		key = strings.TrimPrefix(key, s.prefix)
		if strings.HasSuffix(key, "_FILE") {
			fileVars[key] = value
			continue
		}

		settings[envKeyToConfigKey(key)] = value
	}

	for key, path := range fileVars {
		base := strings.TrimSuffix(key, "_FILE")
		if base == "" {
			continue
		}

		cfgKey := envKeyToConfigKey(base)
		if _, exists := settings[cfgKey]; exists {
			// Explicit FOO wins over FOO_FILE.
			continue
		}

		b, err := os.ReadFile(path)
		if err != nil {
			envVar := key
			if s.prefix != "" {
				envVar = s.prefix + key
			}
			return nil, errors.Wrap(err, errors.Dependency, "failed to read env file").
				WithContext("env", envVar).
				WithContext("path", path)
		}
		// Common secret file format has trailing newline; strip CR/LF only.
		v := strings.TrimRight(string(b), "\r\n")
		settings[cfgKey] = v
	}

	return settings, nil
}

func (s *EnvSource) Priority() int {
	return 100 // 环境变量优先级最高
}

// Name 返回名称。
//
// 说明：
// - Name 返回配置源名称。
func (s *EnvSource) Name() string {
	return "env"
}

func (s *EnvSource) environmentConfigPrefix() string {
	return s.prefix
}

// 接口断言。
var _ IConfigSource = (*EnvSource)(nil)
