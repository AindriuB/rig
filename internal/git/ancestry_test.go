package git

import "testing"

func TestIsAncestor(t *testing.T) {
	gitIdentity(t)
	repo, _ := newRepo(t)
	r := Repo{Dir: repo}
	first, _, err := r.ResolveRef(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, repo, "later.txt", "later")
	later, _, _ := r.ResolveRef(ctx, "main")

	if ok, err := r.IsAncestor(ctx, first, later); err != nil || !ok {
		t.Errorf("first of later = %v, %v; want true", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, later, first); err != nil || ok {
		t.Errorf("later of first = %v, %v; want false", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, "no-such-rev", later); err == nil || ok {
		t.Errorf("unknown rev = %v, %v; want an error", ok, err)
	}
}
