package rest

import (
	"bytes"
	"encoding/json"
	"reflect"

	"gochen-runtime/http/request"
	"gochen/codec/jsoncodec"
	"gochen/errors"
	"gochen/httpx"
)

// JSONBodyFields 表示请求体 JSON 对象中的原始字段集合。
type JSONBodyFields map[string]json.RawMessage

// Has 判断字段是否在 JSON body 中显式出现。
func (f JSONBodyFields) Has(key string) bool {
	_, ok := f[key]
	return ok
}

// bindJSONIntoEntity 绑定 JSON 到实体。
func bindJSONIntoEntity[T any](c httpx.IContext, entity *T) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	return bindJSONBodyIntoEntity(body, entity)
}

func bindJSONBodyIntoEntity[T any](body []byte, entity *T) error {
	// 若 T 本身是非 nil 指针类型（例如 *User），优先"就地绑定"到该指针指向的结构体，
	// 避免 json.Unmarshal 通过 **T 替换指针导致已有字段（如 ID）丢失。
	v := reflect.ValueOf(*entity)
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		return request.DecodeStrictJSON(body, v.Interface())
	}
	return request.DecodeStrictJSON(body, entity)
}

func bindStrictJSON(c httpx.IContext, obj any) error {
	if c == nil {
		return errors.NewCode(errors.InvalidInput, "http context cannot be nil")
	}
	body, err := c.Body()
	if err != nil {
		return err
	}
	return request.DecodeStrictJSON(body, obj)
}

// BindOptionalJSON 在请求体为空时跳过绑定，否则按严格 JSON 语义绑定。
func BindOptionalJSON(c httpx.IContext, obj any) error {
	if c == nil {
		return errors.NewCode(errors.InvalidInput, "http context cannot be nil")
	}
	if obj == nil {
		return errors.NewCode(errors.InvalidInput, "bind target cannot be nil")
	}
	body, err := c.Body()
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return request.DecodeStrictJSON(body, obj)
}

// BindJSONBodyFields 按严格 JSON 语义绑定请求体，并返回字段出现信息。
//
// 说明：
// - 该 helper 适合处理“字段是否出现”会影响业务语义的场景；
// - 返回值只表达字段出现与否，不解释具体业务语义；
// - 业务层可据此把 `null / 缺失 / 具体值` 翻译成自己的 patch / command。
func BindJSONBodyFields(c httpx.IContext, obj any) (JSONBodyFields, error) {
	if c == nil {
		return nil, errors.NewCode(errors.InvalidInput, "http context cannot be nil")
	}
	if obj == nil {
		return nil, errors.NewCode(errors.InvalidInput, "bind target cannot be nil")
	}
	body, fields, err := readJSONBodyObject(c)
	if err != nil {
		return nil, err
	}
	if err := request.DecodeStrictJSON(body, obj); err != nil {
		return nil, err
	}
	return JSONBodyFields(fields), nil
}

// parseJSONBodyObject 解析请求体为 JSON 对象。
func parseJSONBodyObject(c httpx.IContext) (map[string]json.RawMessage, error) {
	_, obj, err := readJSONBodyObject(c)
	return obj, err
}

func readJSONBodyObject(c httpx.IContext) ([]byte, map[string]json.RawMessage, error) {
	body, err := c.Body()
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, errors.NewCode(errors.InvalidInput, "empty request body")
	}
	obj, err := jsoncodec.Decode[map[string]json.RawMessage](body)
	if err != nil {
		return nil, nil, err
	}
	if obj == nil {
		return nil, nil, errors.NewCode(errors.InvalidInput, "invalid JSON object")
	}
	return body, obj, nil
}
