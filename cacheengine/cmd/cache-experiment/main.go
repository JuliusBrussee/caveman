package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

type caseResult struct {
	Name                      string                   `json:"name"`
	Provider                  string                   `json:"provider"`
	Model                     string                   `json:"model"`
	Decision                  cacheengine.Decision     `json:"decision"`
	Reason                    string                   `json:"reason"`
	Mode                      cacheengine.Mode         `json:"mode"`
	Applied                   bool                     `json:"applied"`
	BodyChanged               bool                     `json:"body_changed"`
	OptimizerIDs              []string                 `json:"optimizer_ids,omitempty"`
	Breakpoints               []cacheengine.Breakpoint `json:"breakpoints,omitempty"`
	ExpectedNetInputRateUnits float64                  `json:"expected_net_input_rate_units"`
	ClaimBasis                string                   `json:"claim_basis"`
	VerifiedSavingsUSD        float64                  `json:"verified_savings_usd"`
}

type report struct {
	Experiment string       `json:"experiment"`
	Basis      string       `json:"basis"`
	ProviderIO bool         `json:"provider_io"`
	Cases      []caseResult `json:"cases"`
}

func main() {
	stable := strings.Repeat("stable policy and tool contract. ", 600)
	cases := []struct {
		name string
		req  cacheengine.NativeRequest
	}{
		{
			name: "anthropic stable plus rolling",
			req: request("anthropic", "claude-sonnet-4-6", "/v1/messages", "anthropic", 5000,
				fmt.Sprintf(`{"model":"claude-sonnet-4-6","system":%q,"messages":[{"role":"user","content":"question"}],"max_tokens":64}`, stable)),
		},
		{
			name: "openai explicit stable prefix",
			req: request("openai", "gpt-5.6", "/v1/chat/completions", "openai", 5000,
				fmt.Sprintf(`{"model":"gpt-5.6","messages":[{"role":"system","content":%q},{"role":"user","content":"question"}]}`, stable)),
		},
		{
			name: "gemini provider-managed implicit",
			req: request("gemini", "gemini-2.5-pro", "generateContent", "gemini", 5000,
				fmt.Sprintf(`{"systemInstruction":{"parts":[{"text":%q}]},"contents":[{"role":"user","parts":[{"text":"question"}]}]}`, stable)),
		},
		{
			name: "unknown provider honest zero",
			req:  request("unknown", "model", "/infer", "unknown", 5000, `{"prompt":"question"}`),
		},
	}

	engine := cacheengine.New(cacheengine.Config{MaxKeyShards: 64})
	output := report{
		Experiment: "cacheengine-native-offline-v1",
		Basis:      "modeled fixture token counts; transformations local; no provider hit asserted",
		ProviderIO: false,
	}
	for _, experimentCase := range cases {
		result, err := engine.Optimize(context.Background(), experimentCase.req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		output.Cases = append(output.Cases, caseResult{
			Name:                      experimentCase.name,
			Provider:                  experimentCase.req.Provider,
			Model:                     experimentCase.req.Model,
			Decision:                  result.Decision,
			Reason:                    result.Reason,
			Mode:                      result.Profile.Mode,
			Applied:                   result.Applied,
			BodyChanged:               string(result.Body) != string(experimentCase.req.Body),
			OptimizerIDs:              result.OptimizerIDs,
			Breakpoints:               result.Plan.Breakpoints,
			ExpectedNetInputRateUnits: result.Plan.ExpectedNetInputRateUnits,
			ClaimBasis:                result.ClaimBasis,
			VerifiedSavingsUSD:        result.VerifiedSavingsUSD,
		})
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func request(provider, model, endpoint, epoch string, prefixTokens int, body string) cacheengine.NativeRequest {
	return cacheengine.NativeRequest{
		Scope: "experiment/project", Epoch: epoch, PartitionKey: "fixture-session",
		Provider: provider, Model: model, Endpoint: endpoint, Body: []byte(body),
		RuntimeMode: "optimize", AuthMode: "payg", PrefixTokens: prefixTokens,
		ExpectedCalls: 6, ExpectedRequestsPerMinute: 20,
	}
}
