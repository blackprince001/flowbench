// Package planner converts a profile into a VU schedule: arrival model
// (open or closed), ramp segments, hold, stop conditions, and the optional
// self-imposed arrival cap enforced ahead of the target. It also enforces
// target-config ceilings (max VUs/RPS, disallowed modes) before any load is
// generated (PRD sections 10.3, 13, 15).
package planner
