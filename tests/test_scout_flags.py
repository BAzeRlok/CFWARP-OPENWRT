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

REGISTRATION_TESTS = r'''
package main

import (
    "context"
    "crypto/x509"
    "errors"
    "io"
    "net/http"
    "net/url"
    "strings"
    "testing"
)

const regBaseURL = "https://api.example.invalid/reg"
type account struct { ID string }
var mint func(context.Context, *http.Client, account) (account, error)
func mintAccount(ctx context.Context, c *http.Client, a account) (account, error) { return mint(ctx, c, a) }
type roundTrip func(*http.Request) (*http.Response, error)
func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkFailureSelectsAnotherPath(t *testing.T) {
    for _, failureAt := range []string{"probe", "registration"} {
        t.Run(failureAt, func(t *testing.T) {
            selected, closed, minted := 0, 0, 0
            next := func(context.Context) (registrationPath, error) {
                selected++
                index := selected
                return registrationPath{name:"endpoint", close:func(){closed++}, client:&http.Client{
                    Transport:roundTrip(func(r *http.Request)(*http.Response,error){
                        if r.Method != "GET" || r.URL.String()!=regBaseURL { t.Fatal("probe must not register a device") }
                        if failureAt=="probe" && index==1 { return nil, context.DeadlineExceeded }
                        return &http.Response{StatusCode:405, Body:io.NopCloser(strings.NewReader("method not allowed"))},nil
                    }),
                }},nil
            }
            mint = func(context.Context,*http.Client,account)(account,error){
                minted++
                if failureAt=="registration" && minted==1 { return account{}, &url.Error{Op:"Post",URL:regBaseURL,Err:context.DeadlineExceeded} }
                return account{ID:"success"},nil
            }
            got,err := registerFromPaths(context.Background(), next, account{})
            if err!=nil || got.ID!="success" || selected!=2 || closed!=2 { t.Fatalf("result=%+v err=%v selected=%d closed=%d",got,err,selected,closed) }
            if failureAt=="probe" && minted!=1 { t.Fatal("failed HTTPS probe must not create account") }
        })
    }
}

func TestRegistrationRetryIsBounded(t *testing.T) {
    selected,closed:=0,0
    next:=func(context.Context)(registrationPath,error){
        selected++
        return registrationPath{name:"endpoint",close:func(){closed++},client:&http.Client{Transport:roundTrip(func(*http.Request)(*http.Response,error){return nil,context.DeadlineExceeded})}},nil
    }
    mint=func(context.Context,*http.Client,account)(account,error){t.Fatal("probe failed: registration must not be sent");return account{},nil}
    _,err:=registerFromPaths(context.Background(),next,account{})
    if err==nil || selected!=registrationMaxPaths || closed!=selected { t.Fatalf("err=%v selected=%d closed=%d",err,selected,closed) }
}

func TestRefusalAndPartialAccountAreNotRetried(t *testing.T) {
    for _,id:=range []string{"","already-created"}{
        selected,closed:=0,0
        next:=func(context.Context)(registrationPath,error){
            selected++
            return registrationPath{name:"endpoint",close:func(){closed++},client:&http.Client{Transport:roundTrip(func(*http.Request)(*http.Response,error){return &http.Response{StatusCode:405,Body:io.NopCloser(strings.NewReader(""))},nil})}},nil
        }
        mint=func(context.Context,*http.Client,account)(account,error){
            if id!="" {return account{ID:id},context.DeadlineExceeded}
            return account{},errors.New("API refused registration")
        }
        got,err:=registerFromPaths(context.Background(),next,account{})
        if err==nil || got.ID!=id || selected!=1 || closed!=1 {t.Fatalf("id=%q result=%+v err=%v selected=%d closed=%d",id,got,err,selected,closed)}
    }
}

func TestCancellationAndEmptyDiscovery(t *testing.T) {
    ctx,cancel:=context.WithCancel(context.Background());cancel()
    _,err:=registerFromPaths(ctx,func(context.Context)(registrationPath,error){t.Fatal("cancelled discovery ran");return registrationPath{},nil},account{})
    if !errors.Is(err,context.Canceled){t.Fatal(err)}
    _,err=registerFromPaths(context.Background(),func(context.Context)(registrationPath,error){return registrationPath{},io.EOF},account{})
    if !errors.Is(err,errRegistrationNoEndpoint)||!registrationNetworkError(err){t.Fatal(err)}
}

func TestCertificateErrorsAreNotRetried(t *testing.T) {
    err:=&url.Error{Op:"Get",URL:regBaseURL,Err:x509.UnknownAuthorityError{}}
    if registrationNetworkError(err){t.Fatal("certificate validation failure classified as retryable network error")}
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

    def test_automatic_registration_masking_host(self):
        result = self.invoke("register", "--registration-sni", "ozon.ru")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)["generated_i1"])

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

    def test_registration_path_failover(self):
        directory = pathlib.Path(self.directory.name)
        patch = (PATCHES / "130-registration-paths.patch").read_text()
        section = re.search(
            r"^--- /dev/null\n\+\+\+ b/register_paths\.go\n.*?(?=^--- |\Z)",
            patch, flags=re.M | re.S,
        )
        self.assertIsNotNone(section)
        subprocess.run(
            ["patch", "--batch", "--fuzz=0", "-p1"], input=section.group(),
            text=True, cwd=directory, check=True, capture_output=True,
        )
        (directory / "register_paths_test.go").write_text(REGISTRATION_TESTS)
        env = os.environ.copy()
        env.update({"GO111MODULE": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOCACHE": str(directory / "go-cache")})
        env.pop("GOOS", None)
        env.pop("GOARCH", None)
        subprocess.run(
            ["go", "test", "register_paths.go", "register_paths_test.go"],
            cwd=directory, env=env, check=True, timeout=120,
        )


if __name__ == "__main__":
    unittest.main()
