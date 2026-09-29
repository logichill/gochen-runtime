package config

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"time"

	"gochen/errors"
)

// LoadOption 定义结构体配置加载选项。
type LoadOption func(*loadOptions)

type loadOptions struct {
	implicitEnvironment bool
	postBind            []func(any) error
	validators          []func(any) error
}

// WithImplicitEnvironment 控制是否按 yaml 路径隐式推导环境变量名。
//
// 关闭后，仅显式声明 `env` 或位于 `envprefix` 下的字段接受环境变量来源。
func WithImplicitEnvironment(enabled bool) LoadOption {
	return func(options *loadOptions) {
		options.implicitEnvironment = enabled
	}
}

// WithPostBind 注册来源绑定后的归一化步骤；该步骤先于 tag 校验执行。
func WithPostBind(fn func(any) error) LoadOption {
	return func(options *loadOptions) {
		options.postBind = append(options.postBind, fn)
	}
}

// WithValidation 注册 tag 校验之后执行的业务校验。
func WithValidation(fn func(any) error) LoadOption {
	return func(options *loadOptions) {
		options.validators = append(options.validators, fn)
	}
}

// Load 将配置加载到结构体指针。
//
// 执行顺序固定为：tag 默认值 -> 配置源绑定 -> PostBind -> tag 校验 -> 业务校验。
func (p *Provider) Load(key string, target any, opts ...LoadOption) error {
	rv := reflect.ValueOf(target)
	if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return errors.NewCode(errors.InvalidInput, "target must be a pointer to struct")
	}
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return errors.NewCode(errors.InvalidInput, "target must be a pointer to struct")
	}
	if err := validateConfigType(rv.Elem().Type(), nil, map[reflect.Type]bool{}); err != nil {
		return err
	}

	options := loadOptions{implicitEnvironment: true}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	applyDefaultsRecursive(rv)
	_, err := p.bindStruct(
		strings.Trim(strings.TrimSpace(key), "."),
		configPathSegments(key),
		false,
		true,
		rv.Elem(),
		options,
	)
	if err != nil {
		return err
	}

	for _, fn := range options.postBind {
		if fn == nil {
			return errors.NewCode(errors.InvalidInput, "post-bind hook must not be nil")
		}
		if err := fn(target); err != nil {
			return err
		}
	}

	if err := validateTaggedStruct(target); err != nil {
		return err
	}
	for _, validator := range options.validators {
		if validator == nil {
			return errors.NewCode(errors.InvalidInput, "config validator must not be nil")
		}
		if err := validator(target); err != nil {
			return err
		}
	}
	return nil
}

// validateConfigType rejects recursive struct models before default expansion or
// pointer allocation can turn a type cycle into unbounded traversal.
func validateConfigType(t reflect.Type, path []string, active map[reflect.Type]bool) error {
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Duration(0)) {
		return nil
	}
	if active[t] {
		return errors.NewCode(errors.InvalidInput, "recursive configuration type").
			WithContext("path", strings.Join(path, ".")).
			WithContext("type", t.String())
	}
	active[t] = true
	defer delete(active, t)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !isConfigStructField(field) {
			continue
		}
		name, _ := configFieldName(field)
		if err := validateConfigType(field.Type, appendPath(path, name), active); err != nil {
			return err
		}
	}
	return nil
}

// isConfigStructField reports whether a field participates in the same
// traversal as binding, defaults, and tag validation.  Keeping this predicate
// shared prevents a skipped field from re-entering recursion through another
// phase of the load pipeline.
func isConfigStructField(field reflect.StructField) bool {
	if !field.IsExported() {
		return false
	}
	_, ok := configFieldName(field)
	return ok
}

