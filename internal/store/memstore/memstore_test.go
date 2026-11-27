package memstore

import (
	"testing"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store {
		return New(store.Options{Actor: "tester"})
	})
}

func TestCause(t *testing.T) {
	storetest.Cause(t, func(_ *testing.T, o store.Options) store.Store { return New(o) })
}

func TestOnce(t *testing.T) {
	storetest.Once(t, func(_ *testing.T, o store.Options) (store.Store, func(store.Options) store.Store) {
		s := New(o)
		return s, func(o store.Options) store.Store {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.opts = o.WithDefaults()
			return s
		}
	})
}
