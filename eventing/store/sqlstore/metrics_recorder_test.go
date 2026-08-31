package sqlstore

import (
	"testing"
	"time"
)

type testEventStoreRecorderA struct{ saved int }

func (r *testEventStoreRecorderA) RecordEventSaved(count int, _ time.Duration) { r.saved += count }
func (r *testEventStoreRecorderA) RecordEventLoaded(_ int, _ time.Duration)    {}
func (r *testEventStoreRecorderA) RecordEventStoreError()                      {}

type testEventStoreRecorderB struct{}

func (r testEventStoreRecorderB) RecordEventSaved(_ int, _ time.Duration)  {}
func (r testEventStoreRecorderB) RecordEventLoaded(_ int, _ time.Duration) {}
func (r testEventStoreRecorderB) RecordEventStoreError()                   {}

func TestSQLEventStore_SetMetricsRecorder_AllowsNilAndDifferentTypes(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("expected no panic, got %v", rec)
		}
	}()

	s := &SQLEventStore[int64]{}
	a := &testEventStoreRecorderA{}

	s.SetMetricsRecorder(a)
	s.recordEventSaved(2, 0)
	if a.saved != 2 {
		t.Fatalf("expected saved=2, got %d", a.saved)
	}

	// Switching implementations should never panic.
	s.SetMetricsRecorder(testEventStoreRecorderB{})
	s.SetMetricsRecorder(nil)
	s.SetMetricsRecorder(a)
}
