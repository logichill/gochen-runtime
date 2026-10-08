package monitoringhttp

import (
	"context"
	"gochen/eventing/monitoring"
)

// HandleRoute 执行内置监控路由并返回可安全对外输出的响应体。
func HandleRoute(ctx context.Context, reg *monitoring.Registry, route Route) (int, any) {
	if reg == nil {
		reg = monitoring.DefaultRegistry()
	}
	code, body := route.Handle(ctx, reg)
	return code, sanitizeRouteBody(body)
}

func sanitizeRouteBody(v any) any {
	switch body := v.(type) {
	case monitoring.HealthReport:
		return sanitizeHealthReport(body)
	case monitoring.Snapshot:
		body.Health = sanitizeHealthReport(body.Health)
		if body.SnapshotManager != nil {
			body.SnapshotManager.Error = ""
		}
		if body.Outbox != nil {
			body.Outbox.Error = ""
		}
		return body
	default:
		return v
	}
}

func sanitizeHealthReport(report monitoring.HealthReport) monitoring.HealthReport {
	if len(report.Checks) == 0 {
		return report
	}
	report.Checks = append([]monitoring.CheckResult(nil), report.Checks...)
	for i := range report.Checks {
		report.Checks[i].Message = ""
		report.Checks[i].Error = ""
	}
	return report
}
