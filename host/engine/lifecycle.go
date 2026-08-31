// Package engine 定义应用服务的生命周期状态与编排器。
package engine

// State 定义服务器生命周期状态。
type State int

const (
	// StatePending 等待初始化。
	StatePending State = iota
	// StateInitializing 正在初始化配置和基础环境。
	StateInitializing
	// StatePrepared 资源已就绪（依赖检查通过），等待启动。
	StatePrepared
	// StateRunning 服务正在运行。
	StateRunning
	// StateStopping 正在执行优雅关闭。
	StateStopping
	// StateStopped 服务已停止。
	StateStopped
	// StateError 发生不可恢复的错误。
	StateError
)

// String 返回生命周期状态的稳定文本标签，供日志和诊断使用。
func (state State) String() string {
	switch state {
	case StatePending:
		return "Pending"
	case StateInitializing:
		return "Initializing"
	case StatePrepared:
		return "Prepared"
	case StateRunning:
		return "Running"
	case StateStopping:
		return "Stopping"
	case StateStopped:
		return "Stopped"
	case StateError:
		return "Error"
	default:
		return "Unknown"
	}
}
