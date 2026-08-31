// Package engine 定义应用生命周期编排器及其运行时契约。
package engine

import (
	"context"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"

	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen/errors"
	"gochen/observe/logging"
)

var errBackgroundStartupStillActive = errors.New("background startup is still active")

// -----------------------------------------------------------------------------
// 1. Provider Interface (The Template Hooks)
// -----------------------------------------------------------------------------

// IApp 定义了业务应用必须实现的生命周期钩子。
// 这是一个"模板方法"接口，使用者只需要实现这些特定的步骤。
// 实现者通常是应用层（app包）的具体结构体。
type IApp interface {
	// Name 应用名称
	Name() string

	// LoadConfig 加载配置
	// 步骤 1: 解析配置文件、环境变量、命令行参数
	LoadConfig() error

	// Prepare 初始化依赖
	// 步骤 2: 连接数据库、Redis，构建 Service，组装 Handler (DI)
	Prepare(ctx context.Context) error

	// StartBackground 启动后台任务
	// 步骤 3: 启动 CronJob, MQ Consumer 等非阻塞任务
	StartBackground(ctx context.Context) error

	// Run 启动主服务
	// 步骤 4: 启动 HTTP/gRPC 服务器
	// 注意: 这个方法应当是阻塞的，或者返回一个 error (如果启动失败)
	// 在 Engine 中，我们将把它放入 goroutine 中运行
	Run(ctx context.Context) error

	// Shutdown 清理资源
	// 步骤 5: 关闭连接池、刷新日志、停止后台任务
	Shutdown(ctx context.Context) error
}

// -----------------------------------------------------------------------------
// 2. Engine (The Facade & Orchestrator)
// -----------------------------------------------------------------------------

// Engine 负责编排应用的启动流程。
// 它实现了标准的启动算法：Init -> Setup -> Background -> Run -> Signal -> Shutdown
type Engine struct {
	app     IApp
	options *config.Config
	logger  logging.ILogger
	mu      sync.RWMutex
	state   State
}

// NewEngine 创建一个负责串联完整服务生命周期的编排引擎。
func NewEngine(app IApp, opts ...config.Option) (*Engine, error) {
	return NewEngineConfig(app, config.Build(opts...))
}

// NewEngineConfig 从已构建的共享配置创建生命周期引擎。
func NewEngineConfig(app IApp, cfg *config.Config) (*Engine, error) {
	if app == nil {
		return nil, errors.NewCode(errors.InvalidInput, "app is nil")
	}
	rv := reflect.ValueOf(app)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		if rv.IsNil() {
			return nil, errors.NewCode(errors.InvalidInput, "app is nil")
		}
	}
	cfg = config.Normalize(cfg)
	if cfg.Name == "" {
		cfg.Name = app.Name()
		if cfg.Name == "" {
			cfg.Name = "gochen-app"
		}
	}

	return &Engine{
		app:     app,
		options: cfg,
		logger:  cfg.Logger.WithField("service", cfg.Name),
		state:   StatePending,
	}, nil
}

func (e *Engine) State() State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

func (e *Engine) beginStart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != StatePending {
		return errors.NewCode(errors.FailedPrecondition, "engine has already been started").
			WithContext("state", e.state.String())
	}
	e.state = StateInitializing
	return nil
}

// setState 更新内部生命周期状态。
func (e *Engine) setState(state State) {
	e.mu.Lock()
	e.state = state
	e.mu.Unlock()
}

