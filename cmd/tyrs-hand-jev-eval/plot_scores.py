# /// script
# requires-python = ">=3.11"
# dependencies = ["matplotlib==3.10.6"]
# ///
"""将 Go 评测器导出的分数分布绘为独立图片，不读取密钥或发 API 请求。"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import TypedDict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


class Point(TypedDict):
    positive: bool
    score: float


class Distribution(TypedDict):
    prompt: str
    axis: str
    round: int
    points: list[Point]


def render(source: Path, destination: Path) -> None:
    distributions: list[Distribution] = json.loads(source.read_text())
    prompts = list(dict.fromkeys(item["prompt"] for item in distributions))
    fig, axes = plt.subplots(len(prompts), 2, figsize=(13, 3.1 * len(prompts)), squeeze=False)
    for row, prompt in enumerate(prompts):
        for column, axis in enumerate(("notify", "notification_gate")):
            points = [
                point
                for item in distributions
                if item["prompt"] == prompt and item["axis"] == axis
                for point in item["points"]
            ]
            ax = axes[row, column]
            for positive, color, label in ((False, "#dd8139", "Negative"), (True, "#2478ac", "Positive")):
                values = [point["score"] for point in points if point["positive"] == positive]
                ax.hist(values, bins=[i / 20 for i in range(21)], alpha=0.65, color=color,
                        label=f"{label} (n={len(values)})", edgecolor="white", linewidth=0.6)
            ax.axvline(0.6, linestyle="--", color="#333333", linewidth=1.2, label="0.60 reference")
            title = "Raw notify score" if axis == "notify" else "Derived notification gate score"
            ax.set_title(f"{prompt} / {title}", fontsize=11, loc="left", pad=12)
            ax.set_xlim(0, 1)
            ax.set_xlabel("Score")
            ax.set_ylabel("Messages")
            ax.spines[["top", "right"]].set_visible(False)
            ax.grid(axis="y", alpha=0.15)
            ax.legend(fontsize=8, frameon=False)
    fig.suptitle("Jev: positive / negative score distributions", fontsize=18, y=1.01)
    fig.text(0.02, -0.01,
             "Gate = min(related, max(notify, completed, needs_user_input)); this is a diagnostic score, not a calibrated probability.\n"
             "Successful single-message evaluations only. All holdout rounds are pooled when present. Closure uses separate thresholds.",
             fontsize=9, color="#444444")
    fig.tight_layout(pad=2)
    fig.savefig(destination, dpi=180, bbox_inches="tight", facecolor="white")
    plt.close(fig)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    output: Path = args.output or args.source.with_suffix(".png")
    render(args.source, output)
    print(output)


if __name__ == "__main__":
    main()
