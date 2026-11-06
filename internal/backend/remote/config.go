// Package remote implements backend.TaskBackend for a GA4GH TES endpoint
// that Conquest forwards tasks to (Funnel, TESK, Poiesis, proTES, another
// Conquest, ...). It speaks plain TES 1.x over HTTP; anything specific to
// one implementation is per-endpoint configuration, never code.
package remote

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// S3 URL styles. TES has no field for an S3 endpoint, so implementations
// disagree on how a URL names it.
const (
	// S3StyleBucket sends s3://<bucket>/<key>; the endpoint knows the S3
	// service from its own configuration (TESK, Poiesis).
	S3StyleBucket = "bucket"
	// S3StyleEndpoint sends s3://<s3-host[:port]>/<bucket>/<key>, which
	// Funnel's GenericS3 storage requires for non-AWS services.
	S3StyleEndpoint = "endpoint"
)

// TES versions an endpoint can implement.
const (
	TES10 = "1.0"
	TES11 = "1.1"
)

// Auth types for requests to an endpoint.
const (
	AuthNone              = "none"
	AuthBasic             = "basic"
	AuthBearer            = "bearer"
	AuthClientCredentials = "client_credentials"
)

var endpointName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// EndpointConfig describes one remote TES endpoint.
type EndpointConfig struct {
	// Name identifies the endpoint in routing rules, hints and task
	// records. DNS-label syntax; "local" is reserved.
	Name string `json:"name"`
	// URL is the TES base URL, including the API path, so that tasks are
	// created with POST <URL>/tasks (e.g. https://tes.example.org/ga4gh/tes/v1).
	URL string `json:"url"`
	// TESVersion is the TES version the endpoint implements: "1.1"
	// (default) or "1.0". Tasks that need TES 1.1 features are not sent to
	// 1.0 endpoints, and 1.1-only fields are removed from those that are.
	TESVersion string `json:"tes_version,omitempty"`
	// Auth for requests to the endpoint.
	Auth AuthConfig `json:"auth"`
	// S3URLStyle is S3StyleBucket (default) or S3StyleEndpoint.
	S3URLStyle string `json:"s3_url_style,omitempty"`
	// S3Host is the S3 service's host[:port] as the endpoint reaches it,
	// written into S3StyleEndpoint URLs. Required for that style: Conquest's
	// own S3 address is often cluster-internal.
	S3Host string `json:"s3_host,omitempty"`
	// OptionalFileOutputs sends each FILE output as a wildcard that matches
	// only that file, so that a missing file is skipped instead of failing
	// the task, as Conquest's own filer does. TES has no "optional" flag,
	// and engines declare optional outputs (e.g. Nextflow `optional: true`)
	// as plain files. Enable it only for endpoints that upload nothing for
	// a wildcard matching nothing (Funnel does; Poiesis, for one, uploads
	// all of path_prefix). Needs TES 1.1.
	OptionalFileOutputs bool `json:"optional_file_outputs,omitempty"`
	// Tags are added to every task sent to the endpoint, e.g. a proTES
	// `gateway.backends` choice.
	Tags map[string]string `json:"tags,omitempty"`
	// Headers are added to every request to the endpoint, e.g. an
	// on-behalf-of header a gateway expects. Not for credentials: use Auth.
	Headers map[string]string `json:"headers,omitempty"`
	// DataClasses lists the run data classes (run tag conquest.data_class)
	// whose tasks may go to this endpoint. Empty: none may.
	DataClasses []string `json:"data_classes,omitempty"`
	// ClientSelectable lets clients pick the endpoint with a hint
	// (tag conquest.tes_backend) without an operator rule naming it.
	ClientSelectable bool `json:"client_selectable,omitempty"`
	// MaxCPUCores and MaxRAMGb cap the tasks the endpoint is offered.
	// Zero means no cap.
	MaxCPUCores int32   `json:"max_cpu_cores,omitempty"`
	MaxRAMGb    float64 `json:"max_ram_gb,omitempty"`
	// RequestTimeout bounds each HTTP request (default 30s).
	RequestTimeout Duration `json:"request_timeout,omitempty"`
	// StaleAfter is how long status lookups may fail before the task is
	// given up as SYSTEM_ERROR (default 15m).
	StaleAfter Duration `json:"stale_after,omitempty"`
}