// Start 执行完整的服务生命周期流程。
//
// 流程顺序固定为：LoadConfig -> Prepare -> StartBackground -> Run
// -> Wait Signal -> Shutdown。
func (e *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if err := e.beginStart(); err != nil {
		return err
	}

	sigCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()

	runCtx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	e.logger.Info(runCtx, "starting application",
		logging.String("version", e.options.Version))

	if err := e.initPhase(runCtx); err != nil {
		if errors.Is(err, context.Canceled) {
			return e.shutdownPhase(runCtx, nil)
		}
		e.setState(StateError)
		return err
	}
	if err := e.setupPhase(runCtx); err != nil {
		if errors.Is(err, context.Canceled) {
			return e.shutdownPhase(runCtx, nil)
		}
		return e.finishFailedStartup(runCtx, err)
	}

	errChan, err := e.startPhase(runCtx, cancel)
	if err != nil {
		if errors.Is(err, errBackgroundStartupStillActive) {
			e.setState(StateError)
			return err
		}
		if errors.Is(err, context.Canceled) {
			return e.shutdownPhase(runCtx, nil)
		}
		return e.finishFailedStartup(runCtx, err)
	}

	runErr := e.waitPhase(runCtx, cancel, errChan)
	return e.shutdownPhase(runCtx, runErr)
}

func (e *Engine) finishFailedStartup(ctx context.Context, cause error) error {
	if err := e.completeStopSequence(ctx); err != nil {
		e.setState(StateError)
		return errors.Join(cause, err)
	}
	e.setState(StateError)
	return cause
}

// initPhase 执行初始化阶段：跑 init hooks 并加载配置。
func (e *Engine) initPhase(ctx context.Context) error {
	if err := e.runHooksFailFast(ctx, e.options.OnBeforeInit, "OnBeforeInit hook failed"); err != nil {
		return err
	}

	if err := e.app.LoadConfig(); err != nil {
		e.setState(StateError)
		return errors.Wrap(err, errors.InvalidInput, "failed to load config")
	}

	if err := e.runHooksFailFast(ctx, e.options.OnAfterInit, "OnAfterInit hook failed"); err != nil {
		return err
	}
	return nil
}

// setupPhase 执行依赖装配阶段，并受启动超时约束。
func (e *Engine) setupPhase(ctx context.Context) error {
	// 给依赖初始化一个超时时间，防止卡死
	setupCtx, setupCancel := context.WithTimeout(ctx, e.options.StartupTimeout)
	defer setupCancel()

	if err := e.app.Prepare(setupCtx); err != nil {
		e.setState(StateError)
		return errors.Wrap(err, errors.Dependency, "failed to setup dependencies")
	}
	e.setState(StatePrepared)
	return nil
}

// startPhase 启动Phase。
func (e *Engine) startPhase(ctx context.Context, cancel context.CancelFunc) (chan error, error) {
	if err := e.runHooksFailFast(ctx, e.options.OnBeforeStart, "OnBeforeStart hook failed"); err != nil {
		return nil, err
	}

	// Start Background Tasks
	//
	// 注意：
	// - StartBackground 的 ctx 是“运行期生命周期 ctx”，其取消应由 Engine shutdown 驱动；
	// - StartupTimeout 仅用于约束“StartBackground 必须在启动期尽快返回”，而不能用来派生传入的 ctx，
	//   否则后台任务会在 startPhase 返回后立刻被取消（导致投影/订阅等后台任务不工作）。
	done := make(chan error, 1)
	go func() {
		done <- e.app.StartBackground(ctx)
	}()

	var bgErr error
	if e.options != nil && e.options.StartupTimeout > 0 {
		select {
		case bgErr = <-done:
			bgErr = runtimeutil.NormalizeError(bgErr)
		case <-time.After(e.options.StartupTimeout):
			bgErr = errors.NewCode(errors.Timeout, "background tasks startup timeout")
			waitCtx := context.WithoutCancel(ctx)
			if cancel != nil {
				cancel()
			}
			if !e.waitStartBackgroundDone(waitCtx, done) {
				bgErr = errors.Join(bgErr, backgroundStartupActiveError("background startup did not stop after timeout"))
			}
		case <-ctx.Done():
			bgErr = ctx.Err()
			if !e.waitStartBackgroundDone(context.WithoutCancel(ctx), done) {
				bgErr = errors.Join(bgErr, backgroundStartupActiveError("background startup did not stop after cancellation"))
			}
		}
	} else {
		select {
		case bgErr = <-done:
			bgErr = runtimeutil.NormalizeError(bgErr)
		case <-ctx.Done():
			bgErr = ctx.Err()
			if !e.waitStartBackgroundDone(context.WithoutCancel(ctx), done) {
				bgErr = errors.Join(bgErr, backgroundStartupActiveError("background startup did not stop after cancellation"))
			}
		}
	}

	if bgErr != nil {
		e.setState(StateError)
		return nil, errors.Wrap(bgErr, errors.Internal, "failed to start background tasks")
	}

	// Run main app entrypoint (non-blocking here, orchestrated by signal).
	e.setState(StateRunning)
	errChan := make(chan error, 1)
	go func() {
		e.logger.Info(ctx, "app is running")
		errChan <- e.app.Run(ctx)
	}()

	e.runHooksWarn(ctx, e.options.OnAfterStart, "OnAfterStart hook failed")
	return errChan, nil
}

