package config

import (
	"bufio"
	"os"
	"strings"

	"gochen/errors"
)

// DotEnvSource .env 配置文件源。
type DotEnvSource struct {
	prefix   string
	path     string
	optional bool
}

// DotEnvSourceOption .env 配置源选项。
type DotEnvSourceOption func(*DotEnvSource)

// NewDotEnvSource 创建 DotEnvSource。
func NewDotEnvSource(path string, opts ...DotEnvSourceOption) *DotEnvSource {
	s := &DotEnvSource{
		prefix:   "",
		path:     path,
		optional: false,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithDotEnvOptional 设置为可选配置源（文件不存在时不报错）。
func WithDotEnvOptional() DotEnvSourceOption {
	return func(s *DotEnvSource) {
		s.optional = true
	}
}

// WithDotEnvPrefix 设置前缀过滤并在写入配置键前移除该前缀。
func WithDotEnvPrefix(prefix string) DotEnvSourceOption {
	return func(s *DotEnvSource) {
		s.prefix = prefix
	}
}

// Load 加载 .env 数据并扁平化为配置键。
func (s *DotEnvSource) Load() (map[string]any, error) {
	rawEnv, err := parseDotEnvFile(s.path, s.optional)
	if err != nil {
		return nil, err
	}
	settings := make(map[string]any)
	for key, value := range rawEnv {
		if s.prefix != "" {
			if !strings.HasPrefix(key, s.prefix) {
				continue
			}
			key = strings.TrimPrefix(key, s.prefix)
			if key == "" {
				continue
			}
		}
		settings[envKeyToConfigKey(key)] = value
	}
	return settings, nil
}

func (s *DotEnvSource) Priority() int {
	return 75
}

func (s *DotEnvSource) Name() string {
	return "dotenv:" + s.path
}

func (s *DotEnvSource) environmentConfigPrefix() string {
	return s.prefix
}

// LoadDotEnvIntoEnv 将 .env 文件中的值填入进程环境中。
// 仅在目标环境变量当前为空时写入，保持显式环境变量优先。
func LoadDotEnvIntoEnv(path string, optional bool) error {
	rawEnv, err := parseDotEnvFile(path, optional)
	if err != nil {
		return err
	}
	for key, value := range rawEnv {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return errors.Wrap(err, errors.Dependency, "failed to set dotenv env").
				WithContext("env", key).
				WithContext("path", path)
		}
	}
	return nil
}

func parseDotEnvFile(path string, optional bool) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && optional {
			return map[string]string{}, nil
		}
		return nil, errors.Wrap(err, errors.Dependency, "failed to read dotenv file").
			WithContext("path", path)
	}
	defer file.Close()

	settings := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		value := strings.TrimSpace(parts[1])
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}
		settings[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "failed to parse dotenv file").
			WithContext("path", path)
	}
	return settings, nil
}

var _ IConfigSource = (*DotEnvSource)(nil)

// LoadDotEnv 返回 .env 文件键值，不写入进程环境。
func LoadDotEnv(path string, optional bool) (map[string]string, error) {
	return parseDotEnvFile(path, optional)
}
