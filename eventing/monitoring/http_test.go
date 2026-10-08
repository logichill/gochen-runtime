package monitoringhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gochen/eventing/monitoring"
	"gochen/testkit/require"
)

func TestHandlerExposesSafeDefaults(t *testing.T) {
	reg, err := monitoring.NewRegistry()
	require.NoError(t, err)
	require.NoError(t, reg.Health.Register("always_unhealthy", func(context.Context) (monitoring.HealthStatus, string, error) {
		return monitoring.HealthStatusUnhealthy, "postgres://user:secret@example/db", nil
	}))
	h := NewHandler(reg)

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)

		var report monitoring.HealthReport
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&report))
		for _, check := range report.Checks {
			require.Empty(t, check.Message)
			require.Empty(t, check.Error)
		}
	}

	for _, path := range []string{"/metrics", "/snapshot"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}

func TestHandlerExposesMonitoringWhenEnabled(t *testing.T) {
	reg, err := monitoring.NewRegistry()
	require.NoError(t, err)
	require.NoError(t, reg.Health.Register("always_unhealthy", func(context.Context) (monitoring.HealthStatus, string, error) {
		return monitoring.HealthStatusUnhealthy, "boom", errors.New("postgres://user:secret@example/db")
	}))
	reg.Metrics.RecordEventSaved(2, 3*time.Millisecond)
	h := NewHandlerWithRoutes(reg, FullRouteSet())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var summary monitoring.Summary
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&summary))
	require.GreaterOrEqual(t, summary.EventStore.EventsSaved, int64(2))
}
