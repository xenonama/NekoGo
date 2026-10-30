package xhttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/vision"
	common "github.com/sagernet/sing-box/common/xray"
	"github.com/sagernet/sing-box/common/xray/buf"
	"github.com/sagernet/sing-box/common/xray/signal/done"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/net/http2"
)

// interface to abstract between use of browser dialer, vs net/http
type DialerClient interface {
	IsClosed() bool
	Close()

	OpenStream(context.Context, string, string, io.Reader, bool) (io.ReadCloser, net.Addr, net.Addr, error)
	PostPacket(context.Context, string, string, string, buf.MultiBuffer) error
}

// implements xhttp.DialerClient in terms of direct network connections
type DefaultDialerClient struct {
	options     *option.V2RayXHTTPBaseOptions
	client      *http.Client
	closed      bool
	httpVersion string
	// pool of net.Conn, created using dialUploadConn
	uploadRawPool  *sync.Pool
	dialUploadConn func(ctxInner context.Context) (net.Conn, error)

	mtx sync.RWMutex
}

type clientConnPool struct {
	t     *http2.Transport
	mu    sync.Mutex
	conns map[string][]*http2.ClientConn
}

type efaceWords struct {
	typ  unsafe.Pointer
	data unsafe.Pointer
}

// transportConnPool reads the unexported connection pool of an
// http2.Transport via reflection. x/net keeps it in the connPoolOrDef field
// (previously the connPool field); reflection is used instead of go:linkname
// because current toolchains no longer resolve a link to that symbol.
//
// BUG-032 hardening: every assumption is checked (field presence,
// addressability, nillable kind before IsNil, comma-ok assertion,
// non-nil dynamic value) and failures report ok=false so the caller can
// take the public close-path fallback instead of leaking sockets or
// dereferencing a wild pointer after an x/net reshape.
func transportConnPool(t *http2.Transport) (http2.ClientConnPool, bool) {
	if t == nil {
		return nil, false
	}
	e := reflect.ValueOf(t).Elem()
	field := e.FieldByName("connPoolOrDef")
	if !field.IsValid() {
		field = e.FieldByName("connPool")
		if !field.IsValid() {
			return nil, false
		}
	}
	if !field.CanAddr() {
		return nil, false
	}
	switch field.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		if field.IsNil() {
			return nil, false
		}
	default:
		// A future x/net may store the pool by value; without a stable
		// address of the interface/pointer there is nothing to read.
		return nil, false
	}
	pool, ok := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(http2.ClientConnPool)
	if !ok || pool == nil {
		return nil, false
	}
	return pool, true
}

// asLocalPool reinterprets an http2.ClientConnPool as our mirror struct
// for direct connection teardown. The dynamic type is verified
// structurally first (pointer to a struct named clientConnPool carrying
// mu + conns fields of the expected shape); anything else yields
// ok=false and the caller falls back to CloseIdleConnections instead
// of performing the eface cast blindly (BUG-032).
func asLocalPool(pool http2.ClientConnPool) (*clientConnPool, bool) {
	if pool == nil {
		return nil, false
	}
	rt := reflect.TypeOf(pool)
	if rt.Kind() != reflect.Pointer || rt.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	st := rt.Elem()
	if st.Name() != "clientConnPool" {
		return nil, false
	}
	muField, ok := st.FieldByName("mu")
	if !ok || muField.Type != reflect.TypeOf(sync.Mutex{}) {
		return nil, false
	}
	connsField, ok := st.FieldByName("conns")
	if !ok || connsField.Type.Kind() != reflect.Map {
		return nil, false
	}
	words := (*efaceWords)(unsafe.Pointer(&pool))
	if words.data == nil {
		return nil, false
	}
	return (*clientConnPool)(words.data), true
}

func (c *DefaultDialerClient) Close() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	switch transport := c.client.Transport.(type) {
		case *http.Transport:
			transport.CloseIdleConnections()
	case *http2.Transport:
		connPool, ok := transportConnPool(transport)
		if !ok {
			// Pool shape unrecognized (x/net reshape): fall back to
			// the public path instead of leaking or wild-casting.
			transport.CloseIdleConnections()
			break
		}
		p, ok := asLocalPool(connPool)
		if !ok {
			transport.CloseIdleConnections()
			break
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, vv := range p.conns {
			for _, cc := range vv {
				cc.Close()
			}
		}
		case *http3.Transport:
			transport.Close()
		default:
			// BUG-035: Close runs from recoverable network-error paths,
			// so a future transport variant must not turn a dial/stream
			// failure into a process crash. Log and skip instead.
			log.Printf("xhttp: skipping close of unknown transport type %v", reflect.TypeOf(transport))
			return
	}
}

func (c *DefaultDialerClient) IsClosed() bool {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.closed
}

func (c *DefaultDialerClient) OpenStream(ctx context.Context, url string, sessionId string, body io.Reader, uploadOnly bool) (wrc io.ReadCloser, remoteAddr, localAddr net.Addr, err error) {
	gotConn := done.New()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(connInfo httptrace.GotConnInfo) {
			remoteAddr = connInfo.Conn.RemoteAddr()
			localAddr = connInfo.Conn.LocalAddr()
			if hook, ok := vision.HookFromContext(ctx); ok {
				hook(connInfo.Conn)
			}
			gotConn.Close()
		},
	})
	method := "GET"
	if body != nil {
		method = c.options.GetNormalizedUplinkHTTPMethod()
	}
	req, _ := http.NewRequestWithContext(context.WithoutCancel(ctx), method, url, body)
	FillStreamRequest(req, sessionId, "", c.options)
	wrc = &WaitReadCloser{Wait: make(chan struct{})}
	go func() {
		resp, err := c.client.Do(req)
		if err != nil {
			if !uploadOnly {
				c.Close()
			}
			gotConn.Close()
			common.Close(body)
			wrc.Close()
			return
		}
		if resp.StatusCode != 200 || uploadOnly {
			if resp.StatusCode != 200 {
				c.Close()
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			common.Close(body)
			wrc.Close()
			return
		}
		wrc.(*WaitReadCloser).Set(resp.Body)
	}()
	<-gotConn.Wait()
	return
}

