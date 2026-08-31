package basic

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gochen-runtime/di"
	"gochen/errors"

	"gochen/testkit/require"
)

type basicTestService struct {
	id int64
}

type multiOutA struct {
	id int64
}

type multiOutB struct {
	id int64
}

type ITestIface interface {
	Foo() string
}

type testIfaceImpl struct{}

func (t *testIfaceImpl) Foo() string { return "ok" }

type circularServiceA struct {
	b *circularServiceB
}

type circularServiceB struct {
	a *circularServiceA
}

type groupCycleA struct{}

type groupCycleB struct{}

type lifecycleCloseService struct{}

func (s *lifecycleCloseService) Close() error { return nil }

// TestContainer_RegisterTransient_CreatesNewInstanceEachResolve 验证 Container RegisterTransient CreatesNewInstanceEachResolve。
func TestContainer_RegisterTransient_CreatesNewInstanceEachResolve(t *testing.T) {
	c := New()

	var seq atomic.Int64
	serviceType := reflect.TypeOf((*basicTestService)(nil))
	err := c.RegisterTransient(serviceType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: seq.Add(1)}
	}))
	if err != nil {
		t.Fatalf("RegisterTransient failed: %v", err)
	}

	a, err := c.Resolve(serviceType)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	b, err := c.Resolve(serviceType)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	sa := a.(*basicTestService)
	sb := b.(*basicTestService)
	if sa == sb {
		t.Fatalf("expected transient to create new instance, got same pointer")
	}
	if sa.id == sb.id {
		t.Fatalf("expected different instance ids, got %d and %d", sa.id, sb.id)
	}
}

// TestContainer_RegisterSingleton_ConcurrentResolve_ConstructsOnce 验证 Container RegisterSingleton ConcurrentResolve ConstructsOnce。
func TestContainer_RegisterSingleton_ConcurrentResolve_ConstructsOnce(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	serviceType := reflect.TypeOf((*basicTestService)(nil))
	ready := make(chan struct{})
	release := make(chan struct{})
	err := c.RegisterSingleton(serviceType, di.NewFactory(func() (*basicTestService, error) {
		constructed.Add(1)
		close(ready)
		<-release
		return &basicTestService{id: 42}, nil
	}))
	if err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)

	results := make([]*basicTestService, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			v, err := c.Resolve(serviceType)
			errs[i] = err
			if v != nil {
				results[i] = v.(*basicTestService)
			}
		}(i)
	}
	select {
	case <-ready:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("singleton factory did not start")
	}
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Resolve[%d] error: %v", i, err)
		}
	}

	first := results[0]
	for i, inst := range results {
		if inst == nil {
			t.Fatalf("Resolve[%d] returned nil instance", i)
		}
		if inst != first {
			t.Fatalf("expected all resolves to return same singleton instance")
		}
	}

	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected singleton to be constructed once, got %d", got)
	}
}

// TestContainer_RegisterSingleton_ConcurrentResolve_ErrorIsShared 验证 Container RegisterSingleton ConcurrentResolve ErrorIsShared。
func TestContainer_RegisterSingleton_ConcurrentResolve_ErrorIsShared(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	serviceType := reflect.TypeOf((*basicTestService)(nil))
	ready := make(chan struct{})
	release := make(chan struct{})
	err := c.RegisterSingleton(serviceType, di.NewFactory(func() (*basicTestService, error) {
		constructed.Add(1)
		close(ready)
		<-release
		return nil, context.Canceled
	}))
	if err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)

	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Resolve(serviceType)
		}(i)
	}
	select {
	case <-ready:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("singleton factory did not start")
	}
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("Resolve[%d] expected error, got nil", i)
		}
	}

	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected factory called once, got %d", got)
	}
}

func TestContainer_RegisterSingleton_ConcurrentResolve_PanicIsShared(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	serviceType := reflect.TypeOf((*basicTestService)(nil))
	err := c.RegisterSingleton(serviceType, di.NewFactory(func() *basicTestService {
		constructed.Add(1)
		panic("boom")
	}))
	if err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)

	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Resolve(serviceType)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("Resolve[%d] expected error, got nil", i)
		}
		if !errors.Is(err, errors.Internal) {
			t.Fatalf("Resolve[%d] expected internal error, got %v", i, err)
		}
		if !strings.Contains(err.Error(), "factory panicked") {
			t.Fatalf("Resolve[%d] expected panic wrapper, got %v", i, err)
		}
	}

	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected factory called once, got %d", got)
	}
}

