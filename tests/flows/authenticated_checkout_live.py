# Live-execution conformance fixture (issue #25): the same chained flow as
# authenticated_checkout.py, mode=integration so it's directly executable
# (mode=stress there is deliberately load-scale and must reject direct
# execution). internal/conformance's live-execution parity test runs this
# via `python3` directly and diffs the resulting run artifact against
# `flowbench run authenticated_checkout_live.flow.yaml`.
import os

from flowbench import Flow, Profile, Retry, expect

flow = Flow("authenticated_checkout_live", data="users_live.csv")


@flow.step
def login(ctx):
  r = ctx.http.post(
    "/auth/login",
    json={
      "email": ctx.user["email"],
      "password": ctx.user["password"],
    },
  )
  expect(r.status).to_be(200)
  ctx.vars["token"] = r.json_path("$.data.access_token")
  expect(ctx.vars["token"]).not_to_be(None)


@flow.step(
  retry=Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5)
)
def create_order(ctx):
  r = ctx.http.post(
    "/orders",
    headers={"Authorization": f"Bearer {ctx.vars['token']}"},
    json={"items": ctx.user["cart"]},
  )
  ctx.vars["order_id"] = r.json_path("$.data.id")


@flow.step
def pay(ctx):
  r = ctx.http.post(
    f"/orders/{ctx.vars['order_id']}/pay",
    headers={"Authorization": f"Bearer {ctx.vars['token']}"},
  )
  expect(r.status).to_be(202)


if __name__ == "__main__":
  flow.run(
    Profile(mode="integration"),
    target=os.environ.get("FLOWBENCH_TARGET", "local"),
    targets_dir=os.environ.get("FLOWBENCH_TARGETS_DIR", "targets"),
    store=os.environ.get("FLOWBENCH_STORE", "runs"),
  )
