"""Inventory rollups: group items by tag and compute simple summaries."""

from dataclasses import dataclass, field
from typing import Dict, List, Optional


@dataclass
class Item:
    """A single tracked unit."""

    id: int
    name: str
    score: float
    tags: List[str] = field(default_factory=list)


@dataclass
class Rollup:
    by_tag: Dict[str, int]
    total: int
    top: Optional[str]


def summarize(items: List[Item]) -> Rollup:
    """Group items by tag, total them, and find the highest scorer."""
    if not items:
        raise ValueError("inventory: no items")
    by_tag: Dict[str, int] = {}
    total = 0
    best = -1.0
    top = None
    for it in items:
        total += 1
        for tag in it.tags:
            key = tag.lower()
            by_tag[key] = by_tag.get(key, 0) + 1
        if it.score > best:
            best = it.score
            top = it.name
    return Rollup(by_tag=by_tag, total=total, top=top)


def filter_by_tag(items: List[Item], tag: str) -> List[Item]:
    """Return items carrying the tag, most relevant first."""
    tag = tag.lower()
    matched = [it for it in items if any(t.lower() == tag for t in it.tags)]
    matched.sort(key=lambda it: it.score, reverse=True)
    return matched


def format_rollup(rollup: Rollup) -> str:
    """Render a rollup as a stable, human-readable string."""
    lines = [f"total={rollup.total} top={rollup.top}"]
    for key in sorted(rollup.by_tag):
        lines.append(f"  {key}: {rollup.by_tag[key]}")
    return "\n".join(lines)
