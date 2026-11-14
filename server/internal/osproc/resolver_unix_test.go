//go:build unix

package osproc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc/osproctest"
)

// resolverHelperEnv runs the test binary as a fresh process whose resolver has not been used yet.
const resolverHelperEnv = "PERSONA_TEST_RESOLVER_HELPER"

const resolverHelperPrefix = "resolver-helper: "
const resolverTimingPrefix = "resolver-timing-ns: "

// resolverHelperResult records the system resolver's descriptor through sealing, lookups, a loopback dial and children.
type resolverHelperResult struct {
	Before      osproctest.Report `json:"before"`
	Sealed      osproctest.Report `json:"sealed"`
	AfterLookup osproctest.Report `json:"after_lookup"`
	FirstChild  osproctest.Report `json:"first_child"`
	AfterUse    osproctest.Report `json:"after_use"`
	SecondChild osproctest.Report `json:"second_child"`
}

func resolverHelper(mode string) int {
	if mode == "timing" {
		start := time.Now()
		warmResolver()
		fmt.Printf("%s%d\n", resolverTimingPrefix, time.Since(start).Nanoseconds())
		return 0
	}
	if mode != "check" {
		fmt.Fprintf(os.Stderr, "unknown resolver helper mode %q\n", mode)
		return 1
	}
	res, err := checkResolverInheritance()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, _ := json.Marshal(res)
	fmt.Printf("%s%s\n", resolverHelperPrefix, b)
	return 0
}

func checkResolverInheritance() (resolverHelperResult, error) {
	var res resolverHelperResult
	res.Before = osproctest.Describe()
	reliableResolverWarmup()
	SealInherited()
	res.Sealed = osproctest.Describe()
	if _, err := net.LookupHost("localhost"); err != nil {
		return res, fmt.Errorf("first localhost lookup: %w", err)
	}
	res.AfterLookup = osproctest.Describe()
	var err error
	if res.FirstChild, err = osproctest.Run(); err != nil {
		return res, err
	}
	// Both names are in the local hosts database on macOS; the checks need no DNS server or internet connection.
	for _, host := range []string{"broadcasthost", "localhost"} {
		_, _ = net.LookupHost(host)
	}
	if err := dialLoopback(); err != nil {
		return res, err
	}
	res.AfterUse = osproctest.Describe()
	if res.SecondChild, err = osproctest.Run(); err != nil {
		return res, err
	}
	return res, nil
}

func dialLoopback() error {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("loopback listener: %w", err)
	}
	defer ln.Close()
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	client, err := net.DialTimeout("tcp4", ln.Addr().String(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("loopback dial: %w", err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("loopback accept: %w", err)
	}
	return server.Close()
}

func runResolverHelper(t *testing.T, resolver string) resolverHelperResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), resolverHelperEnv+"=check", "GODEBUG=netdns="+resolver)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s resolver helper: %v", resolver, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, resolverHelperPrefix); ok {
			var res resolverHelperResult
			if err := json.Unmarshal([]byte(rest), &res); err != nil {
				t.Fatal(err)
			}
			return res
		}
	}
	t.Fatalf("%s resolver helper: no result in %q", resolver, out)
	return resolverHelperResult{}
}
