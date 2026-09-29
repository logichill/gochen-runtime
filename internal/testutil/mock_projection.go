package testutil

import (
	"context"

	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/projection"
	"gochen/messaging"
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

// MockProjectionManager 模拟投影管理器，释放函数只清理其绑定的注册。
type MockProjectionManager struct {
	Registered []string
	Started    []string

	// Released 记录释放的投影名，按调用顺序。
	Released []string
	Log      *[]string

	tokens map[string]any
}

// NewMockProjectionManager 创建空 MockProjectionManager。
func NewMockProjectionManager() *MockProjectionManager {
	return &MockProjectionManager{tokens: make(map[string]any)}
}

// RegisterProjectionAny 注册投影，并返回绑定本次注册的释放函数。
func (m *MockProjectionManager) RegisterProjectionAny(_ context.Context, p any) (messaging.UnsubscribeFunc, error) {
	proj, ok := p.(projection.IProjection[int64])
	if !ok || proj == nil {
		return nil, errors.NewCode(errors.InvalidInput, "projection type mismatch")
	}
	name := proj.Name()
	if m.tokens == nil {
		m.tokens = make(map[string]any)
	}
	token := &mockProjectionToken{name: name}
	m.tokens[name] = token
	m.Registered = append(m.Registered, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:register:"+name)
	}
	return func(context.Context) error {
		if m.tokens[name] != token {
			return nil
		}
		delete(m.tokens, name)
		m.Released = append(m.Released, name)
		if m.Log != nil {
			*m.Log = append(*m.Log, "projection:release:"+name)
		}
		return nil
	}, nil
}

// StartProjection 启动指定投影。
func (m *MockProjectionManager) StartProjection(name string) error {
	m.Started = append(m.Started, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:start:"+name)
	}
	return nil
}

type mockProjectionToken struct {
	name string
}
