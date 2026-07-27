#!/usr/bin/env bash
#
# Run every worked example into one run store, so `flowbench serve` has a
# workspace worth browsing.
#
#   ./examples/run-all.sh --serve      # run everything, browse it live
#   ./examples/run-all.sh              # run everything, browse it later
#   ./examples/run-all.sh --public     # include the live public-API examples
#   ./examples/run-all.sh --store demo-runs
#
# **Each example is its own project.** They test different services under
# different profiles against different stubs, so they are not one pile of runs
# that happens to share a directory. Each writes into `<store>/<example>/`, and
# `serve` takes one `--store name=dir` per project — so the split is a matter
# of where the runs land, not of anything the results server had to learn.
#
# Two things worth knowing before reading the output.
#
# **Some of these are supposed to fail.** `breach` breaches its thresholds on
# purpose, `soak` breaches its drift trend against a creeping endpoint,
# `mismatch` waits for a frame that never comes, `errors` asks a GraphQL server
# for a field it refuses, `faults` exists to produce every failure group at
# once, and `sweep` (with --public) deliberately runs 1000 iterations into a
# public API rate-limited to 100 per 15 minutes. A nonzero exit from those is
# the example working, so this script records each exit code and keeps going
# rather than stopping at the first one.
#
# **The public-API examples are off by default.** deck-of-cards, httpbingo,
# bored-api and example.com are services nobody here controls, and their
# READMEs are explicit that a free service should not absorb a load run on a
# loop. `--public` opts in.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

STORE=runs
ADDR=127.0.0.1:7580
WITH_PUBLIC=0
WITH_SERVE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --store) STORE="$2"; shift 2 ;;
    --addr) ADDR="$2"; shift 2 ;;
    --public) WITH_PUBLIC=1; shift ;;
    --serve) WITH_SERVE=1; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

TMP=$(mktemp -d)
BIN=$TMP/flowbench
echo "building flowbench..."
go build -o "$BIN" ./cmd/flowbench || exit 1
mkdir -p "$STORE"

# The auth example's stub demands a different credential per scheme and these
# are the ones it checks for. They are demo values in a local stub — the point
# of the example is that they reach the request and appear in no artifact.
export DEMO_API_TOKEN=tok_demo_bearer_9f8e7d
export DEMO_USER=reports-service
export DEMO_PASSWORD=s3cr3t-basic-pw
export DEMO_API_KEY=ak_demo_5b4a3c2d
export DEMO_SESSION=sess_demo_abc123
export DEMO_CLIENT_ID=client_demo
export DEMO_CLIENT_SECRET=cs_demo_shhh
export DEMO_SIGNING_SECRET=whsec_demo_signing_key
export DEMO_SIGNING_KEY_ID=key-2026-07
# The login example injects this into a body the stub echoes back, so it lands
# in both captured payloads — and is scrubbed to [redacted] before either is
# stored. `grep -r hunter2 "$STORE"` finding nothing is the example's point.
export DEMO_SECRET=hunter2-do-not-leak
export DEMO_TOKEN=tok_demo_public_echo
export API_TOKEN=tok_demo_public_echo

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null; done
  wait 2>/dev/null
  rm -rf "$TMP"
}
# A bare `trap cleanup INT` would tear the stubs down and then fall back into
# whatever was interrupted, so Ctrl-C has to exit as well as clean up.
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

