package config

import (
	"os"
	"path/filepath"
	"strings"

	"gochen/errors"
	"gopkg.in/yaml.v3"
)

// YAMLSource YAML 配置文件源。
type YAMLSource struct {
	path     string
	optional bool
}

// YAMLSourceOption YAML 配置源选项。
type YAMLSourceOption func(*YAMLSource)

// NewYAMLSource 创建YAMLSource。
func NewYAMLSource(path string, opts ...YAMLSourceOption) *YAMLSource {
	s := &YAMLSource{
		path:     path,
		optional: false,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// WithOptional 设置为可选配置源（文件不存在时不报错）。
func WithOptional() YAMLSourceOption {
	return func(s *YAMLSource) {
		s.optional = true
	}
}

// Load 加载数据。
func (s *YAMLSource) Load() (map[string]any, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) && s.optional {
			return make(map[string]any), nil
		}
		return nil, errors.Wrap(err, errors.Dependency, "failed to read config file").
			WithContext("path", s.path)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "failed to parse config file").
			WithContext("path", s.path)
	}

	// 扁平化配置
	settings := make(map[string]any)
	flattenMap("", raw, settings)

	return settings, nil
}

// flattenMap 扁平化嵌套映射。
func flattenMap(prefix string, src map[string]any, dst map[string]any) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}

		switch val := v.(type) {
		case map[string]any:
			flattenMap(key, val, dst)
		default:
			dst[key] = v
		}
	}
}

func (s *YAMLSource) Priority() int {
	return 50 // YAML 文件优先级中等
}

// Name 返回名称。
//
// 说明：
// - Name 返回配置源名称。
func (s *YAMLSource) Name() string {
	return "yaml:" + s.path
}

// 接口断言。
var _ IConfigSource = (*YAMLSource)(nil)

// ResolveConfigFilePath 按“显式环境变量优先，其次候选路径”的顺序解析配置文件路径。
func ResolveConfigFilePath(envKey string, candidates ...string) (string, error) {
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		path := filepath.Clean(raw)
		if _, err := os.Stat(path); err != nil {
			return "", errors.Wrap(err, errors.Dependency, "failed to resolve config file").
				WithContext("env", envKey).
				WithContext("path", path)
		}
		return path, nil
	}

	for _, candidate := range candidates {
		path := filepath.Clean(strings.TrimSpace(candidate))
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", errors.Wrap(err, errors.Dependency, "failed to resolve config file").
				WithContext("env", envKey).
				WithContext("path", path)
		}
	}

	return "", errors.NewCode(errors.Dependency, "config file not found").
		WithContext("env", envKey).
		WithContext("candidates", append([]string(nil), candidates...))
}
