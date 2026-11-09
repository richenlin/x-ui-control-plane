// Package guard implements a small, self-contained defence against credential
// stuffing / brute force attacks on the x-ui login form.
//
// It keeps a sliding window of failed attempts per client IP, locks an IP out
// with exponentially growing lock durations, and additionally applies a global
// circuit breaker for distributed attacks. Loopback clients are never limited so
// that the administrator can always get back in over an SSH tunnel.
//
// State survives restarts: it is persisted as JSON next to the panel database.
package guard

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"x-ui/logger"
)

const stateVersion = 1

// Options configures a Guard. All durations are wall clock durations.
type Options struct {
	// MaxFailures failed attempts from one IP inside Window lock that IP.
	MaxFailures int
	// Window is the sliding window used to count failures.
	Window time.Duration
	// BaseLock is the first lock duration; it doubles on every relapse.
	BaseLock time.Duration
	// MaxLock caps the exponential growth.
	MaxLock time.Duration
	// StrikeReset forgets previous strikes after this much quiet time.
	StrikeReset time.Duration

	// GlobalMaxFailures failures from any IP inside GlobalWindow trip the
	// global circuit breaker (protects against distributed brute force).
	GlobalMaxFailures int
	GlobalWindow      time.Duration
	GlobalLock        time.Duration

	// MaxTrackedIPs bounds the memory/state size under a distributed flood.
	MaxTrackedIPs int
	// StatePath is the JSON state file. Empty disables persistence.
	StatePath string
}

// DefaultOptions returns the hardened defaults (overridable through env vars):
//
//	XUI_LOGIN_MAX_FAILS      (default 5)
//	XUI_LOGIN_WINDOW         (default 10m)
//	XUI_LOGIN_LOCK           (default 15m)
//	XUI_LOGIN_MAX_LOCK       (default 24h)
//	XUI_LOGIN_GLOBAL_FAILS   (default 100)
//	XUI_LOGIN_GLOBAL_WINDOW  (default 10m)
//	XUI_LOGIN_GLOBAL_LOCK    (default 15m)
func DefaultOptions(statePath string) Options {
	return Options{
		MaxFailures:       envInt("XUI_LOGIN_MAX_FAILS", 5),
		Window:            envDuration("XUI_LOGIN_WINDOW", 10*time.Minute),
		BaseLock:          envDuration("XUI_LOGIN_LOCK", 15*time.Minute),
		MaxLock:           envDuration("XUI_LOGIN_MAX_LOCK", 24*time.Hour),
		StrikeReset:       24 * time.Hour,
		GlobalMaxFailures: envInt("XUI_LOGIN_GLOBAL_FAILS", 100),
		GlobalWindow:      envDuration("XUI_LOGIN_GLOBAL_WINDOW", 10*time.Minute),
		GlobalLock:        envDuration("XUI_LOGIN_GLOBAL_LOCK", 15*time.Minute),
		MaxTrackedIPs:     10000,
		StatePath:         statePath,
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		logger.Warningf("login guard: invalid %s=%q, using default %d", key, v, def)
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		logger.Warningf("login guard: invalid %s=%q, using default %s", key, v, def)
	}
	return def
}

// Decision is the outcome of a guard consultation.
type Decision struct {
	Blocked    bool
	RetryAfter time.Duration
	Message    string
}

type ipState struct {
	// Failures holds unix timestamps of failed attempts inside the window.
	Failures []int64 `json:"f"`
	// Until is the unix timestamp until which the IP is locked.
	Until int64 `json:"u"`
	// Strikes counts how often this IP has been locked; drives backoff.
	Strikes int `json:"s"`
	// Last is the unix timestamp of the last failure/lock.
	Last int64 `json:"l"`
}

type persistentState struct {
	Version     int                 `json:"version"`
	IPs         map[string]*ipState `json:"ips"`
	GlobalFails []int64             `json:"global_fails"`
	GlobalUntil int64               `json:"global_until"`
}

// Guard is safe for concurrent use.
type Guard struct {
	opts Options

	mu          sync.Mutex
	ips         map[string]*ipState
	globalFails []int64
	globalUntil int64
	dirty       bool
	lastWrite   time.Time

	now func() time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
}

