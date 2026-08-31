package request

import (
	"encoding"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gochen/errors"
)

// bindURLValues 把查询参数集合绑定到目标结构体。
func bindURLValues(dst any, values url.Values) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.NewCode(errors.InvalidInput, "bind target must be a non-nil pointer")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return errors.NewCode(errors.InvalidInput, "bind target must be a pointer to struct")
	}

	_, err := bindStructValues(rv, values, nil, 0)
	return err
}

type queryFieldNameInfo struct {
	primary string
	alias   string
	tagged  bool
}

func queryFieldPaths(prefix []string, name queryFieldNameInfo) [][]string {
	paths := make([][]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	appendPath := func(seg string) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return
		}
		key := strings.Join(appendSegment(prefix, seg), "\x00")
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		paths = append(paths, appendSegment(prefix, seg))
	}
	appendPath(name.primary)
	appendPath(name.alias)
	return paths
}

var (
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	durationType        = reflect.TypeOf(time.Duration(0))
)

// queryFieldName 推断字段可接受的查询参数名及别名。
func queryFieldName(sf reflect.StructField) queryFieldNameInfo {
	for _, tagKey := range []string{"query", "form", "json"} {
		if v, ok := sf.Tag.Lookup(tagKey); ok {
			name := strings.Split(v, ",")[0]
			if name == "-" {
				return queryFieldNameInfo{}
			}
			if name != "" {
				return queryFieldNameInfo{primary: name, alias: sf.Name, tagged: true}
			}
		}
	}
	return queryFieldNameInfo{primary: sf.Name, alias: sf.Name}
}

// isStructLikeType 判断该类型是否表现为结构体或结构体指针。
func isStructLikeType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Struct {
		return true
	}
	return t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct
}

// isLeafBindableType 判断该类型是否应作为叶子值直接绑定，而不是继续递归展开。
func isLeafBindableType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t == durationType {
		return true
	}
	// 指针类型（如 *time.Time）会在 Implements 中直接命中。
	if t.Implements(textUnmarshalerType) {
		return true
	}
	// 值类型（如 time.Time）通常通过指针接收者实现 UnmarshalText。
	return t.Kind() != reflect.Ptr && reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// dotQueryKey 把字段路径拼成 `a.b.c` 形式的查询参数键。
func dotQueryKey(segments []string) string {
	return strings.Join(segments, ".")
}

// bracketQueryKey 把字段路径拼成 `a[b][c]` 形式的查询参数键。
func bracketQueryKey(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	if len(segments) == 1 {
		return segments[0]
	}
	var b strings.Builder
	b.WriteString(segments[0])
	for _, seg := range segments[1:] {
		b.WriteByte('[')
		b.WriteString(seg)
		b.WriteByte(']')
	}
	return b.String()
}

// lookupURLValues 依次尝试 dot 和 bracket 两种写法查找查询参数值。
func lookupURLValues(values url.Values, segments []string) ([]string, string, bool) {
	if len(segments) == 0 {
		return nil, "", false
	}
	dotKey := dotQueryKey(segments)
	if raw, ok := values[dotKey]; ok && len(raw) > 0 {
		return raw, dotKey, true
	}
	bracketKey := bracketQueryKey(segments)
	if bracketKey != dotKey {
		if raw, ok := values[bracketKey]; ok && len(raw) > 0 {
			return raw, bracketKey, true
		}
	}
	return nil, "", false
}

// appendSegment 在不污染原切片容量的前提下追加一个路径段。
func appendSegment(prefix []string, seg string) []string {
	return append(prefix[:len(prefix):len(prefix)], seg)
}

func bindEmbeddedStructField(fv reflect.Value, values url.Values, prefix []string, depth int) (bool, error) {
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			tmp := reflect.New(fv.Type().Elem())
			changed, err := bindStructValues(tmp.Elem(), values, prefix, depth+1)
			if err != nil {
				return false, err
			}
			if changed {
				fv.Set(tmp)
			}
			return changed, nil
		}
		return bindStructValues(fv.Elem(), values, prefix, depth+1)
	}
	return bindStructValues(fv, values, prefix, depth+1)
}

