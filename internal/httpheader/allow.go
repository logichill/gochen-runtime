package httpheader

import (
	"net/http"
	"sort"
	"strings"
)

// BuildAllowHeader formats an Allow header in a stable order and always includes OPTIONS.
func BuildAllowHeader(methods map[string]struct{}) string {
	if len(methods) == 0 {
		return http.MethodOptions
	}
	remaining := make(map[string]struct{}, len(methods)+1)
	for method := range methods {
		remaining[method] = struct{}{}
	}
	remaining[http.MethodOptions] = struct{}{}
	known := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
	}
	out := make([]string, 0, len(remaining))
	for _, method := range known {
		if _, ok := remaining[method]; ok {
			out = append(out, method)
			delete(remaining, method)
		}
	}
	extra := make([]string, 0, len(remaining))
	for method := range remaining {
		extra = append(extra, method)
	}
	sort.Strings(extra)
	out = append(out, extra...)
	return strings.Join(out, ", ")
}
