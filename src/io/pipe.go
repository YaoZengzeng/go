// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Pipe adapter to connect code expecting an io.Reader
// with code expecting an io.Writer.

package io

import (
	"errors"
	"sync"
	"sync/atomic"
)

// atomicError is a type-safe atomic value for errors.
// We use a struct{ error } to ensure consistent use of a concrete type.
// 我们使用struct{ error }确保对concrete type使用的一致性
type atomicError struct{ v atomic.Value }

func (a *atomicError) Store(err error) {
	a.v.Store(struct{ error }{err})
}
func (a *atomicError) Load() error {
	err, _ := a.v.Load().(struct{ error })
	return err.error
}

// ErrClosedPipe is the error used for read or write operations on a closed pipe.
var ErrClosedPipe = errors.New("io: read/write on closed pipe")

// A pipe is the shared pipe structure underlying PipeReader and PipeWriter.
// pipe是PipeReader和PipeWriter底层共享的pipe结构
type pipe struct {
	wrMu sync.Mutex // Serializes Write operations
	wrCh chan []byte
	rdCh chan int

	// sync.Once只有第一次调用才会生效
	once sync.Once // Protects closing done
	done chan struct{}
	rerr atomicError
	werr atomicError
}

func (p *pipe) Read(b []byte) (n int, err error) {
	select {
	// 每次Read，先从管道p.done中读取，确认管道未关闭
	case <-p.done:
		return 0, p.readCloseError()
	default:
	}

	select {
	case bw := <-p.wrCh:
		// 从管道p.wrCh中读取数据
		nr := copy(b, bw)
		// 将读取的字节数写入管道p.rdCh
		p.rdCh <- nr
		return nr, nil
	case <-p.done:
		return 0, p.readCloseError()
	}
}

func (p *pipe) readCloseError() error {
	rerr := p.rerr.Load()
	if werr := p.werr.Load(); rerr == nil && werr != nil {
		return werr
	}
	return ErrClosedPipe
}

func (p *pipe) CloseRead(err error) error {
	if err == nil {
		// 如果err为nil，则将其设置为ErrClosedPipe
		err = ErrClosedPipe
	}
	// 将其存入rerr
	p.rerr.Store(err)
	// 如果p.once.Do被调用多次，那么只有第一次是生效的
	// 关闭p.done
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *pipe) Write(b []byte) (n int, err error) {
	select {
	case <-p.done:
		return 0, p.writeCloseError()
	default:
		// 进行写操作要先获取锁
		p.wrMu.Lock()
		defer p.wrMu.Unlock()
	}

	// once确保第一次总能进入循环
	for once := true; once || len(b) > 0; once = false {
		select {
		case p.wrCh <- b:
			// 从p.rdCh中获取已经被读取的字节数
			nw := <-p.rdCh
			// 直到b[]中的数据被写完
			b = b[nw:]
			n += nw
		case <-p.done:
			return n, p.writeCloseError()
		}
	}
	return n, nil
}

func (p *pipe) writeCloseError() error {
	werr := p.werr.Load()
	// 如果写没有error，读有error，则返回读的error
	if rerr := p.rerr.Load(); werr == nil && rerr != nil {
		return rerr
	}
	// 否则返回ErrClosedPipe，并不会返回写错误
	return ErrClosedPipe
}

func (p *pipe) CloseWrite(err error) error {
	if err == nil {
		// 当err为nil时，将EOF存入p.werr
		err = EOF
	}
	p.werr.Store(err)
	p.once.Do(func() { close(p.done) })
	return nil
}

// A PipeReader is the read half of a pipe.
type PipeReader struct {
	p *pipe
}

// Read implements the standard Read interface:
// it reads data from the pipe, blocking until a writer
// arrives or the write end is closed.
// 从pipe中读取数据，阻塞直到一个writer到达或者write end被closed掉
// If the write end is closed with an error, that error is
// returned as err; otherwise err is EOF.
// 如果write end关闭时有error，那么返回该error，否则返回的error为EOF
func (r *PipeReader) Read(data []byte) (n int, err error) {
	return r.p.Read(data)
}

// Close closes the reader; subsequent writes to the
// write half of the pipe will return the error ErrClosedPipe.
// 关闭reader，接下来对于write端的写操作，都会返回错误ErrClosedPipe
func (r *PipeReader) Close() error {
	return r.CloseWithError(nil)
}

// CloseWithError closes the reader; subsequent writes
// to the write half of the pipe will return the error err.
func (r *PipeReader) CloseWithError(err error) error {
	return r.p.CloseRead(err)
}

// A PipeWriter is the write half of a pipe.
type PipeWriter struct {
	p *pipe
}

// Write implements the standard Write interface:
// it writes data to the pipe, blocking until one or more readers
// have consumed all the data or the read end is closed.
// If the read end is closed with an error, that err is
// returned as err; otherwise err is ErrClosedPipe.
// Write会向pipe中写数据，阻塞，直到有一个或多个reader消费掉所有数据
// 获取read end已经被关闭了
// 如果read end关闭时带有一个error，则返回该error，否则，返回ErrClosedPipe
func (w *PipeWriter) Write(data []byte) (n int, err error) {
	return w.p.Write(data)
}

// Close closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and EOF.
// 关闭writer，接下来read端的读操作将不会返回字节
func (w *PipeWriter) Close() error {
	return w.CloseWithError(nil)
}

// CloseWithError closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and the error err,
// or EOF if err is nil.
//
// CloseWithError always returns nil.
// 当err为nil的时候，返回EOF
func (w *PipeWriter) CloseWithError(err error) error {
	return w.p.CloseWrite(err)
}

// Pipe creates a synchronous in-memory pipe.
// It can be used to connect code expecting an io.Reader
// with code expecting an io.Writer.
//
// Reads and Writes on the pipe are matched one to one
// except when multiple Reads are needed to consume a single Write.
// That is, each Write to the PipeWriter blocks until it has satisfied
// one or more Reads from the PipeReader that fully consume
// the written data.
// 每个向PipeWriter的写操作都会阻塞，直到有一个或多个符合条件的来自PipeReader的Read
// 操作能够对其进行消费
// The data is copied directly from the Write to the corresponding
// Read (or Reads); there is no internal buffering.
// 数据直接Write写到相应的Read，中间没有buffer
//
// It is safe to call Read and Write in parallel with each other or with Close.
// 同时执行Read和Close，Write和Close，Read和Write都是合法的
// Parallel calls to Read and parallel calls to Write are also safe:
// the individual calls will be gated sequentially.
func Pipe() (*PipeReader, *PipeWriter) {
	p := &pipe{
		wrCh: make(chan []byte),
		rdCh: make(chan int),
		done: make(chan struct{}),
	}
	return &PipeReader{p}, &PipeWriter{p}
}
