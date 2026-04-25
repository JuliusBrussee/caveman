import type { TuiPlugin } from "@opencode-ai/plugin/tui";
import path from "path";
import fs from "fs/promises";
import os from "os";

const STATE_FILE = path.join(
  process.env.XDG_STATE_HOME ?? path.join(os.homedir(), ".local", "state"),
  "opencode",
  "caveman.json",
);

type Level = "lite" | "full" | "ultra";
type State = { disabled: string[]; levels: Record<string, Level> };

const DEFAULT: State = { disabled: [], levels: {} };

function record(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return;
  return value as Record<string, unknown>;
}

function state(value: unknown): State {
  const data = record(value);
  if (!data) return DEFAULT;
  const disabled = Array.isArray(data.disabled)
    ? data.disabled.filter(
        (value): value is string => typeof value === "string",
      )
    : [];
  const levels = Object.fromEntries(
    Object.entries(record(data.levels) ?? {}).flatMap(([key, value]) =>
      value === "lite" || value === "full" || value === "ultra"
        ? [[key, value]]
        : [],
    ),
  ) as Record<string, Level>;
  return { disabled, levels };
}

async function readState(): Promise<State> {
  try {
    return state(JSON.parse(await fs.readFile(STATE_FILE, "utf8")));
  } catch {
    return DEFAULT;
  }
}

async function writeState(state: State) {
  await fs.mkdir(path.dirname(STATE_FILE), { recursive: true });
  await fs.writeFile(STATE_FILE, JSON.stringify(state), "utf8");
}

const tui: TuiPlugin = async (api) => {
  function sid(): string | undefined {
    const r = api.route.current;
    return r.name === "session" ? (r as any).params?.sessionID : undefined;
  }

  api.command.register(() => [
    {
      title: "Enable caveman mode",
      value: "caveman.enable",
      category: "Caveman",
      slash: { name: "enable_caveman" },
      onSelect: async () => {
        const id = sid();
        if (!id) return;
        const state = await readState();
        state.disabled = state.disabled.filter((x) => x !== id);
        await writeState(state);
        api.ui.toast({
          variant: "success",
          message: "Caveman enabled (full mode)",
        });
      },
    },
    {
      title: "Disable caveman mode",
      value: "caveman.disable",
      category: "Caveman",
      slash: { name: "disable_caveman" },
      onSelect: async () => {
        const id = sid();
        if (!id) return;
        const state = await readState();
        if (!state.disabled.includes(id)) state.disabled.push(id);
        await writeState(state);
        api.ui.toast({ variant: "info", message: "Caveman disabled" });
      },
    },
    {
      title: "Set caveman level",
      value: "caveman.level",
      category: "Caveman",
      slash: { name: "caveman_level" },
      onSelect: async () => {
        const id = sid();
        if (!id) return;
        api.ui.dialog.replace(() =>
          api.ui.DialogSelect<Level>({
            title: "Caveman level",
            options: [
              {
                title: "full",
                value: "full",
                description:
                  "Classic caveman. Drop articles, fragments OK. (default)",
              },
              {
                title: "lite",
                value: "lite",
                description: "No filler. Full sentences. Professional.",
              },
              {
                title: "ultra",
                value: "ultra",
                description: "Max compression. Abbreviations, arrows.",
              },
            ],
            onSelect: async (opt) => {
              api.ui.dialog.clear();
              const state = await readState();
              state.levels[id] = opt.value;
              await writeState(state);
              api.ui.toast({
                variant: "success",
                message: `Caveman level: ${opt.value}`,
              });
            },
          }),
        );
      },
    },
  ]);
};

export default { id: "caveman-tui", tui };
