package contracttest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	runtimehttp "gochen-runtime/http"
)

func runTLS(t *testing.T, factory Factory) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(t.TempDir(), "server.crt"), filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid generated certificate")
	}
	for _, setting := range []string{"min_tls_1_3", "required_client_certificate", "in_memory_certificate"} {
		t.Run(setting, func(t *testing.T) {
			logging := false
			cfg := &runtimehttp.WebConfig{Mode: "test", EnableRequestLog: &logging, TLSEnabled: true, CertFile: certFile, KeyFile: keyFile}
			client := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			switch setting {
			case "min_tls_1_3":
				cfg.TLSMinVersion = "1.3"
			case "required_client_certificate":
				cfg.TLSConfig = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
				client.Certificates = []tls.Certificate{pair}
			case "in_memory_certificate":
				cfg.CertFile, cfg.KeyFile = "", ""
				cfg.TLSConfig = &tls.Config{Certificates: []tls.Certificate{pair}}
			}
			server, err := factory(cfg)
			if err != nil {
				t.Fatal(err)
			}
			addr := startServer(t, server)
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, client)
			if err != nil {
				t.Fatalf("valid TLS client rejected: %v", err)
			}
			conn.Close()
			if setting == "in_memory_certificate" {
				return
			}
			conn, err = tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, &tls.Config{
				RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
			})
			if err == nil {
				conn.Close()
				t.Fatalf("TLS 1.2 client without certificate accepted despite %s", setting)
			}
		})
	}
}

func startServer(t *testing.T, server IServer) string {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = server.Start(addr)
		close(serveDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
		select {
		case <-serveDone:
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				t.Errorf("serve: %v", serveErr)
			}
		case <-ctx.Done():
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return addr
		}
		select {
		case <-serveDone:
			t.Fatalf("server failed to start: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
