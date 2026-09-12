package request

import (
	"net/netip"
	"strings"

	"gochen/errors"
)

// NewTrustedProxyChecker 校验可信代理 IP/CIDR，并返回可并发调用的匹配函数。
// 空列表不信任任何代理头，返回 nil 供 ResolveClientIP 使用。
func NewTrustedProxyChecker(trusted []string) (func(netip.Addr) bool, error) {
	if len(trusted) == 0 {
		return nil, nil
	}
	addrs := make(map[netip.Addr]struct{}, len(trusted))
	var prefixes []netip.Prefix
	for _, raw := range trusted {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, errors.NewCode(errors.InvalidInput, "invalid trusted proxy CIDR").WithContext("value", value)
			}
			prefixes = append(prefixes, prefix)
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, errors.NewCode(errors.InvalidInput, "invalid trusted proxy IP").WithContext("value", value)
		}
		addrs[addr] = struct{}{}
	}
	return func(addr netip.Addr) bool {
		if _, ok := addrs[addr]; ok {
			return true
		}
		for _, prefix := range prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}, nil
}
