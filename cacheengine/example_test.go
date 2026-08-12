package cacheengine_test

import (
	"context"
	"fmt"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

func ExampleEngine_Optimize() {
	engine, err := cacheengine.NewChecked(cacheengine.Config{})
	if err != nil {
		panic(err)
	}
	result, err := engine.Optimize(context.Background(), cacheengine.NativeRequest{
		Scope: "org/project", Epoch: "conversation-42", PartitionKey: "agent-7",
		ExpectedRequestsPerMinute: 8, ExpectedCalls: 8,
		Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/responses",
		RuntimeMode: "optimize", AuthMode: "payg", PrefixTokens: 4096,
		Body: []byte(`{"model":"gpt-5.6","instructions":"stable policy","input":[{"role":"user","content":"hello"}],"max_output_tokens":64}`),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Decision, result.Applied)
	// Output: apply true
}
