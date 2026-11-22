package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AindriuB/rig/internal/proc"
)

// ErrRefMoved reports that UpdateRef found the ref somewhere other than the
// expected old value, so another writer got there first.
var ErrRefMoved = errors.New("ref moved")

// plumbingEnv is the fixed identity commit-tree records. It does not depend on
// the user's git configuration.
var plumbingEnv = []string{
	"GIT_AUTHOR_NAME=rig", "GIT_AUTHOR_EMAIL=rig@localhost",
	"GIT_COMMITTER_NAME=rig", "GIT_COMMITTER_EMAIL=rig@localhost",
}

// runIn is run with data on stdin and extra environment.
func (r Repo) runIn(ctx context.Context, stdin []byte, extraEnv []string, args ...string) (proc.Result, error) {
	full := args
	if r.Dir != "" {
		full = append([]string{"-C", r.Dir}, args...)
	}
	e := append(append([]string{}, env...), extraEnv...)
	return proc.Run(ctx, proc.Options{Env: e, Timeout: queryTimeout, Stdin: bytes.NewReader(stdin)}, "git", full...)
}

func (r Repo) outIn(ctx context.Context, stdin []byte, extraEnv []string, args ...string) (string, error) {
	res, err := r.runIn(ctx, stdin, extraEnv, args...)
	if err != nil {
		return "", wrap(args, res, err)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// WriteBlob stores data as a loose blob and returns its object id.
func (r Repo) WriteBlob(ctx context.Context, data []byte) (string, error) {
	return r.outIn(ctx, data, nil, "hash-object", "-w", "--stdin")
}

// TreeEntry is one line of a tree: Mode like "100644", Type "blob" or "tree".
type TreeEntry struct {
	Mode string
	Type string
	OID  string
	Name string
}

// MakeTree writes a tree of the given entries and returns its object id.
func (r Repo) MakeTree(ctx context.Context, entries []TreeEntry) (string, error) {
	var b strings.Builder
	for _, e := range entries {
		if e.Name == "" || strings.ContainsAny(e.Name, "\n\x00") {
			return "", fmt.Errorf("git mktree: invalid entry name %q", e.Name)
		}
		fmt.Fprintf(&b, "%s %s %s\t%s\n", e.Mode, e.Type, e.OID, e.Name)
	}
	return r.outIn(ctx, []byte(b.String()), nil, "mktree")
}

// CommitTree creates a commit of tree with the given parents and message,
// authored and committed as rig <rig@localhost>. It moves no ref.
func (r Repo) CommitTree(ctx context.Context, tree string, parents []string, message string) (string, error) {
	args := []string{"commit-tree", "--no-gpg-sign", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-F", "-")
	return r.outIn(ctx, []byte(message), plumbingEnv, args...)
}

// UpdateRef points ref at newOID if it is currently at oldOID. An empty oldOID
// means the ref must not exist. If the ref is elsewhere it returns ErrRefMoved.
func (r Repo) UpdateRef(ctx context.Context, ref, newOID, oldOID string) error {
	args := []string{"update-ref", ref, newOID, oldOID}
	res, err := r.run(ctx, queryTimeout, args...)
	if err == nil {
		return nil
	}
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		cur, ok, rerr := r.ResolveRef(ctx, ref)
		if rerr == nil && (ok != (oldOID != "") || (ok && cur != oldOID)) {
			return ErrRefMoved
		}
	}
	return wrap(args, res, err)
}

// ResolveRef returns the object id ref points at; ok is false if it does not
// exist.
func (r Repo) ResolveRef(ctx context.Context, ref string) (oid string, ok bool, err error) {
	args := []string{"rev-parse", "-q", "--verify", ref}
	res, err := r.run(ctx, queryTimeout, args...)
	var ee *proc.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, wrap(args, res, err)
	}
	return strings.TrimSpace(string(res.Stdout)), true, nil
}

// Ref is a ref name and the object id it points at.
type Ref struct {
	Name string
	OID  string
}

// ReadRefs lists every ref under prefix, for example "refs/rig/tasks/".
func (r Repo) ReadRefs(ctx context.Context, prefix string) ([]Ref, error) {
	out, err := r.out(ctx, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil || out == "" {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		oid, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("git for-each-ref: unexpected line %q", line)
		}
		refs = append(refs, Ref{Name: name, OID: oid})
	}
	return refs, nil
}

// Blob is the result of one ReadBlobs spec.
type Blob struct {
	Spec    string
	Data    []byte
	Missing bool
}

// ReadBlobs reads every spec (for example "refs/rig/tasks/X:log.jsonl") with
// one git cat-file --batch process. Results are in the order of specs.
func (r Repo) ReadBlobs(ctx context.Context, specs []string) ([]Blob, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	for _, s := range specs {
		if s == "" || strings.ContainsAny(s, "\n\x00") {
			return nil, fmt.Errorf("git cat-file: invalid spec %q", s)
		}
	}
	args := []string{"cat-file", "--batch"}
	res, err := r.runIn(ctx, []byte(strings.Join(specs, "\n")+"\n"), nil, args...)
	if err != nil {
		return nil, wrap(args, res, err)
	}
	rest := res.Stdout
	blobs := make([]Blob, 0, len(specs))
	for _, spec := range specs {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil, fmt.Errorf("git cat-file: output ended before %q", spec)
		}
		header := string(rest[:i])
		rest = rest[i+1:]
		if strings.HasSuffix(header, " missing") {
			blobs = append(blobs, Blob{Spec: spec, Missing: true})
			continue
		}
		f := strings.Fields(header)
		if len(f) != 3 {
			return nil, fmt.Errorf("git cat-file: %q: unexpected header %q", spec, header)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || len(rest) < size+1 {
			return nil, fmt.Errorf("git cat-file: %q: bad header or short output %q", spec, header)
		}
		blobs = append(blobs, Blob{Spec: spec, Data: append([]byte{}, rest[:size]...)})
		rest = rest[size+1:]
	}
	return blobs, nil
}
