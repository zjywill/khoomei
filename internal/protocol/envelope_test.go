package protocol

import (
	"strings"
	"testing"
)

func TestValidAgent(t *testing.T) {
	for _, ok := range []string{"grok", "muse", "私信联系人", "claude code", "a"} {
		if !ValidAgent(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", " grok", "grok ", "a/b", "tab\tname", strings.Repeat("x", 65)} {
		if ValidAgent(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"v":1,"id":"abcdefgh","from_agent":"a","to_agent":"b","ts":"2026-10-24T08:00:00Z","extra":1}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	e := New("muse", "grok", "hi")
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Wake = ""
	if e.RequestedWake() != WakeBatch || e.Validate() != nil {
		t.Fatal("empty wake should default to batch")
	}
	e.TS = "2026-10-24 08:00:00"
	if e.Validate() == nil {
		t.Fatal("bad ts accepted")
	}
}

func TestMinWake(t *testing.T) {
	if MinWake(WakeNow, WakeBatch) != WakeBatch || MinWake(WakeNone, WakeNow) != WakeNone || MinWake(WakeNow, WakeNow) != WakeNow {
		t.Fatal("MinWake order wrong")
	}
}
