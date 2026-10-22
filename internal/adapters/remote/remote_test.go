package remote

import "testing"

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"gpu-box", "dev@gpu-box", "dev@10.0.0.2", "alias_1"} {
		if err := ValidTarget(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=touch /tmp/x", "-Fx", "a b", "a\nb", "a\x1b]2;x\x07"} {
		if ValidTarget(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if args := New("h", "", 0, false).sshArgs("cmd"); args[len(args)-3] != "--" {
		t.Errorf("no -- before the destination: %q", args)
	}
}
