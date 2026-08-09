// Package configcheck validates Glider's production systemd environment files
// without executing their contents.
package configcheck

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Options struct {
	Kind       string
	File       string
	Root       string
	CheckFiles bool
}

type Result struct {
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Arguments int    `json:"arguments"`
	Files     int    `json:"files_checked"`
	Valid     bool   `json:"valid"`
}

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func Validate(options Options) (Result, error) {
	result := Result{Kind: options.Kind, File: options.File}
	if options.File == "" || !filepath.IsAbs(options.File) {
		return result, errors.New("config file must be an absolute path")
	}
	if options.Root == "" {
		options.Root = "/"
	}
	if !filepath.IsAbs(options.Root) {
		return result, errors.New("config root must be absolute")
	}
	values, err := parseEnvironment(options.File)
	if err != nil {
		return result, err
	}
	argumentKey, required, fileFlags, allowedKeys, err := schema(options.Kind)
	if err != nil {
		return result, err
	}
	for key := range values {
		if !allowedKeys[key] {
			return result, fmt.Errorf("unknown %s setting %s", options.Kind, key)
		}
	}
	for key := range allowedKeys {
		if strings.TrimSpace(values[key]) == "" {
			return result, fmt.Errorf("required setting %s is missing", key)
		}
	}
	if strings.Contains(strings.Join(mapValues(values), " "), "CHANGE_ME") {
		return result, errors.New("configuration contains an unresolved CHANGE_ME placeholder")
	}
	arguments, err := parseArguments(values[argumentKey])
	if err != nil {
		return result, err
	}
	result.Arguments = len(arguments)
	for _, forbidden := range []string{"insecure-development", "insecure-etcd", "insecure-registry"} {
		if _, exists := arguments[forbidden]; exists {
			return result, fmt.Errorf("--%s is forbidden in production configuration", forbidden)
		}
	}
	for _, flag := range required {
		if strings.TrimSpace(arguments[flag]) == "" {
			return result, fmt.Errorf("required --%s argument is missing", flag)
		}
	}
	if options.Kind == "backup" {
		days, parseErr := strconv.Atoi(values["GLIDER_BACKUP_RETENTION_DAYS"])
		if parseErr != nil || days < 1 || days > 3650 {
			return result, errors.New("backup retention days must be between 1 and 3650")
		}
		if arguments["key-file"] != values["GLIDER_BACKUP_KEY_FILE"] {
			return result, errors.New("backup and verification key paths differ")
		}
	}
	if options.CheckFiles {
		if err := checkReferencedFiles(options.Root, arguments, fileFlags); err != nil {
			return result, err
		}
		result.Files = len(fileFlags)
	}
	result.Valid = true
	return result, nil
}

func schema(kind string) (string, []string, map[string]fileRequirement, map[string]bool, error) {
	switch kind {
	case "controlplane":
		return "GLIDER_CONTROLPLANE_ARGS", []string{"listen", "instance-id", "cluster-id", "etcd-endpoints", "tls-cert", "tls-key", "client-ca", "etcd-tls-cert", "etcd-tls-key", "etcd-ca", "etcd-tls-server-name", "secret-key-file", "metrics-listen"}, map[string]fileRequirement{"tls-cert": certFile, "tls-key": privateFile, "client-ca": caFile, "etcd-tls-cert": certFile, "etcd-tls-key": privateFile, "etcd-ca": caFile, "secret-key-file": privateFile}, map[string]bool{"GLIDER_CONTROLPLANE_ARGS": true}, nil
	case "node":
		return "GLIDERD_ARGS", []string{"node-id", "cluster-id", "etcd-endpoints", "etcd-tls-cert", "etcd-tls-key", "etcd-ca", "etcd-tls-server-name", "data-dir", "network-cidr", "operations-listen", "tls-cert", "tls-key", "client-ca", "controlplane-endpoint", "controlplane-ca", "controlplane-tls-server-name", "exec-helper", "storage-min-free-bytes", "storage-min-free-percent"}, map[string]fileRequirement{"etcd-tls-cert": certFile, "etcd-tls-key": privateFile, "etcd-ca": caFile, "tls-cert": certFile, "tls-key": privateFile, "client-ca": caFile, "controlplane-ca": caFile, "exec-helper": executableFile}, map[string]bool{"GLIDERD_ARGS": true}, nil
	case "backup":
		return "GLIDER_BACKUP_ARGS", []string{"endpoint", "key-file", "tls-cert", "tls-key", "ca", "tls-server-name", "timeout"}, map[string]fileRequirement{"key-file": privateFile, "tls-cert": certFile, "tls-key": privateFile, "ca": caFile}, map[string]bool{"GLIDER_BACKUP_ARGS": true, "GLIDER_BACKUP_KEY_FILE": true, "GLIDER_BACKUP_RETENTION_DAYS": true}, nil
	default:
		return "", nil, nil, nil, errors.New("config kind must be controlplane, node, or backup")
	}
}

