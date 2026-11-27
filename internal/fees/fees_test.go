package fees

import "testing"

func TestTotalIsEightPercentPlusThirtyCents(t *testing.T) {
	cases := map[int]int{
		10000:  830,
		20000:  1630,
		12345:  1018,
		100:    38,
		1:      30,
		0:      0,
		-500:   0,
		999999: 80030,
	}
	for stay, want := range cases {
		if got := Total(stay); got != want {
			t.Errorf("Total(%d) = %d, want %d", stay, got, want)
		}
	}
}

func TestTheHostPaysTheWholeFee(t *testing.T) {
	if got := HostShare(60000); got != 4830 {
		t.Fatalf("HostShare(60000) = %d, want 4830", got)
	}
}

func TestHostShareNeverExceedsWhatTheHostKept(t *testing.T) {
	if got := HostShare(10); got != 10 {
		t.Fatalf("HostShare(10) = %d, want 10", got)
	}
	if got := HostShare(0); got != 0 {
		t.Fatalf("HostShare(0) = %d, want 0", got)
	}
}
