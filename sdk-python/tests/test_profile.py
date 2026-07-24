import pytest

from flowbench import Profile
from flowbench._errors import FlowCompileError


def test_minimal_integration_profile():
    p = Profile(mode="integration")
    assert p.to_ir() == {"mode": "integration"}


def test_invalid_mode_rejected():
    with pytest.raises(FlowCompileError, match="mode"):
        Profile(mode="bogus")


def test_native_ramp_grammar_passes_through():
    p = Profile(mode="stress", vus="0 -> 500 over 5m")
    assert p.to_ir()["ramp"] == "0 -> 500 over 5m"
    assert "vus" not in p.to_ir()


def test_prd_ramp_function_form_translates():
    p = Profile(mode="stress", vus="ramp(0 -> 500, 5m)")
    assert p.to_ir()["ramp"] == "0 -> 500 over 5m"


def test_ramp_field_also_accepts_function_form():
    p = Profile(mode="stress", ramp="ramp(10 -> 200, 2m)")
    assert p.to_ir()["ramp"] == "10 -> 200 over 2m"


def test_malformed_ramp_rejected():
    with pytest.raises(FlowCompileError, match="ramp"):
        Profile(mode="stress", ramp="not a ramp")


def test_plain_integer_vus_passes_through():
    p = Profile(mode="load", vus=50)
    assert p.to_ir()["vus"] == 50


def test_hold_duration_validated():
    with pytest.raises(FlowCompileError, match="hold"):
        Profile(mode="stress", hold="not-a-duration")


def test_hold_duration_accepted():
    p = Profile(mode="stress", hold="10m")
    assert p.to_ir()["hold"] == "10m"


def test_arrival_cap_validated():
    with pytest.raises(FlowCompileError, match="arrival_cap"):
        Profile(mode="stress", arrival_cap="bogus")


def test_full_prd_example_profile():
    p = Profile(
        mode="stress",
        vus="ramp(0 -> 500, 5m)",
        hold="10m",
        arrival_cap="300/s",
        thresholds=["p95(latency) < 800ms", "error_rate < 1%"],
    )
    assert p.to_ir() == {
        "mode": "stress",
        "ramp": "0 -> 500 over 5m",
        "hold": "10m",
        "arrival_cap": "300/s",
        "thresholds": ["p95(latency) < 800ms", "error_rate < 1%"],
    }
