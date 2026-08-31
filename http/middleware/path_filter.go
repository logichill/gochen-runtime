package middleware

import "strings"

type pathFilter struct {
	exact    map[string]struct{}
	prefixes []string
}

func newPathFilter(paths, prefixes []string) pathFilter {
	filter := pathFilter{
		exact:    make(map[string]struct{}, len(paths)),
		prefixes: make([]string, 0, len(prefixes)),
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			filter.exact[path] = struct{}{}
		}
	}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix != "" {
			filter.prefixes = append(filter.prefixes, prefix)
		}
	}
	return filter
}

func (f pathFilter) matches(path string) bool {
	if _, ok := f.exact[path]; ok {
		return true
	}
	for _, prefix := range f.prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
