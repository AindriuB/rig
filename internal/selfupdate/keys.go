package selfupdate

// trustedKeys lists the base64 32-byte Ed25519 public keys that may sign a
// release. It is a list so a key can be rotated; any one accepting suffices.
//
// It is EMPTY until the maintainer's key from `rigsign keygen` is added, and
// a test refuses to pass with it empty unless run with -prekey.
var trustedKeys = []string{}
