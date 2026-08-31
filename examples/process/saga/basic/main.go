package main

import (
	"context"
	"fmt"
	"log"

	"gochen-runtime/examples/process/saga/basic/internal/transfer"
)

// main 演示一个最小 Saga 编排流程的成功与补偿分支。
func main() {
	log.SetPrefix("[saga_demo] ")
	ctx := context.Background()
	orch := transfer.NewOrchestrator()

	// 启动流程
	id, err := orch.StartTransfer(ctx, 1001, 2002, 50)
	must(err)
	must(orch.Debit(ctx, id))

	// 情景一：成功
	must(orch.Credit(ctx, id, true))
	st, err := orch.GetState(ctx, id)
	must(err)
	fmt.Printf("TX %s -> step=%v status=%v\n", id, st.Data["step"], st.Data["status"])

	// 情景二：失败补偿
	id2, err := orch.StartTransfer(ctx, 1001, 2002, 75)
	must(err)
	must(orch.Debit(ctx, id2))
	if err := orch.Credit(ctx, id2, false); err != nil {
		// 业务设计：Credit 在 false 参数下必然失败，触发 Saga 补偿逻辑（回滚已完成的 Debit）
		// 这里用 log 记录而非 must，是因为这是预期的业务失败路径，demo 应继续运行展示补偿结果
		log.Printf("Credit failed as expected (triggering compensation): %v", err)
	}
	st2, err := orch.GetState(ctx, id2)
	must(err)
	fmt.Printf("TX %s -> step=%v status=%v\n", id2, st2.Data["step"], st2.Data["status"])
}

// must 在示例里用于快速失败，避免重复展开错误处理。
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
