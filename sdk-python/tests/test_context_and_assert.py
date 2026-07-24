import pytest

from flowbench._assert import expect
from flowbench._context import Context, _StepBuilder
from flowbench._errors import FlowCompileError
from flowbench._template import TemplateRef


def make_ctx(has_data_pool=True, available_vars=None):
    builder = _StepBuilder("step", available_vars if available_vars is not None else set())
    return Context(builder, has_data_pool), builder


def test_http_post_records_call_spec_and_returns_response():
    ctx, builder = make_ctx()
    r = ctx.http.post("/auth/login", json={"email": "a@b.com"})
    assert builder.call_spec == {"method": "POST", "url": "/auth/login", "body": {"email": "a@b.com"}}
    assert r._builder is builder


def test_second_call_in_same_step_raises():
    ctx, _ = make_ctx()
    ctx.http.post("/auth/login")
    with pytest.raises(FlowCompileError, match="more than one ctx.http call"):
        ctx.http.get("/other")


def test_headers_and_query_stringified():
    ctx, builder = make_ctx()
    ctx.http.get("/x", headers={"Authorization": "Bearer t"}, query={"limit": 5})
    assert builder.call_spec["headers"] == {"Authorization": "Bearer t"}
    assert builder.call_spec["query"] == {"limit": "5"}


def test_status_is_assertable_subject():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    expect(r.status).to_be(200)
    assert builder.assert_ == [{"source": "status", "op": "eq", "value": 200}]


def test_header_is_assertable_subject():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    expect(r.header("X-Trace")).to_exist()
    assert builder.assert_ == [{"source": "header", "key": "X-Trace", "op": "exists"}]


def test_json_path_extraction_via_vars_setitem():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    ctx.vars["token"] = r.json_path("$.data.access_token")
    assert builder.extract == [{"var": "token", "path": "$.data.access_token"}]
    assert "token" in builder.available_vars


def test_vars_setitem_rejects_non_extraction():
    ctx, _ = make_ctx()
    with pytest.raises(FlowCompileError, match="response.json_path"):
        ctx.vars["token"] = "not-an-extraction"


def test_vars_getitem_returns_template_ref():
    ctx, builder = make_ctx(available_vars={"token"})
    t = ctx.vars["token"]
    assert isinstance(t, TemplateRef)
    assert str(t) == "{{ token }}"


def test_vars_getitem_unavailable_raises():
    ctx, _ = make_ctx(available_vars=set())
    with pytest.raises(FlowCompileError, match="read before it was extracted"):
        ctx.vars["token"]


def test_user_proxy_renders_dotted_ref():
    ctx, _ = make_ctx(has_data_pool=True)
    t = ctx.user["email"]
    assert str(t) == "{{ user.email }}"


def test_user_proxy_unavailable_without_data_pool():
    ctx, _ = make_ctx(has_data_pool=False)
    with pytest.raises(FlowCompileError, match="data=..."):
        ctx.user


def test_env_proxy_renders_dotted_ref():
    ctx, _ = make_ctx()
    t = ctx.env["DEMO_SECRET"]
    assert str(t) == "{{ env.DEMO_SECRET }}"


def test_expect_on_extracted_var_asserts_source_var():
    ctx, builder = make_ctx(available_vars={"token"})
    expect(ctx.vars["token"]).not_to_be(None)
    assert builder.assert_ == [{"source": "var", "key": "token", "op": "exists"}]


def test_expect_to_be_none_maps_to_not_exists():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    expect(r.status).to_be(None)
    assert builder.assert_ == [{"source": "status", "op": "not_exists"}]


def test_expect_on_dotted_ref_rejected():
    ctx, _ = make_ctx()
    with pytest.raises(FlowCompileError, match="cannot assert on"):
        expect(ctx.user["email"])


def test_expect_on_plain_value_rejected():
    with pytest.raises(FlowCompileError, match="only accepts"):
        expect("plain string")


@pytest.mark.parametrize(
    "method,op",
    [
        ("to_be_less_than", "lt"),
        ("to_be_less_than_or_equal", "lte"),
        ("to_be_greater_than", "gt"),
        ("to_be_greater_than_or_equal", "gte"),
        ("to_contain", "contains"),
        ("to_match", "matches"),
    ],
)
def test_assertion_builder_op_mapping(method, op):
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    getattr(expect(r.status), method)(123)
    assert builder.assert_ == [{"source": "status", "op": op, "value": 123}]


def test_to_not_exist():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    expect(r.status).to_not_exist()
    assert builder.assert_ == [{"source": "status", "op": "not_exists"}]


def test_not_to_be_value_maps_to_ne():
    ctx, builder = make_ctx()
    r = ctx.http.post("/x")
    expect(r.status).not_to_be(200)
    assert builder.assert_ == [{"source": "status", "op": "ne", "value": 200}]
