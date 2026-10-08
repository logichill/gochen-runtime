package sqlbuilder

import (
	coredialect "gochen/db/dialect"
	"reflect"
	"strings"

	"gochen-runtime/db/sqlscan"
	"gochen/errors"
)

// expandPlaceholders 将包含 slice/array 参数的占位符表达式展开为标准 (?, ?, ...) 形式。
//
// 说明：
// - 约定：
// - - cond 中 ? 的数量必须与 args 数量一致，否则返回 InvalidInput；
// - - 若对应参数是 slice/array（非字符串/非 []byte），会展开为多个 ? 并扁平化参数；
// - - 空 slice/array（包括 `IN ?` 与 `NOT IN ?`）统一生成 "0=1"；这里刻意保持
// - -   “空集合即无匹配”的 builder 级语义，避免直接调用方在遗漏前置校验时因 `NOT IN ?`
// - -   意外退化为恒真条件而放大查询范围，同时也避免 SQL 语法错误。
func expandPlaceholders(cond string, args []any, d coredialect.IDialect) (string, []any, error) {
	if err := validateCondition(cond, d); err != nil {
		return "", nil, err
	}
	if cond == "" || len(args) == 0 {
		return cond, args, nil
	}

	for _, arg := range args {
		if values, ok := tryFlattenSliceArg(arg); ok && len(values) == 0 {
			// 对所有空集合输入统一 fail-closed 为“无匹配”，包括 NOT IN 场景。
			return "0=1", nil, nil
		}
	}

	var (
		builder strings.Builder
		flat    = make([]any, 0, len(args))
		argIdx  int
	)

	prevScanner := sqlscan.NewPrevTokenScanner(cond)
	for i := 0; i < len(cond); i++ {
		ch := cond[i]
		if next, ok := scanConditionIgnored(cond, i, d); ok {
			builder.WriteString(cond[i:next])
			i = next - 1
			continue
		}
		if ch != '?' {
			builder.WriteByte(ch)
			continue
		}
		if d.PlaceholderStyle() == coredialect.PlaceholderPostgres && sqlscan.IsPostgresJSONQuestionOperator(cond, &prevScanner, i) {
			builder.WriteByte(ch)
			continue
		}

		if argIdx >= len(args) {
			return "", nil, errors.NewCode(errors.InvalidInput, "sql: placeholder/args mismatch").
				WithContext("cond", cond).
				WithContext("arg_idx", argIdx).
				WithContext("args_len", len(args))
		}
		arg := args[argIdx]
		argIdx++

		if values, ok := tryFlattenSliceArg(arg); ok {
			if len(values) == 0 {
				builder.WriteString("0=1")
				continue
			}
			alreadyParenthesized := placeholderAlreadyParenthesized(cond, i)
			if !alreadyParenthesized {
				builder.WriteByte('(')
			}
			for j := range values {
				if j > 0 {
					builder.WriteString(", ")
				}
				builder.WriteByte('?')
			}
			if !alreadyParenthesized {
				builder.WriteByte(')')
			}
			flat = append(flat, values...)
			continue
		}

		builder.WriteByte('?')
		flat = append(flat, arg)
	}

	if argIdx != len(args) {
		return "", nil, errors.NewCode(errors.InvalidInput, "sql: placeholder/args mismatch").
			WithContext("cond", cond).
			WithContext("arg_idx", argIdx).
			WithContext("args_len", len(args))
	}

	return builder.String(), flat, nil
}

func placeholderAlreadyParenthesized(cond string, placeholderIndex int) bool {
	prev := placeholderIndex - 1
	for prev >= 0 && (cond[prev] == ' ' || cond[prev] == '\t' || cond[prev] == '\n' || cond[prev] == '\r') {
		prev--
	}
	next := placeholderIndex + 1
	for next < len(cond) && (cond[next] == ' ' || cond[next] == '\t' || cond[next] == '\n' || cond[next] == '\r') {
		next++
	}
	return prev >= 0 && next < len(cond) && cond[prev] == '(' && cond[next] == ')'
}

// CountConditionArgs 按切片展开规则保守统计条件的绑定参数预算。
// 具体 []byte 保持单参数；其他切片和数组按长度计数，空集合至少预留一个参数。
// GORM 的 IN (?) 会绑定 nil，且不会像本包 builder 一样折叠整个条件并丢弃其余参数。
func CountConditionArgs(args []any) int {
	count := 0
	for _, arg := range args {
		if values, ok := sliceArgValue(arg); ok {
			count += max(1, values.Len())
		} else {
			count++
		}
	}
	return count
}

func sliceArgValue(arg any) (reflect.Value, bool) {
	// 排除字符串与 []byte
	switch arg.(type) {
	case string, []byte:
		return reflect.Value{}, false
	}

	v := reflect.ValueOf(arg)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return reflect.Value{}, false
	}
	return v, true
}

func tryFlattenSliceArg(arg any) ([]any, bool) {
	v, ok := sliceArgValue(arg)
	if !ok {
		return nil, false
	}
	l := v.Len()
	out := make([]any, 0, l)
	for i := 0; i < l; i++ {
		out = append(out, v.Index(i).Interface())
	}
	return out, true
}
