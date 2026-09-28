# authenticated_checkout.py rewritten so its login step uses login.flow.yaml
# through ctx.use(...) (#104). Compiles to IR equivalent to
# authenticated_checkout_use.flow.yaml -- internal/conformance checks this,
# and both fixtures point at the same login.flow.yaml file, so their `use`
# steps' embedded IR is not just equivalent but identical.
import os

from flowbench import Flow, Profile, Retry, expect

flow = Flow("authenticated_checkout_use", data="fixtures/users.csv")


@flow.step
def auth(ctx):
  ctx.use(
    Flow.load("login.flow.yaml"),
    with_={"email": ctx.user["email"], "password": ctx.user["password"]},
  )


@flow.step(
  retry=Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5)
)
def create_order(ctx):
  r = ctx.http.post(
    "/orders",
    headers={"Authorization": f"Bearer {ctx.vars['auth.token']}"},
    json={"items": ctx.user["cart"]},
  )
  ctx.vars["order_id"] = r.json_path("$.data.id")


@flow.step
def pay(ctx):
  r = ctx.http.post(
    f"/orders/{ctx.vars['order_id']}/pay",
    headers={"Authorization": f"Bearer {ctx.vars['auth.token']}"},
  )
  expect(r.status).to_be(202)


if __name__ == "__main__":
  flow.run(
    Profile(mode="integration"),
    target=os.environ.get("FLOWBENCH_TARGET", "local"),
    targets_dir=os.environ.get("FLOWBENCH_TARGETS_DIR", "targets"),
    store=os.environ.get("FLOWBENCH_STORE", "runs"),
  )
