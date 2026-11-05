package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// authorizer sets credentials on requests to one endpoint.
type authorizer interface {
	authorize(ctx context.Context, req *http.Request) error
}

func newAuthorizer(cfg AuthConfig, client *http.Client) authorizer {
	switch cfg.Type {
	case AuthBasic:
		return basicAuth{username: cfg.Username, passwordEnv: cfg.PasswordEnv}
	case AuthBearer:
		return bearerAuth{tokenEnv: cfg.TokenEnv}
	case AuthClientCredentials:
		return &clientCredentials{cfg: cfg, client: client}
	default:
		return noAuth{}
	}
}

type noAuth struct{}

func (noAuth) authorize(context.Context, *http.Request) error { return nil }

type basicAuth struct{ username, passwordEnv string }

func (a basicAuth) authorize(_ context.Context, req *http.Request) error {
	password, err := secretEnv(a.passwordEnv)
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.username, password)
	return nil
}

type bearerAuth struct{ tokenEnv string }

func (a bearerAuth) authorize(_ context.Context, req *http.Request) error {
	token, err := secretEnv(a.tokenEnv)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// clientCredentials gets an access token with the OAuth 2.0 client
// credentials grant and reuses it until shortly before it expires.
type clientCredentials struct {
	cfg    AuthConfig
	client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (a *clientCredentials) authorize(ctx context.Context, req *http.Request) error {
	token, err := a.accessToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (a *clientCredentials) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.expires) {
		return a.token, nil
	}
	secret, err := secretEnv(a.cfg.ClientSecretEnv)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if a.cfg.Scope != "" {
		form.Set("scope", a.cfg.Scope)
	}
	if a.cfg.Audience != "" {
		form.Set("audience", a.cfg.Audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(a.cfg.ClientID), url.QueryEscape(secret))
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request: HTTP %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("token response has no access_token")
	}
	lifetime := time.Duration(tok.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 5 * time.Minute
	}
	// renew a minute early, or halfway through very short lifetimes
	margin := min(time.Minute, lifetime/2)
	a.token, a.expires = tok.AccessToken, time.Now().Add(lifetime-margin)
	return a.token, nil
}

func secretEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return v, nil
}
