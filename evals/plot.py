"""
Generate a boxplot showing the distribution of token compression per
skill, compared against a plain "Answer concisely." control.

Reads evals/snapshots/results.json and writes:
  - evals/snapshots/results.html  (interactive plotly)
  - evals/snapshots/results.png   (static export for README/PR embed)

Run: uv run --with tiktoken --with plotly --with kaleido python evals/plot.py
"""

from __future__ import annotations

import json
import statistics
from pathlib import Path

import plotly.graph_objects as go
import tiktoken

ENCODING = tiktoken.get_encoding("o200k_base")
SNAPSHOT = Path(__file__).parent / "snapshots" / "results.json"
HTML_OUT = Path(__file__).parent / "snapshots" / "results.html"
PNG_OUT = Path(__file__).parent / "snapshots" / "results.png"


def count(text: str) -> int:
    return len(ENCODING.encode(text))


def main() -> None:
    data = json.loads(SNAPSHOT.read_text())
    arms = data["arms"]
    meta = data.get("metadata", {})

    terse_tokens = [count(o) for o in arms["__terse__"]]

    rows = []
    for skill, outputs in arms.items():
        if skill in ("__baseline__", "__terse__"):
            continue
        skill_tokens = [count(o) for o in outputs]
        savings = [
            (1 - (s / t)) * 100 if t else 0.0
            for s, t in zip(skill_tokens, terse_tokens)
        ]
        rows.append(
            {"skill": skill, "savings": savings, "median": statistics.median(savings)}
        )

    rows.sort(key=lambda r: -r["median"])  # best first

    fig = go.Figure()

    for row in rows:
        fig.add_trace(
            go.Box(
                y=row["savings"],
                name=row["skill"],
                boxpoints="all",
                jitter=0.4,
                pointpos=0,
                marker=dict(color="#2ca02c", size=7, opacity=0.7),
                line=dict(color="#2c3e50", width=2),
                fillcolor="rgba(76, 120, 168, 0.25)",
                boxmean=True,
                hovertemplate="<b>%{x}</b><br>%{y:.1f}%<extra></extra>",
            )
        )

    # zero line — "no effect"
    fig.add_hline(
        y=0,
        line=dict(color="black", width=1.5, dash="dash"),
        annotation_text="no effect (= same length as control)",
        annotation_position="top right",
        annotation_font=dict(size=11, color="black"),
    )

    # median labels above each box
    for row in rows:
        fig.add_annotation(
            x=row["skill"],
            y=max(row["savings"]),
            text=f"<b>{row['median']:+.0f}%</b>",
            showarrow=False,
            yshift=22,
            font=dict(size=16, color="#2c3e50"),
        )

    fig.update_layout(
        title=dict(
            text=f"<b>How much shorter does each skill make Claude's answers?</b><br>"
            f"<sub>Distribution of per-prompt savings vs system prompt = "
            f"<i>'Answer concisely.'</i><br>"
            f"{meta.get('model', '?')} · n={meta.get('n_prompts', '?')} prompts · "
            f"single run per arm</sub>",
            x=0.5,
            xanchor="center",
        ),
        xaxis=dict(title="", automargin=True),
        yaxis=dict(
            title="↑ shorter  ·  vs control  ·  longer ↓",
            ticksuffix="%",
            zeroline=False,
            gridcolor="rgba(0,0,0,0.08)",
            range=[-30, 115],
        ),
        plot_bgcolor="white",
        height=560,
        width=980,
        margin=dict(l=140, r=80, t=120, b=120),
        showlegend=False,
        annotations=[
            dict(
                x=0.5,
                y=-0.22,
                xref="paper",
                yref="paper",
                showarrow=False,
                font=dict(size=11, color="#555"),
                text=(
                    "<b>box</b> = IQR (middle 50%) · "
                    "<b>line in box</b> = median · "
                    "<b>dashed line</b> = mean · "
                    "<b>green dots</b> = individual prompts"
                ),
            )
        ],
    )

    # re-add labels after update_layout (which would otherwise wipe them)
    for row in rows:
        fig.add_annotation(
            x=row["skill"],
            y=max(row["savings"]),
            text=f"<b>{row['median']:+.0f}%</b>",
            showarrow=False,
            yshift=22,
            font=dict(size=16, color="#2c3e50"),
        )

    fig.write_html(HTML_OUT)
    print(f"Wrote {HTML_OUT}")
    fig.write_image(PNG_OUT, scale=2)
    print(f"Wrote {PNG_OUT}")


if __name__ == "__main__":
    main()
