// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package httptrace provides mechanisms to trace the events within
// HTTP client requests.
// httptrace包提供机制用于追踪HTTP client requests的事件
package httptrace

import (
	"context"
	"crypto/tls"
	"internal/nettrace"
	"net"
	"reflect"
	"time"
)

// unique type to prevent assignment.
type clientEventContextKey struct{}

// ContextClientTrace returns the ClientTrace associated with the
// provided context. If none, it returns nil.
// ContextClientTrace返回和提供的context相关的ClientTrace，如果没有，则返回nil
func ContextClientTrace(ctx context.Context) *ClientTrace {
	trace, _ := ctx.Value(clientEventContextKey{}).(*ClientTrace)
	return trace
}

// WithClientTrace returns a new context based on the provided parent
// ctx. HTTP client requests made with the returned context will use
// the provided trace hooks, in addition to any previous hooks
// registered with ctx. Any hooks defined in the provided trace will
// be called first.
// WithClientTrace基于提供的parent ctx返回一个新的context，用返回的context
// 创建的HTTP client requests会使用提供的trace hooks，以及任何之前的hooks已经
// 注册的ctx，任何在trace中提供的hooks都会被优先调用
func WithClientTrace(ctx context.Context, trace *ClientTrace) context.Context {
	if trace == nil {
		panic("nil trace")
	}
	old := ContextClientTrace(ctx)
	trace.compose(old)

	// 用context.WithValue存入值，用ctx.Value获取值
	ctx = context.WithValue(ctx, clientEventContextKey{}, trace)
	if trace.hasNetHooks() {
		nt := &nettrace.Trace{
			ConnectStart: trace.ConnectStart,
			ConnectDone:  trace.ConnectDone,
		}
		if trace.DNSStart != nil {
			nt.DNSStart = func(name string) {
				trace.DNSStart(DNSStartInfo{Host: name})
			}
		}
		if trace.DNSDone != nil {
			nt.DNSDone = func(netIPs []interface{}, coalesced bool, err error) {
				addrs := make([]net.IPAddr, len(netIPs))
				for i, ip := range netIPs {
					addrs[i] = ip.(net.IPAddr)
				}
				trace.DNSDone(DNSDoneInfo{
					Addrs:     addrs,
					Coalesced: coalesced,
					Err:       err,
				})
			}
		}
		ctx = context.WithValue(ctx, nettrace.TraceKey{}, nt)
	}
	return ctx
}

// ClientTrace is a set of hooks to run at various stages of an outgoing
// HTTP request. Any particular hook may be nil. Functions may be
// called concurrently from different goroutines and some may be called
// after the request has completed or failed.
// ClientTrace是一系列的hooks，会在一个outgoing HTTP request的各个阶段运行
// 任何特定的hook都可能为nil，函数可能在不同的goroutines中并发运行，并且有些可能
// 在请求完成或失败之后调用
//
// ClientTrace currently traces a single HTTP request & response
// during a single round trip and has no hooks that span a series
// of redirected requests.
// ClientTrace当前仅在单个的round trip中追踪单个的HTTP request和response
// 但是没有hooks横跨一系列的redirected requests
//
// See https://blog.golang.org/http-tracing for more.
type ClientTrace struct {
	// GetConn is called before a connection is created or
	// retrieved from an idle pool. The hostPort is the
	// "host:port" of the target or proxy. GetConn is called even
	// if there's already an idle cached connection available.
	// GetConn在连接创建或者从idle pool中取出之前被调用
	// hostPort是target或者proxy的"host:port"，GetConn会被调用，即使已经
	// 有一个idle cached connection
	GetConn func(hostPort string)

	// GotConn is called after a successful connection is
	// obtained. There is no hook for failure to obtain a
	// connection; instead, use the error from
	// Transport.RoundTrip.
	// GotConn在获取了一个成功的连接之后被调用，如果获取失败则不调用hook
	// 反之，使用Transport.RoundTrip返回的error
	GotConn func(GotConnInfo)

	// PutIdleConn is called when the connection is returned to
	// the idle pool. If err is nil, the connection was
	// successfully returned to the idle pool. If err is non-nil,
	// it describes why not. PutIdleConn is not called if
	// connection reuse is disabled via Transport.DisableKeepAlives.
	// PutIdleConn is called before the caller's Response.Body.Close
	// call returns.
	// For HTTP/2, this hook is not currently used.
	// PutIdleConn在连接返回idle pool中被调用，如果err为nil，则连接成功地返回到
	// idle pool，如果err为non-nil，则描述了为什么没有成功返回。PutIdleConn不会被
	// 调用，如果connection reuse被Transport.DisableKeepAlives禁止
	// PutIdleConn在调用者的Response.Body.Close返回前被调用
	// HTTP/2不使用本hook
	PutIdleConn func(err error)

	// GotFirstResponseByte is called when the first byte of the response
	// headers is available.
	// GotFirstResponseByte在response headers的第一个字节可获取时被调用
	GotFirstResponseByte func()

	// Got100Continue is called if the server replies with a "100
	// Continue" response.
	Got100Continue func()

	// DNSStart is called when a DNS lookup begins.
	// DNSStart在DNS lookup开始时被调用
	DNSStart func(DNSStartInfo)

	// DNSDone is called when a DNS lookup ends.
	DNSDone func(DNSDoneInfo)

	// ConnectStart is called when a new connection's Dial begins.
	// If net.Dialer.DualStack (IPv6 "Happy Eyeballs") support is
	// enabled, this may be called multiple times.
	// ConnectStart在一个新连接的Dial开始时被调用
	ConnectStart func(network, addr string)

	// ConnectDone is called when a new connection's Dial
	// completes. The provided err indicates whether the
	// connection completedly successfully.
	// If net.Dialer.DualStack ("Happy Eyeballs") support is
	// enabled, this may be called multiple times.
	// ConnectDone在新连接的Dial结束时被调用，参数中提供的err表明是否
	// 连接成功
	ConnectDone func(network, addr string, err error)

	// TLSHandshakeStart is called when the TLS handshake is started. When
	// connecting to a HTTPS site via a HTTP proxy, the handshake happens after
	// the CONNECT request is processed by the proxy.
	// TLSHandshakeStart在TLS handshake启动时被调用，当通过一个HTTP proxy连接到HTTPS站点
	// handshake在CONNECT请求被proxy处理前开始
	TLSHandshakeStart func()

	// TLSHandshakeDone is called after the TLS handshake with either the
	// successful handshake's connection state, or a non-nil error on handshake
	// failure.
	// TLSHandshakeDone在TLS handshake之后被调用，要么提供一个成功的handshake的connection state
	// 或者在handshake失败之后，提供一个非nil的error
	TLSHandshakeDone func(tls.ConnectionState, error)

	// WroteHeaders is called after the Transport has written
	// the request headers.
	// WroteHeaders在Transport写入request headers后被调用
	WroteHeaders func()

	// Wait100Continue is called if the Request specified
	// "Expected: 100-continue" and the Transport has written the
	// request headers but is waiting for "100 Continue" from the
	// server before writing the request body.
	Wait100Continue func()

	// WroteRequest is called with the result of writing the
	// request and any body. It may be called multiple times
	// in the case of retried requests.
	// WroteRequest在写入request以及任何body的时候被调用
	// 这在retries requests情况下，可能被多次调用
	WroteRequest func(WroteRequestInfo)
}

