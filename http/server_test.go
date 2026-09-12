package http

import (
	"crypto/tls"
	"testing"
	"time"

	"gochen/errors"
)

func TestNewHTTPServerHeaderGuards(t *testing.T) {
	cfg := &WebConfig{}
	srv, err := NewHTTPServer(cfg, ":0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.ReadHeaderTimeout != DefaultReadHeaderTimeout || srv.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Fatalf("header guards: timeout=%v max=%d", srv.ReadHeaderTimeout, srv.MaxHeaderBytes)
	}
	if cfg.ReadHeaderTimeout != 0 || cfg.MaxHeaderBytes != 0 {
		t.Fatal("constructor mutated caller configuration")
	}
	cfg.ReadHeaderTimeout, cfg.MaxHeaderBytes = 2*time.Second, 2048
	srv, err = NewHTTPServer(cfg, ":0", nil)
	if err != nil || srv.ReadHeaderTimeout != cfg.ReadHeaderTimeout || srv.MaxHeaderBytes != cfg.MaxHeaderBytes {
		t.Fatalf("explicit header guards: server=%+v err=%v", srv, err)
	}
	cfg.ReadHeaderTimeout = -1
	srv, err = NewHTTPServer(cfg, ":0", nil)
	if err != nil || srv.ReadHeaderTimeout != 0 {
		t.Fatalf("explicitly disabled header timeout: server=%+v err=%v", srv, err)
	}
}

func TestNewHTTPServerPreservesTLSConfiguration(t *testing.T) {
	original := &tls.Config{
		ClientAuth:     tls.RequireAndVerifyClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil },
	}
	cfg := &WebConfig{TLSEnabled: true, TLSConfig: original}
	srv, err := NewHTTPServer(cfg, ":0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.TLSConfig == original || original.MinVersion != 0 {
		t.Fatal("TLS configuration was not independently cloned")
	}
	if srv.TLSConfig.MinVersion != tls.VersionTLS12 || srv.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert || srv.TLSConfig.GetCertificate == nil {
		t.Fatalf("TLS policy was lost: %+v", srv.TLSConfig)
	}
	for _, cfg := range []*WebConfig{
		{TLSEnabled: true, TLSMinVersion: "1.1"},
		{TLSEnabled: true, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS11}},
		{TLSEnabled: true, TLSMinVersion: "1.3", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		{TLSEnabled: true},
		{TLSEnabled: true, CertFile: "certificate-only.pem"},
	} {
		if _, err := NewHTTPServer(cfg, ":0", nil); !errors.Is(err, errors.InvalidInput) {
			t.Errorf("invalid TLS policy accepted: %+v, err=%v", cfg, err)
		}
	}
}
