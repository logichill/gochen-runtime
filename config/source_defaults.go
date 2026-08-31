package config

// DefaultsSource 默认值配置源。
type DefaultsSource struct {
	defaults map[string]any
}

// NewDefaultsSource 创建DefaultsSource。
func NewDefaultsSource(defaults map[string]any) *DefaultsSource {
	return &DefaultsSource{defaults: defaults}
}

// Load 加载数据。
func (s *DefaultsSource) Load() (map[string]any, error) {
	settings := make(map[string]any, len(s.defaults))

	// 扁平化默认值
	flattenMap("", s.defaults, settings)

	return settings, nil
}

func (s *DefaultsSource) Priority() int {
	return 0 // 默认值优先级最低
}

// Name 返回名称。
//
// 说明：
// - Name 返回配置源名称。
func (s *DefaultsSource) Name() string {
	return "defaults"
}

// 接口断言。
var _ IConfigSource = (*DefaultsSource)(nil)
