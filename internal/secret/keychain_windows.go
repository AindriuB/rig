//go:build windows

package secret

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	credTypeGeneric         = 1 // CRED_TYPE_GENERIC
	credPersistLocalMachine = 2 // CRED_PERSIST_LOCAL_MACHINE
	errorNotFound           = syscall.Errno(1168)
)

var (
	advapi32      = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite = advapi32.NewProc("CredWriteW")
	procCredRead  = advapi32.NewProc("CredReadW")
	procCredDel   = advapi32.NewProc("CredDeleteW")
	procCredFree  = advapi32.NewProc("CredFree")
)

// credential mirrors CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// Keychain returns the Windows Credential Manager store.
func Keychain() (Store, error) { return keychain{}, nil }

type keychain struct{}

func target(account string) (*uint16, error) {
	return syscall.UTF16PtrFromString(Service + ":" + account)
}

func (keychain) Set(ctx context.Context, account string, s Secret) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	b, err := encode(s)
	if err != nil {
		return err
	}
	t, err := target(account)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	c := credential{
		Type:               credTypeGeneric,
		TargetName:         t,
		CredentialBlobSize: uint32(len(b)),
		CredentialBlob:     &b[0],
		Persist:            credPersistLocalMachine,
	}
	if r, _, e := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return fmt.Errorf("secret: CredWrite: %w", e)
	}
	return nil
}

func (keychain) Get(ctx context.Context, account string) (Secret, error) {
	if err := checkAccount(account); err != nil {
		return Secret{}, err
	}
	t, err := target(account)
	if err != nil {
		return Secret{}, fmt.Errorf("secret: %w", err)
	}
	var p *credential
	r, _, e := procCredRead.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		if errors.Is(e, errorNotFound) {
			return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, account)
		}
		return Secret{}, fmt.Errorf("secret: CredRead: %w", e)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(p)))
	if p.CredentialBlobSize == 0 || p.CredentialBlob == nil {
		return Secret{}, fmt.Errorf("secret: stored value for %s is empty", account)
	}
	raw := append([]byte(nil), unsafe.Slice(p.CredentialBlob, p.CredentialBlobSize)...)
	return decode(raw)
}

func (keychain) Delete(ctx context.Context, account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	t, err := target(account)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	if r, _, e := procCredDel.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0); r == 0 {
		if errors.Is(e, errorNotFound) {
			return nil
		}
		return fmt.Errorf("secret: CredDelete: %w", e)
	}
	return nil
}
