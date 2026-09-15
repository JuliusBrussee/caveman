package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStatsHTMLPreservesPrivateEscapedSnapshot(t *testing.T) {
	report := statsHTMLFixture()
	attack := `</script><img src=x onerror="alert(1)">`
	report.Groups[0].Model = attack
	report.Evidence.Caveats = append(report.Evidence.Caveats, attack)
	path := filepath.Join(t.TempDir(), "reports", "stats.html")
	var store Store
	if err := store.WriteStatsHTML(report, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if strings.Contains(html, attack) {
		t.Fatal("untrusted report text escaped the JSON script boundary")
	}
	start := strings.Index(html, `<script id="stats-data" type="application/json">`)
	if start < 0 {
		t.Fatal("embedded data missing")
	}
	start += len(`<script id="stats-data" type="application/json">`)
	end := strings.Index(html[start:], "</script>")
	var roundTrip StatsReport
	if err := json.Unmarshal([]byte(html[start:start+end]), &roundTrip); err != nil {
		t.Fatalf("embedded data is not valid JSON: %v", err)
	}
	if roundTrip.Groups[0].Model != attack || roundTrip.Groups[0].SavedTokens != report.Groups[0].SavedTokens {
		t.Fatal("escaped snapshot changed its data")
	}
	if roundTrip.Groups[2].APISavingsUSD != nil || roundTrip.Groups[2].APIEquivalentSavingsUSD == nil {
		t.Fatal("subscription equivalent merged into API savings")
	}
	if strings.Contains(html, "innerHTML") || strings.Contains(html, `<script src=`) || strings.Contains(html, `href="http`) || strings.Contains(html, "fetch(") {
		t.Fatal("report must remain offline and use text nodes for report content")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows FileMode carries only the read-only attribute, so a private file
	// still reports 0666; os.Stat above already proves it was created.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("report permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestStatsHTMLAtomicReplacementDoesNotFollowTargetSymlink(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "protected.txt")
	if err := os.WriteFile(protected, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stats.html")
	if err := os.Symlink(protected, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var store Store
	if err := store.WriteStatsHTML(StatsReport{}, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(protected)
	if err != nil || string(raw) != "unchanged" {
		t.Fatalf("report followed output symlink: %q, %v", raw, err)
	}
}

func TestStatsHTMLFixture(t *testing.T) {
	var store Store
	if err := store.WriteStatsHTML(StatsReport{}, ""); err == nil {
		t.Fatal("empty report path accepted")
	}
	dir := t.TempDir()
	// Explicit preview opt-in is for browser review only. Production never
	// substitutes demo values for an empty local ledger.
	if requested := os.Getenv("CAVEMAN_STATS_FIXTURE_DIR"); requested != "" {
		dir = requested
	}
	for name, report := range map[string]StatsReport{
		"caveman-stats-preview.html": statsHTMLFixture(),
		"caveman-stats-empty.html": {
			Schema: "caveman.stats.v1", GeneratedAt: "2026-09-08T12:00:00Z",
			Window:   StatsWindow{From: "2026-08-10T00:00:00Z", To: "2026-09-08T12:00:00Z", Days: 30},
			Evidence: StatsEvidence{Caveats: []string{"DEMONSTRATION FIXTURE: empty ledger, no real usage."}},
		},
	} {
		path := filepath.Join(dir, name)
		if err := store.WriteStatsHTML(report, path); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.ReplaceAll(string(raw), "<h1>Caveman Stats</h1>", "<h1>Caveman Stats · fixture</h1>"))
		raw = []byte(strings.ReplaceAll(string(raw), "Every token has a story. Here is what changed.", "Demonstration data for visual review. No real usage or savings."))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatsHTMLInteractiveAccounting(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the dashboard interaction check")
	}
	path := filepath.Join(t.TempDir(), "stats.html")
	var store Store
	if err := store.WriteStatsHTML(statsHTMLFixture(), path); err != nil {
		t.Fatal(err)
	}
	// A small DOM stand-in exercises the shipped script, including filtering and
	// all DOM rendering. It requires no browser, package install, or network.
	const check = `
const fs=require('node:fs'), vm=require('node:vm'), assert=require('node:assert/strict');
const html=fs.readFileSync(process.argv[1],'utf8');
class Element {
  constructor(tag='div') {this.tag=tag;this.children=[];this.value='';this.style={};this.dataset={};this.attributes={};this.textContent='';this.handlers={};}
  append(...children) {this.children.push(...children);}
  replaceChildren(...children) {this.children=children;}
  setAttribute(key,value) {this.attributes[key]=value;}
  addEventListener(name,fn) {this.handlers[name]=fn;}
  remove() {}
  click() {if(this.handlers.click)this.handlers.click();}
}
const elements=new Map();
for(const match of html.matchAll(/\bid="([^"]+)"/g)) elements.set(match[1],new Element());
elements.get('period').value='all';
elements.get('stats-data').textContent=html.match(/<script id="stats-data" type="application\/json">([\s\S]*?)<\/script>/)[1];
const metrics=['tokens','api','equivalent'].map(metric=>{const el=new Element('button');el.dataset.metric=metric;return el;});
const breakdown=['provider','model','agent'].map(group=>{const el=new Element('button');el.dataset.group=group;return el;});
const document={getElementById:id=>{assert(elements.has(id),'missing element '+id);return elements.get(id);},createElement:tag=>new Element(tag),createElementNS:(_,tag)=>new Element(tag),querySelectorAll:selector=>selector==='[data-metric]'?metrics:breakdown,body:new Element()};
const context=vm.createContext({document,Intl,Date,Map,Set,Number,Array,Object,String,JSON,Math,console,Blob,URL,setTimeout});
vm.runInContext(html.match(/<script>\s*([\s\S]*?)<\/script>/)[1],context);
const read=expression=>vm.runInContext(expression,context);
assert.equal(read("$('net').textContent"),'1.36M');
assert.equal(read("sum(groups).saved_tokens"),1362500);
assert.equal(read("$('receipts').children.length"),42);
assert.equal(read("$('legacy-summary').hidden"),false);
assert.equal(read("sum(groups).saved_tokens"),1362500);
read("state.metric='legacy'; render()");
assert.equal(read("$('chart-value').textContent"),'900');
assert.equal(read("$('net').textContent"),'1.36M');
read("state.metric='tokens'; render()");
read("$('provider').value='custom-provider'; render()");
assert.equal(read("$('net').textContent"),'—');
assert.equal(read("$('api-value').textContent"),'Unknown');
assert.equal(read("$('equivalent-value').textContent"),'Unknown');
assert.match(read("$('scope').textContent"),/^2 requests/);
read("$('provider').value='openai'; $('auth').value='oauth'; render()");
assert.match(read("$('scope').textContent"),/^112 requests/);
assert.equal(read("$('api-value').textContent"),'Unknown');
assert.notEqual(read("$('equivalent-value').textContent"),'Unknown');
assert.equal(read("$('receipts').children.length"),14);
read("$('period').value='1'; render()");
assert.match(read("$('scope').textContent"),/^8 requests/);
assert.equal(read("$('receipts').children.length"),1);
assert.equal(read("$('net').textContent"),'35.5K');
read("$('provider').value=''; $('auth').value=''; $('period').value='all'; render(); renderHero(groups.find(row=>row.saved_tokens<0))");
assert.equal(read("$('net').textContent"),'−24K');
assert.match(read("$('net').className"),/negative/);
assert.match(read("$('api-value').textContent"),/^−\$/);
read("renderChart([{key:'2026-09-05',total:groups.find(row=>row.saved_tokens<0)}],groups.find(row=>row.saved_tokens<0))");
assert.equal(read("$('chart').children[0].children.some(el=>el.tag==='rect'&&el.attributes.fill==='#d44c47')"),true);
read("renderHero(sum([]))");
assert.equal(read("$('net').textContent"),'—');
assert.equal(read("$('spend-value').textContent"),'Unknown');
`
	if output, err := exec.Command(nodeBin, "-e", check, path).CombinedOutput(); err != nil {
		t.Fatalf("dashboard interaction check: %v\n%s", err, output)
	}
}

func statsHTMLFixture() StatsReport {
	usd := func(n float64) *float64 { return &n }
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	report := StatsReport{
		Schema: "caveman.stats.v1", GeneratedAt: now.Format(time.RFC3339),
		Window: StatsWindow{From: "2026-08-26T00:00:00Z", To: now.Format(time.RFC3339), Days: 14},
		Evidence: StatsEvidence{
			Basis: "inferred", TokenBasis: "estimated_engine_o200k", SpendBasis: "provider_counted_x_published_rate",
			CatalogVersions: []string{"fixture-2026-09-08"}, ReceiptsLimit: 200,
			Caveats: []string{"DEMONSTRATION FIXTURE: all displayed counts and prices are invented test data, not measured savings.", "Full-session recovery and earlier native/MCP transforms are outside this counterfactual."},
		},
	}
	for day := 0; day < 14; day++ {
		for provider := 0; provider < 3; provider++ {
			before := int64(90000 + day*6000 + provider*7000)
			saved := int64(21000 + (day%4)*6500 + provider*4000)
			if day == 10 && provider == 0 {
				saved = -24000
			}
			group := StatsGroup{
				Day: now.AddDate(0, 0, day-13).Format("2006-01-02"), Provider: "openai", Model: "gpt-5.6", Agent: "codex", AuthMode: "api_key",
				StatsMetrics: StatsMetrics{
					Requests: 8, SuccessfulRequests: 8, CompleteUsageRequests: 8, InputTokens: before - saved,
					OutputTokens: 13000, CacheReadTokens: 28000, MeasuredRequests: 8, BeforeTokens: before, AfterTokens: before - saved,
					SavedTokens: saved, PricedRequests: 8, APISpendRequests: 8, APISavingsRequests: 8,
					APISpendUSD: usd(.95 + float64(day)*.1), APISavingsUSD: usd(float64(saved) * .000003),
				},
			}
			if saved < 0 {
				group.ExpandedRequests = 1
			}
			if provider == 1 {
				group.Provider, group.Model, group.Agent = "anthropic", "claude-fable-5", "claude"
			}
			if provider == 2 {
				group.AuthMode = "oauth"
				group.APISpendRequests, group.APISavingsRequests = 0, 0
				group.EquivalentSpendRequests, group.EquivalentSavingsRequests = 8, 8
				group.APIEquivalentSpendUSD, group.APIEquivalentSavingsUSD = group.APISpendUSD, group.APISavingsUSD
				group.APISpendUSD, group.APISavingsUSD = nil, nil
			}
			report.Groups = append(report.Groups, group)
			receipt := StatsReceipt{
				ID:        "fixture-" + group.Day + "-" + group.AuthMode + "-" + group.Provider,
				Timestamp: group.Day + "T10:30:00Z", Provider: group.Provider, Model: group.Model, Agent: group.Agent, AuthMode: group.AuthMode,
				StatusCode: 200, MeasurementStatus: "measured", MeasurementBasis: "estimated_engine_o200k", SavingsBasis: "request_boundary_counterfactual", TokenUsageBasis: "provider_counted",
				TokensBefore: before, TokensAfter: before - saved, SavedTokens: &saved, StatsMetrics: group.StatsMetrics,
				EffectiveInputRatePerMillion: usd(3), EstimatedInputDeltaUSD: usd(float64(saved) * .000003),
				Price: &StatsPrice{Provider: group.Provider, Model: group.Model, CatalogVersion: "fixture-2026-09-08", InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: .3, CacheWritePerMillion: 3.75},
			}
			report.Receipts = append([]StatsReceipt{receipt}, report.Receipts...)
		}
	}
	report.Groups = append(report.Groups, StatsGroup{
		Day: "2026-09-08", Provider: "custom-provider", Model: "unpriced-model", Agent: "custom-agent", AuthMode: "unknown",
		StatsMetrics: StatsMetrics{Requests: 2, SuccessfulRequests: 2, UnmeasuredRequests: 2, PartialUsageRequests: 2, UnpricedRequests: 2, UnknownAuthRequests: 2, InputTokens: 7000, LegacyRequests: 1, LegacySavedTokens: 900},
	})
	return report
}
