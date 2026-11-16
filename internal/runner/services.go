package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/anshul-kh/grid-cli/internal/spec"
)

// serviceProbeInterval is the delay between readiness-probe attempts: fast
// enough that a service which starts almost instantly isn't held up by it,
// slow enough that polling a slow-starting one doesn't spin a CPU.
const serviceProbeInterval = 100 * time.Millisecond

// Service is one running §4 service process: started before checks, probed
// for readiness, and stopped after — always, in reverse start order
// (TASK.md §4 Services).
type Service struct {
	ID  string
	cmd *exec.Cmd
}

// StartServices starts every spec.Service in env.Workdir, in order, probing
// each one's readiness (tcp dial or http GET until 2xx) within its own
// timeout before moving on to the next. A start or readiness failure stops
// everything already started, in reverse order, and returns an error — the
// whole run becomes a setup error (GRID_SPEC §4 / TASK.md §4: "On readiness
// failure the whole run is a setup error (INVALID), not a fail").
func StartServices(ctx context.Context, env *Env, specs []spec.Service) ([]*Service, error) {
	started := make([]*Service, 0, len(specs))
	for _, s := range specs {
		svc, err := startOneService(env, s)
		if err != nil {
			StopServices(started)
			return nil, fmt.Errorf("starting service %q: %w", s.ID, err)
		}
		started = append(started, svc)
		if err := probeReady(ctx, s.Ready); err != nil {
			StopServices(started)
			return nil, fmt.Errorf("service %q did not become ready: %w", s.ID, err)
		}
	}
	return started, nil
}

// StopServices stops every service in REVERSE start order — always, even
// when some of them never became ready — so a later service's dependency on
// an earlier one (e.g. a proxy in front of a database) is torn down in the
// opposite order it was brought up. Safe to call with a nil or partial
// slice; callers defer it unconditionally, including on panic and signal
// cancellation, so no service ever outlives its run.
func StopServices(services []*Service) {
	for i := len(services) - 1; i >= 0; i-- {
		services[i].stop()
	}
}

// startOneService launches a service's argv in env.Workdir with the run's
// environment, as its own process-group leader (the same setSysProcAttr
// exec.go's Run uses) so it can be killed as a whole group later. Output is
// captured into capped buffers only for --verbose diagnostics on a later
// failure; a service is not itself evidence.
func startOneService(env *Env, s spec.Service) (*Service, error) {
	if len(s.Run) == 0 {
		return nil, fmt.Errorf("service has an empty run command")
	}
	cmd := exec.Command(s.Run[0], s.Run[1:]...)
	cmd.Dir = env.Workdir
	cmd.Env = env.Environ(nil)
	setSysProcAttr(cmd)
	cmd.Stdout = newCappedBuffer(DefaultCaptureCap)
	cmd.Stderr = newCappedBuffer(DefaultCaptureCap)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Service{ID: s.ID, cmd: cmd}, nil
}

// stopObserved, when non-nil, is called with a service's ID right after its
// stop() completes, in the order services are actually stopped. It is a
// test seam only — StopServices's own logic never reads it, and it costs
// nothing when unset — because the actual stop order is otherwise
// unobservable from outside the package: the hard SIGKILL used below leaves
// nothing for a stopped process to report about itself. Mirrors the existing
// test-injection pattern (api.WithSleeper, Env.Warnf).
var stopObserved func(id string)

// stop kills the service's whole process group and reaps it. Like exec.go's
// timeout path, this is a hard kill (SIGKILL / taskkill /T /F) rather than a
// polite SIGTERM-then-wait: a service that ignores a polite signal must not
// survive the run either (TASK.md §8's orphan-process guarantee applies to
// services exactly as it does to checks).
func (s *Service) stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = killGroup(s.cmd.Process.Pid)
	_ = s.cmd.Wait()
	if stopObserved != nil {
		stopObserved(s.ID)
	}
}

// probeReady polls a service's readiness probe (tcp dial or http GET until
// 2xx) until it succeeds or its own timeout elapses, defaulting to
// DefaultCheckTimeout when unset. No probe configured (neither tcp nor http)
// means the service is considered ready as soon as it starts.
func probeReady(ctx context.Context, r spec.ServiceReady) error {
	if r.TCP == "" && r.HTTP == "" {
		return nil
	}
	timeout := r.Timeout.D()
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		var err error
		if r.TCP != "" {
			err = probeTCPOnce(pctx, r.TCP)
		} else {
			err = probeHTTPOnce(pctx, r.HTTP)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-pctx.Done():
			if lastErr == nil {
				lastErr = pctx.Err()
			}
			return fmt.Errorf("not ready after %s: %w", timeout, lastErr)
		case <-time.After(serviceProbeInterval):
		}
	}
}

// probeTCPOnce reports whether addr accepts a TCP connection right now.
func probeTCPOnce(ctx context.Context, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// probeHTTPOnce reports whether url answers with a 2xx status right now.
func probeHTTPOnce(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}