func backgroundStartupActiveError(message string) error {
	return errors.NewCodeWithCause(errors.FailedPrecondition, message, errBackgroundStartupStillActive)
}

func (e *Engine) waitStartBackgroundDone(ctx context.Context, done <-chan error) bool {
	if done == nil {
		return true
	}
	wait := time.Second
	if e != nil && e.options != nil && e.options.ShutdownTimeout > 0 && e.options.ShutdownTimeout < wait {
		wait = e.options.ShutdownTimeout
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		if e != nil && e.logger != nil {
			e.logger.Warn(ctx, "background startup goroutine did not stop before shutdown",
				logging.Duration("wait", wait))
		}
		return false
	}
}

// waitPhase 等待Phase。
func (e *Engine) waitPhase(ctx context.Context, cancel context.CancelFunc, errChan <-chan error) error {
	var runErr error
	select {
	case err := <-errChan:
		err = runtimeutil.NormalizeError(err)
		if err == nil {
			e.logger.Info(ctx, "app stopped gracefully, shutting down")
			cancel()
			return nil
		}
		e.logger.Error(ctx, "app stopped with error, shutting down", logging.Error(err))
		cancel()
		runErr = err
	case <-ctx.Done():
		// ctx 可能来自 signal.NotifyContext 或上游取消；这里统一进入 shutdown 流程。
		e.logger.Info(ctx, "context cancelled, shutting down")
		cancel()
	}
	return runErr
}

// shutdownPhase 关闭Phase。
func (e *Engine) shutdownPhase(ctx context.Context, runErr error) error {
	if err := e.completeStopSequence(ctx); err != nil {
		e.setState(StateError)
		if runErr != nil {
			return errors.Join(errors.Wrap(runErr, errors.Internal, "app execution error"), err)
		}
		return err
	}

	if runErr != nil {
		e.setState(StateError)
		return errors.Wrap(runErr, errors.Internal, "app execution error")
	}

	e.setState(StateStopped)
	e.logger.Info(ctx, "shutdown complete")
	return nil
}

// completeStopSequence 执行统一的停止序列：stop hooks -> app shutdown -> after hooks。
func (e *Engine) completeStopSequence(ctx context.Context) error {
	e.setState(StateStopping)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), e.options.ShutdownTimeout)
	defer shutdownCancel()

	e.runHooksWarn(shutdownCtx, e.options.OnBeforeStop, "OnBeforeStop hook failed")

	if err := e.app.Shutdown(shutdownCtx); err != nil {
		e.logger.Error(shutdownCtx, "shutdown error", logging.Error(err))
		return err
	}

	e.runHooksWarn(shutdownCtx, e.options.OnAfterStop, "OnAfterStop hook failed")
	return nil
}

// runHooksFailFast 执行钩子集合FailFast。
func (e *Engine) runHooksFailFast(ctx context.Context, hooks []config.Hook, wrapMsg string) error {
	for _, hook := range hooks {
		if err := hook(ctx); err != nil {
			return errors.Wrap(err, errors.Internal, wrapMsg)
		}
	}
	return nil
}

// runHooksWarn 执行钩子集合Warn。
func (e *Engine) runHooksWarn(ctx context.Context, hooks []config.Hook, warnMsg string) {
	for _, hook := range hooks {
		if err := hook(ctx); err != nil {
			e.logger.Warn(ctx, warnMsg, logging.Error(err))
		}
	}
}
