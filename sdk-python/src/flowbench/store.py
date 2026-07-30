"""Writes a live-execution run to the same on-disk format internal/store
produces: meta.json, folded.json, traces.json, series.json, samples.json,
metrics.json, plus a shared index.json. Ports internal/span/fold.go's
Folded/FoldNode and internal/collector's BuildSeries/BuildSamples exactly, so
a Python-driven run's artifact is structurally comparable to a Go-produced
one for the same flow (ADR 0012's two-producer model).
"""

import json
from collections import namedtuple
from datetime import timezone
from pathlib import Path

from .span import OUTCOME_FAILED, OUTCOME_OK, OUTCOME_SKIPPED, finalize

# Matches internal/store's defaultBodyCap / cmd/flowbench's integrationBodyCap.
DEFAULT_BODY_CAP = 2048

_MAX_SERIES_POINTS = 300
_BUCKET_LADDER = [0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800, 3600]
_KEPT_SAMPLES_CEILING = 10_000

# One flow-run's latency record. Python's live execution is single-VU and
# sequential (no coordinated-omission gap), so actual is always the intended
# dispatch time -- Latency() in Go's Sample reduces to service in every case
# reachable here.
Sample = namedtuple("Sample", ["flow", "actual", "service", "outcome", "throttled"])


def _ns(seconds):
  return round(seconds * 1_000_000_000)


class FoldNode:
  def __init__(self, name=""):
    self.name = name
    self.count = 0
    self.total = 0.0
    self.self_time = 0.0
    self.children = {}

  def child(self, name):
    c = self.children.get(name)
    if c is None:
      c = FoldNode(name)
      self.children[name] = c
    return c

  def to_dict(self):
    d = {
      "name": self.name,
      "count": self.count,
      "total": _ns(self.total),
      "self": _ns(self.self_time),
    }
    if self.children:
      d["children"] = {name: c.to_dict() for name, c in self.children.items()}
    return d


class Folded:
  def __init__(self):
    self.root = FoldNode("")

  def add(self, sp):
    _add_span(self.root, sp)

  def to_dict(self):
    return {"root": self.root.to_dict()}


def _add_span(fold_node, sp):
  c = fold_node.child(sp.name)
  c.count += 1
  c.total += sp.duration
  c.self_time += sp.self_time()
  for child in sp.children:
    _add_span(c, child)


def _bucket_width(total):
  for w in _BUCKET_LADDER:
    if total / w <= _MAX_SERIES_POINTS:
      return w
  return _BUCKET_LADDER[-1]


def percentile(samples, q):
  if not samples:
    return 0.0
  xs = sorted(s.service for s in samples)
  i = int(q * (len(xs) - 1))
  i = max(0, min(i, len(xs) - 1))
  return xs[i]


def build_series(samples, total_duration):
  """Buckets samples by dispatch time -- a port of collector.BuildSeries."""
  if not samples:
    return {"bucket": 0, "points": None}

  end = total_duration
  for s in samples:
    if s.actual > end:
      end = s.actual
  width = _bucket_width(end) if end > 0 else _BUCKET_LADDER[0]
  n = int(end / width) + 1

  buckets = [[] for _ in range(n)]
  for s in samples:
    i = max(0, min(int(s.actual / width), n - 1))
    buckets[i].append(s)

  points = [_summarize(i * width, b) for i, b in enumerate(buckets)]
  return {"bucket": _ns(width), "points": points}


def _summarize(at, bucket):
  ok = failed = skipped = throttled = 0
  for s in bucket:
    if s.outcome == OUTCOME_FAILED:
      failed += 1
    elif s.outcome == OUTCOME_SKIPPED:
      skipped += 1
    elif s.outcome == OUTCOME_OK:
      ok += 1
    if s.throttled:
      throttled += 1
  point = {
    "at": _ns(at),
    "flow_runs": len(bucket),
    "ok": ok,
    "failed": failed,
    "skipped": skipped,
    "throttled": throttled,
  }
  if bucket:
    point["p50"] = _ns(percentile(bucket, 0.50))
    point["p95"] = _ns(percentile(bucket, 0.95))
    point["p99"] = _ns(percentile(bucket, 0.99))
  else:
    point["p50"] = point["p95"] = point["p99"] = 0
  return point


def _is_notable(s):
  return s.outcome != OUTCOME_OK or s.throttled


