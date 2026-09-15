// Inventory rollups: group items by tag and compute simple summaries.

export interface Item {
  id: number;
  name: string;
  score: number;
  tags: string[];
}

export interface Rollup {
  byTag: Record<string, number>;
  total: number;
  top: string | null;
}

/** Group items by tag, total them, and find the highest scorer. */
export function summarize(items: Item[]): Rollup {
  if (items.length === 0) {
    throw new Error("inventory: no items");
  }
  const byTag: Record<string, number> = {};
  let total = 0;
  let best = -1;
  let top: string | null = null;
  for (const it of items) {
    total += 1;
    for (const tag of it.tags) {
      const key = tag.toLowerCase();
      byTag[key] = (byTag[key] ?? 0) + 1;
    }
    if (it.score > best) {
      best = it.score;
      top = it.name;
    }
  }
  return { byTag, total, top };
}

/** Return items carrying the tag, most relevant first. */
export function filterByTag(items: Item[], tag: string): Item[] {
  const needle = tag.toLowerCase();
  const matched = items.filter((it) =>
    it.tags.some((t) => t.toLowerCase() === needle),
  );
  matched.sort((a, b) => b.score - a.score);
  return matched;
}

/** Render a rollup as a stable, human-readable string. */
export function formatRollup(rollup: Rollup): string {
  const lines = [`total=${rollup.total} top=${rollup.top}`];
  for (const key of Object.keys(rollup.byTag).sort()) {
    lines.push(`  ${key}: ${rollup.byTag[key]}`);
  }
  return lines.join("\n");
}
