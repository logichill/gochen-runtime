package authz

import (
	"context"

	"gochen/auth"
	"gochen/auth/scoped"
	"gochen/domain/meta"
	"gochen/domain/spec"
)

// NewScopeResolver 将主体的数据范围投影为项目指定维度上的条件。
// resolver 为 nil 时使用已绑定范围或主体的 ActiveScopeID；缺少范围返回 None。
func NewScopeResolver(dimension meta.Dimension, resolver scoped.IDataScopeResolver) scoped.ISpecResolver {
	return scopeResolver{dimension: dimension, resolver: resolver}
}

type scopeResolver struct {
	dimension meta.Dimension
	resolver  scoped.IDataScopeResolver
}

func (r scopeResolver) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension(r.dimension)}}
}

func (r scopeResolver) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	if _, err := meta.ParseDimension(string(r.dimension)); err != nil {
		return spec.Spec{}, nil, err
	}
	var scope scoped.DataScope
	if r.resolver != nil {
		var err error
		scope, err = scoped.ResolveDataScope(ctx, r.resolver)
		if err != nil {
			return spec.Spec{}, nil, err
		}
	} else if bound, ok := scoped.DataScopeFromContext(ctx); ok {
		scope = bound
	} else if principal, ok := PrincipalFromContext(ctx); ok {
		if principal.IsSystem {
			scope = scoped.Global()
		} else if principal.ActiveScopeID > 0 {
			scope = scoped.Filtered(principal.ActiveScopeID)
		}
	}
	scope = scope.Normalize()
	if scope.Kind == scoped.ScopeGlobal {
		return spec.All(), nil, nil
	}
	if scope.Kind == scoped.ScopeFiltered {
		return scoped.ScopeIn(r.dimension, scope.ScopeIDs...), nil, nil
	}
	return spec.Spec{}, nil, nil
}
