// Package registry implements the OCI Distribution HTTP interactions Glider
// needs to pull images. It deliberately does not use a third-party registry
// engine; ADR-0002 fixes this as a mechanism Glider owns.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/santinomarial/glider/internal/image/content"
	"github.com/santinomarial/glider/internal/image/reference"
)

const maxManifestBytes = 8 << 20

var (
	ErrUnauthorized = errors.New("registry authentication failed")
	ErrNotFound     = errors.New("registry content not found")
	ErrProtocol     = errors.New("registry protocol error")
)

type Credentials struct{ Username, Password string }

// CredentialFunc returns credentials for a registry host. A nil function
// means anonymous access only.
type CredentialFunc func(registry string) (Credentials, bool)

type Client struct {
	http        *http.Client
	credentials CredentialFunc
	insecure    bool
}

func NewClient(httpClient *http.Client, credentials CredentialFunc, insecure bool) *Client {
	if httpClient == nil { httpClient = http.DefaultClient }
	return &Client{http: httpClient, credentials: credentials, insecure: insecure}
}

// FetchManifest obtains a manifest/index and independently computes its
// descriptor. If the registry advertises Docker-Content-Digest, it must agree.
func (c *Client) FetchManifest(ctx context.Context, ref reference.Reference, selector string) ([]byte, v1.Descriptor, error) {
	path := "/v2/" + ref.Repository + "/manifests/" + url.PathEscape(selector)
	headers := http.Header{"Accept": []string{
		v1.MediaTypeImageManifest, v1.MediaTypeImageIndex,
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}}
	resp, err := c.do(ctx, ref.Registry, path, headers)
	if err != nil { return nil, v1.Descriptor{}, err }
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxManifestBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil { return nil, v1.Descriptor{}, fmt.Errorf("read manifest: %w", err) }
	if len(data) > maxManifestBytes { return nil, v1.Descriptor{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrProtocol, maxManifestBytes) }
	desc := v1.Descriptor{MediaType: mediaType(resp.Header.Get("Content-Type")), Digest: digest.FromBytes(data), Size: int64(len(data))}
	if advertised := resp.Header.Get("Docker-Content-Digest"); advertised != "" {
		want, err := digest.Parse(advertised)
		if err != nil || want.Validate() != nil { return nil, v1.Descriptor{}, fmt.Errorf("%w: invalid Docker-Content-Digest %q", ErrProtocol, advertised) }
		if want != desc.Digest { return nil, v1.Descriptor{}, fmt.Errorf("%w: manifest response hashes to %s, registry advertised %s", content.ErrDigestMismatch, desc.Digest, want) }
	}
	if expected, err := digest.Parse(selector); err == nil && expected.Validate() == nil && expected != desc.Digest {
		return nil, v1.Descriptor{}, fmt.Errorf("%w: manifest response hashes to %s, requested %s", content.ErrDigestMismatch, desc.Digest, expected)
	}
	return data, desc, nil
}

// FetchBlob streams a descriptor into the verified content store.
func (c *Client) FetchBlob(ctx context.Context, ref reference.Reference, desc v1.Descriptor, store *content.Store) (string, error) {
	path := "/v2/" + ref.Repository + "/blobs/" + url.PathEscape(desc.Digest.String())
	resp, err := c.do(ctx, ref.Registry, path, nil)
	if err != nil { return "", err }
	defer resp.Body.Close()
	if raw := resp.Header.Get("Content-Length"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err != nil || n != desc.Size {
			return "", fmt.Errorf("%w: blob Content-Length %q does not match descriptor size %d", content.ErrSizeMismatch, raw, desc.Size)
		}
	}
	return store.Put(ctx, desc, resp.Body)
}

