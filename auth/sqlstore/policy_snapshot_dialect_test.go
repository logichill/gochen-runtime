package sqlstore

import (
	"strings"
	"testing"
	"time"

	"gochen/testkit/require"

	rtdialect "gochen-runtime/db/dialect"
	auth "gochen/auth"
)

// —— P2-083 补充：方言分支 SQL 生成语法校验（无需真实 MySQL/Postgres 实例）——
//
// buildUpsertQuery 与 LIKE ESCAPE 子句按方言分支生成，此前只有 Unknown
// 方言（compat 路径）与 sqlite（默认分支）有执行型测试，MySQL/Postgres
// 分支的 SQL 语法从未被校验。这里注入方言后直接对 SQL 生成函数断言
// 方言专属语法与占位符/参数一致性。

func TestBuildUpsertQuery_MySQLSyntax(t *testing.T) {
	s := &PolicySnapshotStore{
		tableName: "`authz_policy_snapshots`",
		dialect:   rtdialect.New("mysql"),
	}
	query, args := s.buildUpsertQuery(auth.PolicySnapshot{
		Key:       "k",
		Version:   "v",
		Timestamp: time.Unix(1700000000, 0),
	}, "{}")

	require.Contains(t, query, "ON DUPLICATE KEY UPDATE")
	require.Contains(t, query, "version = ?")
	require.Contains(t, query, "timestamp = ?")
	require.Contains(t, query, "metadata = ?")
	// INSERT 4 个占位符 + UPDATE 3 个占位符。
	require.Equal(t, 7, strings.Count(query, "?"))
	require.Len(t, args, 7)
}

func TestBuildUpsertQuery_PostgresSyntax(t *testing.T) {
	s := &PolicySnapshotStore{
		tableName: `"authz_policy_snapshots"`,
		dialect:   rtdialect.New("postgres"),
	}
	query, args := s.buildUpsertQuery(auth.PolicySnapshot{
		Key:       "k",
		Version:   "v",
		Timestamp: time.Unix(1700000000, 0),
	}, "{}")

	require.Contains(t, query, "ON CONFLICT (snapshot_key) DO UPDATE SET")
	require.NotContains(t, query, "ON DUPLICATE")
	require.Equal(t, 4, strings.Count(query, "?"))
	require.Len(t, args, 4)
}

func TestBuildUpsertQuery_UnknownDialectReturnsEmpty(t *testing.T) {
	s := &PolicySnapshotStore{
		tableName: "authz_policy_snapshots",
		dialect:   rtdialect.New("unknown"),
	}
	query, args := s.buildUpsertQuery(auth.PolicySnapshot{}, "{}")
	require.Equal(t, "", query)
	require.Nil(t, args)
}

func TestLikeEscapeClause_MySQLDeclaresBackslashEscape(t *testing.T) {
	// MySQL 默认配置把反斜杠当作字符串转义符，LIKE ... ESCAPE '\\'
	// 必须显式声明，否则含反斜杠的前缀触发 1064 语法错误。
	require.Equal(t, ` ESCAPE '\\'`, rtdialect.New("mysql").LikeEscapeClause())
	require.Equal(t, ` ESCAPE '\'`, rtdialect.New("postgres").LikeEscapeClause())
}
