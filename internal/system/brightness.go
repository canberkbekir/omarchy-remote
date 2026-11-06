package system

import (
	"errors"
	"time"
)

// Some monitors drop DDC/CI requests now and then, so brightness reads and
// writes are tried a few times, and a write counts only once it reads back.
// Only monitors that have answered before are retried: one that never has,
// like a TV without DDC/CI, gets a single try, so it stays quick to skip.
// Only the read and write primitives differ per OS (see system_linux.go and
// brightness_windows.go); they are variables so tests can stand in for a monitor.
var (
	readMonitorBrightness  = platformReadBrightness
	writeMonitorBrightness = platformWriteBrightness
	forgetMonitor          = platformForgetMonitor
	BrightnessAttempts     = 3
	BrightnessPause        = 200 * time.Millisecond
)

// errNoRetry marks a primitive's failure that another try won't fix, like
// a slow WMI query on Windows: retries are for monitors that drop DDC/CI
// requests, and each retry holds the server's ddc lock.
var errNoRetry = errors.New("not worth another try")

// retryBrightness waits before another attempt and lets the platform drop
// anything it remembered about the monitor failing.
func retryBrightness(name string) {
	time.Sleep(BrightnessPause)
	forgetMonitor(name)
}

// Brightness reads and writes monitor brightness with retries. It remembers
// which monitors have answered; callers make one call at a time (the server
// holds its ddc lock).
type Brightness struct {
	answered map[string]bool
}

// attempts says how many tries a monitor gets.
func (a *Brightness) attempts(name string) int {
	if a.answered[name] {
		return BrightnessAttempts
	}
	return 1
}

// readBrightnessOnce asks the monitor once; the error, if any, says whether
// to try again.
func (a *Brightness) readOnce(name string) (*int, error) {
	value, err := readMonitorBrightness(name)
	if err != nil {
		return nil, err
	}
	if value < 0 || value > 100 {
		return nil, nil
	}
	if a.answered == nil {
		a.answered = map[string]bool{}
	}
	a.answered[name] = true
	return &value, nil
}

// Read returns nil when the monitor doesn't answer.
func (a *Brightness) Read(name string) *int {
	for attempt := range a.attempts(name) {
		if attempt > 0 {
			retryBrightness(name)
		}
		value, err := a.readOnce(name)
		if value != nil {
			return value
		}
		if errors.Is(err, errNoRetry) {
			break
		}
	}
	// Gone quiet, perhaps unplugged or swapped: one try until it answers again.
	delete(a.answered, name)
	return nil
}

// Set reports whether the monitor took the brightness.
func (a *Brightness) Set(name string, target int) bool {
	var previous *int
	for attempt := range a.attempts(name) {
		if attempt > 0 {
			retryBrightness(name)
		}
		if err := writeMonitorBrightness(name, target); err != nil {
			if errors.Is(err, errNoRetry) {
				return false
			}
			previous = nil
			continue
		}
		got, err := a.readOnce(name)
		if got == nil {
			if errors.Is(err, errNoRetry) {
				return false
			}
			previous = nil
			continue
		}
		if max(*got-target, target-*got) <= 1 {
			return true
		}
		// A display with few brightness steps lands on the nearest one; two
		// writes in a row landing on the same value mean that's as close as it goes.
		if previous != nil && *previous == *got {
			return true
		}
		previous = got
	}
	return false
}
