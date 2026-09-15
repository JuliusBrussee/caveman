#!/usr/bin/env python3
"""Render the README savings charts as static SVGs (light + dark).

Reads the committed benchmark table between BENCHMARK-TABLE markers in
README.md (written by benchmarks/run.py) so the charts can never drift
from the published numbers. Wrap-benchmark numbers are transcribed from
docs/WRAP-BENCHMARK.md (generated 2026-08-06, benchmark_counterfactual).

Usage: python3 benchmarks/render_charts.py
Writes: docs/assets/chart-skill-output{,-dark}.svg
        docs/assets/chart-wrap-input{,-dark}.svg
"""

from html import escape
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "docs" / "assets"

FONT = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"

# Palette validated with the dataviz six-check validator against GitHub's
# light (#ffffff) and dark (#0d1117) surfaces. ALL CHECKS PASS both modes.
MODES = {
    "": {  # light
        "text": "#1f2328", "sub": "#59636e", "axis": "#d1d9e0",
        "normal": "#0969da", "caveman": "#e8590c",
    },
    "-dark": {
        "text": "#e6edf3", "sub": "#9198a1", "axis": "#3d444d",
        "normal": "#3987e5", "caveman": "#d95926",
    },
}

# docs/WRAP-BENCHMARK.md per-case table (direct, caveman), 3 runs per arm.
WRAP_CASES = [
    ("CSV outlier hunt", 165823, 74484, "-55.1%"),
    ("Log needle-in-haystack", 148807, 74068, "-50.2%"),
    ("YAML config drift", 132124, 71027, "-46.2%"),
    ("Test-output failure", 150377, 108514, "-27.8%"),
    ("Deployment JSON drift", 147975, 108939, "-26.4%"),
    ("Dashboard HTML alert", 140687, 154641, "+9.9%"),
]


def read_skill_rows(md=None):
    if md is None:
        md = (ROOT / "README.md").read_text(encoding="utf-8")
    block = md.split("<!-- BENCHMARK-TABLE-START -->")[1].split("<!-- BENCHMARK-TABLE-END -->")[0]
    rows, avg = [], None
    columns = None
    for line in block.splitlines():
        if not line.startswith("|"):
            continue
        cells = [cell.strip().replace("**", "") for cell in line.strip("|").split("|")]
        if cells[0] == "Task":
            # The current harness has three arms. Plot the skill's contribution
            # against its terse control; do not relabel baseline as that control.
            columns = {cell: index for index, cell in enumerate(cells)}
            continue
        if columns is None or not {"Terse (tokens)", "Caveman (tokens)"} <= columns.keys():
            continue
        try:
            terse = int(cells[columns["Terse (tokens)"]])
            cave = int(cells[columns["Caveman (tokens)"]])
        except (IndexError, ValueError):
            continue
        if terse < 0 or cave < 0:
            continue
        delta = f"{(cave / terse - 1) * 100:+.0f}%" if terse else "n/a"
        row = (cells[0], terse, cave, delta)
        if row[0] == "Average":
            avg = row
        else:
            rows.append(row)
    rows.sort(key=lambda r: -r[1])
    return rows, avg


def bar(x, y, w, h, fill, r=4):
    """Bar anchored square at the baseline, rounded at the data end."""
    if w <= 0:
        return ""
    w = max(w, r)
    return (f'<path d="M{x},{y} h{w - r:.1f} a{r},{r} 0 0 1 {r},{r} '
            f'v{h - 2 * r} a{r},{r} 0 0 1 -{r},{r} h-{w - r:.1f} z" fill="{fill}"/>')


def legend(c, x, y, labels):
    out, cx = [], x
    for color, label in labels:
        out.append(f'<rect x="{cx}" y="{y - 9}" width="10" height="10" rx="2" fill="{color}"/>')
        out.append(f'<text x="{cx + 16}" y="{y}" font-size="12" fill="{c["sub"]}">{label}</text>')
        cx += 16 + 7 * len(label) + 26
    return "\n".join(out)


