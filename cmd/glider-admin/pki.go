package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"github.com/santinomarial/glider/internal/pki"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runPKI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: glider-admin pki init|issue|bundle")
	}
	switch args[0] {
	case "init":
		return pkiInit(args[1:])
	case "issue":
		return pkiIssue(args[1:])
	case "bundle":
		return pkiBundle(args[1:])
	default:
		return errors.New("unknown pki command")
	}
}
func pkiInit(args []string) error {
	fs := flag.NewFlagSet("pki init", flag.ContinueOnError)
	dir := fs.String("dir", "", "absolute output directory")
	cluster := fs.String("cluster-id", "", "cluster ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("--dir is required")
	}
	if err := pki.InitCA(filepath.Join(*dir, "ca.crt"), filepath.Join(*dir, "ca.key"), *cluster); err != nil {
		return err
	}
	fmt.Printf("CA created in %s; keep ca.key offline with mode 0600\n", *dir)
	return nil
}
func pkiIssue(args []string) error {
	fs := flag.NewFlagSet("pki issue", flag.ContinueOnError)
	caCert := fs.String("ca-cert", "", "CA certificate")
	caKey := fs.String("ca-key", "", "CA private key")
	cert := fs.String("cert", "", "output certificate")
	key := fs.String("key", "", "output private key")
	name := fs.String("name", "", "principal name")
	role := fs.String("role", "", "admin, operator, viewer, node, monitor, or etcd-client")
	cluster := fs.String("cluster-id", "", "cluster ID")
	usage := fs.String("usage", "client", "client, server, or both")
	dns := fs.String("dns-names", "", "comma-separated DNS SANs")
	ips := fs.String("ip-addresses", "", "comma-separated IP SANs")
	valid := fs.Duration("valid-for", 90*24*time.Hour, "leaf validity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	options := pki.IssueOptions{Name: *name, Role: *role, ClusterID: *cluster, DNSNames: splitCSV(*dns), ValidFor: *valid}
	for _, raw := range splitCSV(*ips) {
		ip := net.ParseIP(raw)
		if ip == nil {
			return fmt.Errorf("invalid IP %q", raw)
		}
		options.IPAddresses = append(options.IPAddresses, ip)
	}
	switch *usage {
	case "client":
		options.Client = true
	case "server":
		options.Server = true
	case "both":
		options.Client, options.Server = true, true
	default:
		return errors.New("usage must be client, server, or both")
	}
	if err := pki.Issue(*cert, *key, *caCert, *caKey, options); err != nil {
		return err
	}
	fmt.Printf("issued %s identity %s\n", *role, *name)
	return nil
}
func pkiBundle(args []string) error {
	fs := flag.NewFlagSet("pki bundle", flag.ContinueOnError)
	output := fs.String("output", "", "create-exclusive CA bundle path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" || fs.NArg() < 1 {
		return errors.New("--output and at least one CA certificate are required")
	}
	var bundle []byte
	for _, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rest := data
		found := false
		for {
			block, next := pem.Decode(rest)
			if block == nil {
				break
			}
			rest = next
			if block.Type != "CERTIFICATE" {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || !cert.IsCA {
				return fmt.Errorf("%s does not contain only valid CA certificates", path)
			}
			bundle = append(bundle, pem.EncodeToMemory(block)...)
			found = true
		}
		if !found {
			return fmt.Errorf("%s contains no CA certificate", path)
		}
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = file.Write(bundle); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