// New creates a Guard and loads persisted state (if any).
func New(opts Options) *Guard {
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = 5
	}
	if opts.Window <= 0 {
		opts.Window = 10 * time.Minute
	}
	if opts.BaseLock <= 0 {
		opts.BaseLock = 15 * time.Minute
	}
	if opts.MaxLock < opts.BaseLock {
		opts.MaxLock = 24 * time.Hour
	}
	if opts.GlobalMaxFailures <= 0 {
		opts.GlobalMaxFailures = 100
	}
	if opts.GlobalWindow <= 0 {
		opts.GlobalWindow = 10 * time.Minute
	}
	if opts.GlobalLock <= 0 {
		opts.GlobalLock = 15 * time.Minute
	}
	if opts.MaxTrackedIPs <= 0 {
		opts.MaxTrackedIPs = 10000
	}
	g := &Guard{
		opts:   opts,
		ips:    make(map[string]*ipState),
		now:    time.Now,
		stopCh: make(chan struct{}),
	}
	g.load()
	go g.flushLoop()
	return g
}

// Close stops the background flusher and writes the final state.
func (g *Guard) Close() {
	g.stopOnce.Do(func() { close(g.stopCh) })
	g.mu.Lock()
	defer g.mu.Unlock()
	g.persistLocked(true)
}

