package engine_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

func TestDetect(t *testing.T) {
	e := engine.New(nil, nil)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json object", `{"a":1,"b":[1,2,3]}`, engine.TypeJSON},
		{"json array", `[1,2,3,4]`, engine.TypeJSON},
		{"diff", "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,5 +1,5 @@\n context\n-old\n+new\n context\n", engine.TypeDiff},
		{"go code", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n", engine.TypeCode},
		{"python code", "import os\n\ndef run(x):\n    return x + 1\n\nclass A:\n    def m(self):\n        return 2\n", engine.TypeCode},
		{"log", "2026-01-01T00:00:00Z [INFO] start\n[INFO] step\n[WARN] slow\n[ERROR] boom\n[INFO] end\n", engine.TypeLog},
		{"search results", "src/a.go:10:match\nsrc/b.go:11:match\nsrc/c.go:12:match\nsrc/d.go:13:match\nsrc/e.go:14:match\nsrc/f.go:15:match\n", engine.TypeSearchResult},
		{"csv table", "name,region,revenue,status\nalpha,eu,120,ok\nbravo,us,140,ok\ncharlie,apac,90,ok\ndelta,eu,180,ok\necho,us,160,ok\nfoxtrot,apac,110,ok\ngolf,eu,200,ok\nhotel,us,170,ok\nindia,apac,130,ok\n", engine.TypeTabular},
		{"markdown table", "| name | region | revenue |\n| --- | --- | ---: |\n| alpha | eu | 120 |\n| bravo | us | 140 |\n| charlie | apac | 90 |\n| delta | eu | 180 |\n| echo | us | 160 |\n| foxtrot | apac | 110 |\n", engine.TypeTabular},
		{"yaml config", "service:\n  name: api\n  port: 8080\n  database:\n    host: db.internal\n    port: 5432\n    pool: 20\n  cache:\n    host: cache.internal\n    ttl: 300\n  limits:\n    requests: 1000\n    burst: 50\n  telemetry:\n    enabled: true\n    endpoint: http://otel.internal\n", engine.TypeConfig},
		{"toml config", "[service]\nname = \"api\"\nport = 8080\n[database]\nhost = \"db.internal\"\nport = 5432\npool = 20\n[cache]\nhost = \"cache.internal\"\nttl = 300\n[limits]\nrequests = 1000\nburst = 50\n[telemetry]\nenabled = true\nendpoint = \"http://otel.internal\"\n", engine.TypeConfig},
		{"prose", "The quick brown fox jumps over the lazy dog. Nothing structured here at all.", engine.TypeText},
		{"empty", "", engine.TypeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.Detect([]byte(c.in)); got != c.want {
				t.Errorf("Detect(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}