func configPathSegments(key string) []string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(key), "."), ".")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// bindStruct 递归绑定结构体，并返回是否至少绑定了一个来源值。
func (p *Provider) bindStruct(
	prefix string,
	envPath []string,
	envPathExplicit bool,
	environmentAllowed bool,
	rv reflect.Value,
	options loadOptions,
) (bool, error) {
	rt := rv.Type()
	bound := false

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		fv := rv.Field(i)
		if !field.IsExported() || !fv.CanSet() {
			continue
		}

		keyName, ok := configFieldName(field)
		if !ok {
			continue
		}
		fullKey := joinConfigKey(prefix, keyName)

		yamlName, yamlVisible := yamlFieldName(field)
		if !yamlVisible {
			yamlName = keyName
		}
		nextEnvPath := appendPath(envPath, yamlName)
		nextEnvPathExplicit := envPathExplicit
		if envPrefix := strings.TrimSpace(field.Tag.Get("envprefix")); envPrefix != "" {
			nextEnvPath = splitEnvPrefix(envPrefix)
			nextEnvPathExplicit = true
		}
		envTag, hasEnvTag := field.Tag.Lookup("env")
		envTag = strings.TrimSpace(envTag)
		fieldEnvironmentAllowed := environmentAllowed && envTag != "-"

		if isStructLikeField(fv) {
			fieldBound, err := p.bindStructField(
				fullKey,
				nextEnvPath,
				nextEnvPathExplicit,
				fieldEnvironmentAllowed,
				fv,
				options,
			)
			if err != nil {
				return false, err
			}
			bound = bound || fieldBound
			continue
		}

		allowEnvironment := fieldEnvironmentAllowed && (options.implicitEnvironment || hasEnvTag || nextEnvPathExplicit)
		rawEnvKey := ""
		if allowEnvironment {
			if hasEnvTag && envTag != "" {
				rawEnvKey = envTag
			} else {
				rawEnvKey = pathToEnvKey(nextEnvPath)
			}
		}

		entry, found := p.findConfigEntry(fullKey, rawEnvKey, allowEnvironment)
		if found {
			if err := p.bindFieldValue(fullKey, fv, entry.value); err != nil {
				return false, err
			}
			bound = true
			continue
		}

		if fv.Kind() == reflect.Map {
			value, found := p.collectMapValue(fullKey)
			if !found {
				continue
			}
			if err := p.bindFieldValue(fullKey, fv, value); err != nil {
				return false, err
			}
			bound = true
		}
	}

	return bound, nil
}

func (p *Provider) bindStructField(
	prefix string,
	envPath []string,
	envPathExplicit bool,
	environmentAllowed bool,
	field reflect.Value,
	options loadOptions,
) (bool, error) {
	if field.Kind() != reflect.Pointer {
		return p.bindStruct(prefix, envPath, envPathExplicit, environmentAllowed, field, options)
	}
	if !field.IsNil() {
		return p.bindStruct(prefix, envPath, envPathExplicit, environmentAllowed, field.Elem(), options)
	}

	temporary := reflect.New(field.Type().Elem())
	applyDefaultsRecursive(temporary)
	bound, err := p.bindStruct(prefix, envPath, envPathExplicit, environmentAllowed, temporary.Elem(), options)
	if err != nil {
		return false, err
	}
	if bound {
		field.Set(temporary)
	}
	return bound, nil
}

func configFieldName(field reflect.StructField) (string, bool) {
	if tag, exists := field.Tag.Lookup("config"); exists {
		name, _, _ := strings.Cut(strings.TrimSpace(tag), ",")
		if name == "-" {
			return "", false
		}
		if name != "" {
			return name, true
		}
	}
	return yamlFieldName(field)
}

func joinConfigKey(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + "." + name
}

func (p *Provider) findConfigEntry(canonicalKey, rawEnvKey string, allowEnvironment bool) (configEntry, bool) {
	var selected configEntry
	found := false
	for _, entry := range p.entries {
		matches := false
		if entry.environment {
			matches = allowEnvironment && rawEnvKey != "" && environmentEntryMatches(entry, rawEnvKey)
		} else {
			matches = entry.key == canonicalKey
		}
		if !matches {
			continue
		}
		if !found || entry.priority > selected.priority ||
			(entry.priority == selected.priority && entry.sequence > selected.sequence) {
			selected = entry
			found = true
		}
	}
	return selected, found
}

func environmentEntryMatches(entry configEntry, rawEnvKey string) bool {
	if entry.key == envKeyToConfigKey(rawEnvKey) {
		return true
	}
	prefix := entry.environmentPrefix
	if prefix == "" || !strings.HasPrefix(rawEnvKey, prefix) {
		return false
	}
	trimmed := strings.TrimPrefix(rawEnvKey, prefix)
	return trimmed != "" && entry.key == envKeyToConfigKey(trimmed)
}

