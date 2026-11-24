package selfupdate

// trustedKeys lists the base64 32-byte Ed25519 public keys that may sign a
// release. It is a list so a key can be rotated; any one accepting suffices.
var trustedKeys = []string{
	"S9wHn38VzWxW/fOYxkwI3rfNDszxcMH/HZw9kKBGwjQ=",
}
