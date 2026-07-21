// Package planner converts a profile into a VU schedule: the concurrency curve
// over time, the arrival model, the stop condition, and any self-imposed
// arrival cap. The schedule is the executor's contract — everything needed to
// drive the VU pool, with no reference back to the profile's surface syntax.
//
// Target-config ceilings (max VUs/RPS) and mode disallow are enforced
// separately, ahead of and around the plan; see the target package and the
// safety-rails work.
package planner
