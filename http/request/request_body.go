package request

import (
	"bytes"
	stdErrors "errors"
	"io"
	"net/http"

	"gochen/codec/jsoncodec"
	"gochen/errors"
	"gochen/httpx"
)

// ReadBody 读取并缓存请求体，统一处理 body size 限制与重复读取。
func ReadBody(w http.ResponseWriter, r *http.Request, values map[string]any, cacheKey string) ([]byte, error) {
	if values != nil {
		if data, ok := valueAs[[]byte](values[cacheKey]); ok {
			return data, nil
		}
	}
	if r == nil {
		return nil, errors.NewCode(errors.InvalidInput, "request cannot be nil")
	}
	if r.Body == nil {
		return nil, errors.NewCode(errors.InvalidInput, "request body cannot be nil")
	}

	ApplyBodyLimit(w, r, values)

	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if stdErrors.As(err, &maxErr) {
			return nil, errors.NewCode(errors.PayloadTooLarge, "request body too large")
		}
		return nil, errors.Wrap(err, errors.Internal, "failed to read request body")
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	if values != nil {
		values[cacheKey] = httpx.ValueOf(data)
	}
	return data, nil
}

// ApplyBodyLimit 按请求上下文中的 MaxBodySizeKey 约定限制请求体读取。
func ApplyBodyLimit(w http.ResponseWriter, r *http.Request, values map[string]any) {
	if r == nil || r.Body == nil {
		return
	}

	if values != nil {
		if v, ok := values[httpx.MaxBodySizeKey]; ok {
			switch limit, ok := valueAs[int64](v); {
			case ok:
				r.Body = http.MaxBytesReader(w, r.Body, normalizeBodyLimit(limit))
			default:
				if limitInt, ok := valueAs[int](v); ok {
					r.Body = http.MaxBytesReader(w, r.Body, normalizeBodyLimit(int64(limitInt)))
				} else {
					r.Body = http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBodySizeBytes)
				}
			}
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBodySizeBytes)
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBodySizeBytes)
	}
}

func normalizeBodyLimit(limit int64) int64 {
	if limit > 0 {
		return limit
	}
	return httpx.DefaultMaxBodySizeBytes
}

// BindJSON 使用统一的严格 JSON 语义绑定请求体。
func BindJSON(w http.ResponseWriter, r *http.Request, values map[string]any, cacheKey string, obj any) error {
	body, err := ReadBody(w, r, values, cacheKey)
	if err != nil {
		return err
	}
	return DecodeStrictJSON(body, obj)
}

// DecodeStrictJSON 按统一严格 JSON 语义解码内存中的请求体。
func DecodeStrictJSON(body []byte, obj any) error {
	if obj == nil {
		return errors.NewCode(errors.InvalidInput, "bind target cannot be nil")
	}
	return jsoncodec.DecodeIntoValue(body, obj,
		jsoncodec.WithDisallowUnknownFields(true),
		jsoncodec.WithRejectTrailingData(true),
	)
}

func valueAs[T any](value any) (T, bool) {
	var zero T
	if typed, ok := value.(T); ok {
		return typed, true
	}
	if wrapped, ok := value.(httpx.ContextValue); ok {
		return httpx.ValueAs[T](wrapped)
	}
	return zero, false
}

// BindQuery 把查询参数绑定到结构体目标。
func BindQuery(target any, values map[string][]string) error {
	if target == nil {
		return errors.NewCode(errors.InvalidInput, "bind target cannot be nil")
	}
	return bindURLValues(target, values)
}
