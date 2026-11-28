package hookstate

import "os"

// Inspect returns hook's lease holder and whether its process is live. With
// no lease it returns nil and false.
func Inspect(dir, hook string) (*Holder, bool, error) {
	path := leasePath(dir, hook)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, false, nil
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	h, err := readLease(path)
	if err != nil || h == nil {
		return nil, false, err
	}
	return h, holderLive(*h), nil
}
