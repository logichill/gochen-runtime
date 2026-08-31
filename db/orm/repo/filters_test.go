package repo

import (
	"context"
	"testing"

	"gochen/app/query"
	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/errors"

	"gochen/testkit/require"
)

func TestRepoQueryFiltersQuoteReservedColumn(t *testing.T) {
	m := &capturingQueryModel{
		d: dialect.New("postgres"),
		meta: &orm.ModelMeta{Fields: []orm.FieldMeta{
			{Name: "Order", Column: "order"},
		}},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("Order", query.QueryExpr{Op: query.FilterOpEq, Value: query.StringValue("first")}),
	})

	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, `"order" = ?`, m.lastFindOpts.Where[0].Expr)
}

func TestRepoQueryFiltersRejectNilDialect(t *testing.T) {
	m := &capturingQueryModel{
		nilDialect: true,
		meta: &orm.ModelMeta{Fields: []orm.FieldMeta{
			{Name: "Name", Column: "name"},
		}},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("Name", query.QueryExpr{Op: query.FilterOpEq, Value: query.StringValue("first")}),
	})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}

func TestApplyFilterOpLikeRejectsNilDialect(t *testing.T) {
	m := &capturingQueryModel{nilDialect: true}

	_, err := applyFilterOp(newQueryBuilder(m, context.Background()), "name", query.FilterOpLike, "first", nil)

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "dialect is required")
}

func TestValidateQueryExprRejectsMissingValueType(t *testing.T) {
	err := validateQueryExpr("age", query.QueryExpr{
		Op: query.FilterOpEq,
	})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "query filter value type is required")
}

func TestValidateQueryExprRejectsInvalidValueTypeInList(t *testing.T) {
	err := validateQueryExpr("age", query.QueryExpr{
		Op: query.FilterOpIn,
		Values: []query.QueryValue{
			{Type: query.FieldTypeString, Normalized: "18", String: "18"},
			{Type: query.FieldType("bad-type"), Normalized: "19"},
		},
	})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "invalid query filter value type")
}

func TestValidateQueryExprAllowsUnaryOperatorsWithoutValue(t *testing.T) {
	require.NoError(t, validateQueryExpr("deleted_at", query.QueryExpr{
		Op: query.FilterOpIsNull,
	}))
	require.NoError(t, validateQueryExpr("deleted_at", query.QueryExpr{
		Op: query.FilterOpNotNull,
	}))
}

func TestValidateQueryExprRejectsNonStringLikeValue(t *testing.T) {
	err := validateQueryExpr("active", query.QueryExpr{
		Op:    query.FilterOpLike,
		Value: query.BoolValue(true),
	})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "query filter like value must be string")
}

func TestValidateQueryExprAllowsStringLikeValue(t *testing.T) {
	require.NoError(t, validateQueryExpr("name", query.QueryExpr{
		Op:    query.FilterOpLike,
		Value: query.StringValue("alice"),
	}))
	require.NoError(t, validateQueryExpr("status", query.QueryExpr{
		Op:    query.FilterOpLike,
		Value: query.EnumValue("active"),
	}))
}

func TestBuildOrConditionUsesResolvedColumnsAndBoundArgs(t *testing.T) {
	resolve := func(field string) (string, bool) {
		switch field {
		case "name":
			return "users.name", true
		case "status":
			return "users.status", true
		default:
			return "", false
		}
	}

	expr, args, err := buildOrCondition([]query.OrCondition{
		{
			"status": "active",
			"name":   "alice' OR 1=1 --",
		},
		{
			"name": "bob",
		},
	}, resolve)

	require.NoError(t, err)
	require.Equal(t, "(users.name = ? AND users.status = ?) OR (users.name = ?)", expr)
	require.Equal(t, []any{"alice' OR 1=1 --", "active", "bob"}, args)
	require.NotContains(t, expr, "alice")
	require.NotContains(t, expr, "1=1")
}

func TestBuildOrConditionRejectsUnsafeField(t *testing.T) {
	_, _, err := buildOrCondition([]query.OrCondition{
		{
			"name; DROP TABLE users": "alice",
		},
	}, func(field string) (string, bool) {
		return "", false
	})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}
