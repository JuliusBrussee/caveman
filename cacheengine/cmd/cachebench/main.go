package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
	"github.com/JuliusBrussee/caveman/cacheengine/cachebench"
)

func main() {
	var (
		providerList    = flag.String("providers", "all", "comma-separated providers: anthropic,openai,bedrock,gemini,all")
		turns           = flag.Int("turns", 128, "agent requests per provider")
		compaction      = flag.Int("compaction-every", 64, "start new cache epoch every N turns; 0 disables")
		step            = flag.Duration("step", 3*time.Second, "time between agent requests")
		assumedTTL      = flag.Duration("assumed-ttl", 5*time.Minute, "simulation TTL when provider profile has no explicit TTL")
		staticTokens    = flag.Int("static-tokens", 8192, "declared stable system/tool prefix tokens")
		userTokens      = flag.Int("user-tokens", 64, "declared tokens per user turn")
		assistantTokens = flag.Int("assistant-tokens", 96, "declared tokens per assistant tool call")
		toolTokens      = flag.Int("tool-result-tokens", 256, "declared tokens per tool result")
		summaryTokens   = flag.Int("summary-tokens", 512, "declared tokens in each compaction summary")
		targetRate      = flag.Float64("target", 0.97, "required request and eligible-token hit rate, fraction")
		minRequests     = flag.Int("min-requests", 100, "minimum eligible requests per provider")
		format          = flag.String("format", "text", "text or json")
		includeDetails  = flag.Bool("include-requests", false, "include per-request rows in JSON")
		observations    = flag.String("observations", "", "provider-observation JSONL; switches from simulation to observed replay")
		traceIn         = flag.String("trace-in", "", "request trace JSONL required with -observations")
		traceOut        = flag.String("trace-out", "", "write generated provider request trace as JSONL")
		corpusPath      = flag.String("corpus", "", "LMCache agent corpus file, or - for stdin")
		corpusFormat    = flag.String("corpus-format", cachebench.CorpusFormatLMCacheJSONL, "lmcache-jsonl or hf-rows")
		corpusName      = flag.String("corpus-name", "lmcache-agentic-traces", "corpus name recorded in report")
		corpusLicense   = flag.String("corpus-license", "", "corpus license recorded in report")
		corpusRevision  = flag.String("corpus-revision", "", "immutable corpus revision recorded in report")
		corpusMaxRows   = flag.Int("corpus-max-rows", 100000, "fail closed above this many corpus rows")
		corpusMaxSess   = flag.Int("corpus-max-sessions", 10000, "fail closed above this many corpus sessions")
		corpusMaxBytes  = flag.Int64("corpus-max-bytes", 1<<30, "fail closed above this many retained corpus bytes")
	)
	flag.Parse()
	target := cachebench.Target{RequestHitRate: *targetRate, TokenHitRate: *targetRate, MinEligibleRequest: *minRequests}
	var (
		report cachebench.Report
		err    error
	)
	if *observations != "" && *corpusPath != "" {
		fatal(fmt.Errorf("cachebench: -observations and -corpus are mutually exclusive"))
	}
	if *observations != "" {
		if *traceIn == "" {
			fatal(fmt.Errorf("cachebench: -trace-in required with -observations"))
		}
		report, err = observedReport(*observations, *traceIn, target)
	} else if *corpusPath != "" {
		if *traceIn != "" {
			fatal(fmt.Errorf("cachebench: -trace-in cannot be combined with -corpus"))
		}
		providers, providerErr := selectProviders(*providerList)
		if providerErr != nil {
			fatal(providerErr)
		}
		report, err = corpusReport(*corpusPath, *corpusFormat, cachebench.CorpusMetadata{
			Name: *corpusName, License: *corpusLicense, Revision: *corpusRevision,
		}, cachebench.CorpusLimits{
			MaxRows: *corpusMaxRows, MaxSessions: *corpusMaxSess, MaxRetainedBytes: *corpusMaxBytes,
		}, providers, target, *traceOut)
	} else {
		scenario := cachebench.DefaultScenario()
		scenario.Turns = *turns
		scenario.CompactionEvery = *compaction
		scenario.Step = *step
		scenario.AssumedTTL = *assumedTTL
		scenario.StaticTokens = *staticTokens
		scenario.UserTokens = *userTokens
		scenario.AssistantTokens = *assistantTokens
		scenario.ToolResultTokens = *toolTokens
		scenario.SummaryTokens = *summaryTokens
		providers, providerErr := selectProviders(*providerList)
		if providerErr != nil {
			fatal(providerErr)
		}
		if *traceOut != "" {
			if err := writeTraces(*traceOut, providers, scenario); err != nil {
				fatal(err)
			}
		}
		report, err = cachebench.RunSimulated(context.Background(), cacheengine.New(cacheengine.Config{}), providers, scenario, target)
	}
	if err != nil {
		fatal(err)
	}
	switch *format {
	case "text":
		fmt.Print(cachebench.Render(report))
	case "json":
		if err := cachebench.WriteJSON(os.Stdout, report, *includeDetails); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("cachebench: unknown format %q", *format))
	}
	if !report.Overall.GatePassed {
		os.Exit(1)
	}
}

