package main

// version is rig's semantic version. A caller that depends on rig, such as
// an installer script, compares it with the minimum it needs. Raise the
// minor for a new command, flag or JSON field, and the major for any change
// to an existing command, exit code or JSON field.
const version = "0.7.0"