func (p *Provider) collectMapValue(prefix string) (map[string]any, bool) {
	selected := make(map[string]configEntry)
	prefix += "."
	for _, entry := range p.entries {
		if entry.environment || !strings.HasPrefix(entry.key, prefix) {
			continue
		}
		relative := strings.TrimPrefix(entry.key, prefix)
		if relative == "" {
			continue
		}
		current, exists := selected[relative]
		if !exists || entry.priority > current.priority ||
			(entry.priority == current.priority && entry.sequence > current.sequence) {
			selected[relative] = entry
		}
	}
	if len(selected) == 0 {
		return nil, false
	}

	result := make(map[string]any)
	for relative, entry := range selected {
		setNestedMapValue(result, strings.Split(relative, "."), entry.value)
	}
	return result, true
}

func setNestedMapValue(target map[string]any, path []string, value any) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		target[path[0]] = value
		return
	}
	nested, ok := target[path[0]].(map[string]any)
	if !ok {
		nested = make(map[string]any)
		target[path[0]] = nested
	}
	setNestedMapValue(nested, path[1:], value)
}

func (p *Provider) bindFieldValue(key string, field reflect.Value, value any) error {
	if field.Kind() == reflect.Pointer {
		return p.bindPointerFieldValue(key, field, value)
	}

	switch field.Kind() {
	case reflect.String:
		parsed, ok := stringConfigValue(value)
		if !ok {
			return p.invalidConfig(key, field.Type().String(), value)
		}
		field.SetString(parsed)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Type() == reflect.TypeOf(time.Duration(0)) {
			parsed, err := p.parseDurationValue(key, value)
			if err != nil {
				return err
			}
			field.SetInt(int64(parsed))
			return nil
		}
		parsed, err := p.parseInt64Value(key, value)
		if err != nil {
			return err
		}
		if field.OverflowInt(parsed) {
			return p.invalidConfig(key, field.Type().String(), value)
		}
		field.SetInt(parsed)
	case reflect.Float32, reflect.Float64:
		parsed, err := p.parseFloat64Value(key, value)
		if err != nil {
			return err
		}
		if field.OverflowFloat(parsed) {
			return p.invalidConfig(key, field.Type().String(), value)
		}
		field.SetFloat(parsed)
	case reflect.Bool:
		parsed, err := p.parseBoolValue(key, value)
		if err != nil {
			return err
		}
		field.SetBool(parsed)
	case reflect.Slice:
		return p.bindSliceFieldValue(key, field, value)
	case reflect.Map:
		return p.bindMapFieldValue(key, field, value)
	default:
		return p.invalidConfig(key, field.Type().String(), value)
	}
	return nil
}

func (p *Provider) bindPointerFieldValue(key string, field reflect.Value, value any) error {
	elemType := field.Type().Elem()
	if elemType.Kind() == reflect.Struct {
		return p.invalidConfig(key, field.Type().String(), value)
	}
	elem := reflect.New(elemType).Elem()
	if err := p.bindFieldValue(key, elem, value); err != nil {
		return err
	}
	pointer := reflect.New(elemType)
	pointer.Elem().Set(elem)
	field.Set(pointer)
	return nil
}

func (p *Provider) bindSliceFieldValue(key string, field reflect.Value, value any) error {
	switch field.Type().Elem().Kind() {
	case reflect.String:
		parsed, err := p.parseStringSliceValue(key, value)
		if err != nil {
			return err
		}
		field.Set(stringSliceValueForType(parsed, field.Type()))
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := p.parseIntSliceValue(key, value, field.Type())
		if err != nil {
			return err
		}
		field.Set(parsed)
		return nil
	case reflect.Float32, reflect.Float64:
		parsed, err := p.parseFloatSliceValue(key, value, field.Type())
		if err != nil {
			return err
		}
		field.Set(parsed)
		return nil
	default:
		return p.invalidConfig(key, field.Type().String(), value)
	}
}

