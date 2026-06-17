package helps

// This file implements a minimal HTTP/2 client whose on-wire fingerprint
// matches Chrome rather than Go's standard library. The standard
// golang.org/x/net/http2.Transport emits a SETTINGS frame, connection
// WINDOW_UPDATE and pseudo-header order that are hardcoded and distinctively
// "Go": it always sends SETTINGS_MAX_FRAME_SIZE (which Chrome never sends),
// in a fixed non-ascending order, with a 1<<30 connection window and the
// pseudo-header order :authority,:method,:path,:scheme. Pairing a Chrome TLS
// ClientHello (via utls) with that HTTP/2 layer is itself a strong signal.
//
// chromeH2Conn speaks HTTP/2 directly on top of an already-established
// (utls) TLS connection, emitting Chrome's akamai HTTP/2 fingerprint:
//
//	SETTINGS  1:65536;2:0;4:6291456;6:262144
//	WINDOW_UPDATE(0, 15663105)
//	pseudo-header order :method,:authority,:scheme,:path  (m,a,s,p)
//	no PRIORITY frames
//
// Flow control is owned here so the advertised SETTINGS match real behavior
// (no faking values the underlying transport would violate). It supports
// request bodies, streaming responses and stream multiplexing on one
// connection.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Chrome HTTP/2 fingerprint constants.
const (
	chromeH2HeaderTableSize   = 65536
	chromeH2EnablePush        = 0
	chromeH2InitialWindowSize = 6291456
	chromeH2MaxHeaderListSize = 262144
	// chromeH2ConnWindowIncrement is the connection-level WINDOW_UPDATE Chrome
	// sends immediately after its SETTINGS frame, bringing the connection
	// receive window to 65535 + 15663105 = 15728640.
	chromeH2ConnWindowIncrement = 15663105

	h2DefaultInitialWindow = 65535
	h2DefaultMaxFrameSize  = 16384
	h2DefaultMaxConcurrent = 100
)

// chromeClientSettings is the exact SETTINGS list (values and order) a recent
// Chrome sends. Order matters: the akamai fingerprint encodes settings in the
// order received.
var chromeClientSettings = []http2.Setting{
	{ID: http2.SettingHeaderTableSize, Val: chromeH2HeaderTableSize},
	{ID: http2.SettingEnablePush, Val: chromeH2EnablePush},
	{ID: http2.SettingInitialWindowSize, Val: chromeH2InitialWindowSize},
	{ID: http2.SettingMaxHeaderListSize, Val: chromeH2MaxHeaderListSize},
}

// hopByHopH2Headers are connection-specific headers that must not be carried
// over HTTP/2 (RFC 7540 §8.1.2.2).
var hopByHopH2Headers = map[string]struct{}{
	"connection":        {},
	"keep-alive":        {},
	"proxy-connection":  {},
	"transfer-encoding": {},
	"upgrade":           {},
	"host":              {},
}

// chromeH2Conn is a single HTTP/2 connection that talks to one server with a
// Chrome-like fingerprint.
type chromeH2Conn struct {
	conn net.Conn
	fr   *http2.Framer

	// wmu serializes all frame writes and the shared hpack encoder.
	wmu  sync.Mutex
	henc *hpack.Encoder
	hbuf bytes.Buffer

	mu       sync.Mutex
	cond     *sync.Cond // broadcast on flow-control window changes / shutdown
	streams  map[uint32]*chromeH2Stream
	nextID   uint32
	closed   bool
	goneAway bool
	connErr  error

	// Peer settings (govern how we send).
	connSendWindow int64
	peerInitWindow int32
	peerMaxFrame   uint32
	peerMaxStreams uint32
	activeStreams  uint32
}

// chromeH2Stream is one request/response exchange on a connection.
type chromeH2Stream struct {
	id   uint32
	conn *chromeH2Conn

	sendWindow int64 // our send window for this stream (peer-controlled)

	respCh chan *http.Response
	errCh  chan error
	once   sync.Once // guards delivering exactly one of resp/err

	body *streamBuffer

	gotResponse bool
	ended       bool
}

