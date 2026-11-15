package conn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// testConnection returns a valid connection config pointing at a port
// nothing listens on.
func testConnection(name string) config.Connection {
	return config.Connection{
		Name:         name,
		Addr:         "127.0.0.1:1",
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
}

// testConn builds a connection with a fake pinger. Building does no I/O.
func testConn(t *testing.T, name string, p pinger) *Conn {
	t.Helper()
	c, err := newConn(testConnection(name), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.pinger = p
	c.info = infoFunc(func(context.Context, ...string) (string, error) { return "", errNoInfo })
	return c
}

func TestDisplayAddr(t *testing.T) {
	tests := []struct {
		addr string
		db   int
		want string
	}{
		{"localhost:6379", 0, "localhost:6379/0"},
		{"redis.internal:6380", 15, "redis.internal:6380/15"},
		{"10.0.0.7:6379", 3, "10.0.0.7:6379/3"},
		{"[::1]:6379", 1, "[::1]:6379/1"},
	}
	for _, tt := range tests {
		if got := displayAddr(tt.addr, tt.db); got != tt.want {
			t.Errorf("displayAddr(%q, %d) = %q, want %q", tt.addr, tt.db, got, tt.want)
		}
	}
}

func TestNewRegistry(t *testing.T) {
	a, b, c := testConnection("main"), testConnection("jobs"), testConnection("local")
	b.DB = 3
	c.Addr = "[::1]:6380"
	c.DialTimeout = 2 * time.Second
	reg, err := NewRegistry([]config.Connection{a, b, c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Error(err)
		}
	})

	type view struct {
		Name, Addr string
		Timeout    time.Duration
		Health     Health
	}
	var got []view
	for _, cn := range reg.All() {
		got = append(got, view{cn.Name(), cn.Addr(), cn.timeout, cn.Health()})
	}
	want := []view{
		{"main", "127.0.0.1:1/0", 3 * time.Second, Health{}},
		{"jobs", "127.0.0.1:1/3", 3 * time.Second, Health{}},
		{"local", "[::1]:6380/0", 4 * time.Second, Health{}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("All() (-want +got):\n%s", diff)
	}

	if cn, ok := reg.Get("jobs"); !ok || cn.Name() != "jobs" {
		t.Errorf("Get(jobs) = %v, %v", cn, ok)
	}
	if cn, ok := reg.Get("nope"); ok {
		t.Errorf("Get(nope) = %v, want not found", cn)
	}
	all := reg.All()
	all[0] = nil
	if reg.All()[0] == nil {
		t.Error("All() returned the registry's own slice")
	}
	if _, err := reg.Recheck(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Recheck(nope) error = %v, want ErrNotFound", err)
	}
}

func TestNewRegistryTLSError(t *testing.T) {
	good, bad := testConnection("good"), testConnection("bad")
	bad.TLS = config.TLS{Enabled: true, CAFile: filepath.Join(t.TempDir(), "missing.pem")}
	reg, err := NewRegistry([]config.Connection{good, bad}, nil)
	if err == nil {
		_ = reg.Close()
		t.Fatal("NewRegistry succeeded with a missing CA file")
	}
	if !strings.Contains(err.Error(), "connection bad: tls ca_file") {
		t.Errorf("error %q does not name the connection and the file", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %q does not wrap os.ErrNotExist", err)
	}
}

func TestRegistryCloseTwice(t *testing.T) {
	reg, err := NewRegistry([]config.Connection{testConnection("main")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestTLSConfig(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestCert(t, dir)
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.pem")

	type view struct {
		ServerName         string
		InsecureSkipVerify bool
		MinVersion         uint16
		RootCAs            bool
		Certificates       int
	}
	tests := []struct {
		name    string
		in      config.TLS
		want    *view
		wantErr string
	}{
		{name: "disabled", in: config.TLS{CAFile: missing}, want: nil},
		{name: "system roots", in: config.TLS{Enabled: true}, want: &view{MinVersion: tls.VersionTLS12}},
		{
			name: "every option",
			in:   config.TLS{Enabled: true, CAFile: certFile, CertFile: certFile, KeyFile: keyFile, ServerName: "redis.internal", InsecureSkipVerify: true},
			want: &view{ServerName: "redis.internal", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, RootCAs: true, Certificates: 1},
		},
		{name: "missing CA", in: config.TLS{Enabled: true, CAFile: missing}, wantErr: "tls ca_file: open"},
		{name: "CA without certificates", in: config.TLS{Enabled: true, CAFile: garbage}, wantErr: "tls ca_file: no PEM certificates found"},
		{name: "missing key", in: config.TLS{Enabled: true, CertFile: certFile, KeyFile: missing}, wantErr: "tls cert_file/key_file: open"},
		{name: "key is not a key", in: config.TLS{Enabled: true, CertFile: certFile, KeyFile: certFile}, wantErr: "tls cert_file/key_file:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc, err := tlsConfig(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want prefix %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got *view
			if tc != nil {
				got = &view{tc.ServerName, tc.InsecureSkipVerify, tc.MinVersion, tc.RootCAs != nil, len(tc.Certificates)}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("tlsConfig (-want +got):\n%s", diff)
			}
		})
	}
}

// writeTestCert writes a self-signed certificate and its key as PEM files.
func writeTestCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "asynqui test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
