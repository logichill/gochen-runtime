package repo

import (
	"reflect"
	"strings"

	"gochen/db/orm"
	"gochen/domain/spec"
	"gochen/errors"
)

// specCondition 把领域规约翻译成 SQL 条件。空表达式的返回值表示不加限制。
func (r *Repo[T, ID]) specCondition(s spec.Spec) (orm.Condition, error) {
	return translateSpec(s, r.resolver, r.named, true)
}

// writeSpecCondition 翻译写路径规约：命名条件被直接拒绝，不做降级或跳过。
func (r *Repo[T, ID]) writeSpecCondition(s spec.Spec) (orm.Condition, error) {
	return translateSpec(s, r.resolver, nil, false)
}

func translateSpec(
	s spec.Spec,
	resolver *columnResolver,
	named map[string]NamedConditionFunc,
	allowNamed bool,
) (orm.Condition, error) {
	if err := s.Err(); err != nil {
		return orm.Condition{}, errors.Wrap(err, errors.Internal, "invalid spec")
	}
	switch s.Kind() {
	case spec.KindNone:
		return orm.Condition{Expr: "1 = 0"}, nil
	case spec.KindAll:
		return orm.Condition{}, nil
	case spec.KindEq:
		column, value, err := specValue(s, resolver)
		if err != nil {
			return orm.Condition{}, err
		}
		return orm.Condition{Expr: column + " = ?", Args: []any{value}}, nil
	case spec.KindIn:
		column, err := specColumn(s.Field(), resolver)
		if err != nil {
			return orm.Condition{}, err
		}
		values := s.Values()
		args := make([]any, 0, len(values))
		for _, raw := range values {
			value, err := convertSpecValue(s.Field(), raw, resolver)
			if err != nil {
				return orm.Condition{}, err
			}
			args = append(args, value)
		}
		if len(args) == 0 {
			return orm.Condition{Expr: "1 = 0"}, nil
		}
		return orm.Condition{Expr: column + " IN ?", Args: []any{args}}, nil
	case spec.KindIsNull:
		column, err := specColumn(s.Field(), resolver)
		if err != nil {
			return orm.Condition{}, err
		}
		return orm.Condition{Expr: column + " IS NULL"}, nil
	case spec.KindAnd, spec.KindOr:
		operator := " AND "
		if s.Kind() == spec.KindOr {
			operator = " OR "
		}
		exprs := make([]string, 0, len(s.Children()))
		var args []any
		for _, child := range s.Children() {
			condition, err := translateSpec(child, resolver, named, allowNamed)
			if err != nil {
				return orm.Condition{}, err
			}
			if strings.TrimSpace(condition.Expr) == "" {
				continue
			}
			exprs = append(exprs, "("+condition.Expr+")")
			args = append(args, condition.Args...)
		}
		if len(exprs) == 0 {
			return orm.Condition{}, nil
		}
		return orm.Condition{Expr: strings.Join(exprs, operator), Args: args}, nil
	case spec.KindNamed:
		if !allowNamed {
			return orm.Condition{}, errors.NewCode(errors.Internal,
				"write spec must not contain named conditions").
				WithContext("named", s.Name())
		}
		fn, ok := named[s.Name()]
		if !ok {
			return orm.Condition{}, errors.NewCode(errors.Internal,
				"named condition is not registered on the repository").
				WithContext("named", s.Name())
		}
		condition, err := fn(s.Args())
		if err != nil {
			return orm.Condition{}, errors.Wrap(err, errors.Internal, "named condition failed").
				WithContext("named", s.Name())
		}
		if strings.TrimSpace(condition.Expr) == "" {
			return orm.Condition{}, errors.NewCode(errors.Internal,
				"named condition produced an empty expression").
				WithContext("named", s.Name())
		}
		return condition, nil
	default:
		return orm.Condition{}, errors.NewCode(errors.Internal, "unknown spec kind").
			WithContext("kind", string(s.Kind()))
	}
}

// applyCondition 把翻译出的条件追加到查询；空条件表示不加限制。
func applyCondition(quer *queryBuilder, condition orm.Condition) *queryBuilder {
	if strings.TrimSpace(condition.Expr) == "" {
		return quer
	}
	return quer.Where(condition.Expr, condition.Args...)
}

func specColumn(field string, resolver *columnResolver) (string, error) {
	_, column, ok := resolver.resolveColumn(field)
	if !ok {
		return "", errors.NewCode(errors.Internal, "spec references an unknown field").
			WithContext("field", field)
	}
	return resolver.quote(column), nil
}

func specValue(s spec.Spec, resolver *columnResolver) (string, any, error) {
	column, err := specColumn(s.Field(), resolver)
	if err != nil {
		return "", nil, err
	}
	value, err := convertSpecValue(s.Field(), s.Value(), resolver)
	if err != nil {
		return "", nil, err
	}
	return column, value, nil
}

func convertSpecValue(field string, raw any, resolver *columnResolver) (any, error) {
	metaField, _, ok := resolver.resolveColumn(field)
	if !ok {
		return nil, errors.NewCode(errors.Internal, "spec references an unknown field").
			WithContext("field", field)
	}
	converted, err := metaField.Convert(raw)
	if err != nil {
		return nil, errors.Wrap(err, errors.Internal, "spec value conversion failed").
			WithContext("field", field)
	}
	for converted.Kind() == reflect.Ptr {
		if converted.IsNil() {
			return nil, errors.NewCode(errors.Internal, "spec value is nil after conversion").
				WithContext("field", field)
		}
		converted = converted.Elem()
	}
	return converted.Interface(), nil
}