func (c *DefaultDialerClient) PostPacket(ctx context.Context, url string, sessionId string, seqStr string, payload buf.MultiBuffer) error {
	method := c.options.GetNormalizedUplinkHTTPMethod()
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), method, url, nil)
	if err != nil {
		return err
	}
	FillPacketRequest(req, sessionId, seqStr, payload, c.options)
	if c.httpVersion != "1.1" {
		resp, err := c.client.Do(req)
		if err != nil {
			c.Close()
			return err
		}
		_, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if resp.StatusCode != 200 {
			c.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			return E.New("bad status code: ", resp.Status)
		}
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	} else {
		requestBuff := new(bytes.Buffer)
		requestBuff.Grow(512 + int(req.ContentLength))
		common.Must(req.Write(requestBuff))
		var uploadConn any
		var h1UploadConn *H1Conn
		for {
			uploadConn = c.uploadRawPool.Get()
			newConnection := uploadConn == nil
			if newConnection {
				newConn, err := c.dialUploadConn(context.WithoutCancel(ctx))
				if err != nil {
					return err
				}
				h1UploadConn = NewH1Conn(newConn)
				uploadConn = h1UploadConn
			} else {
				h1UploadConn = uploadConn.(*H1Conn)
				if h1UploadConn.UnreadedResponsesCount > 0 {
					resp, err := http.ReadResponse(h1UploadConn.RespBufReader, req)
					if err != nil {
						c.Close()
						return fmt.Errorf("error while reading response: %s", err.Error())
					}
					_, copyErr := io.Copy(io.Discard, resp.Body)
					closeErr := resp.Body.Close()
					if resp.StatusCode != 200 {
						c.Close()
						return fmt.Errorf("got non-200 error response code: %d", resp.StatusCode)
					}
					if copyErr != nil {
						return copyErr
					}
					if closeErr != nil {
						return closeErr
					}
				}
			}
			_, err := h1UploadConn.Write(requestBuff.Bytes())
			if err == nil {
				break
			} else if newConnection {
				return err
			}
		}
		c.uploadRawPool.Put(uploadConn)
	}
	return nil
}

type WaitReadCloser struct {
	Wait chan struct{}
	io.ReadCloser
	mu     sync.Mutex
	once   sync.Once
	closed bool
}

func (w *WaitReadCloser) notify() {
	w.once.Do(func() {
		close(w.Wait)
	})
}

func (w *WaitReadCloser) Set(rc io.ReadCloser) {
	w.mu.Lock()
	if w.closed || w.ReadCloser != nil {
		w.mu.Unlock()
		rc.Close()
		return
	}
	w.ReadCloser = rc
	w.mu.Unlock()
	w.notify()
}

func (w *WaitReadCloser) Read(b []byte) (int, error) {
	w.mu.Lock()
	rc := w.ReadCloser
	w.mu.Unlock()

	if rc == nil {
		<-w.Wait
		w.mu.Lock()
		rc = w.ReadCloser
		w.mu.Unlock()
		if rc == nil {
			return 0, io.ErrClosedPipe
		}
	}
	return rc.Read(b)
}

func (w *WaitReadCloser) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	rc := w.ReadCloser
	w.ReadCloser = nil
	w.mu.Unlock()

	if rc != nil {
		return rc.Close()
	}

	w.notify()
	return nil
}

func ApplyMetaToRequest(options *option.V2RayXHTTPBaseOptions, req *http.Request, sessionId string, seqStr string) {
	sessionPlacement := options.GetNormalizedSessionPlacement()
	seqPlacement := options.GetNormalizedSeqPlacement()
	sessionKey := options.GetNormalizedSessionKey()
	seqKey := options.GetNormalizedSeqKey()
	if sessionId != "" {
		switch sessionPlacement {
			case option.PlacementPath:
				req.URL.Path = appendToPath(req.URL.Path, sessionId)
			case option.PlacementQuery:
				q := req.URL.Query()
				q.Set(sessionKey, sessionId)
				req.URL.RawQuery = q.Encode()
			case option.PlacementHeader:
				req.Header.Set(sessionKey, sessionId)
			case option.PlacementCookie:
				req.AddCookie(&http.Cookie{Name: sessionKey, Value: sessionId})
		}
	}
	if seqStr != "" {
		switch seqPlacement {
			case option.PlacementPath:
				req.URL.Path = appendToPath(req.URL.Path, seqStr)
			case option.PlacementQuery:
				q := req.URL.Query()
				q.Set(seqKey, seqStr)
				req.URL.RawQuery = q.Encode()
			case option.PlacementHeader:
				req.Header.Set(seqKey, seqStr)
			case option.PlacementCookie:
				req.AddCookie(&http.Cookie{Name: seqKey, Value: seqStr})
		}
	}
}

func appendToPath(path, value string) string {
	if strings.HasSuffix(path, "/") {
		return path + value
	}
	return path + "/" + value
}
