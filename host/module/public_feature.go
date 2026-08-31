package module

import (
	"sort"
	"strings"

	"gochen/errors"
)

// PublicFeature 表示模块允许在登录前公开的部署能力。
//
// Key 只能使用小写字母、数字、点、短横线和下划线；Value 是非空字符串，
// 不得用该结构发布密钥、连接信息或其他敏感运行配置。
type PublicFeature struct {
	Key   string
	Value string
}

// IPublicFeatureProvider 允许模块声明登录前可公开的受限部署能力。
type IPublicFeatureProvider interface {
	PublicFeatures() []PublicFeature
}

func normalizePublicFeatures(features []PublicFeature) ([]PublicFeature, error) {
	if len(features) == 0 {
		return nil, nil
	}

	values := make(map[string]string, len(features))
	for index, feature := range features {
		key := strings.TrimSpace(feature.Key)
		if err := validatePublicFeatureKey(key); err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "invalid public feature key").WithContext("index", index)
		}
		value := strings.TrimSpace(feature.Value)
		if value == "" {
			return nil, errors.NewCode(errors.InvalidInput, "public feature value cannot be empty").
				WithContext("index", index).
				WithContext("key", key)
		}
		if _, exists := values[key]; exists {
			return nil, errors.NewCode(errors.Conflict, "duplicate public feature key").WithContext("key", key)
		}
		values[key] = value
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	normalized := make([]PublicFeature, 0, len(keys))
	for _, key := range keys {
		normalized = append(normalized, PublicFeature{Key: key, Value: values[key]})
	}
	return normalized, nil
}

func validatePublicFeatureKey(key string) error {
	if key == "" {
		return errors.NewCode(errors.InvalidInput, "public feature key cannot be empty")
	}
	for index, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && index > 0:
		case (r == '.' || r == '-' || r == '_') && index > 0:
		default:
			return errors.NewCode(errors.InvalidInput, "public feature key must match [a-z][a-z0-9._-]*").WithContext("key", key)
		}
	}
	return nil
}
