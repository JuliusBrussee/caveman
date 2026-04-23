import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";

type CavemanMode =
	| "lite"
	| "full"
	| "ultra"
	| "wenyan-lite"
	| "wenyan-full"
	| "wenyan-ultra";

type SavedMode = CavemanMode | "off";

type CavemanStateEntry = {
	type: "custom";
	customType: "caveman-state";
	data?: {
		mode?: SavedMode;
	};
};

const validModes: CavemanMode[] = ["lite", "full", "ultra", "wenyan-lite", "wenyan-full", "wenyan-ultra"];

const cavemanPrompt = `IMPORTANT: Caveman mode active.

Speak terse like smart caveman. Keep full technical accuracy. Kill fluff.

Rules:
- Drop filler, pleasantries, hedging.
- Short synonyms good. Fragments OK.
- Technical terms exact.
- Code blocks unchanged.
- Error messages quoted exact.
- Pattern: [thing] [action] [reason]. [next step].

Modes:
- lite: concise, grammatical, professional.
- full: drop articles, fragments OK, classic caveman.
- ultra: max compression, abbreviations OK, arrows OK.
- wenyan-lite: semi-classical Chinese, compressed.
- wenyan-full: full classical terseness.
- wenyan-ultra: extreme classical compression.

Boundaries:
- For security warnings, destructive actions, irreversible steps, and anything safety-critical: prioritize clarity over compression.
- If user says stop caveman or normal mode, stop immediately.
- This mode changes tone only, not correctness.`;

function isStateEntry(entry: unknown): entry is CavemanStateEntry {
	return (
		typeof entry === "object" &&
		entry !== null &&
		"type" in entry &&
		(entry as { type?: unknown }).type === "custom" &&
		"customType" in entry &&
		(entry as { customType?: unknown }).customType === "caveman-state"
	);
}

function normalizeMode(value: string): SavedMode | undefined {
	const normalized = value.trim().toLowerCase();
	if (!normalized) return "full";
	if (["off", "normal", "stop", "disable", "disabled", "none"].includes(normalized)) return "off";
	if (["default", "on", "enable", "enabled"].includes(normalized)) return "full";
	if (normalized === "wenyan") return "wenyan-full";
	if ((validModes as string[]).includes(normalized)) return normalized as CavemanMode;
	return undefined;
}

function getSavedMode(ctx: ExtensionContext): SavedMode {
	const entries = ctx.sessionManager.getEntries();
	for (let i = entries.length - 1; i >= 0; i--) {
		const entry = entries[i];
		if (!isStateEntry(entry)) continue;
		const mode = normalizeMode(entry.data?.mode ?? "");
		if (mode) return mode;
	}
	return "full";
}

function setStatus(ctx: ExtensionContext, mode: SavedMode) {
	ctx.ui.setStatus("caveman", mode === "off" ? "" : `CAVEMAN:${mode.toUpperCase()}`);
}

function persistMode(pi: ExtensionAPI, mode: SavedMode) {
	pi.appendEntry("caveman-state", { mode });
}

function buildPrompt(mode: CavemanMode) {
	return `${cavemanPrompt}

Current caveman mode: ${mode}.
Apply this mode to all normal assistant responses until changed.`;
}

export default function cavemanPiExtension(pi: ExtensionAPI) {
	let mode: SavedMode = "full";

	pi.on("session_start", async (_event, ctx) => {
		mode = getSavedMode(ctx);
		setStatus(ctx, mode);
	});

	pi.registerCommand("caveman", {
		description: "Enable caveman mode or set level: lite, full, ultra, wenyan-lite, wenyan-full, wenyan-ultra, off",
		handler: async (args, ctx) => {
			const nextMode = normalizeMode(args);
			if (!nextMode) {
				ctx.ui.notify(
					"Usage: /caveman [lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra|off]",
					"warning",
				);
				return;
			}

			mode = nextMode;
			persistMode(pi, mode);
			setStatus(ctx, mode);
			ctx.ui.notify(mode === "off" ? "Caveman off. Normal mode." : `Caveman ${mode} on.`, "info");
		},
	});

	pi.registerCommand("normal", {
		description: "Disable caveman mode",
		handler: async (_args, ctx) => {
			mode = "off";
			persistMode(pi, mode);
			setStatus(ctx, mode);
			ctx.ui.notify("Caveman off. Normal mode.", "info");
		},
	});

	pi.on("input", async (event, ctx) => {
		if (event.source === "extension") return { action: "continue" } as const;

		const text = event.text.trim().toLowerCase();
		if (["stop caveman", "normal mode"].includes(text)) {
			mode = "off";
			persistMode(pi, mode);
			setStatus(ctx, mode);
			ctx.ui.notify("Caveman off. Normal mode.", "info");
			return { action: "handled" } as const;
		}

		if (["caveman mode", "talk like caveman", "less tokens please"].includes(text)) {
			mode = "full";
			persistMode(pi, mode);
			setStatus(ctx, mode);
			ctx.ui.notify("Caveman full on.", "info");
			return { action: "handled" } as const;
		}

		return { action: "continue" } as const;
	});

	pi.on("before_agent_start", async (event) => {
		if (mode === "off") return undefined;
		return {
			systemPrompt: `${event.systemPrompt}\n\n${buildPrompt(mode)}`,
		};
	});
}
