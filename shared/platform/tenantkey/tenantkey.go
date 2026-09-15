// Package tenantkey owns tenant-scoped Valkey key formats shared across
// services. Callers cannot derive a project key without organization scope.
package tenantkey

import (
	"errors"
	"strings"
)

// CavePlan returns worker/gateway shared published-plan key.
func CavePlan(organizationID, projectID string) (string, error) {
	prefix, err := projectPrefix("plan", organizationID, projectID)
	if err != nil {
		return "", err
	}
	return prefix, nil
}

// MonthlySpend returns gateway/control-api shared monthly spend key.
func MonthlySpend(organizationID, projectID, month string) (string, error) {
	prefix, err := projectPrefix("spend", organizationID, projectID)
	if err != nil {
		return "", err
	}
	if !safePart(month) {
		return "", errors.New("tenantkey: safe month is required")
	}
	return prefix + ":" + month, nil
}

// MonthlySpendEvent returns idempotency-marker key for one spend event.
func MonthlySpendEvent(organizationID, projectID, month, requestID string) (string, error) {
	prefix, err := projectPrefix("spend-event", organizationID, projectID)
	if err != nil {
		return "", err
	}
	if !safePart(month) || !safePart(requestID) {
		return "", errors.New("tenantkey: safe month and request id are required")
	}
	return prefix + ":" + month + ":" + requestID, nil
}

func projectPrefix(namespace, organizationID, projectID string) (string, error) {
	if !safePart(organizationID) || !safePart(projectID) {
		return "", errors.New("tenantkey: safe organization and project scope are required")
	}
	return "cave:" + namespace + ":" + organizationID + ":" + projectID, nil
}

func safePart(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.ContainsAny(value, ":\r\n\x00")
}