func corpusReport(path, format string, metadata cachebench.CorpusMetadata, limits cachebench.CorpusLimits, providers []cachebench.ProviderConfig, target cachebench.Target, traceOut string) (cachebench.Report, error) {
	reader := os.Stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return cachebench.Report{}, err
		}
		defer file.Close()
		reader = file
	}
	corpus, err := cachebench.ReadAgentCorpus(reader, format, metadata, limits)
	if err != nil {
		return cachebench.Report{}, err
	}
	if traceOut != "" {
		if err := writeCorpusTraces(traceOut, providers, corpus); err != nil {
			return cachebench.Report{}, err
		}
	}
	return cachebench.RunCorpus(context.Background(), cacheengine.New(cacheengine.Config{}), corpus, providers, target)
}

func observedReport(path, tracePath string, target cachebench.Target) (cachebench.Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachebench.Report{}, err
	}
	defer file.Close()
	records, err := cachebench.ReadObservationJSONL(file)
	if err != nil {
		return cachebench.Report{}, err
	}
	traceFile, err := os.Open(tracePath)
	if err != nil {
		return cachebench.Report{}, err
	}
	defer traceFile.Close()
	trace, err := cachebench.ReadTraceJSONL(traceFile)
	if err != nil {
		return cachebench.Report{}, err
	}
	return cachebench.EvaluateObservedAgainstTrace(records, trace, target)
}

func selectProviders(raw string) ([]cachebench.ProviderConfig, error) {
	available := map[string]cachebench.ProviderConfig{}
	for _, provider := range cachebench.DefaultProviders() {
		available[provider.Provider] = provider
	}
	requested := strings.Split(strings.ToLower(strings.TrimSpace(raw)), ",")
	if len(requested) == 1 && requested[0] == "all" {
		return cachebench.DefaultProviders(), nil
	}
	seen := map[string]bool{}
	var providers []cachebench.ProviderConfig
	for _, name := range requested {
		name = strings.TrimSpace(name)
		provider, ok := available[name]
		if !ok || name == "all" {
			valid := make([]string, 0, len(available))
			for candidate := range available {
				valid = append(valid, candidate)
			}
			sort.Strings(valid)
			return nil, fmt.Errorf("cachebench: unknown provider %q; valid: %s", name, strings.Join(valid, ","))
		}
		if !seen[name] {
			seen[name] = true
			providers = append(providers, provider)
		}
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("cachebench: no providers selected")
	}
	return providers, nil
}

func writeTraces(path string, providers []cachebench.ProviderConfig, scenario cachebench.Scenario) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, provider := range providers {
		trace, err := cachebench.GenerateTrace(provider, scenario)
		if err != nil {
			return err
		}
		if err := cachebench.WriteTraceJSONL(file, trace); err != nil {
			return err
		}
	}
	return file.Sync()
}

func writeCorpusTraces(path string, providers []cachebench.ProviderConfig, corpus cachebench.AgentCorpus) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, provider := range providers {
		trace, err := cachebench.BuildCorpusTrace(provider, corpus)
		if err != nil {
			return err
		}
		if err := cachebench.WriteTraceJSONL(file, trace); err != nil {
			return err
		}
	}
	return file.Sync()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