port_open() {
  local host=${1%%:*} port=${1##*:}
  (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null && { exec 3<&-; return 0; }
  return 1
}

# wait_port polls until something accepts on host:port, so a flow never races
# the stub it is about to call.
wait_port() {
  for _ in $(seq 1 100); do
    port_open "$1" && return 0
    sleep 0.1
  done
  echo "  ! nothing listening on $1" >&2
  return 1
}

# start_stub builds the stub and runs the binary directly rather than through
# `go run`. That matters for teardown: `go run` is a parent that spawns the
# server as a child, so killing the PID we hold would leave the server alive
# and holding the port — and the next run of this script would silently reuse
# that stale process, which is worse than failing, because a soak run against a
# stub that has been up for an hour reports numbers about the wrong thing.
start_stub() {
  local name=$1 addr=$2
  if port_open "$addr"; then
    echo "  ! $addr is already in use — stop whatever is on it first (a leftover stub reports the wrong numbers)" >&2
    return 1
  fi
  go build -o "$TMP/$name.stub" "./examples/$name/stub" || return 1
  "$TMP/$name.stub" >"$TMP/$name.stub.log" 2>&1 &
  PIDS+=($!)
  wait_port "$addr" || return 1
  echo "  stub up: $name ($addr)"
}

PASSED=(); FAILED=(); PROJECTS=(); EXAMPLES=(); SERVE_PID=

# Each example is its own project, not a folder of flows that happen to share a
# store: they test different services, under different profiles, against
# different stubs. `serve` takes one --store per project (`name=dir`), so
# writing each example's runs under its own directory is the whole mechanism —
# the results server needs nothing added to show them as separate projects.
# claim registers an example as a project and creates its store. It runs for
# every example *before* anything executes, because `serve` refuses a --store
# that does not exist yet and the whole point of --serve is watching the runs
# land — a server started after the last one has nothing to watch.
claim() {
  local dir=$1 name; name=$(basename "$dir")
  mkdir -p "$STORE/$name"
  EXAMPLES+=("$dir")
  PROJECTS+=("$name=$STORE/$name")
}

run_example() {
  local dir=$1 name; name=$(basename "$dir")
  for flow in "$dir"/*.flow.yaml; do
    [ -e "$flow" ] || continue
    run_one "$flow" "$dir/target.yaml" "$STORE/$name"
  done
}

run_one() {
  local flow=$1 target=$2 store=$3
  printf '\n\033[1m▶ %s\033[0m\n' "$flow"
  if "$BIN" run "$flow" --target "$target" --store "$store"; then
    PASSED+=("$flow")
  else
    FAILED+=("$flow (exit $?)")
  fi
}

# The projects are named explicitly here rather than letting `serve --store
# <parent>` discover them, because discovery runs once at startup and these
# directories are still empty at that point — the whole reason the server is
# started first is to watch them fill. Afterwards, `serve --store <parent>`
# finds them all on its own.
serve_projects() {
  [ "$WITH_SERVE" = 1 ] || return 1
  local args=()
  for p in "${PROJECTS[@]}"; do args+=(--store "$p"); done
  "$BIN" serve "${args[@]}" --addr "$ADDR" &
  SERVE_PID=$!
  PIDS+=("$SERVE_PID")
  wait_port "$ADDR"
}

# Every project is claimed up front so the server can be up before the first
# run rather than after the last.
claim examples/load-local
claim examples/auth-local
claim examples/graphql-local
claim examples/ws-local
claim examples/grpc-local
if [ "$WITH_PUBLIC" = 1 ]; then
  claim examples/deck-of-cards
  claim examples/httpbingo
  claim examples/bored-api
  claim examples/example-com
fi

if serve_projects; then
  printf '\n\033[1mwatching: http://%s\033[0m — %d projects, refresh as runs land\n' "$ADDR" "${#PROJECTS[@]}"
fi

echo
echo "starting local stubs..."
# A stub that will not start aborts the run rather than letting the flows that
# need it report failures about the wrong thing.
start_stub load-local    localhost:8080   || exit 1
start_stub auth-local    localhost:8090   || exit 1
start_stub graphql-local localhost:8091   || exit 1
start_stub ws-local      localhost:8092   || exit 1
start_stub grpc-local    127.0.0.1:50051  || exit 1

# load-local's soak goes first of all, against the freshest stub. Its /slow
# endpoint creeps with the stub's *uptime*, so a stub that has already been
# running for a minute has a high enough baseline to hide the drift — and the
# drift is the whole thing the soak trend check exists to catch.
run_one examples/load-local/soak.flow.yaml examples/load-local/target.yaml "$STORE/load-local"
for flow in examples/load-local/*.flow.yaml; do
  [ "$flow" = examples/load-local/soak.flow.yaml ] && continue
  run_one "$flow" examples/load-local/target.yaml "$STORE/load-local"
done

run_example examples/auth-local
run_example examples/graphql-local
run_example examples/ws-local
run_example examples/grpc-local

if [ "$WITH_PUBLIC" = 1 ]; then
  run_example examples/deck-of-cards
  run_example examples/httpbingo
  run_example examples/bored-api
  run_example examples/example-com
else
  echo
  echo "skipped the public-API examples (deck-of-cards, httpbingo, bored-api, example-com) — pass --public to include them"
fi


printf '\n\033[1m%d clean · %d with failures\033[0m — %d projects under %s/\n' \
  "${#PASSED[@]}" "${#FAILED[@]}" "${#PROJECTS[@]}" "$STORE"
for f in "${FAILED[@]:-}"; do [ -n "$f" ] && echo "  ✗ $f"; done
echo "  (breach, soak, mismatch, errors, faults and sweep are meant to be in that list)"
printf '\nbrowse them any time with:  flowbench serve --store %s\n' "$STORE"

# A negative array subscript needs bash 4.3 and macOS ships 3.2, so the server's
# pid is held on its own rather than read back off the end of PIDS.
if [ -n "$SERVE_PID" ]; then
  printf '\n\033[1mbrowse: http://%s\033[0m — %d projects  (Ctrl-C to stop)\n' "$ADDR" "${#PROJECTS[@]}"
  wait "$SERVE_PID"
fi
