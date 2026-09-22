package mqruntime

import "testing"

func TestEnabledRuntimeRequiresCompleteDemoConfiguration(t *testing.T) {
	for _, c := range []Config{{Enabled: true}, {Enabled: true, Host: "mqnamesrv", Port: 9876}, {Enabled: true, Host: "mqnamesrv", Port: 9876, Mode: "payment", OrderTTLSeconds: 1800}, {Enabled: true, Host: "mqnamesrv", Port: 9876, Mode: "demo", OrderTTLSeconds: 0}} {
		if c.Validate(true) == nil {
			t.Fatalf("unsafe configuration accepted: %+v", c)
		}
	}
	if err := (Config{Enabled: true, Host: "mqnamesrv", Port: 9876, Mode: "demo", OrderTTLSeconds: 1800}).Validate(true); err != nil {
		t.Fatal(err)
	}
	if err := (Config{}).Validate(true); err != nil {
		t.Fatal("disabled MQ must preserve basic RPC", err)
	}
}
