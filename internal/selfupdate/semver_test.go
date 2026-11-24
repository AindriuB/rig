package selfupdate

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.10.0", "0.9.9", 1}, {"v0.5.2", "0.5.2", 0}, {"0.5.1", "v0.5.2", -1},
		{"1.0.0-rc.1", "1.0.0", -1}, {"1.0.0-rc.2", "1.0.0-rc.10", -1},
	} {
		got, err := CompareVersions(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%s,%s)=%d,%v want %d", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := CompareVersions("1.2", "1.0.0"); err == nil {
		t.Error("want error for 1.2")
	}
}

func TestSignedEra(t *testing.T) {
	for v, want := range map[string]bool{"0.5.1": false, "0.5.2": true, "v0.10.0": true, "0.4.9": false} {
		got, err := SignedEra(v)
		if err != nil || got != want {
			t.Errorf("SignedEra(%s)=%v,%v", v, got, err)
		}
	}
}
