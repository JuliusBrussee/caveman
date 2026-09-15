package nativepack

import "testing"

func TestCompiledPackLoadsWithBoundedUniqueClassifiedSkills(t *testing.T) {
	pack, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if pack.Schema != "caveman.native-pack.v1" || !pack.Core.Mandatory || pack.Core.EstimatedTokens > pack.Core.PromptTokenBudget {
		t.Fatalf("invalid compiled Core: %+v", pack.Core)
	}
	if len(pack.Skills) != 6 || len(pack.Targets) != 6 {
		t.Fatalf("compiled pack shape skills=%d targets=%d", len(pack.Skills), len(pack.Targets))
	}
	wants := map[string]string{
		"feature": "lean-build", "bugfix": "surgical-patch", "investigation": "investigate-first",
		"refactor": "safe-refactor", "migration": "migration", "verification": "verify-and-stop",
	}
	for taskType, want := range wants {
		skill, ok := Select(taskType)
		if !ok || skill.ID != want || skill.EvidenceStatus != "structural-test-only" || len(skill.Instructions) > skill.PromptByteBudget {
			t.Fatalf("selection %s = %+v ok=%t", taskType, skill, ok)
		}
	}
	if _, ok := Select("review"); ok {
		t.Fatal("unowned task type must fail closed to Core")
	}
}
