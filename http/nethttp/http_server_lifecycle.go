package nethttp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	runtimehttp "gochen-runtime/http"
	"gochen/errors"
)

const defaultStaticCacheMaxAgeSeconds = 3600

// Start 启动底层 net/http 服务，并在启用 TLS 时完成证书与版本校验。
func (s *Server) Start(addr string) error {
	if err := s.beginStart(); err != nil {
		return err
	}

	if addr == "" {
		addr = fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	}

	if err := s.applySecurityDefaults(); err != nil {
		return s.failStart(err)
	}
	s.applyTimeoutDefaults()

	s.registerRoutes()
	srv, err := s.newHTTPServer(addr)
	if err != nil {
		return s.failStart(err)
	}
	if s.config.TLSEnabled {
		// 证书来源二选一：
		// 1) CertFile/KeyFile；或
		// 2) TLSConfig 内置证书（Certificates/GetCertificate/GetConfigForClient）。
		if s.config.CertFile != "" || s.config.KeyFile != "" {
			if err := s.publishServer(srv); err != nil {
				return err
			}
			return s.finishServe(srv, srv.ListenAndServeTLS(s.config.CertFile, s.config.KeyFile))
		}
		if srv.TLSConfig == nil {
			return s.failStart(errors.NewCode(errors.InvalidInput, "tls enabled but tls config is nil"))
		}
		if len(srv.TLSConfig.Certificates) == 0 && srv.TLSConfig.GetCertificate == nil && srv.TLSConfig.GetConfigForClient == nil {
			return s.failStart(errors.NewCode(errors.InvalidInput, "tls enabled but no certificate configured"))
		}
		if err := s.publishServer(srv); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return s.finishServe(srv, err)
		}
		return s.finishServe(srv, srv.Serve(tls.NewListener(ln, srv.TLSConfig)))
	}
	if err := s.publishServer(srv); err != nil {
		return err
	}
	return s.finishServe(srv, srv.ListenAndServe())
}

func (s *Server) beginStart() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	switch s.lifecycle {
	case serverLifecycleStopped:
		return http.ErrServerClosed
	case serverLifecycleStarting, serverLifecycleServing:
		return errors.NewCode(errors.Conflict, "http server is already starting or running")
	default:
		s.lifecycle = serverLifecycleStarting
		return nil
	}
}

func (s *Server) failStart(startErr error) error {
	s.lifecycleMu.Lock()
	stopped := s.lifecycle == serverLifecycleStopped
	if !stopped {
		s.lifecycle = serverLifecycleIdle
	}
	s.lifecycleMu.Unlock()
	if stopped {
		return http.ErrServerClosed
	}
	return startErr
}

func (s *Server) publishServer(srv *http.Server) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycle == serverLifecycleStopped {
		return http.ErrServerClosed
	}
	if s.lifecycle != serverLifecycleStarting {
		return errors.NewCode(errors.Conflict, "http server start state changed unexpectedly")
	}
	s.server = srv
	s.lifecycle = serverLifecycleServing
	return nil
}

func (s *Server) finishServe(srv *http.Server, serveErr error) error {
	s.lifecycleMu.Lock()
	if s.server == srv {
		s.server = nil
	}
	if s.lifecycle != serverLifecycleStopped {
		s.lifecycle = serverLifecycleIdle
	}
	s.lifecycleMu.Unlock()
	return serveErr
}

func (s *Server) serverActive() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.lifecycle == serverLifecycleStarting || s.lifecycle == serverLifecycleServing
}

// newHTTPServer 根据当前 WebConfig 构造底层 `*http.Server`。
func (s *Server) newHTTPServer(addr string) (*http.Server, error) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    s.config.MaxHeaderBytes,
	}

	if !s.config.TLSEnabled {
		return srv, nil
	}

	var tlsCfg *tls.Config
	if s.config.TLSConfig != nil {
		tlsCfg = s.config.TLSConfig.Clone()
	} else {
		tlsCfg = &tls.Config{}
	}

	minVersion, err := parseTLSMinVersion(s.config.TLSMinVersion)
	if err != nil {
		return nil, err
	}
	// 如果调用方未显式指定，则使用 TLSMinVersion（或默认 TLS1.2）。
	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = minVersion
	} else if tlsCfg.MinVersion < minVersion {
		return nil, errors.NewCode(errors.InvalidInput, "tls config min version lower than tls_min_version").
			WithContext("tls_min_version", s.config.TLSMinVersion).
			WithContext("tls_config_min_version", tlsCfg.MinVersion)
	}
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		return nil, errors.NewCode(errors.InvalidInput, "tls config min version too low").WithContext("value", tlsCfg.MinVersion)
	}
	if len(tlsCfg.NextProtos) == 0 {
		tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	}
	srv.TLSConfig = tlsCfg

	return srv, nil
}

