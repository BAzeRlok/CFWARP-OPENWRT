//go:build warp_windows_helper

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

var errNoVerifiedEndpoint = errors.New("no working endpoints found")

const windowsScanBudget = 180 * time.Second
const windowsProfileBudget = 45 * time.Second

// One device key must not roam between simultaneous probe sockets. Sequential
// probes also make per-endpoint failures reproducible on a Windows desktop.
func runScanCmd(ctx context.Context, opts options) error {
	opts.tunnelParallel = 1
	if !opts.autoMask {
		return runWindowsScanAttempt(ctx, opts)
	}
	auto := opts
	auto.i1Explicit = false
	candidates := regI1Candidates(protoRun{kindAWG, protoAWG}, auto, awgI1, genI1Label)
	return runWindowsProfiles(ctx, opts, candidates, runWindowsScanAttempt)
}

func runWindowsProfiles(ctx context.Context, opts options, candidates []i1Candidate, attempt func(context.Context, options) error) error {
	originalI1, originalLabel := awgI1, genI1Label
	originalPorts := append([]int(nil), warpPorts...)
	defer func() { awgI1, genI1Label, warpPorts = originalI1, originalLabel, originalPorts }()
	ctx, cancel := context.WithTimeout(ctx, windowsScanBudget)
	defer cancel()
	var last error
	for index, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		awgI1, genI1Label = candidate.chain, candidate.label
		warpPorts = append([]int(nil), primaryWarpPorts...)
		metaAddr.Store(nil)
		pingAddr.Store(nil)
		windowsProbeLog.reset()
		fmt.Fprintf(os.Stderr, "\nTunnel profile %d/%d: %s; one worker, 45s search window\n", index+1, len(candidates), i1Note())
		attemptCtx, stop := context.WithTimeout(ctx, windowsProfileBudget)
		err := attempt(attemptCtx, opts)
		timedOut := attemptCtx.Err() != nil
		stop()
		windowsProbeLog.summary(os.Stderr)
		if err == nil {
			if timedOut {
				fmt.Fprintln(os.Stderr, "Search window ended; selecting the best endpoint from completed verification probes")
			}
			fmt.Fprintf(os.Stderr, "Selected verified profile: %s\n", i1Note())
			return nil
		}
		// Never drop explicit geography filters or retry bad accounts/configs.
		if !errors.Is(err, errNoVerifiedEndpoint) && !strings.Contains(err.Error(), "no reachable WARP port") {
			return err
		}
		last = err
		if timedOut {
			fmt.Fprintln(os.Stderr, "Profile search time limit reached; trying another masking profile")
		} else {
			fmt.Fprintln(os.Stderr, "No verified data path; trying another masking profile")
		}
	}
	if ctx.Err() != nil {
		last = ctx.Err()
	}
	return fmt.Errorf("%w after automatic profile checks: %v; see handshake/HTTPS probe failures above", errNoVerifiedEndpoint, last)
}

func windowsEndpointPing(ip netip.Addr, timeout time.Duration, pings int) (time.Duration, bool) {
	if pings > 0 {
		return 0, false // Ranking already uses real in-tunnel latency.
	}
	return pingHost(ip, timeout)
}

type probeLog struct {
	mu       sync.Mutex
	counts   map[string]int
	examples map[string]int
}

var windowsProbeLog probeLog

func (log *probeLog) reset() {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.counts, log.examples = make(map[string]int), make(map[string]int)
}

func (log *probeLog) record(stage, endpoint string, err error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.counts == nil {
		log.counts, log.examples = make(map[string]int), make(map[string]int)
	}
	log.counts[stage]++
	if err != nil && log.examples[stage] < 3 {
		log.examples[stage]++
		fmt.Fprintf(os.Stderr, "  %s %s: %v\n", stage, endpoint, err)
	}
}

func (log *probeLog) summary(output io.Writer) {
	log.mu.Lock()
	defer log.mu.Unlock()
	fmt.Fprintf(output, "Probe results: verified=%d, handshake_failed=%d, https_failed=%d, data_lost=%d, icmp_unavailable=%d, trace_fallback=%d\n",
		log.counts["verified"], log.counts["handshake"], log.counts["https"], log.counts["data_lost"], log.counts["icmp_unavailable"], log.counts["trace_fallback"])
}
