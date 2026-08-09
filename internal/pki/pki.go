// Package pki creates Glider's narrowly scoped mutual-TLS identities.
package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type IssueOptions struct {
	Name, Role, ClusterID string
	DNSNames              []string
	IPAddresses           []net.IP
	Client, Server        bool
	ValidFor              time.Duration
}

func InitCA(certPath, keyPath, clusterID string) error {
	if strings.TrimSpace(clusterID) == "" {
		return errors.New("cluster ID is required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := serialNumber()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Glider " + clusterID + " CA", Organization: []string{"Glider"}}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, public, private)
	if err != nil {
		return err
	}
	if err = writeExclusive(keyPath, 0o600, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(private)})); err != nil {
		return err
	}
	if err = writeExclusive(certPath, 0o644, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		os.Remove(keyPath)
		return err
	}
	return nil
}
func Issue(certPath, keyPath, caCertPath, caKeyPath string, options IssueOptions) error {
	allowed := map[string]bool{"admin": true, "operator": true, "viewer": true, "node": true, "monitor": true, "etcd-client": true}
	if !allowed[options.Role] || options.Name == "" || options.ClusterID == "" {
		return errors.New("valid name, role, and cluster ID are required")
	}
	if !options.Client && !options.Server {
		return errors.New("at least one of client or server usage is required")
	}
	if options.ValidFor <= 0 {
		options.ValidFor = 90 * 24 * time.Hour
	}
	if options.ValidFor > 397*24*time.Hour {
		return errors.New("leaf validity cannot exceed 397 days")
	}
	caCert, caKey, err := loadCA(caCertPath, caKeyPath)
	if err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := serialNumber()
	if err != nil {
		return err
	}
	identity, _ := url.Parse("spiffe://glider/" + url.PathEscape(options.ClusterID) + "/" + url.PathEscape(options.Role) + "/" + url.PathEscape(options.Name))
	now := time.Now().UTC()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: options.Name, Organization: []string{"Glider"}, OrganizationalUnit: []string{options.Role}}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(options.ValidFor), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: nil, DNSNames: options.DNSNames, IPAddresses: options.IPAddresses, URIs: []*url.URL{identity}}
	if options.Client {
		template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	}
	if options.Server {
		template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, public, caKey)
	if err != nil {
		return err
	}
	if err = writeExclusive(keyPath, 0o600, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(private)})); err != nil {
		return err
	}
	if err = writeExclusive(certPath, 0o644, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		os.Remove(keyPath)
		return err
	}
	return nil
}
func loadCA(certPath, keyPath string) (*x509.Certificate, ed25519.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid CA key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, errors.New("CA key is not Ed25519")
	}
	if !cert.IsCA {
		return nil, nil, errors.New("signing certificate is not a CA")
	}
	return cert, key, nil
}
func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}
func mustPKCS8(key any) []byte {
	value, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return value
}
func writeExclusive(path string, mode os.FileMode, data []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("PKI output paths must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	return closeErr
}