// newChromeH2Conn performs the HTTP/2 client handshake (preface + Chrome
// SETTINGS + WINDOW_UPDATE) on an established TLS connection and starts the
// read loop.
func newChromeH2Conn(conn net.Conn) (*chromeH2Conn, error) {
	cc := &chromeH2Conn{
		conn:           conn,
		streams:        make(map[uint32]*chromeH2Stream),
		nextID:         1,
		connSendWindow: h2DefaultInitialWindow,
		peerInitWindow: h2DefaultInitialWindow,
		peerMaxFrame:   h2DefaultMaxFrameSize,
		peerMaxStreams: h2DefaultMaxConcurrent,
	}
	cc.cond = sync.NewCond(&cc.mu)
	cc.fr = http2.NewFramer(conn, conn)
	cc.fr.ReadMetaHeaders = hpack.NewDecoder(chromeH2HeaderTableSize, nil)
	cc.fr.MaxHeaderListSize = chromeH2MaxHeaderListSize
	cc.henc = hpack.NewEncoder(&cc.hbuf)

	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		return nil, err
	}
	if err := cc.fr.WriteSettings(chromeClientSettings...); err != nil {
		return nil, err
	}
	if err := cc.fr.WriteWindowUpdate(0, chromeH2ConnWindowIncrement); err != nil {
		return nil, err
	}

	go cc.readLoop()
	return cc, nil
}

// CanTakeNewRequest reports whether the connection can start another stream.
func (cc *chromeH2Conn) CanTakeNewRequest() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return !cc.closed && !cc.goneAway && cc.activeStreams < cc.peerMaxStreams
}

// Close terminates the connection and unblocks any in-flight stream.
func (cc *chromeH2Conn) Close() error {
	cc.close(net.ErrClosed)
	return nil
}

func (cc *chromeH2Conn) close(err error) {
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		return
	}
	cc.closed = true
	cc.connErr = err
	streams := make([]*chromeH2Stream, 0, len(cc.streams))
	for _, st := range cc.streams {
		streams = append(streams, st)
	}
	cc.cond.Broadcast()
	cc.mu.Unlock()

	for _, st := range streams {
		// fail() unblocks a RoundTrip still waiting for headers; closeWith()
		// unblocks a body Read already in progress.
		st.fail(err)
		st.body.closeWith(err)
	}
	_ = cc.conn.Close()
}

// RoundTrip sends req and returns the response. The response body streams as
// DATA frames arrive.
func (cc *chromeH2Conn) RoundTrip(req *http.Request) (*http.Response, error) {
	st := &chromeH2Stream{
		conn:   cc,
		respCh: make(chan *http.Response, 1),
		errCh:  make(chan error, 1),
		body:   newStreamBuffer(),
	}
	if err := cc.writeRequest(st, req); err != nil {
		return nil, err
	}

	ctx := req.Context()
	select {
	case resp := <-st.respCh:
		resp.Request = req
		return resp, nil
	case err := <-st.errCh:
		return nil, err
	case <-ctx.Done():
		cc.resetStream(st, http2.ErrCodeCancel)
		return nil, ctx.Err()
	}
}

// registerStreamLocked assigns the next client stream ID and registers st.
// The caller holds wmu, so the ID is allocated and the opening HEADERS frame
// is sent without another stream interleaving — HTTP/2 requires that the
// stream IDs of opening HEADERS frames strictly increase.
func (cc *chromeH2Conn) registerStreamLocked(st *chromeH2Stream) error {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.closed || cc.goneAway {
		if cc.connErr != nil {
			return cc.connErr
		}
		return errors.New("chrome-h2: connection unavailable")
	}
	st.id = cc.nextID
	cc.nextID += 2
	st.sendWindow = int64(cc.peerInitWindow)
	cc.streams[st.id] = st
	cc.activeStreams++
	return nil
}

func (cc *chromeH2Conn) removeStream(id uint32) {
	cc.mu.Lock()
	if _, ok := cc.streams[id]; ok {
		delete(cc.streams, id)
		cc.activeStreams--
	}
	cc.mu.Unlock()
}

func (cc *chromeH2Conn) resetStream(st *chromeH2Stream, code http2.ErrCode) {
	cc.wmu.Lock()
	_ = cc.fr.WriteRSTStream(st.id, code)
	cc.wmu.Unlock()
	cc.removeStream(st.id)
	st.body.closeWith(errors.New("chrome-h2: stream reset"))
}

