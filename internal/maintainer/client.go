// Package maintainer is the host client of the existing GitHub broker. GitHub
// tokens remain remote; only the opaque factory connection credential is local.
package maintainer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const Origin = "https://maintainer.darkfactory.build"
const prefix = "/v1/github/connections"

var (
	ErrDenied      = errors.New("GitHub connection access expired or was removed; refresh GitHub access")
	ErrUnavailable = errors.New("GitHub connection unavailable; retry when the Maintainer and GitHub are reachable")
	ErrInvalid     = errors.New("invalid GitHub connection request or reply")
)

// Credential is deliberately opaque to operator DTOs, logging and providers.
type Credential struct{ id, secret string }

func (Credential) String() string   { return "MaintainerCredential(<redacted>)" }
func (Credential) GoString() string { return "MaintainerCredential(<redacted>)" }

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}
type Delegation struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository"`
}
type Status struct {
	ConnectionID string       `json:"connection_id"`
	State        string       `json:"state"`
	User         *User        `json:"github_user,omitempty"`
	Repositories []Delegation `json:"repositories"`
}
type Authorization struct {
	ConnectionID string `json:"connection_id"`
	URL          string `json:"authorization_url"`
	ExpiresAt    int64  `json:"expires_at"`
}
type Installation struct {
	ID                  int64   `json:"id"`
	AppID               int64   `json:"app_id"`
	Account             User    `json:"account"`
	RepositorySelection string  `json:"repository_selection"`
	SuspendedAt         *string `json:"suspended_at"`
	URL                 string  `json:"html_url,omitempty"`
}
type Repository struct {
	ID          int64  `json:"id"`
	Name        string `json:"full_name"`
	Permissions struct {
		Pull     bool `json:"pull"`
		Push     bool `json:"push"`
		Maintain bool `json:"maintain"`
		Admin    bool `json:"admin"`
	} `json:"permissions"`
}
type Installations struct {
	Items    []Installation `json:"installations"`
	NextPage *int           `json:"next_page"`
}
type Repositories struct {
	Items    []Repository `json:"repositories"`
	NextPage *int         `json:"next_page"`
}

// Client never follows redirects, uses an ambient authenticated proxy, caches
// authorization, or falls back to the legacy operator/service connection.
type Client struct {
	http   *http.Client
	origin string
}

func NewClient() *Client {
	return &Client{origin: Origin, http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (client *Client) Connect(ctx context.Context) (Credential, Authorization, error) {
	var result struct {
		Authorization
		Secret string `json:"credential"`
	}
	if err := client.request(ctx, Credential{}, http.MethodPost, prefix, struct{}{}, &result); err != nil {
		return Credential{}, Authorization{}, err
	}
	credential, err := parseCredential(result.ConnectionID, result.Secret)
	u, urlErr := url.Parse(result.URL)
	if err != nil || urlErr != nil || u.Scheme != "https" || u.Host != "github.com" || u.Path != "/login/oauth/authorize" || u.User != nil || u.Fragment != "" || result.ExpiresAt <= time.Now().Unix() {
		return Credential{}, Authorization{}, ErrInvalid
	}
	return credential, result.Authorization, nil
}

func (client *Client) Status(ctx context.Context, credential Credential) (Status, error) {
	var result Status
	err := client.request(ctx, credential, http.MethodGet, prefix+"/"+credential.id, nil, &result)
	if err != nil {
		return Status{}, err
	}
	if result.ConnectionID != credential.id || (result.State != "connected" && result.State != "pending" && result.State != "disconnected") {
		return Status{}, ErrInvalid
	}
	return result, nil
}
func (client *Client) Installations(ctx context.Context, credential Credential, page int) (Installations, error) {
	var result Installations
	if page < 1 || page > 1000 {
		return result, ErrInvalid
	}
	err := client.request(ctx, credential, http.MethodGet, prefix+"/"+credential.id+"/installations?page="+strconv.Itoa(page), nil, &result)
	if err != nil {
		return Installations{}, err
	}
	return result, nil
}
func (client *Client) Repositories(ctx context.Context, credential Credential, installation int64, page int) (Repositories, error) {
	var result Repositories
	if installation <= 0 || page < 1 || page > 1000 {
		return result, ErrInvalid
	}
	err := client.request(ctx, credential, http.MethodGet, prefix+"/"+credential.id+"/repositories?installation_id="+strconv.FormatInt(installation, 10)+"&page="+strconv.Itoa(page), nil, &result)
	if err != nil {
		return Repositories{}, err
	}
	return result, nil
}
func (client *Client) Delegate(ctx context.Context, credential Credential, repositories []Delegation) error {
	if len(repositories) > 100 {
		return ErrInvalid
	}
	if repositories == nil {
		repositories = []Delegation{}
	}
	for _, repository := range repositories {
		if repository.InstallationID <= 0 || repository.RepositoryID <= 0 || len(repository.Repository) > 140 {
			return ErrInvalid
		}
	}
	return client.request(ctx, credential, http.MethodPut, prefix+"/"+credential.id+"/repositories", struct {
		Repositories []Delegation `json:"repositories"`
	}{repositories}, nil)
}
func (client *Client) Disconnect(ctx context.Context, credential Credential) error {
	return client.request(ctx, credential, http.MethodDelete, prefix+"/"+credential.id, nil, nil)
}

func parseCredential(id, secret string) (Credential, error) {
	value, err := hex.DecodeString(secret)
	digest := sha256.Sum256([]byte(secret))
	if err != nil || len(value) != 32 || hex.EncodeToString(value) != secret || hex.EncodeToString(digest[:]) != id {
		return Credential{}, ErrInvalid
	}
	return Credential{id: id, secret: secret}, nil
}
func (client *Client) request(ctx context.Context, credential Credential, method, path string, input, output any) error {
	if path != prefix {
		if _, err := parseCredential(credential.id, credential.secret); err != nil {
			return ErrDenied
		}
	}
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return ErrInvalid
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, client.origin+path, bytes.NewReader(body))
	if err != nil {
		return ErrInvalid
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if credential.secret != "" {
		request.Header.Set("Authorization", "Bearer "+credential.secret)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrDenied
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrUnavailable
	}
	const maximum = 1 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || len(data) > maximum {
		return ErrUnavailable
	}
	if !json.Valid(data) {
		return ErrInvalid
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return ErrInvalid
	}
	return nil
}
