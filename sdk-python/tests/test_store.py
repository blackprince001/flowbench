import json
from datetime import datetime, timezone

import pytest

from flowbench.secret import SecretSet
from flowbench.span import Span
from flowbench.store import (
  Folded,
  Sample,
  build_samples,
  build_series,
  percentile,
  write_run,
)


def test_folded_single_span():
  folded = Folded()
  sp = Span("flow:f", 0.0)
  sp.duration = 1.0
  folded.add(sp)
  d = folded.to_dict()
  assert d["root"]["children"]["flow:f"]["count"] == 1
  assert d["root"]["children"]["flow:f"]["total"] == 1_000_000_000


def test_folded_merges_same_path_across_roots():
  folded = Folded()
  for _ in range(3):
    sp = Span("flow:f", 0.0)
    sp.duration = 1.0
    folded.add(sp)
  assert folded.root.children["flow:f"].count == 3
  assert folded.root.children["flow:f"].total == 3.0


def test_folded_self_time_excludes_children():
  folded = Folded()
  root = Span("flow:f", 0.0)
  root.duration = 1.0
  child = root.child("login", 0.0)
  child.duration = 0.4
  folded.add(root)
  assert folded.root.children["flow:f"].self_time == pytest.approx(0.6)


def test_folded_omits_children_key_when_leaf():
  folded = Folded()
  sp = Span("leaf", 0.0)
  folded.add(sp)
  leaf_dict = folded.to_dict()["root"]["children"]["leaf"]
  assert "children" not in leaf_dict


def test_percentile_empty():
  assert percentile([], 0.5) == 0.0


def test_percentile_basic():
  samples = [
    Sample(flow="f", actual=0, service=s, outcome="ok", throttled=False)
    for s in (0.1, 0.2, 0.3, 0.4, 0.5)
  ]
  assert percentile(samples, 0.5) == 0.3


def test_build_series_empty():
  assert build_series([], 0) == {"bucket": 0, "points": None}


def test_build_series_single_bucket():
  samples = [
    Sample(flow="f", actual=0.0, service=0.1, outcome="ok", throttled=False),
    Sample(flow="f", actual=0.05, service=0.2, outcome="failed", throttled=False),
  ]
  series = build_series(samples, 0.1)
  assert series["bucket"] == 100_000_000  # 100ms, the narrowest ladder width
  # n = int(end/width) + 1, matching Go's BuildSeries exactly: end==width
  # yields 2 buckets, not 1.
  assert len(series["points"]) == 2
  point = series["points"][0]
  assert point["flow_runs"] == 2
  assert point["ok"] == 1
  assert point["failed"] == 1


def test_build_series_every_point_has_all_keys():
  samples = [Sample(flow="f", actual=0.0, service=0.1, outcome="ok", throttled=False)]
  point = build_series(samples, 0.1)["points"][0]
  assert set(point) == {
    "at",
    "flow_runs",
    "ok",
    "failed",
    "skipped",
    "throttled",
    "p50",
    "p95",
    "p99",
  }


def test_build_samples_empty():
  assert build_samples([]) == {"total": 0, "kept": 0, "every_nth": 1, "runs": None}


def test_build_samples_keeps_all_when_under_ceiling():
  samples = [
    Sample(flow="f", actual=i * 0.1, service=0.1, outcome="ok", throttled=False)
    for i in range(5)
  ]
  result = build_samples(samples)
  assert result["total"] == 5
  assert result["kept"] == 5
  assert result["every_nth"] == 1


def test_build_samples_keeps_notable_unconditionally():
  samples = [
    Sample(flow="f", actual=0.0, service=0.1, outcome="failed", throttled=False),
    Sample(flow="f", actual=0.1, service=0.1, outcome="ok", throttled=True),
  ]
  result = build_samples(samples)
  assert result["kept"] == 2
  outcomes = {r["outcome"] for r in result["runs"]}
  assert outcomes == {"failed", "ok"}


def test_build_samples_omits_throttled_key_when_false():
  samples = [Sample(flow="f", actual=0.0, service=0.1, outcome="ok", throttled=False)]
  run = build_samples(samples)["runs"][0]
  assert "throttled" not in run


def test_write_run_produces_all_six_files_and_index(tmp_path):
  root = Span("flow:checkout", 0.0)
  root.duration = 0.5
  root.outcome = "ok"
  login = root.child("login", 0.0)
  login.duration = 0.5
  login.set_call("POST", "/auth/login", 200, "")
  login.set_raw(b'{"e":"a"}', b'{"data":{"token":"x"}}')

  samples = [
    Sample(flow="checkout", actual=0.0, service=0.5, outcome="ok", throttled=False)
  ]
  info = {
    "scenario": "checkout.py",
    "mode": "integration",
    "initiator": "tester",
    "target": "local",
    "started_at": datetime(2026, 7, 25, 12, 0, 0, tzinfo=timezone.utc),
    "duration": 0.5,
  }

  run_dir = write_run(tmp_path, info, [root], samples, SecretSet())

  for name in (
    "meta.json",
    "folded.json",
    "traces.json",
    "series.json",
    "samples.json",
    "metrics.json",
  ):
    assert (run_dir / name).is_file(), name

  meta = json.loads((run_dir / "meta.json").read_text())
  assert meta["scenario"] == "checkout.py"
  assert meta["mode"] == "integration"
  assert meta["iterations"] == 1
  assert meta["flow_runs"] == 1
  assert meta["error_rate"] == 0.0
  assert "commit" not in meta
  assert "dirty" not in meta

  traces = json.loads((run_dir / "traces.json").read_text())
  assert traces[0]["Name"] == "flow:checkout"
  assert traces[0]["Children"][0]["Payload"]["method"] == "POST"

  index = json.loads((tmp_path / "index.json").read_text())
  assert len(index) == 1
  assert index[0]["id"] == meta["id"]


def test_write_run_prepends_to_existing_index(tmp_path):
  info = {
    "scenario": "a.py",
    "mode": "integration",
    "initiator": "t",
    "target": "local",
    "started_at": datetime(2026, 7, 25, 12, 0, 0, tzinfo=timezone.utc),
    "duration": 0.1,
  }
  samples = [Sample(flow="a", actual=0.0, service=0.1, outcome="ok", throttled=False)]
  first = Span("flow:a", 0.0)
  first.duration = 0.1
  write_run(tmp_path, info, [first], samples, SecretSet())

  info2 = dict(info, started_at=datetime(2026, 7, 25, 12, 0, 1, tzinfo=timezone.utc))
  second = Span("flow:a", 0.0)
  second.duration = 0.1
  write_run(tmp_path, info2, [second], samples, SecretSet())

  index = json.loads((tmp_path / "index.json").read_text())
  assert len(index) == 2
  # newest first
  assert index[0]["started_at"] > index[1]["started_at"]
