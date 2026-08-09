package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync"
	"time"
)

// certificateReloader reads a leaf pair for every new handshake. If an
// external certificate manager is between its two atomic renames, the last
// fully validated pair remains active instead of exposing a mismatched pair.
type certificateReloader struct {
	mu              sync.Mutex
	certFile        string
	keyFile         string
	current         *tls.Certificate
	now             func() time.Time
	allowLastOnFail bool
}

func newCertificateReloader(certFile, keyFile string) (*certificateReloader, error) {
	reloader := &certificateReloader{certFile: certFile, keyFile: keyFile, now: time.Now}
	certificate, err := reloader.read()
	if err != nil {
		return nil, err
	}
	reloader.current = certificate
	reloader.allowLastOnFail = true
	return reloader, nil
}

func (r *certificateReloader) Certificate() (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	certificate, err := r.read()
	if err == nil {
		r.current = certificate
		return r.current, nil
	}
	if r.allowLastOnFail && r.current != nil {
		return r.current, nil
	}
	return nil, err
}

func (r *certificateReloader) read() (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("leaf certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, err
	}
	now := r.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("leaf certificate is not currently valid")
	}
	certificate.Leaf = leaf
	return &certificate, nil
}
