"""Nearest-neighbour distances of seats: ONE helper for the exporter and the visual guard.

A uniform grid hash: the cell is a guess of the step, a point looks at the cells around it and widens
the ring while its best distance is still larger than the ring reaches. About O(n) for a hall, whatever
the shape (a single column included).
"""

from __future__ import annotations

import math
import statistics
from typing import Optional


def nearest_distances(points: list[tuple[float, float]], ignore_coincident: bool = True) -> list[Optional[float]]:
    """For every point the distance to its nearest other point (None when it has none).

    With `ignore_coincident` a point at the very same place does not count as a neighbour.
    """
    n = len(points)
    if n < 2:
        return [None] * n
    xs = [p[0] for p in points]
    ys = [p[1] for p in points]
    width, height = max(xs) - min(xs), max(ys) - min(ys)
    cell = max(math.sqrt(width * height / n), max(width, height) / n)
    if not cell > 0:
        cell = 1.0
    x0, y0 = min(xs), min(ys)
    cells: dict[tuple[int, int], list[int]] = {}
    keys: list[tuple[int, int]] = []
    for i, (x, y) in enumerate(points):
        key = (int((x - x0) // cell), int((y - y0) // cell))
        keys.append(key)
        cells.setdefault(key, []).append(i)
    span = max(int(width // cell), int(height // cell)) + 1

    out: list[Optional[float]] = []
    for i, (x, y) in enumerate(points):
        cx, cy = keys[i]
        best = math.inf

        def look(indices: list[int]) -> None:
            nonlocal best
            for j in indices:
                if j == i:
                    continue
                d = math.hypot(points[j][0] - x, points[j][1] - y)
                if d == 0 and ignore_coincident:
                    continue
                if d < best:
                    best = d

        k = 0
        while k <= span:
            if k == 0:
                look(cells.get((cx, cy), []))
            elif 8 * k > len(cells):
                # The ring is bigger than the occupied cells: look at all of them once and stop.
                for indices in cells.values():
                    look(indices)
                break
            else:
                for dx in range(-k, k + 1):
                    look(cells.get((cx + dx, cy - k), []))
                    look(cells.get((cx + dx, cy + k), []))
                for dy in range(-k + 1, k):
                    look(cells.get((cx - k, cy + dy), []))
                    look(cells.get((cx + k, cy + dy), []))
            if best <= k * cell:
                break
            k += 1
        out.append(None if math.isinf(best) else best)
    return out


def robust_step(points: list[tuple[float, float]]) -> float:
    """The grid step of a hall: the median of every seat's nearest-neighbour distance (1.0 when unknown).

    The median ignores a few close pairs (a jittered seat, an unmerged duplicate).
    """
    found = [d for d in nearest_distances(points) if d is not None]
    return float(statistics.median(found)) if found else 1.0
