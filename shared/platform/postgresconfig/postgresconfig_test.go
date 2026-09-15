package postgresconfig

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithOrgRejectsEmptyScopeBeforeOpeningTransaction(t *testing.T) {
	err := WithOrg(context.Background(), nil, "  ", nil)
	if err == nil || !strings.Contains(err.Error(), "organization scope is required") {
		t.Fatalf("empty scope error = %v", err)
	}
}

func TestProductionRequiresVerifyFullAndCA(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave?sslmode=require"); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("sslmode=require error = %v", err)
	}
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave?sslmode=verify-full"); err == nil || !strings.Contains(err.Error(), caEnvironment) {
		t.Fatalf("missing CA error = %v", err)
	}
}

func TestProductionPinsProvidedCAAndServerName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, string(ca))
	config, err := ParsePoolConfig("postgres://user:pass@db.internal:5432/cave?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.TLSConfig == nil || config.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Fatal("production TLS verification is disabled")
	}
	if config.ConnConfig.TLSConfig.ServerName != "db.internal" {
		t.Fatalf("server name = %q", config.ConnConfig.TLSConfig.ServerName)
	}
}

func TestProductionReadsCAFromPathWithoutPuttingPEMInEnvironment(t *testing.T) {
	ca := []byte("-----BEGIN CERTIFICATE-----\nfile-backed-ca\n-----END CERTIFICATE-----\n")
	path := filepath.Join(t.TempDir(), "postgres-ca.pem")
	if err := os.WriteFile(path, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	t.Setenv(caFileEnvironment, path)
	got, err := caPEMFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSpace(string(ca)) {
		t.Fatalf("file-backed CA = %q, want file contents", got)
	}
}

func TestRejectsDirectAndFileCATogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postgres-ca.pem")
	if err := os.WriteFile(path, []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "direct")
	t.Setenv(caFileEnvironment, path)
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both CA sources error = %v", err)
	}
}

func TestLocalPlaintextRemainsAvailable(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "")
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRejectsMalformedAndNonPostgresURLs(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	for _, databaseURL := range []string{
		"://broken",
		"https://db.example/cave?sslmode=verify-full",
		"postgres:///cave?sslmode=verify-full",
	} {
		if _, err := ParsePoolConfig(databaseURL); err == nil || !strings.Contains(err.Error(), "must be a Postgres URL") {
			t.Fatalf("ParsePoolConfig(%q) error = %v", databaseURL, err)
		}
	}
}

func TestConfiguredCARejectsInvalidPEMAndPlaintextTLS(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "not a certificate")
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=verify-full"); err == nil || !strings.Contains(err.Error(), "contains no valid certificate") {
		t.Fatalf("invalid CA error = %v", err)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	t.Setenv(caEnvironment, string(ca))
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err == nil || !strings.Contains(err.Error(), "TLS is disabled") {
		t.Fatalf("plaintext with CA error = %v", err)
	}
}

func TestPoolConstructorsReturnParseErrorsWithoutDialing(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "")
	const invalid = "postgres://%zz"
	if _, err := NewPool(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "parse DATABASE_URL") {
		t.Fatalf("NewPool() error = %v", err)
	}
	if _, err := NewRuntimePool(context.Background(), invalid, "cave_control_runtime"); err == nil || !strings.Contains(err.Error(), "parse DATABASE_URL") {
		t.Fatalf("NewRuntimePool() error = %v", err)
	}
}

func TestValidateRuntimeIdentityRequiresExpectedRoleBeforeQuery(t *testing.T) {
	if err := ValidateRuntimeIdentity(context.Background(), nil, " "); err == nil || !strings.Contains(err.Error(), "expected runtime role is required") {
		t.Fatalf("empty expected role error = %v", err)
	}
}

func TestRuntimeIdentityDecisionFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		session    string
		superuser  bool
		bypassRLS  bool
		member     bool
		ownsTenant bool
		want       string
	}{
		{name: "safe", session: "cave_control", member: true},
		{name: "session role can be restored", session: "postgres", member: true, want: "differs from session_user"},
		{name: "superuser", session: "cave_control", superuser: true, member: true, want: "unsafe runtime identity"},
		{name: "bypass RLS", session: "cave_control", bypassRLS: true, member: true, want: "unsafe runtime identity"},
		{name: "table owner", session: "cave_control", member: true, ownsTenant: true, want: "unsafe runtime identity"},
		{name: "wrong group", session: "cave_control", want: "is not a member"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRuntimeIdentity(
				"cave_control",
				test.session,
				"cave_control_runtime",
				test.superuser,
				test.bypassRLS,
				test.member,
				test.ownsTenant,
			)
			if test.want == "" {
				if err != nil {
					t.Fatalf("safe identity error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("identity error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTenantPolicyRequiresExactOrganizationEquality(t *testing.T) {
	base := tenantTableSchema{
		name:                  "spans",
		rowSecurity:           true,
		forceRowSecurity:      true,
		policyExists:          true,
		policyPermissive:      true,
		policyCommand:         "*",
		policyAppliesToPublic: true,
		policyUsingExpression: `(organization_id = (current_setting('app.current_organization_id'::text, true))::uuid)`,
		policyCheckExpression: `(organization_id = (current_setting('app.current_organization_id'::text, true))::uuid)`,
	}
	if !tenantPolicyIsCanonical(base) {
		t.Fatal("canonical UUID tenant policy was rejected")
	}
	oneArgument := base
	oneArgument.policyUsingExpression = `(organization_id = (current_setting('app.current_organization_id'::text))::uuid)`
	oneArgument.policyCheckExpression = oneArgument.policyUsingExpression
	if !tenantPolicyIsCanonical(oneArgument) {
		t.Fatal("fail-closed one-argument current_setting policy was rejected")
	}

	unsafeExpressions := []string{
		`(organization_id <> (current_setting('app.current_organization_id'::text, true))::uuid)`,
		`((organization_id = (current_setting('app.current_organization_id'::text, true))::uuid) OR true)`,
		`(organization_id = (current_setting('app.other_organization_id'::text, true))::uuid)`,
	}
	for _, expression := range unsafeExpressions {
		t.Run(expression, func(t *testing.T) {
			table := base
			table.policyUsingExpression = expression
			table.policyCheckExpression = expression
			if tenantPolicyIsCanonical(table) {
				t.Fatalf("unsafe tenant policy accepted: %s", expression)
			}
		})
	}

	mismatchedCheck := base
	mismatchedCheck.policyCheckExpression = `true`
	if tenantPolicyIsCanonical(mismatchedCheck) {
		t.Fatal("policy with a weaker WITH CHECK expression was accepted")
	}
}

func TestTenantSchemaValidatesEveryForeignKeyByAlignedColumnPosition(t *testing.T) {
	tables := []tenantTableSchema{{
		name:                  "children",
		rowSecurity:           true,
		forceRowSecurity:      true,
		policyExists:          true,
		policyPermissive:      true,
		policyCommand:         "*",
		policyAppliesToPublic: true,
		policyUsingExpression: `(organization_id = current_setting('app.current_organization_id'::text, true))`,
		policyCheckExpression: `(organization_id = current_setting('app.current_organization_id'::text, true))`,
	}}
	foreignKeys := []tenantForeignKeySchema{
		{
			name:                "children_parent_safe",
			childTable:          "children",
			parentTable:         "parents",
			carriesOrganization: true,
		},
		{
			name:                "children_parent_unsafe",
			childTable:          "children",
			parentTable:         "parents",
			carriesOrganization: false,
		},
	}
	violations := tenantSchemaViolations(tables, foreignKeys)
	if len(violations) != 1 || !strings.Contains(violations[0], "children_parent_unsafe") {
		t.Fatalf("per-constraint violations = %v, want only unsafe FK", violations)
	}

	foreignKeys = []tenantForeignKeySchema{{
		name:                "children_parent_wrong_ordinal",
		childTable:          "children",
		parentTable:         "parents",
		childHasProjectID:   true,
		parentHasProjectID:  true,
		carriesOrganization: true,
		carriesProject:      false,
	}}
	violations = tenantSchemaViolations(tables, foreignKeys)
	if len(violations) != 1 || !strings.Contains(violations[0], "organization+project") {
		t.Fatalf("ordinal project-scope violations = %v", violations)
	}
}
