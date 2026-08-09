// Package transport owns Glider's authenticated transport boundary.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func ServerCredentials(certFile, keyFile, clientCAFile string) (credentials.TransportCredentials, error) {
	config, err := ServerTLSConfig(certFile, keyFile, clientCAFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(config), nil
}
func ServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	pool, err := loadPool(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("load client CA: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}, nil
}
func ClientCredentials(certFile, keyFile, caFile, serverName string) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}
	pool, err := loadPool(caFile)
	if err != nil {
		return nil, fmt.Errorf("load server CA: %w", err)
	}
	if strings.TrimSpace(serverName) == "" {
		return nil, errors.New("TLS server name is required")
	}
	return credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS13}), nil
}
func EtcdTLSConfig(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load etcd client certificate: %w", err)
	}
	pool, err := loadPool(caFile)
	if err != nil {
		return nil, fmt.Errorf("load etcd CA: %w", err)
	}
	if strings.TrimSpace(serverName) == "" {
		return nil, errors.New("etcd TLS server name is required")
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS13}, nil
}
func loadPool(file string) (*x509.CertPool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("no CA certificates found")
	}
	return pool, nil
}

type Principal struct {
	Name  string
	Roles map[string]bool
}
type principalKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	value, ok := ctx.Value(principalKey{}).(Principal)
	return value, ok
}
func UnaryAuthorizationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		principal, err := authenticatedPrincipal(ctx)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		if !authorized(principal.Roles, info.FullMethod) {
			return nil, status.Error(codes.PermissionDenied, "principal is not authorized for this operation")
		}
		return handler(context.WithValue(ctx, principalKey{}, principal), req)
	}
}
func authenticatedPrincipal(ctx context.Context) (Principal, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return Principal{}, errors.New("peer identity is missing")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return Principal{}, errors.New("verified client certificate is missing")
	}
	cert := info.State.VerifiedChains[0][0]
	name := cert.Subject.CommonName
	if name == "" {
		return Principal{}, errors.New("client certificate common name is missing")
	}
	roles := map[string]bool{}
	for _, role := range cert.Subject.OrganizationalUnit {
		roles[strings.ToLower(role)] = true
	}
	if len(roles) == 0 {
		return Principal{}, errors.New("client certificate contains no role organizational unit")
	}
	return Principal{Name: name, Roles: roles}, nil
}
func authorized(roles map[string]bool, method string) bool {
	if roles["admin"] {
		return true
	}
	if strings.Contains(method, ".NodeOperations/") {
		return roles["operator"]
	}
	name := method[strings.LastIndex(method, "/")+1:]
	read := strings.HasPrefix(name, "List") || strings.HasPrefix(name, "Get")
	if roles["viewer"] && read {
		return true
	}
	if roles["operator"] && (read || name == "PutTask" || name == "DeleteTask" || name == "PutWorkload" || name == "PutService" || name == "Schedule" || name == "DrainNode") {
		return true
	}
	if roles["node"] && (name == "PutNode" || name == "PutEvent") {
		return true
	}
	return false
}