func (c *Client) do(ctx context.Context, registryHost, path string, headers http.Header) (*http.Response, error) {
	makeRequest := func(token string) (*http.Request, error) {
		scheme := "https"
		if c.insecure { scheme = "http" }
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+registryHost+path, nil)
		if err != nil { return nil, err }
		for k, values := range headers { for _, value := range values { req.Header.Add(k, value) } }
		if token != "" { req.Header.Set("Authorization", "Bearer "+token) }
		return req, nil
	}
	req, err := makeRequest("")
	if err != nil { return nil, err }
	resp, err := c.http.Do(req)
	if err != nil { return nil, fmt.Errorf("registry GET %s: %w", path, err) }
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		drainClose(resp.Body)
		token, err := c.bearerToken(ctx, registryHost, challenge)
		if err != nil { return nil, err }
		req, _ = makeRequest(token)
		resp, err = c.http.Do(req)
		if err != nil { return nil, fmt.Errorf("authenticated registry GET %s: %w", path, err) }
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden { return nil, fmt.Errorf("%w: %s", ErrUnauthorized, resp.Status) }
		if resp.StatusCode == http.StatusNotFound { return nil, fmt.Errorf("%w: %s", ErrNotFound, path) }
		return nil, fmt.Errorf("registry GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (c *Client) bearerToken(ctx context.Context, registryHost, challenge string) (string, error) {
	scheme, params, err := parseChallenge(challenge)
	if err != nil || !strings.EqualFold(scheme, "Bearer") { return "", fmt.Errorf("%w: unsupported WWW-Authenticate challenge %q", ErrUnauthorized, challenge) }
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || (realm.Scheme != "https" && !(c.insecure && realm.Scheme == "http")) {
		return "", fmt.Errorf("%w: unsafe bearer token realm", ErrProtocol)
	}
	q := realm.Query()
	if service := params["service"]; service != "" { q.Set("service", service) }
	if scope := params["scope"]; scope != "" { q.Set("scope", scope) }
	realm.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if c.credentials != nil {
		if cred, ok := c.credentials(registryHost); ok { req.SetBasicAuth(cred.Username, cred.Password) }
	}
	resp, err := c.http.Do(req)
	if err != nil { return "", fmt.Errorf("request registry bearer token: %w", err) }
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return "", fmt.Errorf("%w: token service returned %s", ErrUnauthorized, resp.Status) }
	var payload struct { Token string `json:"token"`; AccessToken string `json:"access_token"` }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil { return "", fmt.Errorf("%w: decode token response: %v", ErrProtocol, err) }
	if payload.Token == "" { payload.Token = payload.AccessToken }
	if payload.Token == "" { return "", fmt.Errorf("%w: token response contained no token", ErrProtocol) }
	return payload.Token, nil
}

func parseChallenge(value string) (string, map[string]string, error) {
	space := strings.IndexByte(value, ' ')
	if space <= 0 { return "", nil, errors.New("missing authentication parameters") }
	scheme := strings.TrimSpace(value[:space])
	rest := value[space+1:]
	params := make(map[string]string)
	for len(strings.TrimSpace(rest)) > 0 {
		rest = strings.TrimSpace(rest)
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 { return "", nil, errors.New("malformed authentication parameter") }
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = strings.TrimSpace(rest[eq+1:])
		if !strings.HasPrefix(rest, `"`) { return "", nil, errors.New("authentication value must be quoted") }
		rest = rest[1:]
		var value bytes.Buffer
		escaped, closed := false, false
		for i, r := range rest {
			if escaped { value.WriteRune(r); escaped = false; continue }
			if r == '\\' { escaped = true; continue }
			if r == '"' { rest = rest[i+1:]; closed = true; break }
			value.WriteRune(r)
		}
		if !closed { return "", nil, errors.New("unterminated authentication value") }
		params[key] = value.String()
		rest = strings.TrimSpace(rest)
		if rest == "" { break }
		if rest[0] != ',' { return "", nil, errors.New("malformed authentication parameter separator") }
		rest = rest[1:]
	}
	return scheme, params, nil
}

func mediaType(value string) string {
	if semi := strings.IndexByte(value, ';'); semi >= 0 { value = value[:semi] }
	return strings.TrimSpace(value)
}

func drainClose(body io.ReadCloser) { _, _ = io.Copy(io.Discard, io.LimitReader(body, 4096)); _ = body.Close() }
