// Package certutil generates and persists the self-signed certificate used
// by the gemini listener (gemini clients do TOFU, so the cert must be stable
// across restarts).
package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// LoadOrCreate returns a TLS certificate for hostname, generating and
// persisting a self-signed one under dataDir on first use.
func LoadOrCreate(dataDir, hostname string) (tls.Certificate, error) {
	certPath := filepath.Join(dataDir, "gemini-cert.pem")
	keyPath := filepath.Join(dataDir, "gemini-key.pem")

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		// regenerate when expired (or expiring within 30 days)
		if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
			if time.Now().Add(30 * 24 * time.Hour).Before(leaf.NotAfter) {
				return cert, nil
			}
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("writing cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("writing key: %w", err)
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}
