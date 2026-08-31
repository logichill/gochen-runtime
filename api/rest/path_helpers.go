package rest

import (
	"strings"

	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/errors"
	"gochen/httpx"
)

// ParsePathWithCodec 使用 codec 解析路径参数。
func ParsePathWithCodec[T any](c httpx.IContext, key string, codec codec.ICodec[T, any], invalidMsg string) (T, error) {
	var zero T
	if c == nil {
		return zero, errors.NewCode(errors.InvalidInput, "http context cannot be nil")
	}
	if codec == nil {
		return zero, errors.NewCode(errors.InvalidInput, "path codec cannot be nil")
	}
	if strings.TrimSpace(invalidMsg) == "" {
		invalidMsg = "invalid path parameter"
	}
	value := strings.TrimSpace(c.Param(key))
	decoded, err := codec.Decode(value)
	if err != nil {
		return zero, errors.Wrap(err, errors.InvalidInput, invalidMsg)
	}
	return decoded, nil
}

// ParsePath 使用默认 ID codec 解析路径参数。
func ParsePath[T comparable](c httpx.IContext, key, invalidMsg string) (T, error) {
	var zero T
	pathCodec, err := idcodec.NewDefault[T]()
	if err != nil {
		return zero, errors.Wrap(err, errors.InvalidInput, "failed to build path codec")
	}
	return ParsePathWithCodec(c, key, pathCodec, invalidMsg)
}