func bindNestedStructField(fv reflect.Value, values url.Values, prefix []string, name queryFieldNameInfo, depth int) (bool, error) {
	if name.primary == "" {
		return false, nil
	}

	segments := queryFieldPaths(prefix, name)

	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			tmp := reflect.New(fv.Type().Elem())
			changedAny := false
			for _, path := range segments {
				changed, err := bindStructValues(tmp.Elem(), values, path, depth+1)
				if err != nil {
					return false, err
				}
				if changed {
					changedAny = true
				}
			}
			if changedAny {
				fv.Set(tmp)
			}
			return changedAny, nil
		}

		changedAny := false
		for _, path := range segments {
			changed, err := bindStructValues(fv.Elem(), values, path, depth+1)
			if err != nil {
				return false, err
			}
			if changed {
				changedAny = true
			}
		}
		return changedAny, nil
	}

	changedAny := false
	for _, path := range segments {
		changed, err := bindStructValues(fv, values, path, depth+1)
		if err != nil {
			return false, err
		}
		if changed {
			changedAny = true
		}
	}
	return changedAny, nil
}

// bindStructValues 遍历结构体字段并把匹配的查询参数写入对应字段。
func bindStructValues(rv reflect.Value, values url.Values, prefix []string, depth int) (bool, error) {
	if depth > 16 {
		return false, errors.NewCode(errors.Internal, "query binding recursion too deep")
	}
	if rv.Kind() != reflect.Struct {
		return false, errors.NewCode(errors.Internal, "query binding expects struct value")
	}

	rt := rv.Type()
	changedAny := false

	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}

		name := queryFieldName(sf)
		if name.primary == "" {
			continue
		}

		fv := rv.Field(i)

		// 匿名嵌入结构体：默认 flatten；若显式 tag 命名则按嵌套对象处理。
		if sf.Anonymous && isStructLikeType(sf.Type) && !isLeafBindableType(sf.Type) {
			var (
				changed bool
				err     error
			)
			if !name.tagged {
				changed, err = bindEmbeddedStructField(fv, values, prefix, depth+1)
			} else {
				changed, err = bindNestedStructField(fv, values, prefix, name, depth+1)
			}
			if err != nil {
				return false, err
			}
			if changed {
				changedAny = true
			}
			continue
		}

		// 具名结构体字段：按嵌套对象 key 绑定（支持 dot/bracket 形式）。
		if isStructLikeType(sf.Type) && !isLeafBindableType(sf.Type) {
			changed, err := bindNestedStructField(fv, values, prefix, name, depth+1)
			if err != nil {
				return false, err
			}
			if changed {
				changedAny = true
			}
			continue
		}

		var (
			rawValues []string
			key       string
			ok        bool
		)
		for _, path := range queryFieldPaths(prefix, name) {
			rawValues, key, ok = lookupURLValues(values, path)
			if ok {
				break
			}
		}
		if !ok {
			continue
		}

		if err := setFieldFromStrings(fv, rawValues); err != nil {
			return false, errors.NewCode(errors.InvalidInput, "invalid query parameter").
				WithContext("field", sf.Name).
				WithContext("key", key).
				WithContext("value", strings.Join(rawValues, ",")).
				WithContext("cause", err.Error())
		}
		changedAny = true
	}

	return changedAny, nil
}

// setFieldFromStrings 把字符串切片解析并写入目标字段。
func setFieldFromStrings(fv reflect.Value, raw []string) error {
	if !fv.CanSet() {
		return errors.NewCode(errors.Unsupported, "unsupported operation")
	}

	// Special cases based on concrete type.
	if fv.Type() == durationType {
		d, err := time.ParseDuration(raw[0])
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}

	// Prefer encoding.TextUnmarshaler when available (e.g., time.Time, netip.Addr).
	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(raw[0]))
		}
	}

	// Pointer: allocate and set element.
	if fv.Kind() == reflect.Ptr {
		elem := reflect.New(fv.Type().Elem())
		if err := setFieldFromStrings(elem.Elem(), raw); err != nil {
			return err
		}
		fv.Set(elem)
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw[0])
		return nil
	case reflect.Bool:
		v, err := strconv.ParseBool(raw[0])
		if err != nil {
			return err
		}
		fv.SetBool(v)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(raw[0], 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetInt(v)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v, err := strconv.ParseUint(raw[0], 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetUint(v)
		return nil
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(raw[0], fv.Type().Bits())
		if err != nil {
			return err
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.NewCode(errors.InvalidInput, "invalid non-finite float value")
		}
		fv.SetFloat(v)
		return nil
	case reflect.Slice:
		elemType := fv.Type().Elem()
		out := reflect.MakeSlice(fv.Type(), 0, len(raw))
		for _, s := range raw {
			ev := reflect.New(elemType).Elem()
			if err := setFieldFromStrings(ev, []string{s}); err != nil {
				return err
			}
			out = reflect.Append(out, ev)
		}
		fv.Set(out)
		return nil
	default:
		return errors.NewCode(errors.Unsupported, "unsupported operation")
	}
}
