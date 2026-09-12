package http

import (
	"crypto/tls"
	"net"
	nethttp "net/http"
	"strconv"
	"strings"

	"gochen/errors"
)

// NewHTTPServer 按统一的超时、请求头和 TLS 规则构造底层 HTTP 服务。
// 配置及 TLSConfig 会被复制；启用 TLS 时必须提供文件证书或内存证书来源。
func NewHTTPServer(config *WebConfig, addr string, handler nethttp.Handler) (*nethttp.Server, error) {
	cfg := *DefaultWebConfig()
	if config != nil {
		cfg = *config
	}
	if cfg.Host == "" {
		cfg.Host = DefaultWebHost
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultWebPort
	}
	if addr == "" {
		addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = DefaultReadHeaderTimeout
	} else if cfg.ReadHeaderTimeout < 0 {
		cfg.ReadHeaderTimeout = 0
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = DefaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	srv := &nethttp.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	if !cfg.TLSEnabled {
		return srv, nil
	}

	minVersion, err := parseTLSMinVersion(cfg.TLSMinVersion)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{}
	if cfg.TLSConfig != nil {
		tlsCfg = cfg.TLSConfig.Clone()
	}
	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = minVersion
	} else if tlsCfg.MinVersion < minVersion {
		return nil, errors.NewCode(errors.InvalidInput, "tls config min version lower than tls_min_version").
			WithContext("tls_min_version", cfg.TLSMinVersion).
			WithContext("tls_config_min_version", tlsCfg.MinVersion)
	}
	if len(tlsCfg.NextProtos) == 0 {
		tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, errors.NewCode(errors.InvalidInput, "tls certificate and key files must be configured together")
	}
	if cfg.CertFile == "" && len(tlsCfg.Certificates) == 0 && tlsCfg.GetCertificate == nil && tlsCfg.GetConfigForClient == nil {
		return nil, errors.NewCode(errors.InvalidInput, "tls enabled but no certificate configured")
	}
	srv.TLSConfig = tlsCfg
	return srv, nil
}

func parseTLSMinVersion(raw string) (uint16, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "default", "1.2", "tls1.2", "tls12", "tlsv1.2":
		return tls.VersionTLS12, nil
	case "1.3", "tls1.3", "tls13", "tlsv1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, errors.NewCode(errors.InvalidInput, "invalid tls_min_version; require TLS 1.2 or later").WithContext("value", raw)
	}
}
