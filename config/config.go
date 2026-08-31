// Package config 提供统一的配置管理抽象。
//
// 设计目标：
//   - 统一配置加载、验证、环境隔离能力。
//   - 支持多配置源（环境变量、YAML文件、代码默认值）
//   - 类型安全的配置获取。
//   - 可组合的配置验证。
//
// 配置优先级（从高到低）：
//   - 环境变量 (Priority: 100)
//   - 配置文件 (Priority: 50)
//   - 代码默认值 (Priority: 0)
//
// 使用示例：
//
//	// 创建配置提供者。
//	provider, err := config.NewProvider(
//	    config.WithEnvSource("APP_"),
//	    config.WithYAMLSource("config.yaml"),
//	)
//
//	// 类型安全的获取。
//	port := provider.GetInt("server.port", 8080)
//	timeout := provider.GetDuration("server.timeout", 30*time.Second)
//
//	// 加载到结构体（默认值、来源绑定与校验在同一条链路执行）。
//	var cfg ServerConfig
//	if err := provider.Load("server", &cfg); err != nil {
//	    return err
//	}
package config

import (
	"gochen/errors"
	"strings"
	"time"
)

// IConfigProvider 配置提供者接口。
//
// 提供类型安全的配置读取能力。结构体绑定由具体 Provider 的 Load 方法提供，
// 避免把含实现私有选项状态的签名暴露给第三方 provider 实现者。
type IConfigProvider interface {
	// Get 获取配置值（原始类型）
	//
	// key 使用点分隔的路径格式，如 "server.port"
	Get(key string) any

	// GetString 获取字符串配置
	//
	// 如果配置不存在或类型不匹配，返回 defaultValue
	GetString(key string, defaultValue string) string

	// GetStringStrict 获取字符串配置（严格模式）
	//
	// - key 缺失：返回 Validation error（missing configuration）
	// - 类型不匹配：返回 Validation error（invalid configuration）
	GetStringStrict(key string) (string, error)

	// GetInt 获取整数配置
	//
	// 如果配置不存在或类型不匹配，返回 defaultValue
	GetInt(key string, defaultValue int) int

	// GetIntStrict 获取整数配置（严格模式）
	//
	// - key 缺失：返回 Validation error（missing configuration）
	// - 类型不匹配/解析失败：返回 Validation error（invalid configuration）
	GetIntStrict(key string) (int, error)

	// GetInt64 获取 int64 配置
	GetInt64(key string, defaultValue int64) int64

	// GetFloat64 获取浮点数配置
	GetFloat64(key string, defaultValue float64) float64

	// GetBool 获取布尔配置
	GetBool(key string, defaultValue bool) bool

	// GetBoolStrict 获取布尔配置（严格模式）
	//
	// - key 缺失：返回 Validation error（missing configuration）
	// - 类型不匹配/解析失败：返回 Validation error（invalid configuration）
	GetBoolStrict(key string) (bool, error)

	// GetDuration 获取时间间隔配置
	//
	// 支持 Go 标准时间格式，如 "30s", "5m", "1h"
	GetDuration(key string, defaultValue time.Duration) time.Duration

	// GetDurationStrict 获取时间间隔配置（严格模式）
	//
	// - key 缺失：返回 Validation error（missing configuration）
	// - 类型不匹配/解析失败：返回 Validation error（invalid configuration）
	GetDurationStrict(key string) (time.Duration, error)

	// GetStringSlice 获取字符串切片配置
	//
	// 支持：
	// - []string
	// - []any（元素会转换为 string）
	// - 逗号分隔字符串（会 TrimSpace 且忽略空元素）
	GetStringSlice(key string, defaultValue []string) []string

	// GetStringMap 获取字符串映射配置
	GetStringMap(key string, defaultValue map[string]string) map[string]string

	// Has 检查配置是否存在
	Has(key string) bool

	// ExportSettings 导出所有配置（用于调试）
	ExportSettings() map[string]any
}

// IConfigSource 配置源接口。
//
// 支持从不同来源加载配置，如环境变量、文件等。
type IConfigSource interface {
	// Load 加载配置
	//
	// 返回扁平化的配置映射，key 使用点分隔格式
	Load() (map[string]any, error)

	// Priority 配置源优先级
	//
	// 数字越大优先级越高，高优先级覆盖低优先级
	Priority() int

	// Name 配置源名称（用于日志和调试）
	Name() string
}

// ValidationError 配置验证错误。
type ValidationError struct {
	Key     string // 配置键
	Message string // 错误消息
}

func (e *ValidationError) Error() string {
	return e.Key + ": " + e.Message
}

func (e *ValidationError) ErrorCode() errors.ErrorCode {
	return errors.Validation
}

// ValidationErrors 多个验证错误。
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	var sb strings.Builder
	sb.WriteString("configuration validation failed:\n")
	for _, err := range e {
		sb.WriteString("  - ")
		sb.WriteString(err.Error())
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (e ValidationErrors) ErrorCode() errors.ErrorCode {
	return errors.Validation
}