// writeRequest encodes and writes the HEADERS (and any body DATA) for st.
func (cc *chromeH2Conn) writeRequest(st *chromeH2Stream, req *http.Request) error {
	hasBody := req.Body != nil && req.Body != http.NoBody
	// Read the peer max frame size before taking wmu to avoid a wmu->mu lock
	// inversion (handleSettings takes mu->wmu).
	maxFrame := int(cc.maxFrameSize())

	// Assign the stream ID, encode the headers and write the opening HEADERS
	// frame under a single wmu critical section. This keeps two invariants
	// HTTP/2 requires: opening HEADERS are sent in increasing stream-ID order,
	// and the HPACK encode order equals the on-wire send order (otherwise
	// concurrent streams desync the peer's dynamic table).
	cc.wmu.Lock()
	err := cc.registerStreamLocked(st)
	if err == nil {
		var block []byte
		if block, err = cc.encodeHeadersLocked(req); err == nil {
			err = cc.writeHeaderBlockLocked(st.id, block, !hasBody, maxFrame)
		}
	}
	cc.wmu.Unlock()
	if err != nil {
		if st.id != 0 {
			cc.removeStream(st.id)
		}
		return err
	}

	if hasBody {
		if err = cc.writeBody(st, req.Body); err != nil {
			_ = req.Body.Close()
			return err
		}
		_ = req.Body.Close()
	}
	return nil
}

// encodeHeadersLocked builds the HPACK header block with Chrome's pseudo-header
// order (:method, :authority, :scheme, :path) followed by the regular headers.
// The caller must hold wmu so the encode order matches the send order.
func (cc *chromeH2Conn) encodeHeadersLocked(req *http.Request) ([]byte, error) {
	authority := req.Host
	if authority == "" {
		authority = req.URL.Host
	}
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}

	cc.hbuf.Reset()

	write := func(name, value string) error {
		return cc.henc.WriteField(hpack.HeaderField{Name: name, Value: value})
	}
	if err := write(":method", req.Method); err != nil {
		return nil, err
	}
	if err := write(":authority", authority); err != nil {
		return nil, err
	}
	if err := write(":scheme", "https"); err != nil {
		return nil, err
	}
	if err := write(":path", path); err != nil {
		return nil, err
	}

	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, ":") {
			continue
		}
		if _, skip := hopByHopH2Headers[lower]; skip {
			continue
		}
		for _, v := range values {
			if err := write(lower, v); err != nil {
				return nil, err
			}
		}
	}
	if req.ContentLength > 0 && req.Header.Get("Content-Length") == "" {
		if err := write("content-length", strconv.FormatInt(req.ContentLength, 10)); err != nil {
			return nil, err
		}
	}

	block := make([]byte, cc.hbuf.Len())
	copy(block, cc.hbuf.Bytes())
	return block, nil
}

// writeHeaderBlockLocked writes a header block, splitting into CONTINUATION
// frames when it exceeds the peer's max frame size. Caller holds wmu.
func (cc *chromeH2Conn) writeHeaderBlockLocked(streamID uint32, block []byte, endStream bool, maxFrame int) error {
	first := block
	var rest []byte
	if len(block) > maxFrame {
		first = block[:maxFrame]
		rest = block[maxFrame:]
	}
	if err := cc.fr.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: first,
		EndStream:     endStream,
		EndHeaders:    len(rest) == 0,
	}); err != nil {
		return err
	}
	for len(rest) > 0 {
		chunk := rest
		if len(chunk) > maxFrame {
			chunk = rest[:maxFrame]
			rest = rest[maxFrame:]
		} else {
			rest = nil
		}
		if err := cc.fr.WriteContinuation(streamID, len(rest) == 0, chunk); err != nil {
			return err
		}
	}
	return nil
}

// writeBody streams the request body as DATA frames, respecting connection and
// stream send windows and the peer's max frame size.
func (cc *chromeH2Conn) writeBody(st *chromeH2Stream, body io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := cc.writeData(st, buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			cc.wmu.Lock()
			err := cc.fr.WriteData(st.id, true, nil)
			cc.wmu.Unlock()
			return err
		}
		if readErr != nil {
			cc.resetStream(st, http2.ErrCodeCancel)
			return readErr
		}
	}
}

