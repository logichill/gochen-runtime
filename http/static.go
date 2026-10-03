package http

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gochen-runtime/internal/httpheader"
)

const defaultStaticCacheMaxAgeSeconds = 3600

// NewStaticHandler 提供统一的静态目录服务：拒绝隐藏路径和越界符号链接，只允许目录中的 index.html。
// 路由前缀由适配器在调用前剥离；.well-known 保持可访问。
func NewStaticHandler(root string) http.Handler {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		})
	}

	rootResolved := rootAbs
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootResolved = resolved
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", httpheader.BuildAllowHeader(map[string]struct{}{
				http.MethodGet:  {},
				http.MethodHead: {},
			}))
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		rel, ok := cleanStaticRelPath(r.URL.Path)
		if !ok || hasDisallowedHiddenSegment(rel) {
			http.NotFound(w, r)
			return
		}

		full := rootAbs
		if rel != "" {
			full = filepath.Join(rootAbs, filepath.FromSlash(rel))
		}
		fullAbs, err := filepath.Abs(full)
		if err != nil || !isWithinRoot(rootAbs, fullAbs) {
			http.NotFound(w, r)
			return
		}

		resolvedPath, err := filepath.EvalSymlinks(fullAbs)
		if err != nil || !isAllowedStaticTarget(rootResolved, resolvedPath) {
			http.NotFound(w, r)
			return
		}

		fi, err := os.Stat(resolvedPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		servedPath := resolvedPath
		servedName := filepath.Base(resolvedPath)
		servedInfo := fi

		if fi.IsDir() {
			// 目录请求：只允许 index.html；不提供目录列表。
			// 注意：由于外层使用了 StripPrefix，r.URL.Path 可能为空字符串（例如请求 /static/）。
			// 该场景等价于根目录请求，应该直接尝试 index.html，而不是重定向到 "./"。
			if r.URL.Path != "" && !strings.HasSuffix(r.URL.Path, "/") {
				// 让客户端相对原始 URL 解析；http.Redirect 会使用已剥离前缀的路径补全地址。
				location := url.URL{Path: "./" + path.Base(r.URL.Path) + "/", RawQuery: r.URL.RawQuery}
				w.Header().Set("Location", location.String())
				w.WriteHeader(http.StatusMovedPermanently)
				return
			}

			indexPath := filepath.Join(resolvedPath, "index.html")
			resolvedIndexPath, err := filepath.EvalSymlinks(indexPath)
			if err != nil || !isAllowedStaticTarget(rootResolved, resolvedIndexPath) {
				http.NotFound(w, r)
				return
			}

			idxInfo, err := os.Stat(resolvedIndexPath)
			if err != nil || idxInfo.IsDir() {
				http.NotFound(w, r)
				return
			}
			servedPath = resolvedIndexPath
			servedName = "index.html"
			servedInfo = idxInfo
		}

		// 静态资源安全默认：避免 MIME sniff。
		if w.Header().Get("X-Content-Type-Options") == "" {
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		// 必要缓存策略：HTML 默认 no-cache，其余资源默认短缓存。
		if w.Header().Get("Cache-Control") == "" {
			ext := strings.ToLower(path.Ext(servedName))
			if ext == ".html" || ext == ".htm" {
				w.Header().Set("Cache-Control", "no-cache")
			} else {
				w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", defaultStaticCacheMaxAgeSeconds))
			}
		}

		f, err := os.Open(servedPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()

		// ServeContent 支持 Range/If-Modified-Since 等，且不会产生目录列表。
		http.ServeContent(w, r, servedName, servedInfo.ModTime(), io.NewSectionReader(f, 0, servedInfo.Size()))
	})
}

// isAllowedStaticTarget 对符号链接解析后的路径执行与请求路径相同的隐藏文件限制。
func isAllowedStaticTarget(root, target string) bool {
	if !isWithinRoot(root, target) {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || !hasDisallowedHiddenSegment(filepath.ToSlash(rel))
}

func cleanStaticRelPath(raw string) (string, bool) {
	raw = strings.TrimPrefix(raw, "/")
	raw = path.Clean(raw)
	if raw == "." {
		return "", true
	}
	// 相对路径下，path.Clean 不会抹掉 leading ".."；对其显式拒绝。
	if strings.HasPrefix(raw, "..") || strings.Contains(raw, "/..") {
		return "", false
	}
	return raw, true
}

// hasDisallowedHiddenSegment 判断DisallowedHiddenSegment。
func hasDisallowedHiddenSegment(rel string) bool {
	if rel == "" {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, ".") {
			// 例外：.well-known 常用于 ACME/标准化发现路径；保留默认可用性。
			if seg == ".well-known" {
				continue
			}
			return true
		}
	}
	return false
}

// isWithinRoot 判断WithinRoot。
func isWithinRoot(rootAbs, fullAbs string) bool {
	if rootAbs == fullAbs {
		return true
	}
	sep := string(os.PathSeparator)
	root := rootAbs
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(fullAbs, root)
}
