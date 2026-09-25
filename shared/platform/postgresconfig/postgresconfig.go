// Package postgresconfig builds pgx pools with production TLS identity checks.
package postgresconfig

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/JuliusBrussee/caveman/shared/platform/runtimeenv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	caEnvironment     = "CAVE_POSTGRES_CA_CERT"
	caFileEnvironment = "CAVE_POSTGRES_CA_CERT_FILE"
)

// ParsePoolConfig validates the connection string and applies the managed
// database CA to every TLS path. Production rejects sslmode=require because it
// encrypts without authenticating the server; verify-full is mandatory.
func ParsePoolConfig(databaseURL string) (*pgxpool.Config, error) {
	production := runtimeenv.IsProduction()
	if production {
		parsed, err := url.Parse(databaseURL)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
			return nil, errors.New("postgres: production DATABASE_URL must be a Postgres URL")
		}
		if parsed.Query().Get("sslmode") != "verify-full" {
			return nil, errors.New("postgres: production DATABASE_URL requires sslmode=verify-full")
		}
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}
	caPEM, err := caPEMFromEnvironment()
	if err != nil {
		return nil, err
	}
	if production && caPEM == "" {
		return nil, fmt.Errorf("postgres: %s or %s is required in production", caEnvironment, caFileEnvironment)
	}
	if caPEM == "" {
		return config, nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("postgres: %s contains no valid certificate", caEnvironment)
	}
	if config.ConnConfig.TLSConfig == nil {
		return nil, errors.New("postgres: CA certificate configured while TLS is disabled")
	}
	config.ConnConfig.TLSConfig.RootCAs = roots
	config.ConnConfig.TLSConfig.InsecureSkipVerify = false
	config.ConnConfig.TLSConfig.ServerName = config.ConnConfig.Host
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.TLSConfig == nil {
			if production {
				return nil, errors.New("postgres: production connection includes a plaintext fallback")
			}
			continue
		}
		fallback.TLSConfig.RootCAs = roots
		fallback.TLSConfig.InsecureSkipVerify = false
		fallback.TLSConfig.ServerName = fallback.Host
	}
	return config, nil
}

func caPEMFromEnvironment() (string, error) {
	filePath := strings.TrimSpace(os.Getenv(caFileEnvironment))
	direct := strings.TrimSpace(os.Getenv(caEnvironment))
	if filePath != "" && direct != "" {
		return "", fmt.Errorf("postgres: set only %s or %s, not both", caFileEnvironment, caEnvironment)
	}
	if filePath == "" {
		return direct, nil
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("postgres: %s: %w", caFileEnvironment, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("postgres: %s must point to a regular file", caFileEnvironment)
	}
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("postgres: read %s: %w", caFileEnvironment, err)
	}
	return strings.TrimSpace(string(contents)), nil
}

// NewPool constructs a pool from the hardened configuration.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := ParsePoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, config)
}

