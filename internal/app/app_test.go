package app

import "testing"

func TestValidateProxyListen(t *testing.T) {
	cases := []struct {
		addr    string
		allow   bool
		wantErr bool
	}{
		{"127.0.0.1:7879", false, false},
		{"localhost:7879", false, false}, // not an IP; not rejected
		{"[::1]:7879", false, false},
		{"0.0.0.0:7879", false, true},
		{":7879", false, true},
		{"192.168.1.5:7879", false, true},
		{"0.0.0.0:7879", true, false}, // override allows it
		{"192.168.1.5:7879", true, false},
	}
	for _, c := range cases {
		err := ValidateProxyListen(c.addr, c.allow)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateProxyListen(%q, allow=%v) err=%v, wantErr=%v", c.addr, c.allow, err, c.wantErr)
		}
	}
}
