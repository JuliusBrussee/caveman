package cachebench

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

const defaultReplayResponseLimit = 16 << 20

// HTTPReplayCredentials contains provider secrets used only by transport.
type HTTPReplayCredentials struct {
	OpenAIAPIKey    string
	AnthropicAPIKey string
	GeminiAPIKey    string
	BedrockAPIKey   string
	AWS             awssig.Credentials
}

// HTTPReplayConfig configures safe built-in provider HTTP transport.
type HTTPReplayConfig struct {
	Client                *http.Client
	Credentials           HTTPReplayCredentials
	BaseURLs              map[string]string
	AllowInsecureLoopback bool
	MaxRequestBytes       int64
	MaxResponseBytes      int64
	RequestTimeout        time.Duration
}

// HTTPReplayTransport sends official non-streaming provider requests.
type HTTPReplayTransport struct {
	client           *http.Client
	credentials      HTTPReplayCredentials
	baseURLs         map[string]*url.URL
	maxRequestBytes  int64
	maxResponseBytes int64
}

// NewHTTPReplayTransport validates endpoints, limits, TLS, and redirect policy.
func NewHTTPReplayTransport(config HTTPReplayConfig) (*HTTPReplayTransport, error) {
	requestLimit := config.MaxRequestBytes
	if requestLimit == 0 {
		requestLimit = 64 << 20
	}
	if requestLimit < 1 || requestLimit > 256<<20 {
		return nil, errors.New("cachebench: replay request limit must be between 1 byte and 256 MiB")
	}
	limit := config.MaxResponseBytes
	if limit == 0 {
		limit = defaultReplayResponseLimit
	}
	if limit < 1 || limit > 256<<20 {
		return nil, errors.New("cachebench: replay response limit must be between 1 byte and 256 MiB")
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = 2 * time.Minute
	}
	if requestTimeout < time.Second || requestTimeout > time.Hour {
		return nil, errors.New("cachebench: replay request timeout must be between 1 second and 1 hour")
	}
	bases := map[string]string{
		"openai":    "https://api.openai.com",
		"anthropic": "https://api.anthropic.com",
		"gemini":    "https://generativelanguage.googleapis.com",
	}
	for provider, value := range config.BaseURLs {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "openai" && provider != "anthropic" && provider != "gemini" && provider != "bedrock" {
			return nil, fmt.Errorf("cachebench: unsupported replay base URL provider %q", provider)
		}
		bases[provider] = value
	}
	parsed := make(map[string]*url.URL, len(bases))
	for provider, value := range bases {
		base, err := validateReplayBaseURL(value, config.AllowInsecureLoopback)
		if err != nil {
			return nil, fmt.Errorf("cachebench: %s replay base URL: %w", provider, err)
		}
		parsed[provider] = base
	}
	client := config.Client
	if client == nil {
		client = &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 90 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		}
	} else {
		clone := *client
		if config.RequestTimeout != 0 || clone.Timeout <= 0 || clone.Timeout > requestTimeout {
			clone.Timeout = requestTimeout
		}
		client = &clone
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPReplayTransport{client: client, credentials: config.Credentials, baseURLs: parsed, maxRequestBytes: requestLimit, maxResponseBytes: limit}, nil
}