// NewRuntimePool constructs a pool and proves the connected login is a
// least-privilege member of expectedRole. Runtime services must never connect
// as a superuser, BYPASSRLS role, or owner of a tenant table; this remains true
// for local, on-prem, staging, and production deployments alike.
func NewRuntimePool(ctx context.Context, databaseURL, expectedRole string) (*pgxpool.Pool, error) {
	pool, err := NewPool(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := ValidateRuntimeIdentity(ctx, pool, expectedRole); err != nil {
		pool.Close()
		return nil, err
	}
	if err := ValidateTenantSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// ValidateRuntimeIdentity rejects database identities that can bypass RLS or
// do not inherit the service's audited privilege group.
func ValidateRuntimeIdentity(ctx context.Context, pool *pgxpool.Pool, expectedRole string) error {
	expectedRole = strings.TrimSpace(expectedRole)
	if expectedRole == "" {
		return errors.New("postgres: expected runtime role is required")
	}
	var currentUser, sessionUser string
	var superuser, bypassRLS, member, ownsTenantTable bool
	err := pool.QueryRow(ctx, `
		SELECT current_user,
		       session_user,
		       r.rolsuper,
		       r.rolbypassrls,
		       pg_has_role(current_user, $1, 'MEMBER'),
		       EXISTS (
		         SELECT 1
		         FROM pg_class c
		         JOIN pg_namespace n ON n.oid=c.relnamespace
		         JOIN information_schema.columns col
		           ON col.table_schema=n.nspname
		          AND col.table_name=c.relname
		          AND col.column_name='organization_id'
		         WHERE n.nspname='public'
		           AND c.relkind IN ('r','p')
		           AND pg_get_userbyid(c.relowner)=current_user
		       )
		FROM pg_roles r
		WHERE r.rolname=current_user
	`, expectedRole).Scan(&currentUser, &sessionUser, &superuser, &bypassRLS, &member, &ownsTenantTable)
	if err != nil {
		return fmt.Errorf("postgres: inspect runtime identity: %w", err)
	}
	return validateRuntimeIdentity(currentUser, sessionUser, expectedRole, superuser, bypassRLS, member, ownsTenantTable)
}

func validateRuntimeIdentity(currentUser, sessionUser, expectedRole string, superuser, bypassRLS, member, ownsTenantTable bool) error {
	// A safe SET ROLE is not a safe login: session_user may execute SET ROLE
	// NONE later and recover its original privileges. Runtime pools therefore
	// require the authenticated identity and effective identity to be identical.
	if currentUser != sessionUser {
		return fmt.Errorf("postgres: runtime current_user %q differs from session_user %q", currentUser, sessionUser)
	}
	if superuser || bypassRLS || ownsTenantTable {
		return fmt.Errorf("postgres: unsafe runtime identity %q (session_user=%q superuser=%t bypassrls=%t owns_tenant_table=%t)", currentUser, sessionUser, superuser, bypassRLS, ownsTenantTable)
	}
	if !member {
		return fmt.Errorf("postgres: runtime identity %q is not a member of %q", currentUser, expectedRole)
	}
	return nil
}

type tenantTableSchema struct {
	name                   string
	nullableOrganizationID bool
	rowSecurity            bool
	forceRowSecurity       bool
	policyExists           bool
	policyPermissive       bool
	policyCommand          string
	policyAppliesToPublic  bool
	policyUsingExpression  string
	policyCheckExpression  string
}

type tenantForeignKeySchema struct {
	name                string
	childTable          string
	parentTable         string
	childHasProjectID   bool
	parentHasProjectID  bool
	carriesOrganization bool
	carriesProject      bool
}

type catalogQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// organizationPolicyExpression matches the only accepted tenant predicate:
// organization_id equals the transaction-local tenant GUC, optionally cast to
// the column's scalar type by PostgreSQL. It is anchored so OR TRUE, a different
// operator, a different GUC, or an extra predicate cannot pass by containing
// the two expected strings.
var organizationPolicyExpression = regexp.MustCompile(`^organization_id=\(*current_setting\('app\.current_organization_id'(?:::text)?(?:,true)?\)\)*(?:::[A-Za-z_][A-Za-z0-9_.$"]*)?$`)

// ValidateTenantSchema proves structural isolation invariants that are easy to
// regress during schema evolution. Every tenant table must fail closed under
// FORCE RLS, and every foreign-key edge between tenant tables must carry the
// shared organization scope (plus project scope when both tables have it).
func ValidateTenantSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return fmt.Errorf("postgres: begin tenant schema inspection: %w", err)
	}
	defer tx.Rollback(ctx)

	tables, err := inspectTenantTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("postgres: inspect tenant tables: %w", err)
	}
	foreignKeys, err := inspectTenantForeignKeys(ctx, tx)
	if err != nil {
		return fmt.Errorf("postgres: inspect tenant foreign keys: %w", err)
	}
	if violations := tenantSchemaViolations(tables, foreignKeys); len(violations) > 0 {
		return fmt.Errorf("postgres: tenant schema isolation incomplete: %s", strings.Join(violations, "; "))
	}
	if err := validateResolverSchema(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: finish tenant schema inspection: %w", err)
	}
	return nil
}

