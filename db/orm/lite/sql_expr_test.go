package lite

import (
	"testing"

	"gochen/testkit/require"

	"gochen-runtime/db/dialect"
	"gochen/db/orm"
)

func TestBuildTableExpr_QuotesIdentifiersForJoin(t *testing.T) {
	d := dialect.New("postgres")

	expr, err := buildTableExpr(d, "Users", []orm.Join{
		orm.InnerJoin("OrderItems", "OI", orm.On("Users.id", "OI.user_id")),
	})
	require.NoError(t, err)
	require.Equal(t, `"Users" INNER JOIN "OrderItems" "OI" ON "Users"."id" = "OI"."user_id"`, expr)
}

func TestBuildTableExpr_UnknownDialectKeepsRawForJoin(t *testing.T) {
	d := dialect.New("")

	expr, err := buildTableExpr(d, "Users", []orm.Join{
		orm.InnerJoin("OrderItems", "oi", orm.On("Users.id", "oi.user_id")),
	})
	require.NoError(t, err)
	require.Equal(t, `Users INNER JOIN OrderItems oi ON Users.id = oi.user_id`, expr)
}

func TestBuildTableExpr_RejectsAliasWithDot(t *testing.T) {
	d := dialect.New("postgres")

	_, err := buildTableExpr(d, "users", []orm.Join{
		orm.InnerJoin("orders", "o.bad", orm.On("users.id", "o.bad.user_id")),
	})
	require.Error(t, err)
}
