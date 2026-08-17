// Package cacheengine plans and applies provider-native prompt caching through
// one capability-driven API. Core planner is provider/model agnostic; native
// bridge ships standalone compilers whose parity tests track existing Caveman
// adapters. Unknown profiles, malformed bodies, prefix drift, volatile stable
// slots, and caller-managed cache controls pass through unchanged. Applied
// standalone results remain inferred and verified dollars remain zero;
// pass-through and observe-only results carry no claim basis.
package cacheengine
