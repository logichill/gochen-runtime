package repo

import (
	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/sqlbuilder"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
)

// maxBindParameters 返回本仓储可用的占位参数预算。
//
// 向底层数据库探测（db.IBindParameterLimitProvider）；未实现即用默认值。
// IN 列表与多值 INSERT 受同一个约束，因此两处共用这一个来源。
// 批量上限默认 1000，正常批量根本切不动；分片是为了让配置了更大批量上限
// 或更低占位上限的调用方不至于撞上运行期报错，而不是常态开销。
func (r *Repo[T, ID]) maxBindParameters() int {
	if r == nil || r.orm == nil {
		return dialect.DefaultMaxBindParameters
	}
	if provider, ok := r.orm.Database().(db.IBindParameterLimitProvider); ok {
		return dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
	}
	return dialect.DefaultMaxBindParameters
}

// idBatchBudget 为既有租户、范围等条件预留参数，剩余预算才可用于 ID。
func (r *Repo[T, ID]) idBatchBudget(quer *queryBuilder, extra int) (int, error) {
	budget := r.maxBindParameters() - extra
	for _, condition := range orm.CollectQueryOptions(quer.opts...).Where {
		budget -= sqlbuilder.CountConditionArgs(condition.Args)
	}
	if budget <= 0 {
		return 0, errors.NewCode(errors.InvalidInput, "scope conditions exhaust the bind parameter budget")
	}
	return budget, nil
}

// chunkSlice 把列表切成不超过 size 的分片，供逐片下发语句
// （IN 条件的 ID 列表、多值 INSERT 的行列表等）。
//
// 分片只改变语句条数，不改变原子性：所有分片都在调用方已开启的同一事务内执行。
func chunkSlice[E any](items []E, size int) [][]E {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 || len(items) <= size {
		return [][]E{items}
	}
	chunks := make([][]E, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[start:end])
	}
	return chunks
}

// dedupeSlice 按首次出现顺序折叠重复元素。
//
// 供分片前的 ID 列表使用：单条 `id IN (...)` 会把重复 ID 折叠成一行，
// 分片后同一个 ID 落在不同分片则会各返回一行——先去重才能让分片前后的
// 结果集语义保持一致。
func dedupeSlice[E comparable](items []E) []E {
	if len(items) < 2 {
		return items
	}
	seen := make(map[E]struct{}, len(items))
	out := make([]E, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
