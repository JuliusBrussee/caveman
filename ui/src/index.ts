export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

export const surface = "border border-black/10 bg-white/80 shadow-sm dark:border-white/10 dark:bg-white/5";