def build_samples(samples):
  """Keeps every notable flow-run and evenly thins successes -- a port of
  collector.BuildSamples.
  """
  if not samples:
    return {"total": 0, "kept": 0, "every_nth": 1, "runs": None}

  notable = sum(1 for s in samples if _is_notable(s))
  budget = _KEPT_SAMPLES_CEILING - notable
  successes = len(samples) - notable
  stride = 1
  if budget < successes:
    stride = successes + 1 if budget <= 0 else -(-successes // budget)

  runs = []
  seen = 0
  for i, s in enumerate(samples):
    keep = _is_notable(s)
    if not keep:
      keep = seen % stride == 0
      seen += 1
    if not keep or len(runs) >= _KEPT_SAMPLES_CEILING:
      continue
    run = {
      "seq": i,
      "flow": s.flow,
      "at": _ns(s.actual),
      "latency": _ns(s.service),
      "service": _ns(s.service),
      "outcome": s.outcome,
    }
    if s.throttled:
      run["throttled"] = True
    runs.append(run)

  return {"total": len(samples), "kept": len(runs), "every_nth": stride, "runs": runs}


def _run_id(started_at):
  """Matches internal/store's runID: t.UTC().Format("20060102T150405.000000000Z").
  Python's datetime only has microsecond resolution, so the last 3 digits are
  always zero -- close enough for a sortable, unique-enough identifier.
  """
  dt = started_at.astimezone(timezone.utc)
  return dt.strftime("%Y%m%dT%H%M%S.") + f"{dt.microsecond * 1000:09d}Z"


def _error_rate(samples):
  if not samples:
    return 0.0
  failed = sum(1 for s in samples if s.outcome == OUTCOME_FAILED)
  return failed / len(samples)


def _throttle_rate(samples):
  if not samples:
    return 0.0
  throttled = sum(1 for s in samples if s.throttled)
  return throttled / len(samples)


def write_run(store_root, info, roots, samples, secrets):
  """Writes one run's artifacts and returns the run directory.

  info: dict with scenario, mode, initiator, target, commit (optional),
  dirty (optional), started_at (aware datetime), duration (seconds, float).
  roots: one Span per iteration, already wrapped as "flow:<name>" with
  duration/outcome set (mirrors what cmd/flowbench's executeOnce builds).
  samples: a parallel list of Sample, one per root.
  secrets: the SecretSet to redact traces through before writing.
  """
  run_id = _run_id(info["started_at"])
  run_dir = Path(store_root) / run_id
  run_dir.mkdir(parents=True, exist_ok=True)

  folded = Folded()
  for root in roots:
    folded.add(root)
  for root in roots:
    finalize(root, secrets, DEFAULT_BODY_CAP)

  meta = {
    "id": run_id,
    "scenario": info["scenario"],
    "mode": info["mode"],
    "initiator": info["initiator"],
    "target": info["target"],
    "started_at": info["started_at"]
    .astimezone(timezone.utc)
    .strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
    "duration": _ns(info["duration"]),
    "iterations": len(samples),
    "flow_runs": len(samples),
    "error_rate": _error_rate(samples),
    "throttle_rate": _throttle_rate(samples),
    "p50": _ns(percentile(samples, 0.50)),
    "p95": _ns(percentile(samples, 0.95)),
    "p99": _ns(percentile(samples, 0.99)),
  }
  if info.get("identities"):
    meta["identities"] = sorted(info["identities"])
  if info.get("commit"):
    meta["commit"] = info["commit"]
  if info.get("dirty"):
    meta["dirty"] = True
  if info.get("aborted"):
    meta["aborted"] = True

  _write_json(run_dir / "meta.json", meta)
  _write_json(run_dir / "folded.json", folded.to_dict())
  _write_json(run_dir / "traces.json", [r.to_dict() for r in roots])
  _write_json(run_dir / "series.json", build_series(samples, info["duration"]))
  _write_json(run_dir / "samples.json", build_samples(samples))
  _write_json(run_dir / "metrics.json", [])

  _append_index(store_root, meta)
  return run_dir


def prior_identities(store_root, scenario):
  """The structural identities the newest earlier run of this scenario
  recorded, or None when there is nothing to compare against -- no earlier
  run, or one written before identities were kept (a Go-produced run keeps
  none, since the engine's step ids are declared rather than discovered).

  None and the empty set are deliberately different: one means "unknown", the
  other means "that run recorded nothing", and only the second is grounds for
  a warning.
  """
  try:
    with (Path(store_root) / "index.json").open() as f:
      index = json.load(f)
  except (FileNotFoundError, ValueError):
    return None
  for meta in index:  # newest first
    if meta.get("scenario") == scenario:
      identities = meta.get("identities")
      return None if identities is None else set(identities)
  return None


def _write_json(path, value):
  with path.open("w") as f:
    json.dump(value, f, indent=2)
    f.write("\n")


def _append_index(store_root, meta):
  index_path = Path(store_root) / "index.json"
  try:
    with index_path.open() as f:
      index = json.load(f)
  except FileNotFoundError:
    index = []
  index.insert(0, meta)
  _write_json(index_path, index)
