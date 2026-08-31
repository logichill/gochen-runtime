package di

import (
	"fmt"
	"reflect"
)

// TypeKey 返回类型在容器中的稳定字符串 key。
func TypeKey(t reflect.Type) string {
	if t == nil {
		return ""
	}
	switch t.Kind() {
	case reflect.Ptr:
		return "*" + TypeKey(t.Elem())
	case reflect.Slice:
		return "[]" + TypeKey(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), TypeKey(t.Elem()))
	case reflect.Map:
		return "map[" + TypeKey(t.Key()) + "]" + TypeKey(t.Elem())
	case reflect.Chan:
		prefix := "chan "
		switch t.ChanDir() {
		case reflect.SendDir:
			prefix = "chan<- "
		case reflect.RecvDir:
			prefix = "<-chan "
		}
		return prefix + TypeKey(t.Elem())
	}

	if name := t.Name(); name != "" {
		if pkg := t.PkgPath(); pkg != "" {
			return pkg + "." + name
		}
		return name
	}
	return t.String()
}
