package sqlstore

import "gochen-runtime/internal/normalize"

// governanceNormalizePositiveID 在 value 为正数时返回原值，否则返回 0。
func governanceNormalizePositiveID(value int64) int64 {
	return normalize.PositiveID(value)
}
