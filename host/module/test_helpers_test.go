package module_test

import (
	"gochen-runtime/internal/testutil"
	"gochen/httpx"
)

type mockRouteGroup = testutil.MockRouteGroup

func newMockRouteGroup() *mockRouteGroup {
	return testutil.NewMockRouteGroup()
}

type testService struct{ name string }

func NewTestService() *testService  { return &testService{name: "test"} }
func (s *testService) Name() string { return s.name }

type testRouteRegistrar struct{ service *testService }

func NewTestRouteRegistrar(svc *testService) *testRouteRegistrar {
	return &testRouteRegistrar{service: svc}
}

func (r *testRouteRegistrar) RegisterRoutes(group httpx.IRouteGroup) error {
	group.GET("/test", nil)
	group.POST("/test", nil)
	return nil
}
