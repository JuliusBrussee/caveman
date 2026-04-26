import type { TuiPlugin } from "@opencode-ai/plugin/tui"
import { type CavemanMode, readState, writeState } from "./state.ts"

const MODE_OPTIONS: Array<{ title: string; value: CavemanMode; description: string }> = [
  { title: "full", value: "full", description: "Classic caveman. Drop articles, fragments OK." },
  { title: "lite", value: "lite", description: "No filler. Full sentences. Professional." },
  { title: "ultra", value: "ultra", description: "Max compression. Telegraphic OK." },
  { title: "wenyan-lite", value: "wenyan-lite", description: "Semi-classical. Grammar intact, filler gone." },
  { title: "wenyan", value: "wenyan-full", description: "Full classical terseness." },
  { title: "wenyan-ultra", value: "wenyan-ultra", description: "Extreme classical compression." },
]

export const tui: TuiPlugin = async (api) => {
  api.command.register(() => [
    {
      title: "Enable caveman mode",
      value: "caveman.enable",
      category: "Caveman",
      slash: { name: "enable_caveman", aliases: ["caveman_on"] },
      onSelect: () => {
        const state = readState()
        writeState({ enabled: true, mode: state.mode })
        api.ui.toast({ variant: "success", message: `Caveman enabled (${state.mode})` })
      },
    },
    {
      title: "Disable caveman mode",
      value: "caveman.disable",
      category: "Caveman",
      slash: { name: "disable_caveman", aliases: ["caveman_off"] },
      onSelect: () => {
        const state = readState()
        writeState({ enabled: false, mode: state.mode })
        api.ui.toast({ variant: "info", message: "Caveman disabled" })
      },
    },
    {
      title: "Set caveman mode",
      value: "caveman.mode",
      category: "Caveman",
      slash: { name: "caveman", aliases: ["caveman_level"] },
      onSelect: () => {
        api.ui.dialog.replace(() =>
          api.ui.DialogSelect<CavemanMode>({
            title: "Caveman mode",
            options: MODE_OPTIONS,
            onSelect: (option) => {
              api.ui.dialog.clear()
              writeState({ enabled: true, mode: option.value })
              api.ui.toast({ variant: "success", message: `Caveman mode: ${option.title}` })
            },
          }),
        )
      },
    },
  ])
}

export default { id: "caveman-tui", tui }