func (p *Provider) bindMapFieldValue(key string, field reflect.Value, value any) error {
	if field.Type().Key().Kind() != reflect.String {
		return p.invalidConfig(key, field.Type().String(), value)
	}
	if encoded, ok := value.(string); ok {
		decoded := make(map[string]any)
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return p.wrapInvalidConfig(key, field.Type().String(), value, err)
		}
		value = decoded
	}
	raw, ok := value.(map[string]any)
	if !ok {
		if typed, typedOK := value.(map[string]string); typedOK {
			raw = make(map[string]any, len(typed))
			for mapKey, mapValue := range typed {
				raw[mapKey] = mapValue
			}
		} else {
			return p.invalidConfig(key, field.Type().String(), value)
		}
	}

	result := reflect.MakeMapWithSize(field.Type(), len(raw))
	for mapKey, mapValue := range raw {
		keyValue := reflect.New(field.Type().Key()).Elem()
		keyValue.SetString(mapKey)
		valueType := field.Type().Elem()
		valueValue := reflect.New(valueType).Elem()
		if valueType.Kind() == reflect.Interface {
			if mapValue != nil {
				valueValue.Set(reflect.ValueOf(cloneConfigValue(mapValue)))
			}
		} else if valueType.Kind() == reflect.String {
			parsed, ok := stringConfigValue(mapValue)
			if !ok {
				return p.invalidConfig(key, field.Type().String(), value)
			}
			valueValue.SetString(parsed)
		} else {
			return p.invalidConfig(key, field.Type().String(), value)
		}
		result.SetMapIndex(keyValue, valueValue)
	}
	field.Set(result)
	return nil
}

func stringConfigValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func (p *Provider) parseBoolValue(key string, value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off":
			return false, nil
		default:
			return false, p.invalidConfig(key, "bool", value)
		}
	case int:
		return typed != 0, nil
	case int64:
		return typed != 0, nil
	default:
		return false, p.invalidConfig(key, "bool", value)
	}
}

func (p *Provider) parseDurationValue(key string, value any) (time.Duration, error) {
	switch typed := value.(type) {
	case time.Duration:
		return typed, nil
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(typed))
		if err != nil {
			return 0, p.wrapInvalidConfig(key, "duration", value, err)
		}
		return parsed, nil
	case int:
		return p.durationFromSeconds(key, value, int64(typed))
	case int64:
		return p.durationFromSeconds(key, value, typed)
	case float64:
		if !isFiniteFloat(typed) {
			return 0, p.invalidConfig(key, "duration", value)
		}
		scaled := typed * float64(time.Second)
		if scaled >= float64(math.MaxInt64) || scaled < float64(math.MinInt64) {
			return 0, p.invalidConfig(key, "duration", value)
		}
		return time.Duration(scaled), nil
	default:
		return 0, p.invalidConfig(key, "duration", value)
	}
}

func (p *Provider) durationFromSeconds(key string, value any, seconds int64) (time.Duration, error) {
	maxSeconds := int64(math.MaxInt64) / int64(time.Second)
	minSeconds := int64(math.MinInt64) / int64(time.Second)
	if seconds > maxSeconds || seconds < minSeconds {
		return 0, p.invalidConfig(key, "duration", value)
	}
	return time.Duration(seconds) * time.Second, nil
}

// Has 判断指定 key 是否存在且值非 nil。
func (p *Provider) Has(key string) bool {
	value, ok := p.settings[key]
	return ok && value != nil
}

// ExportSettings 返回脱敏后的完整配置快照，适合日志和调试输出。
func (p *Provider) ExportSettings() map[string]any {
	result := make(map[string]any, len(p.settings))
	for key, value := range p.settings {
		result[key] = cloneConfigValue(p.redactIfSensitive(key, value))
	}
	return result
}

// ExportSettingsUnsafe 返回未脱敏的完整配置快照。
func (p *Provider) ExportSettingsUnsafe() map[string]any {
	result := make(map[string]any, len(p.settings))
	for key, value := range p.settings {
		result[key] = cloneConfigValue(value)
	}
	return result
}

func cloneConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			out[key] = cloneConfigValue(nested)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, nested := range typed {
			out[key] = nested
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, nested := range typed {
			out[index] = cloneConfigValue(nested)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []int64:
		return append([]int64(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		return value
	}
}
