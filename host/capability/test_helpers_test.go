package capability

import (
	"context"

	"gochen-runtime/internal/testutil"
	"gochen/messaging"
)

type mockTransport = testutil.MockTransport

func newMockTransport() *mockTransport {
	return testutil.NewMockTransport()
}

type testEventHandler struct {
	msgType string
}

func newTestEventHandler() *testEventHandler {
	return &testEventHandler{msgType: "test.event"}
}

func (h *testEventHandler) Type() string { return h.msgType }
func (h *testEventHandler) Handle(context.Context, messaging.IMessage) error {
	return nil
}

type multiTypeEventHandler struct {
	msgTypes []string
}

func newMultiTypeEventHandler() *multiTypeEventHandler {
	return &multiTypeEventHandler{msgTypes: []string{"a.event", "b.event"}}
}

func (h *multiTypeEventHandler) Type() string { return "multiTypeEventHandler" }
func (h *multiTypeEventHandler) Handle(context.Context, messaging.IMessage) error {
	return nil
}
func (h *multiTypeEventHandler) EventTypes() []string { return h.msgTypes }

type emptyTypeEventHandler struct{}

func (h *emptyTypeEventHandler) Type() string { return "" }
func (h *emptyTypeEventHandler) Handle(context.Context, messaging.IMessage) error {
	return nil
}

type testProjectionBase = testutil.TestProjectionBase

type testProjection1 struct{ testProjectionBase }
type testProjection2 struct{ testProjectionBase }

func newTestProjection1() *testProjection1 {
	return &testProjection1{testProjectionBase: testutil.NewTestProjectionBase("p1")}
}

func newTestProjection2() *testProjection2 {
	return &testProjection2{testProjectionBase: testutil.NewTestProjectionBase("p2")}
}

type mockProjectionManager = testutil.MockProjectionManager

func newMockProjectionManager() *mockProjectionManager {
	return testutil.NewMockProjectionManager()
}

type unregisteringProjectionManager struct {
	testutil.MockProjectionManager
	unregistered []string
}

type resumingProjectionManager struct {
	testutil.MockProjectionManager
	resumed   []string
	resumeErr error
}

func (m *resumingProjectionManager) ResumeFromCheckpoint(_ context.Context, name string) error {
	m.resumed = append(m.resumed, name)
	return m.resumeErr
}

func (m *unregisteringProjectionManager) UnregisterProjectionWithContext(_ context.Context, name string) error {
	m.unregistered = append(m.unregistered, name)
	if m.Log != nil {
		*m.Log = append(*m.Log, "projection:unregister:"+name)
	}
	return nil
}

type testRuntimeComponent struct {
	startedWith string
	startCount  int
	stopCount   int
	log         *[]string
}

func (c *testRuntimeComponent) Start(context.Context) error {
	c.startCount++
	if c.log != nil {
		*c.log = append(*c.log, "runtime:start")
	}
	if c.startedWith == "" {
		c.startedWith = "test"
	}
	return nil
}

func (c *testRuntimeComponent) Stop(context.Context) error {
	c.stopCount++
	if c.log != nil {
		*c.log = append(*c.log, "runtime:stop")
	}
	return nil
}
