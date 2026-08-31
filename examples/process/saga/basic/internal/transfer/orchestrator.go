package transfer

import (
	"context"
	"fmt"
	"sync"
)

// State 保存示例 Saga 的当前步骤与附带数据。
type State struct {
	ID   string
	Data map[string]any
}

// Orchestrator 用内存 map 模拟一个最小可运行的 Saga 编排器。
type Orchestrator struct {
	mu    sync.RWMutex
	store map[string]*State
}

// NewOrchestrator 创建一个空的内存编排器。
func NewOrchestrator() *Orchestrator {
	return &Orchestrator{store: make(map[string]*State)}
}

// StartTransfer 创建一条新的转账 Saga 状态并返回流程 ID。
func (o *Orchestrator) StartTransfer(ctx context.Context, from, to int64, amount int) (string, error) {
	id := fmt.Sprintf("tx-%d-%d-%d", from, to, amount)
	o.mu.Lock()
	o.store[id] = &State{ID: id, Data: map[string]any{"from": from, "to": to, "amount": amount, "step": "started", "status": "pending"}}
	o.mu.Unlock()
	return id, nil
}

// Debit 把 Saga 推进到“已扣款”阶段。
func (o *Orchestrator) Debit(ctx context.Context, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.store[id]
	if !ok {
		return fmt.Errorf("tx not found")
	}
	s.Data["step"] = "debited"
	return nil
}

// Credit 根据执行结果推进成功分支或补偿分支。
func (o *Orchestrator) Credit(ctx context.Context, id string, ok bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, exists := o.store[id]
	if !exists {
		return fmt.Errorf("tx not found")
	}
	if ok {
		s.Data["step"] = "credited"
		s.Data["status"] = "success"
	} else {
		s.Data["step"] = "compensated"
		s.Data["status"] = "failed"
	}
	return nil
}

// GetState 返回指定 Saga 当前保存的状态快照。
func (o *Orchestrator) GetState(ctx context.Context, id string) (*State, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	s, ok := o.store[id]
	if !ok {
		return nil, fmt.Errorf("tx not found")
	}
	return s, nil
}