// Check reports whether ip is currently allowed to attempt a login.
func (g *Guard) Check(ip string) Decision {
	if isLocal(ip) {
		return Decision{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if now.Unix() < g.globalUntil {
		return Decision{
			Blocked:    true,
			RetryAfter: time.Unix(g.globalUntil, 0).Sub(now),
			Message:    "检测到大规模登录尝试，登录功能已临时暂停，请稍后重试",
		}
	}
	if st, ok := g.ips[ip]; ok && now.Unix() < st.Until {
		return Decision{
			Blocked:    true,
			RetryAfter: time.Unix(st.Until, 0).Sub(now),
			Message:    "登录失败次数过多，请稍后重试",
		}
	}
	return Decision{}
}

// Failure records a failed login for ip and reports whether the caller is now
// locked out.
func (g *Guard) Failure(ip string) Decision {
	if isLocal(ip) {
		return Decision{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	st, ok := g.ips[ip]
	if !ok {
		st = &ipState{}
		g.ips[ip] = st
	}
	// forget ancient strikes so that an occasional typo does not keep an IP
	// locked out forever
	if st.Last > 0 && now.Sub(time.Unix(st.Last, 0)) > g.opts.StrikeReset {
		st.Strikes = 0
		st.Until = 0
	}
	st.Failures = pruneOlderThan(st.Failures, now.Add(-g.opts.Window).Unix())
	st.Failures = append(st.Failures, now.Unix())
	st.Last = now.Unix()

	dec := Decision{}
	if len(st.Failures) >= g.opts.MaxFailures {
		if st.Strikes < 16 {
			st.Strikes++
		}
		lock := g.lockFor(st.Strikes)
		st.Until = now.Add(lock).Unix()
		st.Failures = nil
		dec = Decision{
			Blocked:    true,
			RetryAfter: lock,
			Message:    "登录失败次数过多，请稍后重试",
		}
		logger.Warningf("login guard: locked ip %s for %s (strikes=%d)", ip, lock, st.Strikes)
	}

	g.globalFails = pruneOlderThan(g.globalFails, now.Add(-g.opts.GlobalWindow).Unix())
	g.globalFails = append(g.globalFails, now.Unix())
	if len(g.globalFails) >= g.opts.GlobalMaxFailures {
		g.globalUntil = now.Add(g.opts.GlobalLock).Unix()
		dec = Decision{
			Blocked:    true,
			RetryAfter: g.opts.GlobalLock,
			Message:    "检测到大规模登录尝试，登录功能已临时暂停，请稍后重试",
		}
		logger.Warningf("login guard: GLOBAL circuit breaker tripped for %s (%d failures in %s)",
			g.opts.GlobalLock, len(g.globalFails), g.opts.GlobalWindow)
		g.globalFails = nil
	}

	g.evictLocked(now)
	g.persistLocked(dec.Blocked)
	return dec
}

// Success clears the failure history of ip (called after a valid login).
func (g *Guard) Success(ip string) {
	if isLocal(ip) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.ips[ip]; ok {
		delete(g.ips, ip)
		g.persistLocked(false)
	}
}

// lockFor returns the lock duration for the given strike count.
func (g *Guard) lockFor(strikes int) time.Duration {
	if strikes < 1 {
		strikes = 1
	}
	lock := g.opts.BaseLock
	for i := 1; i < strikes; i++ {
		lock *= 2
		if lock >= g.opts.MaxLock {
			return g.opts.MaxLock
		}
	}
	if lock > g.opts.MaxLock {
		lock = g.opts.MaxLock
	}
	return lock
}

func (g *Guard) evictLocked(now time.Time) {
	// drop stale entries
	for ip, st := range g.ips {
		if now.Unix() >= st.Until && now.Sub(time.Unix(st.Last, 0)) > g.opts.StrikeReset {
			delete(g.ips, ip)
		}
	}
	if len(g.ips) <= g.opts.MaxTrackedIPs {
		return
	}
	// too many tracked IPs: keep the most recently active ones
	type entry struct {
		ip   string
		last int64
	}
	entries := make([]entry, 0, len(g.ips))
	for ip, st := range g.ips {
		entries = append(entries, entry{ip, st.Last})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].last > entries[j].last })
	for i := g.opts.MaxTrackedIPs; i < len(entries); i++ {
		delete(g.ips, entries[i].ip)
	}
}

func (g *Guard) load() {
	if g.opts.StatePath == "" {
		return
	}
	data, err := os.ReadFile(g.opts.StatePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warningf("login guard: cannot read state: %v", err)
		}
		return
	}
	var st persistentState
	if err := json.Unmarshal(data, &st); err != nil {
		logger.Warningf("login guard: cannot parse state, starting fresh: %v", err)
		return
	}
	if st.Version != stateVersion {
		return
	}
	if st.IPs != nil {
		g.ips = st.IPs
	}
	g.globalFails = st.GlobalFails
	g.globalUntil = st.GlobalUntil
}

func (g *Guard) persistLocked(force bool) {
	if g.opts.StatePath == "" {
		return
	}
	now := g.now()
	if !force && now.Sub(g.lastWrite) < time.Second {
		g.dirty = true
		return
	}
	g.dirty = false
	g.lastWrite = now

	st := persistentState{
		Version:     stateVersion,
		IPs:         g.ips,
		GlobalFails: g.globalFails,
		GlobalUntil: g.globalUntil,
	}
	data, err := json.Marshal(st)
	if err != nil {
		logger.Warningf("login guard: cannot marshal state: %v", err)
		return
	}
	dir := filepath.Dir(g.opts.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warningf("login guard: cannot create state dir: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".login_guard-*")
	if err != nil {
		logger.Warningf("login guard: cannot create temp state: %v", err)
		return
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, g.opts.StatePath)
	}
	if err != nil {
		_ = os.Remove(tmpName)
		logger.Warningf("login guard: cannot write state: %v", err)
	}
}

func (g *Guard) flushLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-t.C:
			g.mu.Lock()
			if g.dirty {
				g.persistLocked(true)
			}
			g.mu.Unlock()
		}
	}
}

func pruneOlderThan(ts []int64, cutoff int64) []int64 {
	i := 0
	for i < len(ts) && ts[i] < cutoff {
		i++
	}
	if i == 0 {
		return ts
	}
	return append([]int64(nil), ts[i:]...)
}

// isLocal reports whether ip belongs to the loopback interface. Such clients are
// exempt from rate limiting so the administrator can always reach the panel via
// an SSH tunnel, even during an attack.
func isLocal(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

// formatDuration renders a duration the way a human expects in a UI message.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		s := int(d.Seconds())
		if s < 1 {
			s = 1
		}
		return strconv.Itoa(s) + "秒"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes()+0.5)) + "分钟"
	}
	if d < 24*time.Hour {
		return strconv.Itoa(int(d.Hours()+0.5)) + "小时"
	}
	return strconv.Itoa(int(d.Hours()/24+0.5)) + "天"
}