// applyTimeoutDefaults 为未显式设置的超时与 header 限制补上安全默认值。
func (s *Server) applyTimeoutDefaults() {
	if s.config == nil {
		s.config = &runtimehttp.WebConfig{}
	}
	if s.config.ReadHeaderTimeout == 0 {
		s.config.ReadHeaderTimeout = runtimehttp.DefaultReadHeaderTimeout
	}
	if s.config.ReadHeaderTimeout < 0 {
		s.config.ReadHeaderTimeout = 0
	}
	if s.config.ReadTimeout <= 0 {
		s.config.ReadTimeout = runtimehttp.DefaultReadTimeout
	}
	if s.config.WriteTimeout <= 0 {
		s.config.WriteTimeout = runtimehttp.DefaultWriteTimeout
	}
	if s.config.IdleTimeout <= 0 {
		s.config.IdleTimeout = runtimehttp.DefaultIdleTimeout
	}
	if s.config.MaxHeaderBytes <= 0 {
		s.config.MaxHeaderBytes = runtimehttp.DefaultMaxHeaderBytes
	}
}

// applySecurityDefaults 解析 TLS 与受信任代理配置，并执行安全基线校验。
func (s *Server) applySecurityDefaults() error {
	if s.config == nil {
		s.config = &runtimehttp.WebConfig{}
	}

	// TLS：校验最低版本（安全基线为 TLS 1.2）。
	if s.config.TLSEnabled {
		minVersion, err := parseTLSMinVersion(s.config.TLSMinVersion)
		if err != nil {
			return err
		}
		if minVersion < tls.VersionTLS12 {
			return errors.NewCode(errors.InvalidInput, "tls_min_version too low").WithContext("value", s.config.TLSMinVersion)
		}
		// 若用户通过 TLSConfig 显式设置了更低版本，也拒绝。
		if s.config.TLSConfig != nil && s.config.TLSConfig.MinVersion != 0 && s.config.TLSConfig.MinVersion < tls.VersionTLS12 {
			return errors.NewCode(errors.InvalidInput, "tls config min version too low").WithContext("value", s.config.TLSConfig.MinVersion)
		}
	}

	trustedProxyAddrs := make(map[netip.Addr]struct{})
	trustedProxyPrefixes := make([]netip.Prefix, 0, len(s.config.TrustedProxies))

	for _, raw := range s.config.TrustedProxies {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if strings.Contains(v, "/") {
			prefix, err := netip.ParsePrefix(v)
			if err != nil {
				return errors.NewCode(errors.InvalidInput, "invalid trusted proxy CIDR").WithContext("value", v)
			}
			trustedProxyPrefixes = append(trustedProxyPrefixes, prefix)
			continue
		}

		addr, err := netip.ParseAddr(v)
		if err != nil {
			return errors.NewCode(errors.InvalidInput, "invalid trusted proxy IP").WithContext("value", v)
		}
		trustedProxyAddrs[addr] = struct{}{}
	}

	s.mu.Lock()
	s.trustedProxyAddrs = trustedProxyAddrs
	s.trustedProxyPrefixes = trustedProxyPrefixes
	s.mu.Unlock()

	return nil
}

// parseTLSMinVersion 把配置字符串解析为 `crypto/tls` 使用的版本常量。
func parseTLSMinVersion(raw string) (uint16, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	switch v {
	case "", "default":
		return tls.VersionTLS12, nil
	case "1.0", "tls1.0", "tls10", "tlsv1.0":
		return tls.VersionTLS10, nil
	case "1.1", "tls1.1", "tls11", "tlsv1.1":
		return tls.VersionTLS11, nil
	case "1.2", "tls1.2", "tls12", "tlsv1.2":
		return tls.VersionTLS12, nil
	case "1.3", "tls1.3", "tls13", "tlsv1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, errors.NewCode(errors.InvalidInput, "invalid tls_min_version").WithContext("value", raw)
	}
}

// isTrustedProxy 判断 addr 是否属于受信任代理。
//
// 参数：
// - addr：待判断的代理 IP
func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.trustedProxyAddrs[addr]; ok {
		return true
	}
	for _, p := range s.trustedProxyPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Stop 在给定上下文约束下优雅关闭底层 HTTP 服务。
func (s *Server) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	s.lifecycleMu.Lock()
	s.lifecycle = serverLifecycleStopped
	srv := s.server
	s.lifecycleMu.Unlock()
	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

// Handler 返回底层 http.Handler。
func (s *Server) Handler() http.Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.muxExposed = true
	s.ensureMuxLocked()
	return s.mux
}

// ServeMux 返回底层 *http.ServeMux。
func (s *Server) ServeMux() *http.ServeMux {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.muxExposed = true
	s.ensureMuxLocked()
	return s.mux
}

// registerRoutes 注册全部路由。
