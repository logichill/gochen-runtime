package initcap

// Key 是模块初始化 capability 的类型化键。
//
// 该类型服务高级扩展包；普通业务模块应优先使用 host/module/runtimecap
// 暴露的 HTTPFrom、EventBusFrom 等按能力命名的 accessor。
type Key[T any] struct {
	id   *byte
	name string
}

// NewKey 创建模块初始化 capability 键。
func NewKey[T any](name string) Key[T] {
	return Key[T]{
		id:   new(byte),
		name: name,
	}
}

// Name 返回 capability 键的调试名称。
func (k Key[T]) Name() string {
	return k.name
}

// Store 保存模块初始化阶段注入的可选 capability。
type Store struct {
	values map[any]any
}

// Setter 定义一次 capability 注入动作。
type Setter func(map[any]any)

// Set 创建 capability 注入动作。
func Set[T any](key Key[T], value T) Setter {
	return func(values map[any]any) {
		if key.id == nil || values == nil {
			return
		}
		values[key] = value
	}
}

// NewStore 一次性创建 capability store。
func NewStore(setters ...Setter) Store {
	if len(setters) == 0 {
		return Store{}
	}
	values := make(map[any]any, len(setters))
	for _, setter := range setters {
		if setter != nil {
			setter(values)
		}
	}
	if len(values) == 0 {
		return Store{}
	}
	return Store{values: values}
}

// With 返回写入指定 capability 后的新 Store。
func With[T any](store Store, key Key[T], value T) Store {
	if key.id == nil {
		return store
	}
	values := make(map[any]any, len(store.values)+1)
	for k, v := range store.values {
		values[k] = v
	}
	values[key] = value
	return Store{values: values}
}

// Get 读取指定类型的 capability。
func Get[T any](store Store, key Key[T]) (T, bool) {
	var zero T
	if key.id == nil || store.values == nil {
		return zero, false
	}
	value, ok := store.values[key]
	if !ok {
		return zero, false
	}
	typed, ok := value.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}
