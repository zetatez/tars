package auth

import (
	"strings"
	"testing"
)

func TestDeriveAdminKeyDeterministic(t *testing.T) {
	k1 := DeriveAdminKey("machine-a")
	k2 := DeriveAdminKey("machine-a")
	if k1 != k2 {
		t.Fatalf("not deterministic: %s vs %s", k1, k2)
	}
}

func TestDeriveAdminKeyDiffersByMachine(t *testing.T) {
	ka := DeriveAdminKey("machine-a")
	kb := DeriveAdminKey("machine-b")
	if ka == kb {
		t.Fatal("different machines should derive different keys")
	}
}

func TestDeriveAdminKeyFormat(t *testing.T) {
	k := DeriveAdminKey("machine-a")
	idx := strings.IndexByte(k, '_')
	if idx <= 0 {
		t.Fatalf("bad format: %s", k)
	}
	keyID := k[:idx]
	secret := k[idx+1:]
	if !strings.HasPrefix(keyID, adminKeyPrefix+"-") {
		t.Fatalf("key_id prefix = %s", keyID)
	}
	if len(secret) != 64 {
		t.Fatalf("secret len = %d, want 64", len(secret))
	}
}

func TestAdminKeyFromConfig(t *testing.T) {
	k1, err := AdminKeyFromConfig()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	k2, _ := AdminKeyFromConfig()
	if k1 != k2 {
		t.Fatal("not deterministic")
	}
	if want := DeriveAdminKey(MachineID()); k1 != want {
		t.Fatalf("mismatch: got %s want %s", k1, want)
	}
}
