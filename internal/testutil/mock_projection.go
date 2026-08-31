package testutil

import (
	"context"

	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/projection"
)

// TestProjectionBase 提供通用的测试投影基类。
type TestProjectionBase struct {
	ProjectionName string
}

// NewTestProjectionBase 创建指定名称的测试投影基类。
func NewTestProjectionBase(name string) TestProjectionBase {
	return TestProjectionBase{ProjectionName: name}
}

// Name 返回投影名称。
func (p *TestProjectionBase) Name() string { return p.ProjectionName }

// Handle 处理事件。
func (p *TestProjectionBase) Handle(context.Context, eventing.IEvent) error { return nil }

// SupportedEventTypes 返回支持的事件类型。
func (p *TestProjectionBase) SupportedEventTypes() []string { return []string{"*"} }

// Rebuild 重建投影数据。
func (p *TestProjectionBase) Rebuild(context.Context, []eventing.Event[int64]) error { return nil }

// Status 返回投影状态。
func (p *TestProjectionBase) Status() projection.ProjectionStatus {
	return projection.ProjectionStatus{Name: p.ProjectionName, Status: "stopped"}
}

// MockProjectionManager 模拟投影管理器。
type MockProjectionManager struct {
	Registered []string
	Started    []string
	Stopped    []string
	Log        *[]string
}

// NewMockProjectionManager 创建空 MockProjectionManager。
func NewMockProjectionManager() *MockProjectionManager {
	return &MockProjectionManager{}
}

// RegisterProjectionAny 注册任意类型投影。
func (m *MockProjectionManager) RegisterProjectionAny(ctx context.Context, p any) error {
	proj, ok := p.(projection.IProjection[int64])
	if !ok {
		return errors.NewCode(errors.InvalidInput, "projection type mismatch")
	}
	return m.RegisterProjectionWithContext(ctx, proj)
}

// RegisterProjectionWithContext 注册投影。
func (m *MockProjectionManager) RegisterProjectionWithContext(_ context.Context, p projection.IProjection[int64]) error {
	name := p.Name()
	m.Registered = append(m.Registered, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:register:"+name)
	}
	return nil
}

// StartProjection 启动指定投影。
func (m *MockProjectionManager) StartProjection(name string) error {
	m.Started = append(m.Started, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:start:"+name)
	}
	return nil
}

// StopProjection 停止指定投影。
func (m *MockProjectionManager) StopProjection(name string) error {
	m.Stopped = append(m.Stopped, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:stop:"+name)
	}
	return nil
}
