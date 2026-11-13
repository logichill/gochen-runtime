package rest

import (
	"net/http"
	"sort"
	"testing"

	"gochen/auth"
	"gochen/httpx"
	"gochen/testkit"
	"gochen/testkit/require"
)

func specKeys(specs []RouteSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Method+" "+spec.Path)
	}
	return out
}

// recordedRouteKeys 返回注册结果的排序键集合，用于和 Specs 逐条比对。
func recordedRouteKeys(routes []testkit.RecordedRoute) []string {
	out := make([]string, 0, len(routes))
	for _, route := range routes {
		out = append(out, route.Method+" "+route.Path)
	}
	sort.Strings(out)
	return out
}

func TestSpecsMirrorDescriptorActionsInOrder(t *testing.T) {
	specs := Specs(crudDescriptor(), "/users")
	require.Equal(t, []string{
		http.MethodGet + " /users",
		http.MethodPost + " /users",
		http.MethodPost + " /users/bulk",
		http.MethodPut + " /users/bulk",
		http.MethodDelete + " /users/bulk",
		http.MethodGet + " /users/:id",
		http.MethodPut + " /users/:id",
		http.MethodDelete + " /users/:id",
	}, specKeys(specs))
}

func TestSpecsOnlyIncludeDeclaredActions(t *testing.T) {
	descriptor := crudDescriptor()
	descriptor.Actions = descriptor.Actions[:2] // read + list
	require.Equal(t, []string{
		http.MethodGet + " /users",
		http.MethodGet + " /users/:id",
	}, specKeys(Specs(descriptor, "/users")))
}

func TestSpecsCarryPermissionCodes(t *testing.T) {
	for _, spec := range Specs(crudDescriptor(), "/users") {
		if spec.Permission == "" {
			t.Fatalf("spec %s %s has empty permission", spec.Method, spec.Path)
		}
		require.Equal(t, "user:api:"+spec.Action, spec.Permission)
	}
}

// TestSpecsMatchRegisteredRoutes 守住"路由表"与"实际注册"不漂移。
func TestSpecsMatchRegisteredRoutes(t *testing.T) {
	app := &stubApplication{descriptor: crudDescriptor()}
	_, specs, err := Describe[*mountEntity, int64](app, WithBasePath[*mountEntity, int64]("/users"))
	require.NoError(t, err)

	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount[*mountEntity, int64](router, app, WithBasePath[*mountEntity, int64]("/users")))

	got := recordedRouteKeys(router.RecordedRoutes())
	want := specKeys(specs)
	sort.Strings(want)
	require.ElementsMatch(t, want, got)
}

func TestDescribeDoesNotRegisterRoutes(t *testing.T) {
	app := &stubApplication{descriptor: crudDescriptor()}
	descriptor, specs, err := Describe[*mountEntity, int64](app, WithBasePath[*mountEntity, int64]("/users"))
	require.NoError(t, err)
	require.Equal(t, "user", descriptor.Resource)
	require.Equal(t, 8, len(specs))
}

func TestDescribeRejectsNilApplication(t *testing.T) {
	_, _, err := Describe[*mountEntity, int64](nil)
	require.Error(t, err)
}

// TestDescribeRunsSameValidationAsMount：Describe 复用 prepare，因此装配期校验不可绕过。
func TestDescribeRunsSameValidationAsMount(t *testing.T) {
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionRestore, Kind: auth.KindUpdate},
		},
	}}

	_, _, err := Describe[*mountEntity, int64](app,
		WithBasePath[*mountEntity, int64]("/users"),
		WithOperatorExtractor[*mountEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }), WithRoutes[*mountEntity, int64](OpRestore))
	require.Error(t, err)
}
