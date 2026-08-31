package lite

import (
	"gochen/db"
	"gochen/errors"
	"reflect"
)

// scanRowsIntoDest 将 rows 扫描到 dest 中。
//
// 说明：
// - struct/ptr 目标用于已定位到当前行的 First 路径；
// - slice 目标用于会自行迭代 rows 的 Find 路径。
func scanRowsIntoDest(rows db.IRows, dest any, orm *Orm) error {
	rv := reflect.ValueOf(dest)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.NewCode(errors.InvalidInput, "basic.scanRowsIntoDest: dest must be non-nil pointer")
	}

	elem := rv.Elem()
	switch elem.Kind() {
	case reflect.Slice:
		elemType := elem.Type().Elem()
		for rows.Next() {
			item, scanTarget := newSliceScanValue(elemType)
			if err := scanOneRow(rows, scanTarget, orm); err != nil {
				return err
			}
			elem.Set(reflect.Append(elem, item))
		}
		return rows.Err()
	case reflect.Ptr:
		if isScannableDBField(elem.Type()) {
			return scanOneRow(rows, elem, orm)
		}
		if elem.IsNil() {
			elem.Set(reflect.New(elem.Type().Elem()))
		}
		return scanOneRow(rows, elem.Elem(), orm)
	case reflect.Struct:
		// 已在 First 手动 Next() 过一行，这里直接扫描当前行
		return scanOneRow(rows, elem, orm)
	default:
		return scanOneRow(rows, elem, orm)
	}
}

func validateFindDest(dest any) error {
	rv := reflect.ValueOf(dest)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.NewCode(errors.InvalidInput, "basic.Find: dest must be non-nil pointer")
	}
	return nil
}

func isSliceDest(dest any) bool {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return false
	}
	return rv.Elem().Kind() == reflect.Slice
}

func newSliceScanValue(elemType reflect.Type) (reflect.Value, reflect.Value) {
	if elemType.Kind() == reflect.Ptr {
		if isScannableDBField(elemType) {
			item := reflect.New(elemType).Elem()
			return item, item
		}
		item := reflect.New(elemType.Elem())
		return item, item.Elem()
	}
	item := reflect.New(elemType).Elem()
	return item, item
}

// scanOneRow 将 rows 的当前行扫描到 v 中（v 必须为可寻址的 reflect.Value）。
//
// 处理两种场景：
//  1. v 是标量（isScannableDBField 为 true）：要求结果集恰好只有一列，直接用 v.Addr() 接收。
//  2. v 是结构体：通过 structMeta 的 columnToInfo 按列名映射字段；未知列写入丢弃变量；
//     指针字段为 nil 时由 fieldByIndexAlloc 自动分配。
func scanOneRow(rows db.IRows, v reflect.Value, orm *Orm) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	destPtrs := make([]any, len(cols))

	if isScannableDBField(v.Type()) {
		if len(cols) != 1 {
			return errors.NewCode(errors.InvalidInput, "basic.scanRowsIntoDest: scalar dest requires single selected column").
				WithContext("column_count", len(cols))
		}
		destPtrs[0] = v.Addr().Interface()
		return rows.Scan(destPtrs...)
	}

	// 按目标类型构建 structMeta
	sm := orm.structMetaForValue(v.Addr().Interface())

	if sm == nil {
		if len(cols) != 1 {
			return errors.NewCode(errors.InvalidInput, "basic.scanRowsIntoDest: scalar dest requires single selected column").
				WithContext("column_count", len(cols))
		}
		destPtrs[0] = v.Addr().Interface()
		return rows.Scan(destPtrs...)
	}

	for i, col := range cols {
		if fi, ok := sm.columnToInfo[col]; ok {
			fv := fieldByIndexAlloc(v, fi.Index)
			if !fv.IsValid() || !fv.CanSet() {
				var tmp any
				destPtrs[i] = &tmp
				continue
			}
			destPtrs[i] = scanDestForField(fv)
		} else {
			var tmp any
			destPtrs[i] = &tmp
		}
	}

	if err := rows.Scan(destPtrs...); err != nil {
		return err
	}
	return nil
}

func scanDestForField(fv reflect.Value) any {
	if fv.Kind() == reflect.Ptr && implementsSQLScanner(fv.Type()) {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return fv.Interface()
	}
	return fv.Addr().Interface()
}

// fieldByIndexSafe 按多级索引安全地访问嵌套字段。
// 遇到 nil 指针或越界索引时返回零值 reflect.Value，不会 panic。
func fieldByIndexSafe(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i < 0 || i >= v.NumField() {
			return reflect.Value{}
		}
		v = v.Field(i)
	}
	return v
}

// fieldByIndexAlloc 按多级索引访问嵌套字段，并在路径上遇到 nil 指针时自动分配。
// 若字段不可寻址或索引越界则返回零值 reflect.Value。
// 主要用于 scan 路径：确保扫描目标字段已分配内存。
func fieldByIndexAlloc(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || i < 0 || i >= v.NumField() {
			return reflect.Value{}
		}
		v = v.Field(i)
	}
	return v
}
