package runtime

import (
	"context"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	"gochen/errors"
	"gochen/httpx"
	"gochen/messaging"
)

// StartBackground 启动后台任务（不阻塞主服务）。
//
// 说明：
// - StartBackground 实现 IServer.StartBackground。
// - 该阶段包含“路由挂载”（RegisterRoutes）：只做 HTTP 路由装配与启动期校验，不进入运行期；
// - 再调用所有模块的 Start(ctx)（事件订阅/投影注册/后台任务等），失败则回滚；
// - 再启动消息传输层（Transport），避免遗漏订阅导致消息丢失。
func (s *Host) StartBackground(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if !s.backgroundStarted.CompareAndSwap(false, true) {
		return errors.NewCode(errors.Conflict, "host background already started")
	}
	started := false
	defer func() {
		if !started && !s.transportOwned {
			s.backgroundStarted.Store(false)
		}
	}()
	if err := s.failFastCircularDependencies(); err != nil {
		return err
	}

	// 先挂载路由（若模块提供独立阶段），避免“借 Start 挂路由”引入运行期副作用。
	if err := s.registerModuleRoutes(ctx); err != nil {
		return err
	}

	if len(s.modules) > 0 {
		stops := make([]module.ModuleStopFunc, 0, len(s.modules))
		for _, m := range s.modules {
			if m == nil {
				continue
			}
			stop, err := m.Start(ctx)
			err = runtimeutil.NormalizeError(err)
			if err != nil {
				rollbackStops := append(append([]module.ModuleStopFunc(nil), stops...), stop)
				cleanupCtx, cancel := s.cleanupContext(ctx)
				remaining, rollbackErr := s.stopModules(cleanupCtx, rollbackStops, "failed to roll back module")
				cancel()
				s.moduleStops = remaining
				started = len(remaining) > 0
				return errors.Join(
					errors.Wrap(err, errors.Dependency, "failed to start module").WithContext("module", m.Name()),
					rollbackErr,
				)
			}
			stops = append(stops, stop)
		}
		s.moduleStops = stops
	}

	if s.runtime != nil && s.runtime.Transport != nil {
		if err := s.startTransport(ctx); err != nil {
			cleanupCtx, cancel := s.cleanupContext(ctx)
			defer cancel()
			remaining, rollbackErr := s.stopModules(cleanupCtx, s.moduleStops, "failed to roll back module")
			s.moduleStops = remaining
			started = len(remaining) > 0
			// 与 Shutdown 保持相同顺序：模块清理仍需传输层时保留其所有权，
			// 待模块全部停止后才释放本次启动取得的 Transport。
			var transportStopErr error
			if !started {
				if stopErr := s.stopOwnedTransport(cleanupCtx); stopErr != nil {
					transportStopErr = errors.Wrap(stopErr, errors.Dependency, "failed to roll back message transport start")
				}
			}
			return errors.Join(
				errors.Wrap(err, errors.Dependency, "failed to start message transport"),
				rollbackErr,
				transportStopErr,
			)
		}
	}
	started = true
	return nil
}

func (s *Host) registerModuleRoutes(ctx context.Context) error {
	switch s.routeRegistration.Load() {
	case routeRegistrationComplete:
		return nil
	case routeRegistrationFailed:
		return errors.NewCode(errors.Conflict, "host route registration failed; create a new host before retrying")
	case routeRegistrationInProgress:
		return errors.NewCode(errors.Conflict, "host route registration is already in progress")
	}
	if !s.routeRegistration.CompareAndSwap(routeRegistrationPending, routeRegistrationInProgress) {
		return s.registerModuleRoutes(ctx)
	}

	for _, m := range s.modules {
		if m == nil {
			continue
		}
		rm, ok := m.(module.IRouteModule)
		if !ok {
			continue
		}
		if err := runtimeutil.NormalizeError(rm.RegisterRoutes(ctx)); err != nil {
			s.routeRegistration.Store(routeRegistrationFailed)
			return errors.Wrap(err, errors.Dependency, "failed to register module routes").WithContext("module", m.Name())
		}
	}

	if s.config != nil && s.config.FailFastOnRouteConflicts && s.runtime != nil && s.runtime.HTTPServer != nil {
		if rr, ok := s.runtime.HTTPServer.(httpx.IRouteRegistry); ok {
			if conflicts := rr.RouteConflicts(); len(conflicts) > 0 {
				s.routeRegistration.Store(routeRegistrationFailed)
				return errors.NewCode(errors.InvalidInput, "route conflicts detected").WithContext("conflicts", conflicts)
			}
		}
	}

	s.routeRegistration.Store(routeRegistrationComplete)
	return nil
}

func (s *Host) startTransport(ctx context.Context) error {
	transport := s.runtime.Transport
	wasRunning, observable := transportRunning(transport)
	startErr := runtimeutil.NormalizeError(transport.Start(ctx))
	if startErr == nil {
		// 已运行实例即使允许幂等 Start，也仍由原所有者负责停止。
		s.transportOwned = !observable || !wasRunning
		return nil
	}

	if observable && !wasRunning {
		if nowRunning, _ := transportRunning(transport); nowRunning {
			// 这里只记录所有权，由调用方在模块回滚完成后统一清理。
			s.transportOwned = true
		}
	}
	return startErr
}

func transportRunning(transport capability.ITransport) (running bool, observable bool) {
	fullTransport, ok := any(transport).(messaging.ITransport)
	if !ok || runtimeutil.IsTypedNil(fullTransport) {
		return false, false
	}
	return fullTransport.Stats().Running, true
}

func (s *Host) failFastCircularDependencies() error {
	if s == nil || s.container == nil {
		return nil
	}
	diag, ok := di.Diagnose(s.container)
	if !ok {
		return nil
	}
	if diag == nil || len(diag.CircularDependencies) == 0 {
		return nil
	}
	return errors.NewCode(errors.Dependency, "circular dependencies detected in DI container").
		WithContext("cycles", diag.CircularDependencies)
}
