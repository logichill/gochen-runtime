package testutil

import (
	"gochen-runtime/internal/testutil"
)

// MockRouteGroup 转发到 runtime 统一的 internal/testutil.MockRouteGroup。
type MockRouteGroup = testutil.MockRouteGroup

// NewMockRouteGroup 创建空路由分组。
func NewMockRouteGroup() *MockRouteGroup {
	return testutil.NewMockRouteGroup()
}