func parseEnvironment(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, found := strings.Cut(text, "=")
		if !found || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid environment syntax on line %d", line)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate setting %s", key)
		}
		if strings.HasPrefix(value, `"`) {
			value, err = strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("invalid quoted value for %s: %w", key, err)
			}
		} else if strings.ContainsAny(value, " \t\r\n") {
			return nil, fmt.Errorf("unquoted whitespace in %s", key)
		}
		values[key] = value
	}
	return values, scanner.Err()
}

func parseArguments(value string) (map[string]string, error) {
	arguments := map[string]string{}
	for _, field := range strings.Fields(value) {
		if !strings.HasPrefix(field, "--") {
			return nil, fmt.Errorf("argument %q must use --name=value", field)
		}
		name, value, found := strings.Cut(strings.TrimPrefix(field, "--"), "=")
		if !found || name == "" || value == "" || strings.ContainsAny(value, "'\"`$;|&<>") {
			return nil, fmt.Errorf("unsafe or empty argument %q", field)
		}
		if _, duplicate := arguments[name]; duplicate {
			return nil, fmt.Errorf("duplicate --%s argument", name)
		}
		arguments[name] = value
	}
	return arguments, nil
}

type fileRequirement int

const (
	privateFile fileRequirement = iota
	certFile
	caFile
	executableFile
)

func checkReferencedFiles(root string, arguments map[string]string, requirements map[string]fileRequirement) error {
	for flag, requirement := range requirements {
		configured := arguments[flag]
		if !filepath.IsAbs(configured) {
			return fmt.Errorf("--%s must reference an absolute path", flag)
		}
		path := filepath.Join(root, strings.TrimPrefix(configured, "/"))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("check --%s file: %w", flag, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("--%s is not a regular file", flag)
		}
		switch requirement {
		case privateFile:
			if info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("--%s private file is accessible by group or others", flag)
			}
		case executableFile:
			if info.Mode().Perm()&0o111 == 0 {
				return fmt.Errorf("--%s is not executable", flag)
			}
		case certFile:
			if _, err := parseCertificate(path, false); err != nil {
				return fmt.Errorf("--%s: %w", flag, err)
			}
		case caFile:
			if _, err := parseCertificate(path, true); err != nil {
				return fmt.Errorf("--%s: %w", flag, err)
			}
		}
	}
	for certFlag, keyFlag := range map[string]string{"tls-cert": "tls-key", "etcd-tls-cert": "etcd-tls-key"} {
		if arguments[certFlag] == "" {
			continue
		}
		certPath := filepath.Join(root, strings.TrimPrefix(arguments[certFlag], "/"))
		keyPath := filepath.Join(root, strings.TrimPrefix(arguments[keyFlag], "/"))
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
			return fmt.Errorf("--%s and --%s do not form a keypair: %w", certFlag, keyFlag, err)
		}
	}
	return nil
}

func parseCertificate(path string, requireCA bool) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if requireCA && !certificate.IsCA {
		return nil, errors.New("certificate is not a CA")
	}
	return certificate, nil
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