// WroteRequestInfo contains information provided to the WroteRequest
// hook.
type WroteRequestInfo struct {
	// Err is any error encountered while writing the Request.
	Err error
}

// compose modifies t such that it respects the previously-registered hooks in old,
// subject to the composition policy requested in t.Compose.
// compose修改t，从而它能够尊重在old中之前已经注册的hooks
func (t *ClientTrace) compose(old *ClientTrace) {
	if old == nil {
		return
	}
	tv := reflect.ValueOf(t).Elem()
	ov := reflect.ValueOf(old).Elem()
	structType := tv.Type()
	for i := 0; i < structType.NumField(); i++ {
		tf := tv.Field(i)
		hookType := tf.Type()
		if hookType.Kind() != reflect.Func {
			continue
		}
		of := ov.Field(i)
		if of.IsNil() {
			continue
		}
		if tf.IsNil() {
			tf.Set(of)
			continue
		}

		// Make a copy of tf for tf to call. (Otherwise it
		// creates a recursive call cycle and stack overflows)
		// 创建一个tf的拷贝用于tf的调用
		tfCopy := reflect.ValueOf(tf.Interface())

		// We need to call both tf and of in some order.
		// 我们要以一定的顺序调用tf和of
		newFunc := reflect.MakeFunc(hookType, func(args []reflect.Value) []reflect.Value {
			tfCopy.Call(args)
			return of.Call(args)
		})
		tv.Field(i).Set(newFunc)
	}
}

// DNSStartInfo contains information about a DNS request.
type DNSStartInfo struct {
	Host string
}

// DNSDoneInfo contains information about the results of a DNS lookup.
type DNSDoneInfo struct {
	// Addrs are the IPv4 and/or IPv6 addresses found in the DNS
	// lookup. The contents of the slice should not be mutated.
	Addrs []net.IPAddr

	// Err is any error that occurred during the DNS lookup.
	Err error

	// Coalesced is whether the Addrs were shared with another
	// caller who was doing the same DNS lookup concurrently.
	Coalesced bool
}

func (t *ClientTrace) hasNetHooks() bool {
	if t == nil {
		return false
	}
	return t.DNSStart != nil || t.DNSDone != nil || t.ConnectStart != nil || t.ConnectDone != nil
}

// GotConnInfo is the argument to the ClientTrace.GotConn function and
// contains information about the obtained connection.
type GotConnInfo struct {
	// Conn is the connection that was obtained. It is owned by
	// the http.Transport and should not be read, written or
	// closed by users of ClientTrace.
	Conn net.Conn

	// Reused is whether this connection has been previously
	// used for another HTTP request.
	Reused bool

	// WasIdle is whether this connection was obtained from an
	// idle pool.
	WasIdle bool

	// IdleTime reports how long the connection was previously
	// idle, if WasIdle is true.
	IdleTime time.Duration
}
