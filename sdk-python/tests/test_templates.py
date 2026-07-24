import json

from flowbench._template import TemplateRef


def test_renders_as_template_text():
    t = TemplateRef("token")
    assert str(t) == "{{ token }}"
    assert t == "{{ token }}"


def test_dotted_ref_renders():
    t = TemplateRef("user.email")
    assert str(t) == "{{ user.email }}"


def test_carries_ref_and_builder():
    sentinel = object()
    t = TemplateRef("token", builder=sentinel)
    assert t.ref == "token"
    assert t._builder is sentinel


def test_participates_in_fstrings():
    t = TemplateRef("token")
    assert f"Bearer {t}" == "Bearer {{ token }}"


def test_participates_in_fstring_url_interpolation():
    t = TemplateRef("order_id")
    assert f"/orders/{t}/pay" == "/orders/{{ order_id }}/pay"


def test_embeds_in_dict_literal_and_json_dumps():
    t = TemplateRef("cart")
    body = {"items": t}
    assert json.dumps(body) == '{"items": "{{ cart }}"}'


def test_survives_json_roundtrip_as_plain_str():
    t = TemplateRef("cart")
    dumped = json.dumps({"items": t})
    loaded = json.loads(dumped)
    assert loaded == {"items": "{{ cart }}"}
    assert isinstance(loaded["items"], str)
    assert not isinstance(loaded["items"], TemplateRef)
