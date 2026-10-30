#!/usr/bin/python3
"""Run the packaged scanner's real flag setup without a network or Go modules.

mini_flags.go is a new file supplied by our source patches. Apply its patch
sections in order, then compile it with inert definitions for unrelated scanner
functions. In particular, no registration request or tunnel can be created here.
"""

import json
import os
import pathlib
import re
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
PATCHES = ROOT / "warp-warpscout/patches"

HARNESS = r'''
package main

import (
    "encoding/json"
    "flag"
    "net/netip"
    "os"
)

// Dependencies outside mini_flags.go. The real setup and common validation
// below are taken verbatim from the package patches, not duplicated here.
const (
    defaultAccount = "warpscout-account.json"
    protoAWG = "awg"
    registerSample = 5
    confStdout = "-"
    minDurabilityPings = 2
    i1Keyword = "none"
    mtuMin = 576
    mtuMax = 1500
    junkCountLimitMin = 1
    junkCountLimitMax = 128
    tunnelMTU = 1280
)

var (
    awgJc = 6
    awgJmin = 10
    awgJmax = 50
    awgI1 string
    pingTarget string
    generatedI1 bool
)

type command struct { name, brief string }

func regenI1(o options) error {
    generatedI1 = true
    return nil
}

func parseTargets(s string) ([]netip.Prefix, error) {
    prefix, err := netip.ParsePrefix(s)
    return []netip.Prefix{prefix}, err
}

func main() {
    fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
    var opts options
    switch os.Args[1] {
    case "register":
        setupRegisterFlags(fs, &opts)
    case "scan":
        setupScanFlags(fs, &opts)
    default:
        panic("unknown test command")
    }
    fs.Parse(os.Args[2:])
    beforeJobs, beforeSample := opts.tunnelParallel, opts.perSubnet
    applyCommonFlags(fs, &opts)
    json.NewEncoder(os.Stdout).Encode(map[string]any{
        "before_jobs": beforeJobs,
        "before_sample": beforeSample,
        "jobs": opts.tunnelParallel,
        "sample": opts.perSubnet,
        "proto": opts.proto,
        "generated_i1": generatedI1,
        "account": opts.accountPath,
        "interface": opts.iface,
    })
}
'''


class ScoutFlagTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="warp-scout-flags-")
        cls.addClassCleanup(cls.directory.cleanup)
        directory = pathlib.Path(cls.directory.name)
        for path in sorted(PATCHES.glob("*.patch")):
            sections = re.findall(
                r"^--- (?:a/mini_flags\.go|/dev/null)\n\+\+\+ b/mini_flags\.go\n"
                r".*?(?=^diff --git |^--- |\Z)", path.read_text(), flags=re.M | re.S,
            )
            for section in sections:
                subprocess.run(
                    ["patch", "--batch", "--fuzz=0", "-p1"],
                    input=section, text=True, cwd=directory, check=True,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                )
        if not (directory / "mini_flags.go").is_file():
            raise RuntimeError("package patches did not produce mini_flags.go")
        (directory / "harness.go").write_text(HARNESS)
        cls.binary = directory / "scout-flags"
        env = os.environ.copy()
        env.update({
            "GO111MODULE": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off",
            "GOSUMDB": "off", "GOCACHE": str(directory / "go-cache"),
        })
        # Always execute native host code, even in a cross-compilation shell.
        env.pop("GOOS", None)
        env.pop("GOARCH", None)
        subprocess.run(
            ["go", "build", "-o", str(cls.binary), "harness.go", "mini_flags.go"],
            cwd=directory, env=env, check=True, timeout=120,
        )

    def invoke(self, *args):
        return subprocess.run(
            [str(self.binary), *args], capture_output=True, text=True, timeout=5,
        )

    def test_registration_defaults_and_manager_retry(self):
        for extra, generated in [([], False), (["--gen-i1", "quic", "--i1-sni", "ozon.ru"], True)]:
            with self.subTest(generated_i1=generated):
                result = self.invoke(
                    "register", "--plain", "--relay", "none", "--account",
                    "/not-created/account.json", "--interface", "wan", *extra,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                parsed = json.loads(result.stdout)
                self.assertGreaterEqual(parsed["before_jobs"], 1)
                self.assertGreaterEqual(parsed["before_sample"], 1)
                self.assertEqual(parsed["jobs"], parsed["before_jobs"])
                self.assertEqual(parsed["proto"], "awg")
                self.assertEqual(parsed["generated_i1"], generated)
                self.assertEqual(parsed["account"], "/not-created/account.json")
                self.assertEqual(parsed["interface"], "wan")

    def test_scan_defaults_remain_valid(self):
        result = self.invoke("scan")
        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = json.loads(result.stdout)
        self.assertGreaterEqual(parsed["jobs"], 1)
        self.assertGreaterEqual(parsed["sample"], 1)

    def test_invalid_scan_counts_still_rejected(self):
        for option in ("--sample", "--tunnel-jobs"):
            for count in ("0", "-1"):
                with self.subTest(option=option, count=count):
                    result = self.invoke("scan", option, count)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn("-sample and -tunnel-jobs must be at least 1", result.stderr)


if __name__ == "__main__":
    unittest.main()
