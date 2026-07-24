# The PRD section 11 sample flow, verbatim: the chained login -> extract ->
# act -> assert path, authored on the Python surface (issue #22). Compiles
# to IR equivalent to authenticated_checkout.flow.yaml (internal/conformance
# checks this). The explicit `expect(ctx.vars["token"]).not_to_be(None)`
# line is the Python spelling of the YAML fixture's `token != null` assert
# -- the PRD's prose example omits it, but it's needed for true IR parity.
from flowbench import Flow, Profile, Retry, expect

flow = Flow("authenticated_checkout", data="fixtures/users.csv")


@flow.step
def login(ctx):
    r = ctx.http.post("/auth/login", json={
        "email": ctx.user["email"],
        "password": ctx.user["password"],
    })
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")
    expect(ctx.vars["token"]).not_to_be(None)


@flow.step(retry=Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5))
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
    flow.run(Profile(
        mode="stress",
        vus="ramp(0 -> 500, 5m)", hold="10m",
        arrival_cap="300/s",
        thresholds=["p95(latency) < 800ms", "error_rate < 1%"],
    ))
