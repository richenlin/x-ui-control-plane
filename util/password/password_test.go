package password

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("s3cret-pässword")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHashed(h) || strings.Contains(h, "s3cret") {
		t.Fatalf("unexpected hash: %q", h)
	}
	if ok, legacy := Verify(h, "s3cret-pässword"); !ok || legacy {
		t.Fatalf("valid password rejected")
	}
	if ok, _ := Verify(h, "wrong"); ok {
		t.Fatalf("invalid password accepted")
	}
}

func TestLegacyPlaintextIsAcceptedAndReported(t *testing.T) {
	ok, legacy := Verify("plain-password", "plain-password")
	if !ok || !legacy {
		t.Fatalf("legacy plaintext password must verify and be reported for upgrade")
	}
	if ok, _ := Verify("plain-password", "other"); ok {
		t.Fatalf("wrong legacy password accepted")
	}
}

func TestLongPasswordIsTruncatedConsistently(t *testing.T) {
	long := strings.Repeat("a", 200)
	h, err := Hash(long)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := Verify(h, long); !ok {
		t.Fatalf("long password must still verify")
	}
}