func (cc *chromeH2Conn) writeData(st *chromeH2Stream, data []byte) error {
	for len(data) > 0 {
		n, err := cc.reserveSendWindow(st, len(data))
		if err != nil {
			return err
		}
		cc.wmu.Lock()
		err = cc.fr.WriteData(st.id, false, data[:n])
		cc.wmu.Unlock()
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// reserveSendWindow blocks until connection and stream send windows allow at
// least one byte, then reserves min(want, conn, stream, maxFrame) bytes.
func (cc *chromeH2Conn) reserveSendWindow(st *chromeH2Stream, want int) (int, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for {
		if cc.closed {
			if cc.connErr != nil {
				return 0, cc.connErr
			}
			return 0, errors.New("chrome-h2: connection closed")
		}
		avail := minInt64Chrome(cc.connSendWindow, st.sendWindow)
		if avail > 0 {
			n := int64(want)
			if avail < n {
				n = avail
			}
			if mf := int64(cc.peerMaxFrame); mf < n {
				n = mf
			}
			cc.connSendWindow -= n
			st.sendWindow -= n
			return int(n), nil
		}
		cc.cond.Wait()
	}
}

func (cc *chromeH2Conn) maxFrameSize() uint32 {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.peerMaxFrame
}

// readLoop reads frames until the connection fails, dispatching to streams.
func (cc *chromeH2Conn) readLoop() {
	for {
		frame, err := cc.fr.ReadFrame()
		if err != nil {
			cc.close(err)
			return
		}
		switch f := frame.(type) {
		case *http2.MetaHeadersFrame:
			cc.handleHeaders(f)
		case *http2.DataFrame:
			cc.handleData(f)
		case *http2.SettingsFrame:
			cc.handleSettings(f)
		case *http2.WindowUpdateFrame:
			cc.handleWindowUpdate(f)
		case *http2.RSTStreamFrame:
			cc.handleRST(f)
		case *http2.PingFrame:
			if !f.IsAck() {
				cc.wmu.Lock()
				_ = cc.fr.WritePing(true, f.Data)
				cc.wmu.Unlock()
			}
		case *http2.GoAwayFrame:
			cc.handleGoAway(f)
		}
	}
}

func (cc *chromeH2Conn) handleHeaders(f *http2.MetaHeadersFrame) {
	cc.mu.Lock()
	st := cc.streams[f.StreamID]
	cc.mu.Unlock()
	if st == nil {
		return
	}

	if !st.gotResponse {
		st.gotResponse = true
		resp := buildResponse(f, st.body)
		st.deliver(resp)
	}
	if f.StreamEnded() {
		cc.endStream(st, io.EOF)
	}
}

func (cc *chromeH2Conn) handleData(f *http2.DataFrame) {
	cc.mu.Lock()
	st := cc.streams[f.StreamID]
	cc.mu.Unlock()

	flowLen := int(f.Header().Length)
	if flowLen > 0 {
		// Replenish receive windows immediately: we buffer the data, so we are
		// always able to receive more. This keeps advertised windows honest
		// without risking a stall from a slow consumer.
		cc.wmu.Lock()
		_ = cc.fr.WriteWindowUpdate(0, uint32(flowLen))
		if st != nil {
			_ = cc.fr.WriteWindowUpdate(f.StreamID, uint32(flowLen))
		}
		cc.wmu.Unlock()
	}
	if st == nil {
		return
	}
	if data := f.Data(); len(data) > 0 {
		st.body.write(data)
	}
	if f.StreamEnded() {
		cc.endStream(st, io.EOF)
	}
}

func (cc *chromeH2Conn) handleSettings(f *http2.SettingsFrame) {
	if f.IsAck() {
		return
	}
	var newHeaderTableSize uint32
	var headerTableSizeChanged bool
	cc.mu.Lock()
	_ = f.ForeachSetting(func(s http2.Setting) error {
		switch s.ID {
		case http2.SettingInitialWindowSize:
			delta := int64(s.Val) - int64(cc.peerInitWindow)
			cc.peerInitWindow = int32(s.Val)
			for _, st := range cc.streams {
				st.sendWindow += delta
			}
		case http2.SettingMaxFrameSize:
			cc.peerMaxFrame = s.Val
		case http2.SettingMaxConcurrentStreams:
			cc.peerMaxStreams = s.Val
		case http2.SettingHeaderTableSize:
			newHeaderTableSize = s.Val
			headerTableSizeChanged = true
		}
		return nil
	})
	cc.cond.Broadcast()
	cc.mu.Unlock()

	// Apply the encoder table size and ACK under wmu only (never while holding
	// mu) so the lock order stays wmu->mu everywhere.
	cc.wmu.Lock()
	if headerTableSizeChanged {
		cc.henc.SetMaxDynamicTableSize(newHeaderTableSize)
	}
	_ = cc.fr.WriteSettingsAck()
	cc.wmu.Unlock()
}

func (cc *chromeH2Conn) handleWindowUpdate(f *http2.WindowUpdateFrame) {
	cc.mu.Lock()
	if f.StreamID == 0 {
		cc.connSendWindow += int64(f.Increment)
	} else if st := cc.streams[f.StreamID]; st != nil {
		st.sendWindow += int64(f.Increment)
	}
	cc.cond.Broadcast()
	cc.mu.Unlock()
}

func (cc *chromeH2Conn) handleRST(f *http2.RSTStreamFrame) {
	cc.mu.Lock()
	st := cc.streams[f.StreamID]
	cc.mu.Unlock()
	if st != nil {
		cc.endStreamErr(st, fmt.Errorf("chrome-h2: stream reset by peer (code %v)", f.ErrCode))
	}
}

func (cc *chromeH2Conn) handleGoAway(f *http2.GoAwayFrame) {
	cc.mu.Lock()
	cc.goneAway = true
	var doomed []*chromeH2Stream
	for id, st := range cc.streams {
		if id > f.LastStreamID {
			doomed = append(doomed, st)
		}
	}
	cc.cond.Broadcast()
	cc.mu.Unlock()
	for _, st := range doomed {
		cc.endStreamErr(st, errors.New("chrome-h2: server sent GOAWAY"))
	}
}

// endStream finishes a stream's body normally (EOF) and unregisters it.
func (cc *chromeH2Conn) endStream(st *chromeH2Stream, reason error) {
	cc.mu.Lock()
	if st.ended {
		cc.mu.Unlock()
		return
	}
	st.ended = true
	delete(cc.streams, st.id)
	cc.activeStreams--
	cc.mu.Unlock()
	st.body.closeWith(reason)
}

// endStreamErr finishes a stream with an error, delivering it to a waiting
// RoundTrip if the response has not been delivered yet.
func (cc *chromeH2Conn) endStreamErr(st *chromeH2Stream, err error) {
	cc.mu.Lock()
	if st.ended {
		cc.mu.Unlock()
		return
	}
	st.ended = true
	delete(cc.streams, st.id)
	cc.activeStreams--
	delivered := st.gotResponse
	cc.mu.Unlock()
	if !delivered {
		st.fail(err)
	}
	st.body.closeWith(err)
}

func (st *chromeH2Stream) deliver(resp *http.Response) {
	st.once.Do(func() { st.respCh <- resp })
}

func (st *chromeH2Stream) fail(err error) {
	st.once.Do(func() { st.errCh <- err })
}

// buildResponse constructs an *http.Response from response HEADERS.
func buildResponse(f *http2.MetaHeadersFrame, body *streamBuffer) *http.Response {
	status := f.PseudoValue("status")
	code, _ := strconv.Atoi(status)
	header := make(http.Header)
	for _, hf := range f.RegularFields() {
		header.Add(hf.Name, hf.Value)
	}
	resp := &http.Response{
		StatusCode: code,
		Status:     status + " " + http.StatusText(code),
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header:     header,
		Body:       &chromeH2Body{buf: body},
	}
	if cl := header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			resp.ContentLength = n
		}
	} else {
		resp.ContentLength = -1
	}
	return resp
}

func minInt64Chrome(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// chromeH2Body adapts a streamBuffer to an io.ReadCloser response body.
type chromeH2Body struct {
	buf *streamBuffer
}

func (b *chromeH2Body) Read(p []byte) (int, error) { return b.buf.Read(p) }

func (b *chromeH2Body) Close() error {
	b.buf.closeWith(nil)
	return nil
}

// streamBuffer is an in-memory FIFO that the read loop appends to without
// blocking and the response body drains. This decouples a slow consumer from
// the shared connection read loop so one stream cannot stall the others.
type streamBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	err    error
	closed bool
}

func newStreamBuffer() *streamBuffer {
	b := &streamBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *streamBuffer) write(p []byte) {
	b.mu.Lock()
	if !b.closed {
		b.buf.Write(p)
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

// closeWith marks the buffer finished. A nil error means a clean EOF.
func (b *streamBuffer) closeWith(err error) {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		if err != nil {
			b.err = err
		} else if b.err == nil {
			b.err = io.EOF
		}
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *streamBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if b.buf.Len() > 0 {
			return b.buf.Read(p)
		}
		if b.err != nil {
			return 0, b.err
		}
		b.cond.Wait()
	}
}
