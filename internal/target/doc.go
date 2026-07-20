// Package target turns a target config into the run-time safety gate: the
// default base URL relative calls resolve against, and the host allow-list
// that refuses, before any request is sent, a flow reaching outside the
// declared base URLs. Ceilings and disallowed modes ride along for the
// planner (M2).
package target