func inspectTenantTables(ctx context.Context, queryer catalogQuerier) ([]tenantTableSchema, error) {
	rows, err := queryer.Query(ctx, `
		SELECT c.relname,
		       NOT organization.attnotnull,
		       c.relrowsecurity,
		       c.relforcerowsecurity,
		       pol.oid IS NOT NULL,
		       coalesce(pol.polpermissive, false),
		       coalesce(pol.polcmd::text, ''),
		       coalesce(pol.polroles=ARRAY[0::oid], false),
		       coalesce(pg_get_expr(pol.polqual, pol.polrelid), ''),
		       coalesce(pg_get_expr(pol.polwithcheck, pol.polrelid), '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute org
		  ON org.attrelid=c.oid
		 AND org.attname='organization_id'
		 AND NOT org.attisdropped
		LEFT JOIN pg_policy pol
		  ON pol.polrelid=c.oid
		 AND pol.polname='organization_isolation'
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		ORDER BY c.relname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []tenantTableSchema
	for rows.Next() {
		var table tenantTableSchema
		if err := rows.Scan(
			&table.name,
			&table.nullableOrganizationID,
			&table.rowSecurity,
			&table.forceRowSecurity,
			&table.policyExists,
			&table.policyPermissive,
			&table.policyCommand,
			&table.policyAppliesToPublic,
			&table.policyUsingExpression,
			&table.policyCheckExpression,
		); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func inspectTenantForeignKeys(ctx context.Context, queryer catalogQuerier) ([]tenantForeignKeySchema, error) {
	rows, err := queryer.Query(ctx, `
		WITH tenant_tables AS (
			SELECT c.oid,
			       c.relname AS table_name,
			       EXISTS (
			         SELECT 1
			         FROM pg_attribute project
			         WHERE project.attrelid=c.oid
			           AND project.attname='project_id'
			           AND NOT project.attisdropped
			       ) AS has_project_id
			FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_attribute org
			  ON org.attrelid=c.oid
			 AND org.attname='organization_id'
			 AND NOT org.attisdropped
			WHERE n.nspname='public' AND c.relkind IN ('r','p')
		), fk_columns AS (
			SELECT con.oid AS constraint_oid,
			       con.conname AS constraint_name,
			       child.table_name AS child_table,
			       parent.table_name AS parent_table,
			       child.has_project_id AS child_has_project_id,
			       parent.has_project_id AS parent_has_project_id,
			       child_column.attname AS child_column,
			       parent_column.attname AS parent_column
			FROM pg_constraint con
			JOIN tenant_tables child ON child.oid=con.conrelid
			JOIN tenant_tables parent ON parent.oid=con.confrelid
			JOIN LATERAL unnest(con.conkey) WITH ORDINALITY
			  AS child_key(attnum, key_ordinality) ON true
			JOIN LATERAL unnest(con.confkey) WITH ORDINALITY
			  AS parent_key(attnum, key_ordinality) ON parent_key.key_ordinality=child_key.key_ordinality
			JOIN pg_attribute child_column
			  ON child_column.attrelid=con.conrelid
			 AND child_column.attnum=child_key.attnum
			 AND NOT child_column.attisdropped
			JOIN pg_attribute parent_column
			  ON parent_column.attrelid=con.confrelid
			 AND parent_column.attnum=parent_key.attnum
			 AND NOT parent_column.attisdropped
			WHERE con.contype='f'
		)
		SELECT constraint_name,
		       child_table,
		       parent_table,
		       child_has_project_id,
		       parent_has_project_id,
		       bool_or(child_column='organization_id' AND parent_column='organization_id'),
		       bool_or(child_column='project_id' AND parent_column='project_id')
		FROM fk_columns
		GROUP BY constraint_oid, constraint_name, child_table, parent_table,
		         child_has_project_id, parent_has_project_id
		ORDER BY child_table, parent_table, constraint_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var foreignKeys []tenantForeignKeySchema
	for rows.Next() {
		var foreignKey tenantForeignKeySchema
		if err := rows.Scan(
			&foreignKey.name,
			&foreignKey.childTable,
			&foreignKey.parentTable,
			&foreignKey.childHasProjectID,
			&foreignKey.parentHasProjectID,
			&foreignKey.carriesOrganization,
			&foreignKey.carriesProject,
		); err != nil {
			return nil, err
		}
		foreignKeys = append(foreignKeys, foreignKey)
	}
	return foreignKeys, rows.Err()
}

func tenantSchemaViolations(tables []tenantTableSchema, foreignKeys []tenantForeignKeySchema) []string {
	var violations []string
	for _, table := range tables {
		var missing []string
		if table.nullableOrganizationID {
			missing = append(missing, "NOT NULL organization_id")
		}
		if !table.rowSecurity {
			missing = append(missing, "RLS")
		}
		if !table.forceRowSecurity {
			missing = append(missing, "FORCE RLS")
		}
		if !tenantPolicyIsCanonical(table) {
			missing = append(missing, "canonical organization_isolation policy")
		}
		if len(missing) > 0 {
			violations = append(violations, fmt.Sprintf("table %s lacks %s", table.name, strings.Join(missing, ", ")))
		}
	}
	for _, foreignKey := range foreignKeys {
		requiresProject := foreignKey.childHasProjectID && foreignKey.parentHasProjectID
		if foreignKey.carriesOrganization && (!requiresProject || foreignKey.carriesProject) {
			continue
		}
		scope := "organization"
		if requiresProject {
			scope = "organization+project"
		}
		violations = append(violations, fmt.Sprintf(
			"foreign key %s on %s -> %s lacks %s scope",
			foreignKey.name,
			foreignKey.childTable,
			foreignKey.parentTable,
			scope,
		))
	}
	sort.Strings(violations)
	return violations
}

func tenantPolicyIsCanonical(table tenantTableSchema) bool {
	if !table.policyExists || !table.policyPermissive || table.policyCommand != "*" || !table.policyAppliesToPublic {
		return false
	}
	using := normalizePolicyExpression(table.policyUsingExpression)
	check := normalizePolicyExpression(table.policyCheckExpression)
	return using != "" && using == check && organizationPolicyExpression.MatchString(using)
}

func normalizePolicyExpression(expression string) string {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, expression)
	for expressionHasEnclosingParens(normalized) {
		normalized = normalized[1 : len(normalized)-1]
	}
	return normalized
}

