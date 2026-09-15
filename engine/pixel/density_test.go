// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"os"
	"testing"
)

// TestResolveDensity pins the level × model resolution table, including the
// fail-closed directions: unknown model → conservative + std tier regardless of
// requested level; GPT-5.6 → balanced geometry but never the hi-res Anthropic
// canvas.
func TestResolveDensity(t *testing.T) {
	std := StandardPixelTier
	hi := HiResPixelTier
	cons := func(tier PixelTier) renderParams {
		return renderParams{Tier: tier, CellAdv: 5, PitchY: 8, Zebra: false, Layers: 1}
	}
	bal := func(tier PixelTier) renderParams {
		return renderParams{Tier: tier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 1}
	}
	mx := func(tier PixelTier) renderParams {
		return renderParams{Tier: tier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 2}
	}

	cases := []struct {
		model string
		level DensityLevel
		want  renderParams
	}{
		// Hi-res-tier families get the hi-res canvas at every level.
		{"claude-fable-5", DensityConservative, cons(hi)},
		{"claude-fable-5", DensityBalanced, bal(hi)},
		{"claude-fable-5", DensityMax, mx(hi)},
		{"claude-mythos-5", DensityBalanced, bal(hi)},
		{"claude-sonnet-5", DensityBalanced, bal(hi)},
		{"claude-opus-4-8", DensityBalanced, bal(hi)},
		{"claude-opus-4-8[1m]", DensityBalanced, bal(hi)},     // variant tag stripped
		{"anthropic/claude-opus-4-8", DensityMax, mx(hi)},     // provider prefix
		{"claude-opus-5", DensityConservative, cons(hi)},      // 5.x > 4.8
		{"claude-fable-5-20260101", DensityBalanced, bal(hi)}, // dated family member

		// Opus below 4.8 is NOT hi-res → unknown → fail closed to conservative+std.
		{"claude-opus-4-7", DensityBalanced, cons(std)},
		{"claude-opus-4-5", DensityMax, cons(std)},

		// GPT-5.6: balanced geometry, but std canvas only — never hi-res.
		{"gpt-5.6", DensityConservative, cons(std)},
		{"gpt-5.6", DensityBalanced, bal(std)},
		{"gpt-5.6", DensityMax, mx(std)},
		{"gpt-5.6-mini", DensityBalanced, bal(std)},

		// Unknown / unrecognised models fail closed: conservative + std tier,
		// regardless of the requested level.
		{"gpt-5.5", DensityBalanced, cons(std)},
		{"claude-3-5-sonnet", DensityMax, cons(std)},
		{"", DensityBalanced, cons(std)},
		{"totally-made-up", DensityMax, cons(std)},

		// Unknown level string fails closed to conservative geometry.
		{"claude-fable-5", DensityLevel("ultra"), cons(hi)},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+string(tc.level), func(t *testing.T) {
			got := ResolveDensity(tc.model, tc.level)
			if got != tc.want {
				t.Fatalf("ResolveDensity(%q, %q) = %+v, want %+v", tc.model, tc.level, got, tc.want)
			}
		})
	}
}

// TestDensityFromEnv pins the env selector: default balanced, falsey values
// conservative, invalid values fail closed to conservative, case-insensitive.
func TestDensityFromEnv(t *testing.T) {
	cases := []struct {
		set  bool
		raw  string
		want DensityLevel
	}{
		{false, "", DensityBalanced},   // unset → balanced
		{true, "", DensityBalanced},    // empty → balanced
		{true, "   ", DensityBalanced}, // whitespace → balanced
		{true, "balanced", DensityBalanced},
		{true, "conservative", DensityConservative},
		{true, "max", DensityMax},
		{true, "MAX", DensityMax}, // case-insensitive
		{true, "0", DensityConservative},
		{true, "false", DensityConservative},
		{true, "off", DensityConservative},
		{true, "none", DensityConservative},
		{true, "no", DensityConservative},
		{true, "ultra", DensityConservative},   // invalid → fail closed
		{true, "garbage", DensityConservative}, // invalid → fail closed
	}
	for _, tc := range cases {
		name := "unset"
		if tc.set {
			name = "set=" + tc.raw
		}
		t.Run(name, func(t *testing.T) {
			setPixelDensityEnv(t, tc.set, tc.raw)
			if got := DensityFromEnv(); got != tc.want {
				t.Fatalf("DensityFromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDensityHardFloors verifies the stall-envelope floors clamp any sub-floor
// config: mono pitch floors at 6, zebra pitch floors at 5, layers cap at 2, and
// layers floor at 1.
func TestDensityHardFloors(t *testing.T) {
	cases := []struct {
		in   renderParams
		want renderParams
	}{
		// Mono below its floor is raised to 6.
		{renderParams{Tier: StandardPixelTier, CellAdv: 5, PitchY: 4, Zebra: false, Layers: 1},
			renderParams{Tier: StandardPixelTier, CellAdv: 5, PitchY: 6, Zebra: false, Layers: 1}},
		// Zebra below its floor is raised to 5 (not 6 — colour buys the extra rung).
		{renderParams{Tier: StandardPixelTier, CellAdv: 4, PitchY: 3, Zebra: true, Layers: 1},
			renderParams{Tier: StandardPixelTier, CellAdv: 4, PitchY: 5, Zebra: true, Layers: 1}},
		// Layers never exceed 2.
		{renderParams{Tier: HiResPixelTier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 3},
			renderParams{Tier: HiResPixelTier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 2}},
		// Layers never fall below 1.
		{renderParams{Tier: StandardPixelTier, CellAdv: 5, PitchY: 8, Zebra: false, Layers: 0},
			renderParams{Tier: StandardPixelTier, CellAdv: 5, PitchY: 8, Zebra: false, Layers: 1}},
		// A valid config is unchanged.
		{renderParams{Tier: HiResPixelTier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 2},
			renderParams{Tier: HiResPixelTier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 2}},
	}
	for _, tc := range cases {
		if got := applyDensityFloors(tc.in); got != tc.want {
			t.Fatalf("applyDensityFloors(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// TestResolveDensityNeverBelowFloor is an invariant guard: no level × recognised
// model may resolve to a stall-envelope geometry.
func TestResolveDensityNeverBelowFloor(t *testing.T) {
	for _, model := range []string{"claude-fable-5", "gpt-5.6", "claude-opus-4-8", "unknown"} {
		for _, level := range []DensityLevel{DensityConservative, DensityBalanced, DensityMax} {
			rp := ResolveDensity(model, level)
			floor := 6
			if rp.Zebra {
				floor = 5
			}
			if rp.PitchY < floor {
				t.Fatalf("%s/%s pitch %d below floor %d", model, level, rp.PitchY, floor)
			}
			if rp.Layers < 1 || rp.Layers > 2 {
				t.Fatalf("%s/%s layers %d out of [1,2]", model, level, rp.Layers)
			}
		}
	}
}

// setPixelDensityEnv sets or unsets CAVE_PIXEL_DENSITY, restoring the prior
// process value on cleanup (there is no t.Unsetenv for the unset case).
func setPixelDensityEnv(t *testing.T, set bool, raw string) {
	t.Helper()
	const key = "CAVE_PIXEL_DENSITY"
	prev, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
	if set {
		os.Setenv(key, raw)
	} else {
		os.Unsetenv(key)
	}
}
