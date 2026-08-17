package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
	"gopkg.in/yaml.v3"
)

func yamlFixture() []byte {
	var b strings.Builder
	b.WriteString("service:\n  name: checkout\n  owner: payments\n  database:\n    host: db.internal\n    port: 5432\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "  feature_%02d:\n    enabled: %t\n    rollout: %d\n", i, i%2 == 0, i)
	}
	b.WriteString("  incident:\n    status: ERROR\n    action: rollback\n")
	return []byte(b.String())
}

func TestConfigYAMLKeepsParentsImportantAndQueryRelevantLines(t *testing.T) {
	c, ok := compressors.NewConfig().(compressors.QueryAwareCompressor)
	if !ok {
		t.Fatal("config compressor must be query-aware")
	}
	if c.SafetyClass() != safety.S4 {
		t.Fatal("config compressor must be S4")
	}
	input := yamlFixture()
	out, ok := c.CompressQuery(input, "feature_37 rollout")
	if !ok {
		t.Fatal("expected YAML compression")
	}
	if len(out) >= len(input) {
		t.Fatal("expected YAML to shrink")
	}
	for _, want := range [][]byte{
		[]byte("service:"),
		[]byte("feature_37:"),
		[]byte("rollout: 37"),
		[]byte("status: ERROR"),
		[]byte("config lines elided (caveman)"),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed YAML missing %q:\n%s", want, out)
		}
	}
	var parsed any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("compressed YAML must remain valid: %v\n%s", err, out)
	}
	if second, ok := c.CompressQuery(out, "feature_37 rollout"); ok && !bytes.Equal(out, second) {
		t.Fatalf("compressed YAML must be idempotent:\nfirst=%s\nsecond=%s", out, second)
	}
}

func TestConfigYAMLQueryKeepsSequenceItemParent(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "  - name: worker-%03d\n    environment: staging\n    max_inflight: 80\n    drift: false\n", i)
		if i == 61 {
			b.WriteString("  - name: settlement-writer\n    environment: production\n    max_inflight: 240\n    expected_max_inflight: 80\n    drift: true\n")
		}
	}
	input := []byte(b.String())
	c := compressors.NewConfig().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery(input, "find production config drift expected actual")
	if !ok {
		t.Fatal("expected sequence-shaped YAML compression")
	}
	for _, want := range [][]byte{
		[]byte("- name: settlement-writer"),
		[]byte("environment: production"),
		[]byte("max_inflight: 240"),
		[]byte("expected_max_inflight: 80"),
		[]byte("drift: true"),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed YAML missing %q:\n%s", want, out)
		}
	}
	var parsed any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("compressed sequence YAML must remain valid: %v\n%s", err, out)
	}
}

func TestConfigTOMLKeepsSectionHeadersAndRelevantValue(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "[feature.%02d]\nenabled = %t\nowner = \"team-%02d\"\n", i, i%2 == 0, i)
	}
	input := []byte(b.String())
	c := compressors.NewConfig().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery(input, "feature 17 team-17")
	if !ok {
		t.Fatal("expected TOML compression")
	}
	for _, want := range [][]byte{[]byte("[feature.17]"), []byte(`owner = "team-17"`), []byte("# …")} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed TOML missing %q:\n%s", want, out)
		}
	}
}

func TestConfigINIStaysSectionShapedAndKeepsQueryValue(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "[feature.%02d]\nenabled = %t\nowner = team-%02d\n", i, i%2 == 0, i)
	}
	input := []byte(b.String())
	c := compressors.NewConfig().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery(input, "feature 19 team-19")
	if !ok {
		t.Fatal("expected INI compression")
	}
	for _, want := range [][]byte{[]byte("[feature.19]"), []byte("owner = team-19"), []byte("# …")} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed INI missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") || strings.Contains(line, "=") {
			continue
		}
		t.Fatalf("compressed INI contains malformed line %q", line)
	}
}

func TestConfigPreservesCRLFWithoutAddingTrailingNewline(t *testing.T) {
	input := bytes.ReplaceAll(bytes.TrimSuffix(yamlFixture(), []byte("\n")), []byte("\n"), []byte("\r\n"))
	out, ok := compressors.NewConfig().Compress(input)
	if !ok {
		t.Fatal("expected YAML compression")
	}
	if bytes.HasSuffix(out, []byte{'\r'}) || bytes.HasSuffix(out, []byte{'\n'}) {
		t.Fatalf("unexpected trailing newline bytes: %q", out[len(out)-4:])
	}
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("compressed YAML mixed newline styles")
	}
}

func TestConfigInvalidTinyOrBinaryPassesThrough(t *testing.T) {
	c := compressors.NewConfig()
	var malformed strings.Builder
	malformed.WriteString("service:\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&malformed, "  feature_%02d: true\n", i)
	}
	malformed.WriteString("  broken: [one, two\n")

	var blockScalar strings.Builder
	blockScalar.WriteString("service:\n  script: |\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&blockScalar, "    echo step-%02d\n", i)
	}

	var multilineTOML strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&multilineTOML, "[feature.%02d]\nenabled = true\n", i)
	}
	multilineTOML.WriteString("description = \"\"\"multi\nline\nvalue\"\"\"\n")

	for _, input := range [][]byte{
		[]byte("service:\n  port: 8080\n"),
		[]byte("this is prose\nwithout config syntax\nrepeated over lines\n"),
		[]byte(malformed.String()),
		[]byte(blockScalar.String()),
		[]byte(multilineTOML.String()),
		{0xff, 0xfe, 0xfd},
	} {
		if out, ok := c.Compress(input); ok {
			t.Fatalf("unsafe input must pass through, got %q", out)
		}
	}
}
