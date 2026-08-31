package runtime

import (
	"context"
	"fmt"
	"net/http"
	"slices"

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
//   - Transport 仅在所有模块均已成功停止后才被停止：若有模块 stop 失败，说明该模块仍在运行，
//     Transport 须继续服务，不能提前关闭。
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
		var remaining []module.ModuleStopFunc
		for i := len(s.moduleStops) - 1; i >= 0; i-- {
			stop := s.moduleStops[i]
			if stop == nil {
				continue
			}
			if err := stop(ctx); err != nil {
				errs = append(errs, errors.Wrap(err, errors.Internal, "failed to stop module"))
				remaining = append(remaining, stop)
			}
		}
		// remaining 是倒序收集的，翻转后恢复原始注册顺序，
		// 使下次 Shutdown 重试时仍能按注册倒序停止。
		slices.Reverse(remaining)
		s.moduleStops = remaining
	}

	backgroundStopped := len(s.moduleStops) == 0
	if backgroundStopped && s.runtime != nil && s.runtime.Transport != nil {
		if err := messaging.StopTransport(ctx, s.runtime.Transport); err != nil {
			errs = append(errs, errors.Wrap(err, errors.Internal, "failed to stop message transport"))
			backgroundStopped = false
		}
	}

	if backgroundStopped {
		s.backgroundStarted.Store(false)
	}
	return errors.Join(errs...)
}

// rollbackModuleStops 回滚模块停止函数集合，并保留失败项供 Shutdown 重试。
func (s *Host) rollbackModuleStops(ctx context.Context, stops []module.ModuleStopFunc) ([]module.ModuleStopFunc, error) {
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
			errs = append(errs, errors.Wrap(err, errors.Internal, "failed to roll back module"))
			remaining = append(remaining, stop)
		}
	}
	slices.Reverse(remaining)
	return remaining, errors.Join(errs...)
}
