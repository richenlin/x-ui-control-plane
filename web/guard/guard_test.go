package guard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testOptions(dir string) Options {
	return Options{
		MaxFailures:       3,
		Window:            10 * time.Minute,
		BaseLock:          time.Minute,
		MaxLock:           8 * time.Minute,
		StrikeReset:       time.Hour,
		GlobalMaxFailures: 1000,
		GlobalWindow:      10 * time.Minute,
		GlobalLock:        time.Minute,
		MaxTrackedIPs:     100,
		StatePath:         filepath.Join(dir, "state.json"),
	}
}

func TestLocksAfterThresholdAndBacksOff(t *testing.T) {
	g := New(testOptions(t.TempDir()))
	defer g.Close()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }

	const ip = "203.0.113.7"
	if d := g.Check(ip); d.Blocked {
		t.Fatalf("IP must not be blocked initially")
	}
	for i := 0; i < 2; i++ {
		if d := g.Failure(ip); d.Blocked {
			t.Fatalf("unexpected block after %d failures", i+1)
		}
	}
	d := g.Failure(ip)
	if !d.Blocked {
		t.Fatalf("expected lock after 3 failures")
	}
	if d.RetryAfter != time.Minute {
		t.Fatalf("unexpected lock duration: %s", d.RetryAfter)
	}
	if got := g.Check(ip); !got.Blocked {
		t.Fatalf("IP should be locked")
	}

	// lock expires, but the next relapse doubles the duration
	now = now.Add(time.Minute + time.Second)
	if got := g.Check(ip); got.Blocked {
		t.Fatalf("lock should have expired")
	}
	for i := 0; i < 3; i++ {
		g.Failure(ip)
	}
	if got := g.Check(ip); !got.Blocked {
		t.Fatalf("second lock expected")
	}
	if got := g.Check(ip); got.RetryAfter <= time.Minute {
		t.Fatalf("lock duration must grow, got %s", got.RetryAfter)
	}
}

func TestSuccessClearsFailures(t *testing.T) {
	g := New(testOptions(t.TempDir()))
	defer g.Close()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }

	const ip = "203.0.113.8"
	g.Failure(ip)
	g.Failure(ip)
	g.Success(ip)
	g.Failure(ip)
	g.Failure(ip)
	if d := g.Check(ip); d.Blocked {
		t.Fatalf("success must reset the failure window")
	}
}

func TestLoopbackIsNeverLimited(t *testing.T) {
	g := New(testOptions(t.TempDir()))
	defer g.Close()
	for i := 0; i < 100; i++ {
		g.Failure("127.0.0.1")
	}
	if d := g.Check("127.0.0.1"); d.Blocked {
		t.Fatalf("loopback must stay usable for the administrator")
	}
}

func TestGlobalCircuitBreaker(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.GlobalMaxFailures = 4
	g := New(opts)
	defer g.Close()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }

	// one failure each from four different IPs
	for i := 0; i < 4; i++ {
		ip := "198.51.100." + string(rune('1'+i))
		g.Failure(ip)
	}
	if d := g.Check("192.0.2.1"); !d.Blocked {
		t.Fatalf("global breaker should block new attempts")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	g := New(opts)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	const ip = "203.0.113.9"
	for i := 0; i < 3; i++ {
		g.Failure(ip)
	}
	g.Close()

	if _, err := os.Stat(opts.StatePath); err != nil {
		t.Fatalf("state file missing: %v", err)
	}

	g2 := New(opts)
	defer g2.Close()
	g2.now = func() time.Time { return now }
	if d := g2.Check(ip); !d.Blocked {
		t.Fatalf("lockout must survive a panel restart")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Second:  "5秒",
		15 * time.Minute: "15分钟",
		2 * time.Hour:    "2小时",
		48 * time.Hour:   "2天",
	}
	for d, want := range cases {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
