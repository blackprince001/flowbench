# WebSocket on the Python surface, paired with ws_session.flow.yaml.
# Compiles to equivalent IR (internal/conformance checks this).
#
# ctx.ws compiles to a `ws` step: a session opened by one step and worked on
# by later ones, closed when the iteration ends. frame(...) says which frame a
# step is waiting for — a filter, not an assertion, since a duplex connection
# carries traffic the step never asked for.
from flowbench import Bearer, Flow, Profile, env, expect, frame

flow = Flow("ws_session", auth=Bearer(env("FEED_TOKEN")))


@flow.step
def open_feed(ctx):
  ctx.ws(
    "/feed",
    subprotocols=["flowbench.v1"],
    headers={"X-Client": "flowbench"},
  )


@flow.step
def subscribe(ctx):
  r = ctx.ws(
    send={"op": "subscribe", "symbol": "FB-001"},
    receive=frame("$.type").to_be("ack"),
    timeout="2s",
  )
  ctx.vars["subscription_id"] = r.json_path("$.id")
  expect(r.json_path("$.status")).to_be("ok")


@flow.step
def first_tick(ctx):
  r = ctx.ws(
    receive=[frame("$.type").to_be("tick"), frame("$.symbol").to_be("FB-001")],
    timeout="5s",
  )
  expect(r.json_path("$.price")).to_exist()


@flow.step
def open_control(ctx):
  ctx.ws("/control", session="control")


@flow.step
def ping_control(ctx):
  r = ctx.ws(
    session="control",
    send={"op": "ping", "subscription": ctx.vars["subscription_id"]},
    receive=True,
    timeout="1s",
  )
  expect(r.json_path("$.op")).to_be("pong")


if __name__ == "__main__":
  flow.run(Profile(mode="integration"))