// TestContainer_RegisterConstructor_MultiOutputs_ConstructsOnce 验证 Container RegisterConstructor MultiOutputs ConstructsOnce。
func TestContainer_RegisterConstructor_MultiOutputs_ConstructsOnce(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	err := c.RegisterConstructor(di.NewConstructor(func() (*multiOutA, *multiOutB, error) {
		constructed.Add(1)
		return &multiOutA{id: 1}, &multiOutB{id: 2}, nil
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	aType := reflect.TypeOf((*multiOutA)(nil))
	bType := reflect.TypeOf((*multiOutB)(nil))
	aName := di.TypeKey(aType)
	bName := di.TypeKey(bType)

	a1, err := c.Resolve(aType)
	if err != nil {
		t.Fatalf("Resolve(A) failed: %v", err)
	}
	b1, err := c.Resolve(bType)
	if err != nil {
		t.Fatalf("Resolve(B) failed: %v", err)
	}
	a2, err := c.Resolve(aType)
	if err != nil {
		t.Fatalf("Resolve(A2) failed: %v", err)
	}
	b2, err := c.Resolve(bType)
	if err != nil {
		t.Fatalf("Resolve(B2) failed: %v", err)
	}

	if a1.(*multiOutA) != a2.(*multiOutA) {
		t.Fatalf("expected A to be singleton")
	}
	if b1.(*multiOutB) != b2.(*multiOutB) {
		t.Fatalf("expected B to be singleton")
	}
	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected multi-output constructor called once, got %d", got)
	}

	types := c.ListRegisteredTypes()
	if types[aName] != reflect.TypeOf((*multiOutA)(nil)) {
		t.Fatalf("expected registered type for %s", aName)
	}
	if types[bName] != reflect.TypeOf((*multiOutB)(nil)) {
		t.Fatalf("expected registered type for %s", bName)
	}
}

// TestContainer_RegisterConstructor_MultiOutputs_ErrorIsShared 验证 Container RegisterConstructor MultiOutputs ErrorIsShared。
func TestContainer_RegisterConstructor_MultiOutputs_ErrorIsShared(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	err := c.RegisterConstructor(di.NewConstructor(func() (*multiOutA, *multiOutB, error) {
		constructed.Add(1)
		return nil, nil, context.Canceled
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	aType := reflect.TypeOf((*multiOutA)(nil))
	bType := reflect.TypeOf((*multiOutB)(nil))

	if _, err := c.Resolve(aType); err == nil {
		t.Fatalf("expected Resolve(A) error, got nil")
	}
	if _, err := c.Resolve(bType); err == nil {
		t.Fatalf("expected Resolve(B) error, got nil")
	}
	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected multi-output constructor called once, got %d", got)
	}
}

// TestContainer_RegisterSingleton_InvalidFactorySignature_ReturnsError 验证 Container RegisterSingleton InvalidFactorySignature ReturnsError。
func TestContainer_RegisterSingleton_InvalidFactorySignature_ReturnsError(t *testing.T) {
	c := New()

	err := c.RegisterSingleton(reflect.TypeOf((*basicTestService)(nil)), di.NewFactory(func() (*basicTestService, int) {
		return &basicTestService{id: 1}, 123
	}))
	if err == nil {
		t.Fatalf("expected RegisterSingleton to return error for invalid factory signature, got nil")
	}
}

// TestContainer_RegisterConstructor_MultiOutputs_PanicIsShared 验证 Container RegisterConstructor MultiOutputs PanicIsShared。
func TestContainer_RegisterConstructor_MultiOutputs_PanicIsShared(t *testing.T) {
	c := New()

	var constructed atomic.Int64
	err := c.RegisterConstructor(di.NewConstructor(func() (*multiOutA, *multiOutB, error) {
		constructed.Add(1)
		panic("boom")
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	aType := reflect.TypeOf((*multiOutA)(nil))
	bType := reflect.TypeOf((*multiOutB)(nil))

	_, errA := c.Resolve(aType)
	if errA == nil {
		t.Fatalf("expected Resolve(A) to return error, got nil")
	}
	_, errB := c.Resolve(bType)
	if errB == nil {
		t.Fatalf("expected Resolve(B) to return error, got nil")
	}
	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected multi-output constructor called once, got %d", got)
	}
}

// TestContainer_RegisterSingleton_FactoryReturnsOnlyError_DoesNotPanic 验证 Container RegisterSingleton FactoryReturnsOnlyError DoesNotPanic。
func TestContainer_RegisterSingleton_FactoryReturnsOnlyError_DoesNotPanic(t *testing.T) {
	c := New()

	err := c.RegisterSingleton(reflect.TypeOf((*basicTestService)(nil)), di.NewFactory(func() error { return nil }))
	if err == nil {
		t.Fatalf("expected RegisterSingleton to return error for error-only factory, got nil")
	}
}

func TestContainer_Resolve_FailsFastOnCircularDependency(t *testing.T) {
	c := New()

	err := c.RegisterConstructor(di.NewConstructor(func(b *circularServiceB) *circularServiceA {
		return &circularServiceA{b: b}
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor(A) failed: %v", err)
	}
	err = c.RegisterConstructor(di.NewConstructor(func(a *circularServiceA) *circularServiceB {
		return &circularServiceB{a: a}
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor(B) failed: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := c.Resolve(reflect.TypeOf((*circularServiceA)(nil)))
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected circular dependency error, got nil")
		}
		if !errors.Is(err, errors.Dependency) {
			t.Fatalf("expected Dependency error, got %v", err)
		}
		if !strings.Contains(err.Error(), "circular dependency") {
			t.Fatalf("expected circular dependency context, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Resolve deadlocked on circular dependency")
	}
}

func TestContainer_RegisterConstructor_MultiOutputCycle_FailsFast(t *testing.T) {
	c := New()

	err := c.RegisterConstructor(di.NewConstructor(func(dep *groupCycleB) (*groupCycleA, *groupCycleB, error) {
		return &groupCycleA{}, &groupCycleB{}, nil
	}))
	if err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := c.Resolve(reflect.TypeOf((*groupCycleA)(nil)))
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected circular dependency error, got nil")
		}
		if !errors.Is(err, errors.Dependency) {
			t.Fatalf("expected Dependency error, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Resolve deadlocked on multi-output circular dependency")
	}
}

func TestContainer_Resolve_ConcurrentSlowSingletonDoesNotFailAsCycle(t *testing.T) {
	c := New()

	serviceType := reflect.TypeOf((*basicTestService)(nil))
	ready := make(chan struct{})
	release := make(chan struct{})
	if err := c.RegisterSingleton(serviceType, di.NewFactory(func() *basicTestService {
		close(ready)
		<-release
		return &basicTestService{id: 1}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	firstErr := make(chan error, 1)
	go func() {
		_, err := c.Resolve(serviceType)
		firstErr <- err
	}()

	select {
	case <-ready:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("singleton factory did not start")
	}

	secondErr := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := c.Resolve(serviceType)
		secondErr <- err
	}()

	select {
	case <-secondStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("second resolver did not start")
	}
	close(release)

	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first Resolve failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("first Resolve did not finish")
	}
	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("second Resolve failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("second Resolve did not finish")
	}
}

func TestContainer_Resolve_FailsFastOnCrossGoroutineSingletonCycle(t *testing.T) {
	type gateA struct{}
	type gateB struct{}

	c := New()
	aGateStarted := make(chan struct{})
	bGateStarted := make(chan struct{})

	require.NoError(t, c.RegisterTransient(reflect.TypeOf((*gateA)(nil)), di.NewFactory(func() *gateA {
		close(aGateStarted)
		<-bGateStarted
		return &gateA{}
	})))
	require.NoError(t, c.RegisterTransient(reflect.TypeOf((*gateB)(nil)), di.NewFactory(func() *gateB {
		close(bGateStarted)
		<-aGateStarted
		return &gateB{}
	})))
	require.NoError(t, c.RegisterSingleton(reflect.TypeOf((*circularServiceA)(nil)), di.NewFactory(func(_ *gateA, b *circularServiceB) *circularServiceA {
		return &circularServiceA{b: b}
	})))
	require.NoError(t, c.RegisterSingleton(reflect.TypeOf((*circularServiceB)(nil)), di.NewFactory(func(_ *gateB, a *circularServiceA) *circularServiceB {
		return &circularServiceB{a: a}
	})))

	errCh := make(chan error, 2)
	go func() {
		_, err := c.Resolve(reflect.TypeOf((*circularServiceA)(nil)))
		errCh <- err
	}()
	go func() {
		_, err := c.Resolve(reflect.TypeOf((*circularServiceB)(nil)))
		errCh <- err
	}()

	for range 2 {
		select {
		case err := <-errCh:
			require.Error(t, err)
			require.Truef(t, errors.Is(err, errors.Dependency), "expected Dependency error, got %v", err)
			require.Contains(t, err.Error(), "circular dependency")
		case <-time.After(time.Second):
			t.Fatal("Resolve deadlocked on cross-goroutine singleton cycle")
		}
	}
}

func TestContainer_Resolve_NestedSlowSingletonFromOtherGoroutineDoesNotFailAsCycle(t *testing.T) {
	c := New()

	type slowSingleton struct{ id int }
	type nestedConsumer struct {
		dep *slowSingleton
	}
	type rootConsumer struct {
		dep *nestedConsumer
	}

	slowType := reflect.TypeOf((*slowSingleton)(nil))
	nestedType := reflect.TypeOf((*nestedConsumer)(nil))
	rootType := reflect.TypeOf((*rootConsumer)(nil))
	ready := make(chan struct{})
	release := make(chan struct{})

	if err := c.RegisterSingleton(slowType, di.NewFactory(func() *slowSingleton {
		close(ready)
		<-release
		return &slowSingleton{id: 1}
	})); err != nil {
		t.Fatalf("RegisterSingleton(slow) failed: %v", err)
	}
	if err := c.RegisterSingleton(nestedType, di.NewFactory(func(dep *slowSingleton) *nestedConsumer {
		return &nestedConsumer{dep: dep}
	})); err != nil {
		t.Fatalf("RegisterSingleton(nested) failed: %v", err)
	}
	if err := c.RegisterSingleton(rootType, di.NewFactory(func(dep *nestedConsumer) *rootConsumer {
		return &rootConsumer{dep: dep}
	})); err != nil {
		t.Fatalf("RegisterSingleton(root) failed: %v", err)
	}

	firstErr := make(chan error, 1)
	go func() {
		_, err := c.Resolve(slowType)
		firstErr <- err
	}()

	select {
	case <-ready:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("slow singleton factory did not start")
	}

	secondErr := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := c.Resolve(rootType)
		secondErr <- err
	}()

	select {
	case <-secondStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("nested resolver did not start")
	}
	close(release)

	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first Resolve failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("first Resolve did not finish")
	}
	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("nested Resolve failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("nested Resolve did not finish")
	}
}

// TestContainer_Diagnose 验证 Diagnose 方法。
func TestContainer_Diagnose(t *testing.T) {
	c := New()

	// 注册一些服务
	svcType := reflect.TypeOf((*basicTestService)(nil))
	aType := reflect.TypeOf((*multiOutA)(nil))
	svcKey := di.TypeKey(svcType)
	aKey := di.TypeKey(aType)
	_ = c.RegisterSingleton(svcType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 1}
	}))

	_ = c.RegisterTransient(aType, di.NewFactory(func() *multiOutA {
		return &multiOutA{id: 2}
	}))

	diag := c.Diagnose()

	if len(diag.RegisteredServices) != 2 {
		t.Fatalf("expected 2 registered services, got %d", len(diag.RegisteredServices))
	}

	// 验证服务信息
	foundSingleton := false
	foundTransient := false
	for _, svc := range diag.RegisteredServices {
		if svc.Name == svcKey {
			foundSingleton = true
			if svc.Lifetime != "singleton" {
				t.Errorf("expected singleton lifetime, got %s", svc.Lifetime)
			}
		}
		if svc.Name == aKey {
			foundTransient = true
			if svc.Lifetime != "transient" {
				t.Errorf("expected transient lifetime, got %s", svc.Lifetime)
			}
		}
	}

	if !foundSingleton {
		t.Error("expected to find singleton service")
	}
	if !foundTransient {
		t.Error("expected to find transient service")
	}
}

func TestContainer_Diagnose_SingletonLifecycleMethods(t *testing.T) {
	c := New()

	serviceType := reflect.TypeOf((*lifecycleCloseService)(nil))
	serviceKey := di.TypeKey(serviceType)
	if err := c.RegisterSingleton(serviceType, di.NewFactory(func() *lifecycleCloseService {
		return &lifecycleCloseService{}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	diag := c.Diagnose()
	for _, svc := range diag.RegisteredServices {
		if svc.Name != serviceKey {
			continue
		}
		if len(svc.LifecycleMethods) != 1 || svc.LifecycleMethods[0] != "Close" {
			t.Fatalf("expected Close lifecycle method, got %#v", svc.LifecycleMethods)
		}
		return
	}
	t.Fatalf("expected to find service %s", serviceKey)
}

// TestContainer_Diagnose_UnresolvableDependencies 验证诊断无法解析的依赖。
func TestContainer_Diagnose_UnresolvableDependencies(t *testing.T) {
	c := New()

	// 注册一个有未满足依赖的服务
	serviceType := reflect.TypeOf((*multiOutA)(nil))
	_ = c.RegisterSingleton(serviceType, di.NewFactory(func(dep *basicTestService) *multiOutA {
		return &multiOutA{id: 1}
	}))

	diag := c.Diagnose()

	if len(diag.UnresolvableDependencies) != 1 {
		t.Fatalf("expected 1 unresolvable dependency, got %d", len(diag.UnresolvableDependencies))
	}

	unresolvable := diag.UnresolvableDependencies[0]
	if unresolvable.Service != di.TypeKey(serviceType) {
		t.Errorf("expected service %q, got %s", di.TypeKey(serviceType), unresolvable.Service)
	}
	if unresolvable.ParamIndex != 0 {
		t.Errorf("expected param index 0, got %d", unresolvable.ParamIndex)
	}
	expectParamType := di.TypeKey(reflect.TypeOf((*basicTestService)(nil)))
	if unresolvable.ParamType != expectParamType {
		t.Errorf("expected param type %q, got %q", expectParamType, unresolvable.ParamType)
	}
}

func TestContainer_Diagnose_ValueRegistrationDoesNotSatisfyPointerDependency(t *testing.T) {
	c := New()

	valueType := reflect.TypeOf(basicTestService{})
	if err := c.RegisterInstance(valueType, di.NewInstance(basicTestService{id: 1})); err != nil {
		t.Fatalf("RegisterInstance failed: %v", err)
	}
	consumerType := reflect.TypeOf((*multiOutA)(nil))
	if err := c.RegisterSingleton(consumerType, di.NewFactory(func(dep *basicTestService) *multiOutA {
		_ = dep
		return &multiOutA{id: 1}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	diag := c.Diagnose()
	if len(diag.UnresolvableDependencies) != 1 {
		t.Fatalf("expected pointer dependency to be unresolvable, got %v", diag.UnresolvableDependencies)
	}
	unresolvable := diag.UnresolvableDependencies[0]
	if unresolvable.Service != di.TypeKey(consumerType) {
		t.Fatalf("expected service %q, got %q", di.TypeKey(consumerType), unresolvable.Service)
	}
	if unresolvable.ParamType != di.TypeKey(reflect.TypeOf((*basicTestService)(nil))) {
		t.Fatalf("expected pointer param type, got %q", unresolvable.ParamType)
	}

	_, err := c.Resolve(consumerType)
	if err == nil {
		t.Fatal("expected resolve to fail for pointer dependency backed only by value registration")
	}
	if strings.Contains(err.Error(), "factory panicked") {
		t.Fatalf("expected clean dependency error, got panic wrapper: %v", err)
	}
}

// TestContainer_Diagnose_NoIssues 验证没有问题时的诊断结果。
func TestContainer_Diagnose_NoIssues(t *testing.T) {
	c := New()

	// 注册满足依赖的服务链
	_ = c.RegisterSingleton(reflect.TypeOf((*basicTestService)(nil)), di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 1}
	}))

	_ = c.RegisterSingleton(reflect.TypeOf((*multiOutA)(nil)), di.NewFactory(func(dep *basicTestService) *multiOutA {
		return &multiOutA{id: 1}
	}))

	diag := c.Diagnose()

	if len(diag.UnresolvableDependencies) != 0 {
		t.Errorf("expected no unresolvable dependencies, got %d", len(diag.UnresolvableDependencies))
	}
	if len(diag.CircularDependencies) != 0 {
		t.Errorf("expected no circular dependencies, got %d", len(diag.CircularDependencies))
	}
}

func TestContainer_Diagnose_DependencyGraph_UsesResolvedServiceKeys(t *testing.T) {
	c := New()

	ifaceType := reflect.TypeOf((*ITestIface)(nil)).Elem()
	ifaceKey := di.TypeKey(ifaceType)
	consumerType := reflect.TypeOf((*basicTestService)(nil))
	_ = c.RegisterSingleton(ifaceType, di.NewFactory(func() ITestIface {
		return &testIfaceImpl{}
	}))
	_ = c.RegisterSingleton(consumerType, di.NewFactory(func(dep ITestIface) *basicTestService {
		_ = dep
		return &basicTestService{id: 1}
	}))

	diag := c.Diagnose()
	deps := diag.DependencyGraph[di.TypeKey(consumerType)]
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	if deps[0] != ifaceKey {
		t.Fatalf("expected dep key %q, got %q", ifaceKey, deps[0])
	}
}

// TestContainer_DiagnoseString 验证 DiagnoseString 方法。
func TestContainer_DiagnoseString(t *testing.T) {
	c := New()

	svcType := reflect.TypeOf((*basicTestService)(nil))
	svcKey := di.TypeKey(svcType)
	_ = c.RegisterSingleton(svcType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 1}
	}))

	result := c.DiagnoseString()

	if result == "" {
		t.Fatal("expected non-empty diagnosis string")
	}

	// 验证包含关键内容
	if !strings.Contains(result, "DI Container Diagnosis") {
		t.Error("expected diagnosis string to contain header")
	}
	if !strings.Contains(result, svcKey) {
		t.Error("expected diagnosis string to contain service name")
	}
	if !strings.Contains(result, "singleton") {
		t.Error("expected diagnosis string to contain lifetime")
	}
}

func TestContainer_DiagnoseString_IncludesSingletonLifecycleMethods(t *testing.T) {
	c := New()

	svcType := reflect.TypeOf((*lifecycleCloseService)(nil))
	if err := c.RegisterSingleton(svcType, di.NewFactory(func() *lifecycleCloseService {
		return &lifecycleCloseService{}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	result := c.DiagnoseString()
	if !strings.Contains(result, "lifecycle: [Close]") {
		t.Fatalf("expected lifecycle methods in diagnosis string, got:\n%s", result)
	}
}

type circularA struct{ b *circularB }
type circularB struct{ a *circularA }

func TestContainer_Diagnose_CircularDependencies(t *testing.T) {
	c := New()

	_ = c.RegisterConstructor(di.NewConstructor(func(b *circularB) *circularA { return &circularA{b: b} }))
	_ = c.RegisterConstructor(di.NewConstructor(func(a *circularA) *circularB { return &circularB{a: a} }))

	diag := c.Diagnose()
	if len(diag.CircularDependencies) == 0 {
		t.Fatalf("expected circular dependencies, got none")
	}
}

type IGenericRepo[T any] interface {
	Get() T
}

type stringRepoImpl struct{}

func (*stringRepoImpl) Get() string { return "ok" }

type altStringRepoImpl struct{}

func (*altStringRepoImpl) Get() string { return "alt" }

func TestContainer_ResolveGenericInterfaceFromConcreteConstructor(t *testing.T) {
	c := New()
	if err := c.RegisterConstructor(di.NewConstructor(func() *stringRepoImpl { return &stringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	var got IGenericRepo[string]
	if err := c.Invoke(di.NewInvocation(func(repo IGenericRepo[string]) { got = repo })); err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}
	if got == nil || got.Get() != "ok" {
		t.Fatalf("unexpected repo: %#v", got)
	}
}

func TestContainer_DirectResolveGenericInterfaceFromConcreteConstructor(t *testing.T) {
	c := New()
	if err := c.RegisterConstructor(di.NewConstructor(func() *stringRepoImpl { return &stringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	repoType := reflect.TypeOf((*IGenericRepo[string])(nil)).Elem()
	if !c.IsRegistered(repoType) {
		t.Fatal("expected compatible interface type to be registered")
	}

	got, err := c.Resolve(repoType)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	repo, ok := got.(IGenericRepo[string])
	if !ok {
		t.Fatalf("expected compatible repo interface, got %T", got)
	}
	if repo.Get() != "ok" {
		t.Fatalf("unexpected repo result: %s", repo.Get())
	}
}

func TestContainer_ResolveGenericInterfaceAmbiguous(t *testing.T) {
	c := New()
	if err := c.RegisterConstructor(di.NewConstructor(func() *stringRepoImpl { return &stringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}
	if err := c.RegisterConstructor(di.NewConstructor(func() *altStringRepoImpl { return &altStringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	err := c.Invoke(di.NewInvocation(func(repo IGenericRepo[string]) {}))
	if err == nil || !strings.Contains(err.Error(), "multiple services match parameter type") {
		t.Fatalf("expected ambiguous match error, got: %v", err)
	}
}

func TestContainer_DirectResolveGenericInterfaceAmbiguous(t *testing.T) {
	c := New()
	if err := c.RegisterConstructor(di.NewConstructor(func() *stringRepoImpl { return &stringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}
	if err := c.RegisterConstructor(di.NewConstructor(func() *altStringRepoImpl { return &altStringRepoImpl{} })); err != nil {
		t.Fatalf("RegisterConstructor failed: %v", err)
	}

	repoType := reflect.TypeOf((*IGenericRepo[string])(nil)).Elem()
	if c.IsRegistered(repoType) {
		t.Fatal("expected ambiguous compatible interface type not to be registered")
	}

	got, err := c.Resolve(repoType)
	if err == nil {
		t.Fatalf("expected ambiguous Resolve error, got value %#v", got)
	}
	if !errors.Is(err, errors.Conflict) {
		t.Fatalf("expected Conflict error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "multiple services match service type") {
		t.Fatalf("expected ambiguous service type error, got: %v", err)
	}
}

func TestContainer_RegisterSingleton_RegistersTypedLookupKey(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf((*basicTestService)(nil))

	if err := c.RegisterSingleton(serviceType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 7}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	got, err := c.Resolve(serviceType)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if got.(*basicTestService).id != 7 {
		t.Fatalf("unexpected service: %#v", got)
	}
	if !c.IsRegistered(serviceType) {
		t.Fatal("expected service to be registered by type")
	}

	types := c.ListRegisteredTypes()
	if len(types) != 1 || types[di.TypeKey(serviceType)] != serviceType {
		t.Fatalf("unexpected registered types: %v", types)
	}
}

func TestContainer_RegisterSingleton_CannotDuplicateType(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf((*basicTestService)(nil))

	if err := c.RegisterSingleton(serviceType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 1}
	})); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	err := c.RegisterSingleton(serviceType, di.NewFactory(func() *basicTestService {
		return &basicTestService{id: 2}
	}))
	if err == nil {
		t.Fatal("expected duplicate type registration to be rejected")
	}
}

func TestContainer_RegisterSingleton_RejectsMismatchedFactoryOutput(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf(0)

	err := c.RegisterSingleton(serviceType, di.NewFactory(func() string { return "oops" }))
	if err == nil {
		t.Fatal("expected mismatched typed singleton registration to fail")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}
	if c.IsRegistered(serviceType) {
		t.Fatal("expected mismatched typed singleton registration not to pollute container state")
	}
}

func TestContainer_RegisterTransient_RejectsMismatchedFactoryOutput(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf(0)

	err := c.RegisterTransient(serviceType, di.NewFactory(func() string { return "oops" }))
	if err == nil {
		t.Fatal("expected mismatched typed transient registration to fail")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}
	if c.IsRegistered(serviceType) {
		t.Fatal("expected mismatched typed transient registration not to pollute container state")
	}
}

func TestContainer_RegisterInstance_RejectsMismatchedInstanceType(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf("")

	err := c.RegisterInstance(serviceType, di.NewInstance(123))
	if err == nil {
		t.Fatal("expected mismatched instance registration to fail")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}
	if c.IsRegistered(serviceType) {
		t.Fatal("expected mismatched instance registration not to pollute container state")
	}
}

func TestContainer_RegisterInstance_RejectsTypedNilInstance(t *testing.T) {
	c := New()
	serviceType := reflect.TypeOf((*basicTestService)(nil))

	err := c.RegisterInstance(serviceType, di.NewInstance((*basicTestService)(nil)))
	if err == nil {
		t.Fatal("expected typed nil instance registration to fail")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}
	if c.IsRegistered(serviceType) {
		t.Fatal("expected typed nil instance registration not to pollute container state")
	}
}
