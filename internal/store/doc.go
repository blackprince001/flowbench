// Package store owns the run store: a directory of run artifacts with an
// index — aggregate metrics, threshold outcomes, folded flame data, raw
// trace trees, agent series, and run attribution (initiator, target,
// flow-file git commit). No retention machinery; the directory belongs to
// the user, and everything the results server shows lives here
// (PRD sections 10.7, 10.8, 13).
package store
