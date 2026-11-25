// Package selfupdate finds, downloads and verifies a signed rig release, then
// stages it and replaces the running binary. The release source is fixed in
// the binary. It also runs the opt-in automatic update (update.auto).
package selfupdate
