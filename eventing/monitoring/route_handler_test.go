package monitoringhttp

import (
	"context"
	"errors"
	"gochen/eventing/monitoring"
	"gochen/testkit/require"
	"testing"
)

func TestHandleRouteSanitizesHealthDetails(t *testing.T) {
	reg, err := monitoring.NewRegistry()
	require.NoError(t, err)
	require.NoError(t, reg.Health.Register("always_unhealthy", func(context.Context) (monitoring.HealthStatus, string, error) {
		return monitoring.HealthStatusUnhealthy, "postgres://user:secret@example/db", nil
	}))

	code, body := HandleRoute(context.Background(), reg, Routes(DefaultRouteSet())[0])
	require.Equal(t, statusServiceUnavailable, code)
	report := body.(monitoring.HealthReport)
	require.NotEmpty(t, report.Checks)
	for _, check := range report.Checks {
		require.Empty(t, check.Message)
		require.Empty(t, check.Error)
	}
}

type failingSnapshotStatsProvider struct{}

func (failingSnapshotStatsProvider) SnapshotStats(context.Context) (monitoring.SnapshotManagerStats, error) {
	return monitoring.SnapshotManagerStats{}, errors.New("postgres://user:secret@example/snapshots")
}

type failingOutboxMetricsProvider struct{}

func (failingOutboxMetricsProvider) Snapshot(context.Context) (*monitoring.OutboxSnapshot, error) {
	return nil, errors.New("postgres://user:secret@example/outbox")
}

func (failingOutboxMetricsProvider) Health(context.Context) (monitoring.HealthStatus, string, error) {
	return monitoring.HealthStatusUnhealthy, "outbox secret", errors.New("outbox health secret")
}

func TestHandleRouteSanitizesSnapshotProviderErrors(t *testing.T) {
	reg, err := monitoring.NewRegistry(
		monitoring.WithSnapshotStatsProvider(failingSnapshotStatsProvider{}),
		monitoring.WithOutboxMetricsProvider(failingOutboxMetricsProvider{}),
	)
	require.NoError(t, err)

	var snapshotRoute Route
	for _, route := range Routes(FullRouteSet()) {
		if route.Name == RouteSnapshot {
			snapshotRoute = route
			break
		}
	}
	require.NotNil(t, snapshotRoute.Handle)

	_, body := HandleRoute(context.Background(), reg, snapshotRoute)
	snapshot := body.(monitoring.Snapshot)
	require.NotNil(t, snapshot.SnapshotManager)
	require.Empty(t, snapshot.SnapshotManager.Error)
	require.NotNil(t, snapshot.Outbox)
	require.Empty(t, snapshot.Outbox.Error)
	for _, check := range snapshot.Health.Checks {
		require.Empty(t, check.Message)
		require.Empty(t, check.Error)
	}
}