def chart_skill(c):
    rows, avg = read_skill_rows()
    if not rows or avg is None:
        return (f'<svg xmlns="http://www.w3.org/2000/svg" width="880" height="92" viewBox="0 0 880 92" '
                f'font-family="{FONT}" role="img" aria-label="No reviewed API output benchmark result published">'
                f'<text x="0" y="24" font-size="15" fill="{c["text"]}">Output reduction: no reviewed API benchmark result published.</text>'
                f'<text x="0" y="53" font-size="13" fill="{c["sub"]}">Current-session savings are unknown without a measured comparison.</text>'
                '</svg>')
    groups = rows + [avg]
    W, LEFT, PLOT = 880, 248, 430
    stride, bh, gap = 40, 10, 2
    top = 56
    H = top + stride * len(groups) + 46
    xmax = max(1, *(max(r[1], r[2]) for r in groups))
    sc = PLOT / xmax
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
         f'font-family="{FONT}" role="img" aria-label="Output tokens per task, terse control versus caveman">']
    p.append(f'<text x="0" y="16" font-size="13" font-weight="600" fill="{c["text"]}">'
             'Output tokens per task — same prompt, same model, real API runs</text>')
    p.append(legend(c, 0, 38, [(c["normal"], "Terse control"), (c["caveman"], "Caveman")]))
    y = top
    for i, (task, normal, cave, saved) in enumerate(groups):
        last = i == len(groups) - 1
        if last:
            p.append(f'<line x1="0" y1="{y - 7}" x2="{W}" y2="{y - 7}" stroke="{c["axis"]}" stroke-width="1"/>')
        weight = ' font-weight="700"' if last else ""
        p.append(f'<text x="{LEFT - 12}" y="{y + bh + gap / 2 + 4}" font-size="12" text-anchor="end" '
                 f'fill="{c["text"]}"{weight}>{escape(task)}</text>')
        p.append(bar(LEFT, y, normal * sc, bh, c["normal"]))
        p.append(f'<text x="{LEFT + normal * sc + 6}" y="{y + bh - 1}" font-size="11" fill="{c["sub"]}">{normal:,}</text>')
        y2 = y + bh + gap
        p.append(bar(LEFT, y2, cave * sc, bh, c["caveman"]))
        p.append(f'<text x="{LEFT + cave * sc + 6}" y="{y2 + bh - 1}" font-size="11" fill="{c["sub"]}">'
                 f'{cave:,} <tspan font-weight="700" fill="{c["caveman"]}">{saved}</tspan></text>')
        y += stride
    p.append(f'<line x1="{LEFT}" y1="{top - 6}" x2="{LEFT}" y2="{y - 12}" stroke="{c["axis"]}" stroke-width="1"/>')
    p.append(f'<text x="0" y="{H - 10}" font-size="11" fill="{c["sub"]}">'
             'Source: benchmarks/run.py through the real Claude API · output tokens only — see honest-numbers note below</text>')
    p.append("</svg>")
    return "\n".join(p)


def chart_wrap(c):
    groups = WRAP_CASES
    W, LEFT, PLOT = 880, 248, 430
    stride, bh, gap = 40, 10, 2
    top = 76
    H = top + stride * len(groups) + 46
    xmax = max(max(g[1], g[2]) for g in groups)
    sc = PLOT / xmax
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
         f'font-family="{FONT}" role="img" aria-label="Provider-reported input tokens per benchmark case, direct Claude Code versus caveman wrap">']
    p.append(f'<text x="0" y="16" font-size="13" font-weight="600" fill="{c["text"]}">'
             'Provider-reported input tokens per case — pinned Claude Code benchmark, 3 runs each</text>')
    p.append(f'<text x="0" y="34" font-size="12" fill="{c["sub"]}">'
             'Total: 885,793 → 591,673 (−33.2%) · all 18/18 exact-answer checks passed</text>')
    p.append(legend(c, 0, 56, [(c["normal"], "Direct Claude Code"), (c["caveman"], "Caveman wrap")]))
    y = top
    for task, direct, cave, delta in groups:
        p.append(f'<text x="{LEFT - 12}" y="{y + bh + gap / 2 + 4}" font-size="12" text-anchor="end" '
                 f'fill="{c["text"]}">{task}</text>')
        p.append(bar(LEFT, y, direct * sc, bh, c["normal"]))
        p.append(f'<text x="{LEFT + direct * sc + 6}" y="{y + bh - 1}" font-size="11" fill="{c["sub"]}">{direct:,}</text>')
        y2 = y + bh + gap
        p.append(bar(LEFT, y2, cave * sc, bh, c["caveman"]))
        p.append(f'<text x="{LEFT + cave * sc + 6}" y="{y2 + bh - 1}" font-size="11" fill="{c["sub"]}">'
                 f'{cave:,} <tspan font-weight="700" fill="{c["caveman"]}">{delta}</tspan></text>')
        y += stride
    p.append(f'<line x1="{LEFT}" y1="{top - 6}" x2="{LEFT}" y2="{y - 12}" stroke="{c["axis"]}" stroke-width="1"/>')
    p.append(f'<text x="0" y="{H - 10}" font-size="11" fill="{c["sub"]}">'
             'HTML got no transform, caveman still paid its own overhead — loss shown, not hidden · docs/WRAP-BENCHMARK.md</text>')
    p.append("</svg>")
    return "\n".join(p)


def main():
    for suffix, c in MODES.items():
        (ASSETS / f"chart-skill-output{suffix}.svg").write_text(chart_skill(c), encoding="utf-8")
        (ASSETS / f"chart-wrap-input{suffix}.svg").write_text(chart_wrap(c), encoding="utf-8")
    print("wrote 4 SVGs to docs/assets/")


if __name__ == "__main__":
    main()
