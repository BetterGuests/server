package publicweb

import "testing"

func TestRequestsNeverReachPrivateAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "10.0.0.4:443", "169.254.169.254:80", "[::1]:443", "[fd12::1]:443", "100.100.0.1:443", "[::ffff:192.168.1.1]:443"} {
		if RefusePrivateAddress("tcp", address, nil) == nil {
			t.Errorf("%s was allowed", address)
		}
	}
	if err := RefusePrivateAddress("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}
