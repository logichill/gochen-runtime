package repo

import (
	"context"
	"gochen/app/query"
	"gochen/db/orm"
	"gochen/errors"
	"gochen/testkit/require"
	"testing"
)

func TestReadPredicateAndAdvancedORPreserveIsolation(t *testing.T) {
	r, capturing := newCaptureRepo(t)
	// lite consumes explicit field metadata; it does not infer queryable columns.
	r.model.Meta().Fields = []orm.FieldMeta{{Name: "ID", Column: "id"}, {Name: "Name", Column: "name"}}
	_, err := capturing.Exec(t.Context(), `INSERT INTO capture_entities (id,tenant_id,managed_scope_id,name) VALUES
 (1,'tenant-a',101,'visible'),(2,'tenant-a',101,'hidden'),(3,'tenant-b',101,'match'),(4,'tenant-a',202,'match'),(5,'tenant-a',101,'match')`)
	require.NoError(t, err)
	WithReadPredicate[*captureEntity, int64](func(context.Context) (orm.Condition, error) {
		return orm.Condition{Expr: "id = ? OR id = ?", Args: []any{int64(1), int64(5)}}, nil
	})(r)
	ctx := scopedContext(t, "tenant-a", 101)
	opts := query.QueryOptions{Advanced: query.AdvancedFilters{Or: []query.OrCondition{{"name": "visible"}, {"name": "match"}}}, Sorts: []query.Sort{{Field: "id", Direction: query.ASC}}, Limit: 1, Offset: 1}
	rows, err := r.Query(ctx, opts)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(5), rows[0].ID)
	count, err := r.QueryCount(ctx, opts)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	_, err = r.Get(ctx, 2)
	require.True(t, errors.Is(err, errors.NotFound))
	q, err := r.ScopedQuery(ctx)
	require.NoError(t, err)
	count, err = q.Count()
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	sentinel := errors.NewCode(errors.Database, "predicate lookup failed")
	WithReadPredicate[*captureEntity, int64](func(context.Context) (orm.Condition, error) { return orm.Condition{}, sentinel })(r)
	capturing.reset()
	_, err = r.Query(ctx, opts)
	require.Equal(t, sentinel, err)
	_, err = r.Count(ctx)
	require.Equal(t, sentinel, err)
	require.Empty(t, capturing.captured())
}
