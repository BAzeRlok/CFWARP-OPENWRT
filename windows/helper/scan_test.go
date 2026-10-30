//go:build warp_windows_helper

package main

import (
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestWindowsAutomaticProfileFailover(t *testing.T) {
	originalChain, originalLabel := awgI1, genI1Label
	candidates := []i1Candidate{{chain: "first", label: "first"}, {chain: "second", label: "second"}}
	for _, failure := range []string{"no data", "ports closed", "explicit filter", "invalid account"} {
		t.Run(failure, func(t *testing.T) {
			attempts := 0
			selected := ""
			err := runWindowsProfiles(context.Background(), options{excludeCountry: "RU"}, candidates, func(ctx context.Context, o options) error {
				attempts++
				if o.excludeCountry != "RU" {
					t.Fatal("explicit filters dropped")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded scan")
				}
				if attempts == 1 {
					switch failure {
					case "no data":
						return errNoVerifiedEndpoint
					case "ports closed":
						return errors.New("no reachable WARP port")
					case "explicit filter":
						return errors.New("every endpoint was excluded by country RU")
					default:
						return errors.New("incomplete account")
					}
				}
				selected = renderConf(options{}, "188.114.96.1:2408", protoRun{kindAWG, protoAWG})
				return nil
			})
			if failure == "no data" || failure == "ports closed" {
				if err != nil || attempts != 2 || !strings.Contains(selected, "I1 = second") {
					t.Fatalf("attempts=%d config=%q err=%v", attempts, selected, err)
				}
			} else if err == nil || attempts != 1 {
				t.Fatalf("unrelated failure retried: %d %v", attempts, err)
			}
			if awgI1 != originalChain || genI1Label != originalLabel {
				t.Fatal("mask globals leaked across scans")
			}
		})
	}
}

func TestWindowsScanCancellationAndExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	candidates := []i1Candidate{{chain: "one"}, {chain: "two"}}
	err := runWindowsProfiles(ctx, options{}, candidates, func(context.Context, options) error { attempts++; return nil })
	if err == nil || attempts != 0 {
		t.Fatal("cancelled search still ran")
	}
	err = runWindowsProfiles(context.Background(), options{}, candidates, func(context.Context, options) error { attempts++; return errNoVerifiedEndpoint })
	if !errors.Is(err, errNoVerifiedEndpoint) || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestWindowsUnknownCountryCannotBypassFilters(t *testing.T) {
	ph := phaseResult{results: []endpointResult{{ok: true, durable: true, exit: metaResult{colo: "ARN", loc: "RU"}}}}
	if len(filterByCountry(ph, []string{"RU"}, false).results) != 0 || len(filterByCountry(ph, []string{"SE"}, true).results) != 0 {
		t.Fatal("unknown node country bypassed a restriction")
	}
	if len(filterByColo(ph, []string{"ARN"}, true).results) != 1 {
		t.Fatal("verified node code lost")
	}
	if len(applyFilters(ph, options{}).results) != 1 {
		t.Fatal("unrestricted search rejected valid trace")
	}
}

func TestWindowsAutomaticMaskFlag(t *testing.T) {
	var o options
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	setupScanFlags(fs, &o)
	if err := fs.Parse([]string{"--auto-mask"}); err != nil || !o.autoMask {
		t.Fatalf("flag: %v %+v", err, o)
	}
}
