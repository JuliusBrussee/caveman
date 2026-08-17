// Package nativepack loads policy compiled from public/skills canonical sources.
package nativepack

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

//go:embed native-pack.generated.json
var embedded []byte

type Core struct {
	Source            string `json:"source"`
	Mandatory         bool   `json:"mandatory"`
	PromptTokenBudget int    `json:"prompt_token_budget"`
	EstimatedTokens   int    `json:"estimated_tokens"`
	EstimateBasis     string `json:"estimate_basis"`
	Instructions      string `json:"instructions"`
}

type Activation struct {
	Core string `json:"core"`
	Task string `json:"task"`
}

type Skill struct {
	ID               string   `json:"id"`
	Summary          string   `json:"summary"`
	Activation       string   `json:"activation"`
	TaskTypes        []string `json:"task_types"`
	EvidenceStatus   string   `json:"evidence_status"`
	PromptByteBudget int      `json:"prompt_byte_budget"`
	Conflicts        []string `json:"conflicts"`
	Precedence       int      `json:"precedence"`
	Guardrails       []string `json:"guardrails"`
	EntryCondition   string   `json:"entry_condition"`
	StopCondition    string   `json:"stop_condition"`
	Instructions     string   `json:"instructions"`
}

type Pack struct {
	Schema   string                `json:"schema"`
	ID       string                `json:"id"`
	Version  string                `json:"version"`
	Protocol int                   `json:"protocol"`
	Core     Core                  `json:"core"`
	Targets  map[string]Activation `json:"targets"`
	Skills   []Skill               `json:"skills"`
}

var (
	loadOnce sync.Once
	loaded   Pack
	loadErr  error
)

func Load() (Pack, error) {
	loadOnce.Do(func() {
		loadErr = json.Unmarshal(embedded, &loaded)
		if loadErr == nil {
			loadErr = validate(loaded)
		}
	})
	return loaded, loadErr
}

func Select(taskType string) (Skill, bool) {
	pack, err := Load()
	if err != nil {
		return Skill{}, false
	}
	taskType = strings.ToLower(strings.TrimSpace(taskType))
	var selected Skill
	found := false
	for _, skill := range pack.Skills {
		for _, candidate := range skill.TaskTypes {
			if candidate == taskType {
				if !found || skill.Precedence > selected.Precedence {
					selected, found = skill, true
				}
			}
		}
	}
	return selected, found
}

func validate(pack Pack) error {
	if pack.Schema != "caveman.native-pack.v1" || pack.ID == "" || pack.Version == "" || pack.Protocol < 1 {
		return errors.New("native pack: invalid identity")
	}
	if !pack.Core.Mandatory || pack.Core.Instructions == "" || pack.Core.EstimatedTokens > pack.Core.PromptTokenBudget {
		return errors.New("native pack: invalid Core budget or instructions")
	}
	owners := map[string][]Skill{}
	ids := map[string]bool{}
	for _, skill := range pack.Skills {
		if skill.ID == "" || ids[skill.ID] || skill.Activation != "classified" || skill.Instructions == "" || len(skill.Instructions) > skill.PromptByteBudget {
			return fmt.Errorf("native pack: invalid skill %q", skill.ID)
		}
		ids[skill.ID] = true
		for _, taskType := range skill.TaskTypes {
			owners[taskType] = append(owners[taskType], skill)
		}
	}
	for _, skill := range pack.Skills {
		for _, conflict := range skill.Conflicts {
			if !ids[conflict] {
				return fmt.Errorf("native pack: %s conflicts with unknown skill %s", skill.ID, conflict)
			}
		}
	}
	for taskType, candidates := range owners {
		for left := 0; left < len(candidates); left++ {
			for right := left + 1; right < len(candidates); right++ {
				a, b := candidates[left], candidates[right]
				if !contains(a.Conflicts, b.ID) || !contains(b.Conflicts, a.ID) || a.Precedence == b.Precedence {
					return fmt.Errorf("native pack: unresolved conflict for task type %q between %s and %s", taskType, a.ID, b.ID)
				}
			}
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
