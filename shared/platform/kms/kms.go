// Package kms wraps and unwraps small secrets with production key manager.
// Persisted envelopes record provider, region, and logical key ID so rotations
// do not make existing ciphertext ambiguous.
package kms

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/runtimeenv"
)

const (
	ProviderScaleway       = "scaleway"
	SecretsKeyEnvironment  = "KMS_SECRETS_KEY_ARN"
	PayloadsKeyEnvironment = "KMS_PAYLOADS_KEY_ARN"
	prefix                 = "cave-kms-v1:"
	maxPlaintextBytes      = 65535
	maxEnvelopeBytes       = 256 << 10
	maxResponseBytes       = 512 << 10
	scalewayAPI            = "https://api.scaleway.com"
)

var (
	regionPattern = regexp.MustCompile(`^[a-z]{2,4}-[a-z0-9]{3,8}$`)
	keyIDPattern  = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
)

// Config contains no persisted state. APIBaseURL and HTTPClient support focused
// tests; production environment loading always uses Scaleway's fixed API host.
type Config struct {
	Provider   string
	Region     string
	KeyID      string
	AuthToken  string
	APIBaseURL string
	HTTPClient *http.Client
	// AllowedDecryptKeyIDs supports explicit rotation/backward compatibility.
	// Encrypt always uses KeyID; envelope metadata cannot select any other key.
	AllowedDecryptKeyIDs []string
}

// Client is immutable and safe for concurrent use.
type Client struct {
	provider      string
	region        string
	keyID         string
	token         string
	apiBaseURL    string
	httpClient    *http.Client
	decryptKeyIDs map[string]struct{}
}

// Envelope is safe to persist. Ciphertext is opaque provider output.
type Envelope struct {
	Provider   string `json:"provider"`
	Region     string `json:"region"`
	KeyID      string `json:"key_id"`
	Ciphertext string `json:"ciphertext"`
}

