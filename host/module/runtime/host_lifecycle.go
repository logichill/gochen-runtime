package runtime

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen/errors"
	"gochen/messaging"
)

// Run 启动主服务并阻塞运行。
//
// 说明：
// - Run 实现 IServer.Run：启动 HTTP 服务并阻塞等待退出。
func (s *Host) Run(ctx context.Context) error {
	if s.runtime == nil || s.runtime.HTTPServer == nil {
		return errors.NewCode(errors.Internal, "HTTP server not initialized")
	}

	addr := ""
	if s.config != nil && s.config.Port > 0 {
		host := s.config.Host
		if host == "" {
			addr = fmt.Sprintf(":%d", s.config.Port)
		} else {
			addr = fmt.Sprintf("%s:%d", host, s.config.Port)
		}
	}

	if err := s.runtime.HTTPServer.Start(addr); err != nil {
		// 对于基于 net/http 的实现，ErrServerClosed 视为正常退出
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return nil
}

// Shutdown 优雅停止并释放资源。
//
// 说明：
//   - Shutdown 实现 IServer.Shutdown：先停 HTTP 服务，再按注册倒序停模块，最后关消息传输层；
//   - 任一步出错不会中断后续步骤；所有错误用 errors.Join 聚合返回；
//   - 失败的模块 stop 函数会保留在 s.moduleStops，以便上层重试或诊断；
//   - 仅停止本 Host 启动取得的 Transport，且须等待所有模块停止成功；
//     Transport 清理失败时保留所有权供重试，启动前已运行的实例不受影响；
//   - Shutdown 遵循调用方 ctx 的取消与超时；需要独立清理预算的调用方（如 Engine）应自行派生
//     带超时且不随退出信号取消的 context 后再调用。
func (s *Host) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	var errs []error

	if s.runtime != nil && s.runtime.HTTPServer != nil {
		if err := s.runtime.HTTPServer.Stop(ctx); err != nil {
			errs = append(errs, errors.Wrap(err, errors.Internal, "failed to stop HTTP server"))
		}
	}

	if len(s.moduleStops) > 0 {
		remaining, stopErr := s.stopModules(ctx, s.moduleStops, "failed to stop module")
		if stopErr != nil {
			errs = append(errs, stopErr)
		}
		s.moduleStops = remaining
	}

	backgroundStopped := len(s.moduleStops) == 0
	if backgroundStopped {
		if err := s.stopOwnedTransport(ctx); err != nil {
			errs = append(errs, errors.Wrap(err, errors.Internal, "failed to stop message transport"))
			backgroundStopped = false
		}
	}

	if backgroundStopped {
		s.backgroundStarted.Store(false)
	}
	return errors.Join(errs...)
}

func (s *Host) stopOwnedTransport(ctx context.Context) error {
	if !s.transportOwned {
		return nil
	}
	if err := messaging.StopTransport(ctx, s.runtime.Transport); err != nil {
		return err
	}
	s.transportOwned = false
	return nil
}

// cleanupContext 为启动失败回滚派生独立清理预算：保留调用方 ctx 的取值，
// 但不继承其取消/超时，避免已启动资源因启动失败时的取消而无法回收。
func (s *Host) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := config.DefaultShutdownTimeout
	if s != nil && s.config != nil && s.config.ShutdownTimeout > 0 {
		timeout = s.config.ShutdownTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// stopModules 按注册倒序调用模块停止函数，失败项按原顺序保留供下次重试。
func (s *Host) stopModules(ctx context.Context, stops []module.ModuleStopFunc, failureMessage string) ([]module.ModuleStopFunc, error) {
	if len(stops) == 0 {
		return nil, nil
	}
	var errs []error
	remaining := make([]module.ModuleStopFunc, 0, len(stops))
	for i := len(stops) - 1; i >= 0; i-- {
		stop := stops[i]
		if stop == nil {
			continue
		}
		if err := stop(ctx); err != nil {
			errs = append(errs, errors.Wrap(err, errors.Internal, failureMessage))
			remaining = append(remaining, stop)
		}
	}
	// remaining 是倒序收集的，翻转后恢复原始注册顺序，
	// 使下次停止重试时仍能按注册倒序执行。
	slices.Reverse(remaining)
	return remaining, errors.Join(errs...)
}
