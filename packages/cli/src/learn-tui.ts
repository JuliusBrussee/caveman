import * as p from "@clack/prompts";

export type LearnTuiMove = {
  title: string;
  kind: string;
  detail: string;
  action?: string;
};

export type LearnTuiModel = {
  score: number | null;
  scope: string;
  sessions: string;
  diff?: string;
  status?: string;
  moves: LearnTuiMove[];
  protected?: string;
  confirmed?: number;
  findings: number;
  report: string;
};

export type LearnTuiAction = "implement" | "details" | "report" | "done";
export type LearnTuiResult = { action: LearnTuiAction; focus?: string };
export type LearnAgentOption = { value: string; label: string; hint?: string };

const color = !process.env.NO_COLOR && !!process.stdout.isTTY;
const ansi = (code: string, value: string) => color ? `\x1b[${code}m${value}\x1b[0m` : value;
const bold = (value: string) => ansi("1", value);
const dim = (value: string) => ansi("2", value);
const green = (value: string) => ansi("32", value);
const amber = (value: string) => ansi("33", value);

export function learnScoreBar(score: number, width = 24): string {
  const safe = Math.max(0, Math.min(100, Math.round(score)));
  const filled = Math.round((safe / 100) * width);
  return `${"━".repeat(filled)}${"─".repeat(width - filled)}`;
}

export function learnMoveBody(move: LearnTuiMove): string {
  return [
    bold(move.title),
    green(move.detail),
    ...(move.action ? [dim(move.action)] : []),
  ].join("\n");
}

export function learnScoreBody(model: LearnTuiModel): string {
  const confirmed = model.confirmed && model.confirmed > 0
    ? dim(`${model.confirmed} fixes measured since you applied them — run caveman learn --all`)
    : undefined;
  if (model.score === null) {
    return [model.sessions, ...(model.status ? [model.status] : []), ...(confirmed ? [confirmed] : [])].join("\n");
  }
  const score = Math.max(0, Math.min(100, Math.round(model.score)));
  return [
    `${green(learnScoreBar(score))}  ${bold(`${score}/100`)}`,
    ...(confirmed ? [confirmed] : []),
    dim(model.scope),
    model.sessions,
    ...(model.diff ? [amber(model.diff)] : []),
  ].join("\n");
}

export type LearnProgress = {
  start: (message: string) => void;
  update: (message: string) => void;
  stop: (message: string) => void;
  fail: (message: string) => void;
};

export function createLearnProgress(): LearnProgress {
  const progress = p.spinner();
  return {
    start(message) {
      progress.start(message);
    },
    update(message) {
      progress.message(message);
    },
    stop(message) {
      progress.stop(message);
    },
    fail(message) {
      progress.error(message);
    },
  };
}

export async function selectLearnAgent(options: LearnAgentOption[]): Promise<string | null> {
  const selected = await p.select<string>({
    message: "Implement findings with",
    options,
  });
  if (p.isCancel(selected)) {
    p.cancel("Implementation cancelled");
    return null;
  }
  return selected;
}

export async function renderLearnTui(model: LearnTuiModel): Promise<LearnTuiResult> {
  p.intro(`${green(bold(" CAVEMAN "))} ${dim("LEARN / LOCAL SETUP")}`);

  if (model.score !== null) {
    p.note(learnScoreBody(model), "SETUP SCORE");
  } else {
    p.note(learnScoreBody(model), "LEARN RESULT");
  }

  if (model.moves.length > 0) {
    const moves = model.moves.map((move, index) => {
      const number = dim(String(index + 1).padStart(2, "0"));
      return `${number}  ${learnMoveBody(move)}`;
    });
    p.note(moves.join("\n\n"), "TOP MOVES");
  }

  if (model.protected) {
    p.log.warn(`${bold("Protected")}  ${model.protected}`);
  }

  const options: Array<{ value: LearnTuiAction; label: string; hint?: string }> = [];
  if (model.moves.length > 0) {
    options.push({
      value: "implement",
      label: "Implement with agent",
      hint: "Claude Code or Codex · approval before edits",
    });
  }
  if (model.findings > 0) {
    options.push({
      value: "details",
      label: `Show all ${model.findings} findings`,
      hint: "full ids, evidence, and suggestions",
    });
  }
  options.push(
    {
      value: "report",
      label: "Open visual report",
      hint: model.report,
    },
    {
      value: "done",
      label: "Done",
    },
  );

  const selected = await p.select<LearnTuiAction>({
    message: "Choose next step",
    initialValue: model.moves.length > 0 ? "implement" : "report",
    options,
  });

  if (p.isCancel(selected)) {
    p.cancel("Done");
    return { action: "done" };
  }

  if (selected === "implement") {
    const focus = await p.text({
      message: "Anything agent should focus on?",
      placeholder: "Press Enter to work through all top moves",
      defaultValue: "",
    });
    if (p.isCancel(focus)) {
      p.cancel("Implementation cancelled");
      return { action: "done" };
    }
    p.outro("Opening implementation agent");
    return focus.trim() ? { action: "implement", focus: focus.trim() } : { action: "implement" };
  } else if (selected === "details") p.outro("Showing complete findings");
  else if (selected === "report") p.outro("Opening local report");
  else p.outro(`Saved report · ${model.report}`);
  return { action: selected };
}
