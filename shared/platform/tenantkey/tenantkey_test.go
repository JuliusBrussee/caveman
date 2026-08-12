package tenantkey_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/tenantkey"
)

func TestProjectKeysCannotCollideAcrossOrganizations(t *testing.T) {
	a, err := tenantkey.CavePlan("org-a", "project-shared")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tenantkey.CavePlan("org-b", "project-shared")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("cross-organization keys collide: %q", a)
	}
}

func TestProjectKeysRejectMissingOrAmbiguousScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		org     string
		project string
	}{
		{name: "missing organization", project: "project-a"},
		{name: "missing project", org: "org-a"},
		{name: "organization separator", org: "org:a", project: "project-a"},
		{name: "project separator", org: "org-a", project: "project:a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tenantkey.CavePlan(test.org, test.project); err == nil {
				t.Fatal("unsafe scope accepted")
			}
		})
	}
}