// New validates configuration and returns immutable client.
func New(cfg Config) (*Client, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider != ProviderScaleway {
		return nil, fmt.Errorf("kms: unsupported provider %q", provider)
	}
	region, keyID := strings.TrimSpace(cfg.Region), strings.TrimSpace(cfg.KeyID)
	if err := validateLocation(region, keyID); err != nil {
		return nil, err
	}
	decryptKeyIDs := map[string]struct{}{keyID: {}}
	for _, allowedKeyID := range cfg.AllowedDecryptKeyIDs {
		allowedKeyID = strings.TrimSpace(allowedKeyID)
		if err := validateLocation(region, allowedKeyID); err != nil {
			return nil, err
		}
		decryptKeyIDs[allowedKeyID] = struct{}{}
	}
	token := strings.TrimSpace(cfg.AuthToken)
	if len(token) < 20 {
		return nil, errors.New("kms: auth token is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if baseURL == "" {
		baseURL = scalewayAPI
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("kms: invalid API base URL")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{provider: provider, region: region, keyID: keyID, token: token, apiBaseURL: baseURL, httpClient: client, decryptKeyIDs: decryptKeyIDs}, nil
}

// FromEnvironment loads production configuration. Endpoint is not configurable,
// preventing env-driven host redirection of KMS credentials.
func FromEnvironment() (*Client, error) {
	return fromEnvironment(SecretsKeyEnvironment)
}

// FromPayloadEnvironment selects the payload KEK and permits the secrets KEK
// only for decrypting artifacts created before key separation shipped.
func FromPayloadEnvironment() (*Client, error) {
	return fromEnvironment(PayloadsKeyEnvironment, SecretsKeyEnvironment)
}

func fromEnvironment(primaryKeyEnvironment string, legacyKeyEnvironments ...string) (*Client, error) {
	region := strings.TrimSpace(os.Getenv("CAVE_KMS_REGION"))
	if region == "" {
		region = strings.TrimSpace(os.Getenv("SCW_DEFAULT_REGION"))
	}
	token := strings.TrimSpace(os.Getenv("CAVE_KMS_AUTH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("SCW_SECRET_KEY"))
	}
	allowed := make([]string, 0, len(legacyKeyEnvironments))
	for _, name := range legacyKeyEnvironments {
		if keyID := strings.TrimSpace(os.Getenv(name)); keyID != "" {
			allowed = append(allowed, keyID)
		}
	}
	return New(Config{
		Provider:             os.Getenv("CAVE_KMS_PROVIDER"),
		Region:               region,
		KeyID:                os.Getenv(primaryKeyEnvironment),
		AuthToken:            token,
		APIBaseURL:           scalewayAPI,
		AllowedDecryptKeyIDs: allowed,
	})
}

// IsEnvelope reports whether blob uses versioned KMS envelope format.
func IsEnvelope(blob []byte) bool { return bytes.HasPrefix(blob, []byte(prefix)) }

// Encrypt delegates to configured environment client.
func Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	client, err := FromEnvironment()
	if err != nil {
		return nil, err
	}
	return client.Encrypt(ctx, plaintext)
}

// EncryptPayload wraps an artifact data key with the dedicated payload KEK.
func EncryptPayload(ctx context.Context, plaintext []byte) ([]byte, error) {
	client, err := FromPayloadEnvironment()
	if err != nil {
		return nil, err
	}
	return client.Encrypt(ctx, plaintext)
}

// Encrypt delegates encryption to key manager.
func (c *Client) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("kms: plaintext is empty")
	}
	if len(plaintext) > maxPlaintextBytes {
		return nil, fmt.Errorf("kms: plaintext exceeds %d bytes", maxPlaintextBytes)
	}
	var response struct {
		KeyID      string `json:"key_id"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := c.call(ctx, c.region, c.keyID, "encrypt", map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}, &response); err != nil {
		return nil, err
	}
	if response.KeyID != c.keyID || strings.TrimSpace(response.Ciphertext) == "" {
		return nil, errors.New("kms: invalid encrypt response")
	}
	envelope, err := json.Marshal(Envelope{Provider: c.provider, Region: c.region, KeyID: response.KeyID, Ciphertext: response.Ciphertext})
	if err != nil {
		return nil, fmt.Errorf("kms: encode envelope: %w", err)
	}
	return append([]byte(prefix), envelope...), nil
}

// Decrypt delegates to configured environment client.
func Decrypt(ctx context.Context, blob []byte) ([]byte, error) {
	client, err := FromEnvironment()
	if err != nil {
		return nil, err
	}
	return client.Decrypt(ctx, blob)
}

// DecryptPayload unwraps an artifact data key with the dedicated payload KEK,
// while allowing the explicitly configured legacy secrets key during cutover.
func DecryptPayload(ctx context.Context, blob []byte) ([]byte, error) {
	client, err := FromPayloadEnvironment()
	if err != nil {
		return nil, err
	}
	return client.Decrypt(ctx, blob)
}

// Decrypt unwraps versioned KMS envelope. Metadata can choose only validated
// key identity under configured provider; it can never choose host or token.
func (c *Client) Decrypt(ctx context.Context, blob []byte) ([]byte, error) {
	if !IsEnvelope(blob) {
		return nil, errors.New("kms: unknown envelope format")
	}
	if len(blob) > maxEnvelopeBytes {
		return nil, errors.New("kms: envelope exceeds size limit")
	}
	var envelope Envelope
	if err := json.Unmarshal(blob[len(prefix):], &envelope); err != nil {
		return nil, fmt.Errorf("kms: decode envelope: %w", err)
	}
	if envelope.Provider != c.provider {
		return nil, errors.New("kms: envelope provider does not match configured provider")
	}
	if err := validateLocation(envelope.Region, envelope.KeyID); err != nil {
		return nil, err
	}
	if envelope.Region != c.region {
		return nil, errors.New("kms: envelope region is not approved")
	}
	if _, ok := c.decryptKeyIDs[envelope.KeyID]; !ok {
		return nil, errors.New("kms: envelope key ID is not approved")
	}
	if strings.TrimSpace(envelope.Ciphertext) == "" {
		return nil, errors.New("kms: envelope ciphertext is empty")
	}
	var response struct {
		KeyID     string `json:"key_id"`
		Plaintext string `json:"plaintext"`
	}
	if err := c.call(ctx, envelope.Region, envelope.KeyID, "decrypt", map[string]string{
		"ciphertext": envelope.Ciphertext,
	}, &response); err != nil {
		return nil, err
	}
	if response.KeyID != envelope.KeyID || response.Plaintext == "" {
		return nil, errors.New("kms: invalid decrypt response")
	}
	plaintext, err := base64.StdEncoding.DecodeString(response.Plaintext)
	if err != nil {
		return nil, errors.New("kms: decrypt response plaintext is not valid base64")
	}
	if len(plaintext) == 0 || len(plaintext) > maxPlaintextBytes {
		return nil, errors.New("kms: decrypt response plaintext exceeds size limit")
	}
	return plaintext, nil
}

// ValidateProduction verifies real KMS configuration without network request.
func ValidateProduction() error {
	if !runtimeenv.IsProduction() {
		return nil
	}
	_, err := FromEnvironment()
	return err
}

// ValidatePayloadProduction verifies the dedicated artifact-payload KEK is
// configured. This is separate from ValidateProduction because control-plane
// services that never handle artifacts need only the secrets key.
func ValidatePayloadProduction() error {
	if !runtimeenv.IsProduction() {
		return nil
	}
	_, err := FromPayloadEnvironment()
	return err
}

// ProbeProduction proves the configured secrets KEK can both encrypt and
// decrypt. Static validation alone cannot detect a revoked token, missing key,
// wrong IAM policy, or unavailable Key Manager endpoint.
func ProbeProduction(ctx context.Context) error {
	if !runtimeenv.IsProduction() {
		return nil
	}
	client, err := FromEnvironment()
	if err != nil {
		return err
	}
	return client.Probe(ctx)
}

// ProbePayloadProduction performs the same live round-trip with the payload KEK.
func ProbePayloadProduction(ctx context.Context) error {
	if !runtimeenv.IsProduction() {
		return nil
	}
	client, err := FromPayloadEnvironment()
	if err != nil {
		return err
	}
	return client.Probe(ctx)
}

// Probe verifies live key access without persisting tenant data.
func (c *Client) Probe(ctx context.Context) error {
	plaintext := make([]byte, 32)
	if _, err := rand.Read(plaintext); err != nil {
		return fmt.Errorf("kms: generate probe: %w", err)
	}
	envelope, err := c.Encrypt(ctx, plaintext)
	if err != nil {
		return fmt.Errorf("kms: probe encrypt: %w", err)
	}
	decrypted, err := c.Decrypt(ctx, envelope)
	if err != nil {
		return fmt.Errorf("kms: probe decrypt: %w", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		return errors.New("kms: probe plaintext mismatch")
	}
	return nil
}

func validateLocation(region, keyID string) error {
	if !regionPattern.MatchString(region) {
		return errors.New("kms: invalid Scaleway region")
	}
	if !keyIDPattern.MatchString(keyID) {
		return errors.New("kms: invalid Scaleway key ID")
	}
	return nil
}

func (c *Client) call(ctx context.Context, region, keyID, operation string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("kms: encode %s request: %w", operation, err)
	}
	endpoint := c.apiBaseURL + "/key-manager/v1alpha1/regions/" + url.PathEscape(region) +
		"/keys/" + url.PathEscape(keyID) + "/" + operation
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("kms: create %s request: %w", operation, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("x-auth-token", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kms: %s request failed: %w", operation, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("kms: %s returned HTTP %d", operation, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("kms: read %s response: %w", operation, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("kms: %s response exceeds limit", operation)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("kms: decode %s response: %w", operation, err)
	}
	return nil
}
