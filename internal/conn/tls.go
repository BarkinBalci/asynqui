package conn

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// tlsConfig builds the client TLS configuration, reading the CA and client
// certificate files now. It returns nil when TLS is disabled.
func tlsConfig(t config.TLS) (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}
	tc := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Empty means the host of the address, as inferred by tls.Dial.
		ServerName:         t.ServerName,
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // explicit operator opt-in (tls.insecure_skip_verify)
	}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile) //nolint:gosec // the path is chosen by the operator
		if err != nil {
			return nil, fmt.Errorf("tls ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls ca_file: no PEM certificates found")
		}
		tc.RootCAs = pool
	}
	if t.CertFile != "" || t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls cert_file/key_file: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}
