// Package store is rig's backend-neutral task store: the types a task log is
// made of, the fold that turns a log into a task, ids, finding dedupe, the
// merge of two logs, and the Store port that backends implement.
//
// A task is an append-only log of events. Everything else (state, fields,
// counters, findings, notes) is derived by folding that log in (seq, id)
// order. The package names no workflow and knows no git.
package store