func expressionHasEnclosingParens(expression string) bool {
	if len(expression) < 2 || expression[0] != '(' || expression[len(expression)-1] != ')' {
		return false
	}
	depth := 0
	inString := false
	for i := 0; i < len(expression); i++ {
		switch expression[i] {
		case '\'':
			if inString && i+1 < len(expression) && expression[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
		case '(':
			if !inString {
				depth++
			}
		case ')':
			if !inString {
				depth--
				if depth == 0 && i != len(expression)-1 {
					return false
				}
			}
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0 && !inString
}

// validateResolverSchema keeps cross-tenant SECURITY DEFINER capabilities
// fixed-shape and least-privilege. A compromised runtime login can EXECUTE an
// approved function; it can never inherit its owner or widen that owner's DML.
func validateResolverSchema(ctx context.Context, queryer catalogQuerier) error {
	var violations string
	err := queryer.QueryRow(ctx, `
		WITH resolver_roles(role_name) AS (
			VALUES
			  ('cave_resolver'),
			  ('cave_auth_resolver'),
			  ('cave_integration_resolver'),
			  ('cave_billing_resolver'),
			  ('cave_worker_resolver'),
			  ('cave_purge_resolver'),
			  ('cave_ops_data_resolver'),
			  ('cave_device_resolver')
		), runtime_roles(role_name) AS (
			VALUES ('cave_app'), ('cave_worker'), ('cave_control_api'), ('cave_worker_runtime')
		), allowed_access(role_name, table_name, privilege_type) AS (
			VALUES
			  ('cave_auth_resolver', 'users', 'SELECT'),
			  ('cave_auth_resolver', 'users', 'UPDATE'),
			  ('cave_auth_resolver', 'memberships', 'SELECT'),
			  ('cave_auth_resolver', 'organizations', 'SELECT'),
			  ('cave_auth_resolver', 'sessions', 'SELECT'),
			  ('cave_auth_resolver', 'sessions', 'INSERT'),
			  ('cave_auth_resolver', 'audit_logs', 'INSERT'),
			  ('cave_auth_resolver', 'sessions', 'UPDATE'),
			  ('cave_auth_resolver', 'password_reset_tokens', 'SELECT'),
			  ('cave_auth_resolver', 'password_reset_tokens', 'INSERT'),
			  ('cave_auth_resolver', 'password_reset_tokens', 'UPDATE'),
			  ('cave_auth_resolver', 'password_reset_tokens', 'DELETE'),
			  ('cave_auth_resolver', 'oidc_providers', 'SELECT'),
			  ('cave_auth_resolver', 'password_reset_deliveries', 'SELECT'),
			  ('cave_auth_resolver', 'password_reset_deliveries', 'INSERT'),
			  ('cave_auth_resolver', 'password_reset_deliveries', 'UPDATE'),
			  ('cave_auth_resolver', 'password_reset_deliveries', 'DELETE'),
			  ('cave_integration_resolver', 'project_github_repos', 'SELECT'),
			  ('cave_integration_resolver', 'project_github_repos', 'UPDATE'),
			  ('cave_integration_resolver', 'policy_deliveries', 'SELECT'),
			  ('cave_billing_resolver', 'billing_accounts', 'SELECT'),
			  ('cave_billing_resolver', 'billing_accounts', 'UPDATE'),
			  ('cave_billing_resolver', 'gainshare_charges', 'SELECT'),
			  ('cave_billing_resolver', 'organizations', 'SELECT'),
			  ('cave_billing_resolver', 'projects', 'SELECT'),
			  ('cave_worker_resolver', 'organizations', 'SELECT'),
			  ('cave_worker_resolver', 'projects', 'SELECT'),
			  ('cave_worker_resolver', 'quality_monitors', 'SELECT'),
			  ('cave_worker_resolver', 'webhooks', 'SELECT'),
			  ('cave_worker_resolver', 'webhook_deliveries', 'SELECT'),
			  ('cave_worker_resolver', 'webhook_deliveries', 'UPDATE'),
			  ('cave_worker_resolver', 'digest_deliveries', 'SELECT'),
			  ('cave_worker_resolver', 'digest_deliveries', 'DELETE'),
			  ('cave_worker_resolver', 'audit_reports', 'SELECT'),
			  ('cave_worker_resolver', 'audit_reports', 'UPDATE'),
			  ('cave_worker_resolver', 'detector_watermarks', 'SELECT'),
			  ('cave_worker_resolver', 'cave_plan_snapshots', 'SELECT'),
			  ('cave_worker_resolver', 'memberships', 'SELECT'),
			  ('cave_worker_resolver', 'users', 'SELECT'),
			  ('cave_worker_resolver', 'experiments', 'SELECT'),
			  ('cave_worker_resolver', 'job_outbox', 'SELECT'),
			  ('cave_worker_resolver', 'data_subject_requests', 'SELECT'),
			  ('cave_worker_resolver', 'retention_policies', 'SELECT'),
			  ('cave_worker_resolver', 'opportunities', 'SELECT'),
			  ('cave_worker_resolver', 'workflow_fingerprints', 'SELECT'),
			  ('cave_ops_data_resolver', 'wrap_entitlements', 'SELECT')
			  ,('cave_device_resolver', 'device_grant_activations', 'SELECT')
			  ,('cave_device_resolver', 'device_grant_activations', 'UPDATE')
			  ,('cave_device_resolver', 'sessions', 'SELECT')
			  ,('cave_device_resolver', 'sessions', 'UPDATE')
			  ,('cave_device_resolver', 'project_api_keys', 'SELECT')
			  ,('cave_device_resolver', 'project_api_keys', 'UPDATE')
			  ,('cave_device_resolver', 'audit_logs', 'INSERT')
		), allowed_columns(role_name, table_name, column_name, privilege_type) AS (
			VALUES
			  ('cave_device_resolver', 'device_grant_activations', 'id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'organization_id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'user_id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'device_code_hash', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'gateway_key_hash', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'session_id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'gateway_key_id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'project_id', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'state', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'replay_expires_at', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'cache_tombstoned_at', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'old_key_hashes', 'SELECT'),
			  ('cave_device_resolver', 'device_grant_activations', 'state', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'refresh_token_ciphertext', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'gateway_key_ciphertext', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'gateway_key_cache_body_ciphertext', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'ack_token_ciphertext', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'cache_activated_at', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'issued_at', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'acknowledged_at', 'UPDATE'),
			  ('cave_device_resolver', 'device_grant_activations', 'updated_at', 'UPDATE'),
			  ('cave_device_resolver', 'sessions', 'id', 'SELECT'),
			  ('cave_device_resolver', 'sessions', 'organization_id', 'SELECT'),
			  ('cave_device_resolver', 'sessions', 'revoked_at', 'SELECT'),
			  ('cave_device_resolver', 'sessions', 'revoked_at', 'UPDATE'),
			  ('cave_device_resolver', 'project_api_keys', 'id', 'SELECT'),
			  ('cave_device_resolver', 'project_api_keys', 'organization_id', 'SELECT'),
			  ('cave_device_resolver', 'project_api_keys', 'project_id', 'SELECT'),
			  ('cave_device_resolver', 'project_api_keys', 'revoked_at', 'SELECT'),
			  ('cave_device_resolver', 'project_api_keys', 'revoked_at', 'UPDATE'),
			  ('cave_device_resolver', 'audit_logs', 'id', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'organization_id', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'actor_type', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'actor_id', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'action', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'resource_type', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'resource_id', 'INSERT'),
			  ('cave_device_resolver', 'audit_logs', 'metadata', 'INSERT')
		), violations AS (
			SELECT format('resolver role %I is missing or bypass-capable', rr.role_name) AS violation
			FROM resolver_roles rr
			LEFT JOIN pg_roles r ON r.rolname=rr.role_name
			WHERE r.oid IS NULL OR r.rolcanlogin OR r.rolsuper OR r.rolbypassrls
			UNION ALL
			SELECT format('runtime role %I can inherit or SET resolver role %I', runtime.role_name, resolver.role_name)
			FROM runtime_roles runtime
			CROSS JOIN resolver_roles resolver
			WHERE pg_has_role(runtime.role_name, resolver.role_name, 'MEMBER')
			UNION ALL
			SELECT format('historical cave_resolver still owns function %I', p.proname)
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid=p.pronamespace
			JOIN pg_roles r ON r.oid=p.proowner
			WHERE n.nspname='public' AND r.rolname='cave_resolver'
			UNION ALL
			SELECT 'device grant reconciler has an unexpected SECURITY DEFINER owner'
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid=p.pronamespace
			JOIN pg_roles r ON r.oid=p.proowner
			WHERE n.nspname='public'
			  AND p.proname='cave_reconcile_expired_device_grants'
			  AND pg_get_function_identity_arguments(p.oid) IN ('integer', 'p_limit integer')
			  AND (r.rolname <> 'cave_device_resolver' OR NOT p.prosecdef)
			UNION ALL
			SELECT 'device grant reconciler is missing'
			WHERE NOT EXISTS (
				SELECT 1
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid=p.pronamespace
				JOIN pg_roles r ON r.oid=p.proowner
				WHERE n.nspname='public'
				  AND p.proname='cave_reconcile_expired_device_grants'
				  AND pg_get_function_identity_arguments(p.oid) IN ('integer', 'p_limit integer')
				  AND p.prosecdef
				  AND r.rolname='cave_device_resolver'
			)
			UNION ALL
			SELECT 'device grant reconciler search_path is not pinned'
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid=p.pronamespace
			WHERE n.nspname='public'
			  AND p.proname='cave_reconcile_expired_device_grants'
			  AND pg_get_function_identity_arguments(p.oid) IN ('integer', 'p_limit integer')
			  AND NOT ('search_path=pg_catalog, public, pg_temp' = ANY(COALESCE(p.proconfig, ARRAY[]::text[])))
			UNION ALL
			SELECT 'device grant reconciler is executable by an untrusted role'
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid=p.pronamespace
			WHERE n.nspname='public'
			  AND p.proname='cave_reconcile_expired_device_grants'
			  AND pg_get_function_identity_arguments(p.oid) IN ('integer', 'p_limit integer')
			  AND (
				  has_function_privilege('cave_worker', p.oid, 'EXECUTE')
				  OR has_function_privilege('cave_resolver', p.oid, 'EXECUTE')
				  OR EXISTS (
					  SELECT 1
					  FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) acl
					  WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE'
				  )
			  )
			UNION ALL
			SELECT 'device resolver schema ACL is unsafe'
			WHERE NOT has_schema_privilege('cave_device_resolver', 'public', 'USAGE')
			   OR has_schema_privilege('cave_device_resolver', 'public', 'CREATE')
			UNION ALL
			SELECT format('historical cave_resolver still has table privilege on %I', c.relname)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname='public' AND c.relkind IN ('r','p')
			  AND has_table_privilege('cave_resolver', c.oid, 'SELECT,INSERT,UPDATE,DELETE')
			UNION ALL
			SELECT format('resolver role %I has unexpected %s on %I', rr.role_name, privilege.privilege_type, c.relname)
			FROM resolver_roles rr
			JOIN pg_roles role ON role.rolname=rr.role_name
			CROSS JOIN pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			CROSS JOIN (VALUES ('SELECT'), ('INSERT'), ('UPDATE'), ('DELETE')) privilege(privilege_type)
			WHERE n.nspname='public' AND c.relkind IN ('r','p')
			  AND rr.role_name NOT IN ('cave_resolver', 'cave_purge_resolver')
			  AND has_table_privilege(rr.role_name, c.oid, privilege.privilege_type)
			  AND NOT EXISTS (
			    SELECT 1 FROM allowed_access allowed
			    WHERE allowed.role_name=rr.role_name
			      AND allowed.table_name=c.relname
			      AND allowed.privilege_type=privilege.privilege_type
			  )
			UNION ALL
			SELECT format('resolver role %I is missing %s on %I.%I', allowed.role_name, allowed.privilege_type, allowed.table_name, allowed.column_name)
			FROM allowed_columns allowed
			WHERE NOT has_column_privilege(
				allowed.role_name,
				format('%I.%I', 'public', allowed.table_name),
				allowed.column_name,
				allowed.privilege_type
			)
			UNION ALL
			SELECT format('resolver role %I has unexpected %s on %I.%I', cp.grantee, cp.privilege_type, cp.table_name, cp.column_name)
			FROM (
				SELECT grantee.rolname AS grantee, acl.privilege_type,
				       c.relname AS table_name, a.attname AS column_name
				FROM pg_attribute a
				JOIN pg_class c ON c.oid=a.attrelid
				JOIN pg_namespace n ON n.oid=c.relnamespace
				CROSS JOIN LATERAL aclexplode(a.attacl) acl
				JOIN pg_roles grantee ON grantee.oid=acl.grantee
				WHERE n.nspname='public' AND NOT a.attisdropped
			) cp
			WHERE cp.grantee='cave_device_resolver'
			  AND NOT EXISTS (
				SELECT 1 FROM allowed_columns allowed
				WHERE allowed.role_name=cp.grantee
				  AND allowed.table_name=cp.table_name
				  AND allowed.column_name=cp.column_name
				  AND allowed.privilege_type=cp.privilege_type
			  )
			UNION ALL
			SELECT format('purge resolver has unexpected %s on %I', privilege.privilege_type, c.relname)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			CROSS JOIN (VALUES ('SELECT'), ('INSERT'), ('UPDATE'), ('DELETE')) privilege(privilege_type)
			WHERE n.nspname='public' AND c.relkind IN ('r','p')
			  AND has_table_privilege('cave_purge_resolver', c.oid, privilege.privilege_type)
			  AND NOT (
			    (privilege.privilege_type IN ('SELECT','DELETE') AND (
			      c.relname IN ('organizations','users') OR EXISTS (
			        SELECT 1 FROM pg_attribute org_col
			        WHERE org_col.attrelid=c.oid
			          AND org_col.attname='organization_id' AND NOT org_col.attisdropped
			      )
			    ))
			    OR (privilege.privilege_type='SELECT' AND c.relname='memberships')
			    OR (privilege.privilege_type='UPDATE' AND c.relname='organizations')
			  )
			UNION ALL
			SELECT format('resolver role %I lacks RLS policy on %I', rr.role_name, c.relname)
			FROM resolver_roles rr
			JOIN pg_roles role ON role.rolname=rr.role_name
			CROSS JOIN pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_attribute org_col ON org_col.attrelid=c.oid
			  AND org_col.attname='organization_id' AND NOT org_col.attisdropped
			WHERE n.nspname='public' AND c.relkind IN ('r','p')
			  AND has_table_privilege(rr.role_name, c.oid, 'SELECT,INSERT,UPDATE,DELETE')
			  AND NOT EXISTS (
			    SELECT 1 FROM pg_policy policy
			    WHERE policy.polrelid=c.oid
			      AND role.oid=ANY(policy.polroles)
			      AND policy.polqual IS NOT NULL
			      AND policy.polwithcheck IS NOT NULL
			  )
		)
		SELECT coalesce(string_agg(violation, '; ' ORDER BY violation), '') FROM violations
	`).Scan(&violations)
	if err != nil {
		return fmt.Errorf("postgres: inspect resolver schema: %w", err)
	}
	if violations != "" {
		return fmt.Errorf("postgres: resolver isolation incomplete: %s", violations)
	}
	return nil
}

// WithOrg runs fn inside a transaction whose tenant GUC is transaction-local.
// Empty scopes are rejected: tenant work must never degrade to an unscoped
// query when RLS is the hard boundary.
func WithOrg(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(pgx.Tx) error) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return errors.New("postgres: organization scope is required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_organization_id', $1, true)`, orgID); err != nil {
		return fmt.Errorf("postgres: set organization scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