// Send authenticates and sends one bounded provider request without retry.
func (transport *HTTPReplayTransport) Send(ctx context.Context, outbound ReplayOutbound) (ReplayResponse, error) {
	if transport == nil || transport.client == nil {
		return ReplayResponse{}, errors.New("cachebench: nil HTTP replay transport")
	}
	if int64(len(outbound.Body)) > transport.maxRequestBytes {
		return ReplayResponse{}, errors.New("cachebench: replay body exceeds configured request limit")
	}
	provider := strings.ToLower(strings.TrimSpace(outbound.Provider))
	if !validBoundedText(provider, 64, false) {
		return ReplayResponse{}, errors.New("cachebench: invalid replay provider")
	}
	endpoint, err := transport.endpoint(provider, outbound)
	if err != nil {
		return ReplayResponse{}, err
	}
	if !validUniqueJSONObject(outbound.Body) {
		return ReplayResponse{}, errors.New("cachebench: replay body must be one JSON object")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(outbound.Body))
	if err != nil {
		return ReplayResponse{}, errors.New("cachebench: could not build provider request")
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "application/json")
	request.Header.Set("user-agent", "caveman-cachebench-replay/1")
	if err := transport.authorize(provider, outbound.Region, request, outbound.Body); err != nil {
		return ReplayResponse{}, err
	}
	response, err := transport.client.Do(request)
	if err != nil {
		return ReplayResponse{}, errors.New("cachebench: provider request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, transport.maxResponseBytes+1))
	if err != nil {
		return ReplayResponse{StatusCode: response.StatusCode}, errors.New("cachebench: provider response read failed")
	}
	if int64(len(body)) > transport.maxResponseBytes {
		return ReplayResponse{StatusCode: response.StatusCode}, errors.New("cachebench: provider response exceeds configured limit")
	}
	return ReplayResponse{
		StatusCode: response.StatusCode, Body: body,
		ProviderRequestID: providerRequestID(response.Header),
	}, nil
}

func (transport *HTTPReplayTransport) endpoint(provider string, outbound ReplayOutbound) (*url.URL, error) {
	if !validBoundedText(outbound.Model, 512, false) {
		return nil, errors.New("cachebench: invalid replay model")
	}
	base := transport.baseURLs[provider]
	if provider == "bedrock" && base == nil {
		if !validAWSRegion(outbound.Region) {
			return nil, errors.New("cachebench: invalid Bedrock region")
		}
		parsed, _ := url.Parse("https://bedrock-runtime." + outbound.Region + ".amazonaws.com")
		base = parsed
	}
	if base == nil {
		return nil, fmt.Errorf("cachebench: unsupported replay provider %q", provider)
	}
	var path, rawPath string
	switch provider {
	case "openai":
		if outbound.Endpoint != "/v1/chat/completions" && outbound.Endpoint != "/v1/responses" {
			return nil, errors.New("cachebench: unsupported OpenAI replay endpoint")
		}
		path = outbound.Endpoint
	case "anthropic":
		if outbound.Endpoint != "/v1/messages" {
			return nil, errors.New("cachebench: unsupported Anthropic replay endpoint")
		}
		path = outbound.Endpoint
	case "gemini":
		if outbound.Endpoint != "generateContent" {
			return nil, errors.New("cachebench: unsupported Gemini replay endpoint")
		}
		rawPath = "/v1beta/models/" + url.PathEscape(outbound.Model) + ":generateContent"
	case "bedrock":
		if outbound.Endpoint != "converse" || !validAWSRegion(outbound.Region) {
			return nil, errors.New("cachebench: unsupported Bedrock replay endpoint or region")
		}
		rawPath = "/model/" + url.PathEscape(outbound.Model) + "/converse"
	default:
		return nil, fmt.Errorf("cachebench: unsupported replay provider %q", provider)
	}
	resolved := *base
	if rawPath == "" {
		resolved.Path = strings.TrimRight(base.Path, "/") + path
		resolved.RawPath = ""
	} else {
		resolved.RawPath = strings.TrimRight(base.EscapedPath(), "/") + rawPath
		resolved.Path, _ = url.PathUnescape(resolved.RawPath)
	}
	resolved.RawQuery = ""
	resolved.Fragment = ""
	return &resolved, nil
}

func (transport *HTTPReplayTransport) authorize(provider, region string, request *http.Request, body []byte) error {
	switch provider {
	case "openai":
		if !validReplaySecret(transport.credentials.OpenAIAPIKey) {
			return errors.New("cachebench: OpenAI API key unavailable")
		}
		request.Header.Set("authorization", "Bearer "+transport.credentials.OpenAIAPIKey)
	case "anthropic":
		if !validReplaySecret(transport.credentials.AnthropicAPIKey) {
			return errors.New("cachebench: Anthropic API key unavailable")
		}
		request.Header.Set("x-api-key", transport.credentials.AnthropicAPIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		if !validReplaySecret(transport.credentials.GeminiAPIKey) {
			return errors.New("cachebench: Gemini API key unavailable")
		}
		request.Header.Set("x-goog-api-key", transport.credentials.GeminiAPIKey)
	case "bedrock":
		if validReplaySecret(transport.credentials.BedrockAPIKey) {
			request.Header.Set("authorization", "Bearer "+transport.credentials.BedrockAPIKey)
			return nil
		}
		if !validAWSRegion(region) || !transport.credentials.AWS.Valid() || !validReplaySecret(transport.credentials.AWS.AccessKeyID) || !validReplaySecret(transport.credentials.AWS.SecretAccessKey) || transport.credentials.AWS.SessionToken != "" && !validReplaySecret(transport.credentials.AWS.SessionToken) {
			return errors.New("cachebench: Bedrock credentials unavailable")
		}
		signer := awssig.Signer{Region: region, Service: "bedrock"}
		if err := signer.Sign(request, transport.credentials.AWS, awssig.HashPayload(body), time.Now().UTC()); err != nil {
			return errors.New("cachebench: Bedrock request signing failed")
		}
	default:
		return errors.New("cachebench: unsupported replay provider")
	}
	return nil
}

func validateReplayBaseURL(raw string, allowInsecureLoopback bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid absolute base URL")
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	if parsed.Scheme == "http" && allowInsecureLoopback && isLoopbackHost(parsed.Hostname()) {
		return parsed, nil
	}
	return nil, errors.New("HTTPS required; HTTP is test-only on explicit loopback")
}

func validReplaySecret(value string) bool {
	return validBoundedText(value, 16*1024, false)
}

func validAWSRegion(value string) bool {
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func providerRequestID(header http.Header) string {
	for _, name := range []string{"x-request-id", "request-id", "x-amzn-requestid", "x-goog-request-id"} {
		value := strings.TrimSpace(header.Get(name))
		if value != "" && validProviderRequestID(value) {
			return value
		}
	}
	return ""
}
