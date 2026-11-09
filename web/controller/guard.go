package controller

import (
	"path/filepath"
	"sync"

	"x-ui/config"
	"x-ui/logger"
	"x-ui/web/guard"
)

var (
	loginGuardOnce sync.Once
	loginGuard     *guard.Guard
)

// getLoginGuard returns the process-wide brute force guard, creating it on first
// use. The state file lives next to the panel database so that it survives panel
// restarts (a restart must not reset the counters).
func getLoginGuard() *guard.Guard {
	loginGuardOnce.Do(func() {
		statePath := filepath.Join(filepath.Dir(config.GetDBPath()), ".login_guard.json")
		opts := guard.DefaultOptions(statePath)
		loginGuard = guard.New(opts)
		logger.Infof("login guard enabled: max %d failures / %s per IP, lockout %s..%s, global breaker %d / %s",
			opts.MaxFailures, opts.Window, opts.BaseLock, opts.MaxLock,
			opts.GlobalMaxFailures, opts.GlobalWindow)
	})
	return loginGuard
}