// AuthConfig configures how Conquest authenticates to an endpoint. Secrets
// are never inline: fields ending in _env name environment variables.
type AuthConfig struct {
	Type string `json:"type"`
	// basic
	Username    string `json:"username,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
	// bearer
	TokenEnv string `json:"token_env,omitempty"`
	// client_credentials (OAuth 2.0)
	TokenURL        string `json:"token_url,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	ClientSecretEnv string `json:"client_secret_env,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Audience        string `json:"audience,omitempty"`
}

// Duration is a time.Duration that unmarshals from strings such as "30s".
type Duration struct{ time.Duration }

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// Validate checks the endpoint and fills in defaults.
func (c *EndpointConfig) Validate() error {
	if !endpointName.MatchString(c.Name) || c.Name == "local" {
		return fmt.Errorf("endpoint name %q: must be a DNS label other than \"local\"", c.Name)
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("endpoint %s: url %q must be an absolute http(s) URL", c.Name, c.URL)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint %s: put credentials in auth, not in the url", c.Name)
	}
	c.URL = strings.TrimRight(c.URL, "/")
	switch c.TESVersion {
	case "":
		c.TESVersion = TES11
	case TES10, TES11:
	default:
		return fmt.Errorf("endpoint %s: tes_version %q must be %q or %q", c.Name, c.TESVersion, TES10, TES11)
	}
	if c.OptionalFileOutputs && c.TESVersion != TES11 {
		return fmt.Errorf("endpoint %s: optional_file_outputs needs tes_version %q", c.Name, TES11)
	}
	switch c.S3URLStyle {
	case "":
		c.S3URLStyle = S3StyleBucket
	case S3StyleBucket:
	case S3StyleEndpoint:
		if c.S3Host == "" || strings.ContainsAny(c.S3Host, "/@") {
			return fmt.Errorf("endpoint %s: s3_url_style %q needs s3_host as host[:port]", c.Name, S3StyleEndpoint)
		}
	default:
		return fmt.Errorf("endpoint %s: s3_url_style %q must be %q or %q", c.Name, c.S3URLStyle, S3StyleBucket, S3StyleEndpoint)
	}
	if err := c.Auth.validate(); err != nil {
		return fmt.Errorf("endpoint %s: %w", c.Name, err)
	}
	for k := range c.Headers {
		switch strings.ToLower(k) {
		case "authorization", "host", "content-length", "content-type", "via", "connection", "transfer-encoding":
			return fmt.Errorf("endpoint %s: header %q is set by Conquest", c.Name, k)
		}
	}
	if c.RequestTimeout.Duration <= 0 {
		c.RequestTimeout.Duration = 30 * time.Second
	}
	if c.StaleAfter.Duration <= 0 {
		c.StaleAfter.Duration = 15 * time.Minute
	}
	return nil
}

func (a *AuthConfig) validate() error {
	switch a.Type {
	case "", AuthNone:
		a.Type = AuthNone
	case AuthBasic:
		if a.Username == "" || a.PasswordEnv == "" {
			return fmt.Errorf("auth basic needs username and password_env")
		}
	case AuthBearer:
		if a.TokenEnv == "" {
			return fmt.Errorf("auth bearer needs token_env")
		}
	case AuthClientCredentials:
		if a.TokenURL == "" || a.ClientID == "" || a.ClientSecretEnv == "" {
			return fmt.Errorf("auth client_credentials needs token_url, client_id and client_secret_env")
		}
		if u, err := url.Parse(a.TokenURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("auth client_credentials: token_url %q must be an http(s) URL", a.TokenURL)
		}
	default:
		return fmt.Errorf("auth type %q must be one of none, basic, bearer, client_credentials", a.Type)
	}
	return nil
}
