// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package nettrace contains internal hooks for tracing activity in
// the net package. This package is purely internal for use by the
// net/http/httptrace package and has no stable API exposed to end
// users.
// nettrace包含了internal hooks用于追踪net包的事件
// 这个包完全是供net/http/httptrace内部使用而没有向外部用户暴露稳定的API
package nettrace

// TraceKey is a context.Context Value key. Its associated value should
// be a *Trace struct.
// TraceKey是一个context.Context Value的key，它相关的value应该是一个*Trace结构
type TraceKey struct{}

// LookupIPAltResolverKey is a context.Context Value key used by tests to
// specify an alternate resolver func.
// It is not exposed to outsider users. (But see issue 12503)
// The value should be the same type as lookupIP:
//     func lookupIP(ctx context.Context, host string) ([]IPAddr, error)
type LookupIPAltResolverKey struct{}

// Trace contains a set of hooks for tracing events within
// the net package. Any specific hook may be nil.
// Trace包含了一系列的hooks用来追踪net包内的event
// 任何特定的hook都可能为nil
type Trace struct {
	// DNSStart is called with the hostname of a DNS lookup
	// before it begins.
	DNSStart func(name string)

	// DNSDone is called after a DNS lookup completes (or fails).
	// The coalesced parameter is whether singleflight de-dupped
	// the call. The addrs are of type net.IPAddr but can't
	// actually be for circular dependency reasons.
	DNSDone func(netIPs []interface{}, coalesced bool, err error)

	// ConnectStart is called before a Dial, excluding Dials made
	// during DNS lookups. In the case of DualStack (Happy Eyeballs)
	// dialing, this may be called multiple times, from multiple
	// goroutines.
	ConnectStart func(network, addr string)

	// ConnectStart is called after a Dial with the results, excluding
	// Dials made during DNS lookups. It may also be called multiple
	// times, like ConnectStart.
	ConnectDone func(network, addr string, err error)
}
