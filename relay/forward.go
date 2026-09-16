package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// nodeAuthCookieName is the signed cookie set once a node's access password is
// verified. It is scoped to the node path so unrelated nodes stay protected.
const nodeAuthCookieName = "cv_node_auth"

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func isHopByHop(name string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(name, h) {
			return true
		}
	}
	return false
}

type Forwarder struct {
	hub   *Hub
	store *Store
}

func NewForwarder(hub *Hub, store *Store) *Forwarder {
	return &Forwarder{hub: hub, store: store}
}

// nodeAuthKey derives the per-node HMAC key from its access-password hash.
// Changing the password immediately invalidates every previously issued cookie.
func nodeAuthKey(dev *Device) []byte {
	sum := sha256.Sum256([]byte(dev.AccessPassword))
	return sum[:]
}

func nodeAuthValue(dev *Device) string {
	raw := fmt.Sprintf("%s\x00%s", dev.NodeID, dev.DeviceToken)
	mac := hmac.New(sha256.New, nodeAuthKey(dev))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// nodeAuthCookie returns the signed cookie granting access to dev until the
// password (or token) changes.
func nodeAuthCookie(dev *Device) *http.Cookie {
	return &http.Cookie{
		Name:     nodeAuthCookieName,
		Value:    nodeAuthValue(dev),
		Path:     "/n/" + dev.NodeID + "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   dev.EndpointBaseIsWSS(),
		MaxAge:   30 * 24 * 3600,
	}
}

// nodeAuthValid reports whether the request already holds a valid read-only
// cookie for dev. The check is constant-time to avoid timing attacks.
func nodeAuthValid(r *http.Request, dev *Device) bool {
	c, err := r.Cookie(nodeAuthCookieName)
	if err != nil || c == nil {
		return false
	}
	got, err := hex.DecodeString(c.Value)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(nodeAuthValue(dev))
	return hmac.Equal(got, want)
}

// HandleNode forwards requests under /n/{node_id}/... to the connected device,
// enforcing the node's access password when one is configured.
func (f *Forwarder) HandleNode(w http.ResponseWriter, r *http.Request) {
	const prefix = "/n/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	nodeID, suffix, _ := strings.Cut(rest, "/")
	if nodeID == "" {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}

	dev := f.store.getDeviceByNode(nodeID)
	if dev == nil {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	if dev.AccessPassword != "" && !nodeAuthValid(r, dev) {
		f.serveNodeAuthPage(w, r, dev)
		return
	}

	if r.URL.Path == "/n/"+nodeID+"/" || r.URL.Path == "/n/"+nodeID {
		if dev.AccessPassword != "" && nodeAuthValid(r, dev) {
			http.SetCookie(w, nodeAuthCookie(dev))
		}
	}

	path := "/" + suffix
	targetURI := path
	if r.URL.RawQuery != "" {
		targetURI += "?" + r.URL.RawQuery
	}
	f.forward(w, r, nodeID, targetURI, "")
}

// ServeNodeAuth handles POST /n/{node_id}/_auth with the access password as
// form field "password". On success it sets the signed cookie and redirects
// back to the node.
func (f *Forwarder) ServeNodeAuth(w http.ResponseWriter, r *http.Request) {
	const prefix = "/n/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	nodeID, _, _ := strings.Cut(rest, "/")
	dev := f.store.getDeviceByNode(nodeID)
	if dev == nil || dev.AccessPassword == "" {
		http.Redirect(w, r, "/nodes", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_form")
		return
	}
	entered := strings.TrimSpace(r.PostFormValue("password"))
	if !passwordMatch(dev.AccessPassword, entered) {
		q := url.Values{}
		q.Set("next", r.FormValue("next"))
		http.Redirect(w, r, "/n/"+nodeID+"/_auth?error=1&"+q.Encode(), http.StatusFound)
		return
	}
	http.SetCookie(w, nodeAuthCookie(dev))
	next := strings.TrimSpace(r.FormValue("next"))
	if next == "" {
		next = "/n/" + nodeID + "/"
	}
	if !strings.HasPrefix(next, "/n/"+nodeID+"/") {
		next = "/n/" + nodeID + "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func passwordMatch(storedHash, candidate string) bool {
	want, err := hex.DecodeString(storedHash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(candidate))
	return hmac.Equal(got[:], want)
}

// serveNodeAuthPage renders the password entry page for a protected node.
func (f *Forwarder) serveNodeAuthPage(w http.ResponseWriter, r *http.Request, dev *Device) {
	nodeID := dev.NodeID
	next := r.URL.RequestURI()
	if !strings.HasPrefix(next, "/n/"+nodeID+"/") {
		next = "/n/" + nodeID + "/"
	}
	q := url.Values{}
	q.Set("next", next)
	authURL := "/n/" + nodeID + "/_auth?" + q.Encode()
	writeHTML(w, nodeAuthPageHTML(dev.NodeName, authURL, r.URL.Query().Get("error") == "1"))
}

// handleServiceHost routes subdomain-style requests:
// {slug}-{node_id}-relay.{host}/...
func (f *Forwarder) handleServiceHost(w http.ResponseWriter, r *http.Request) bool {
	hostname := r.Host
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = h
	}
	subdomain := ""
	if idx := strings.IndexByte(hostname, '.'); idx > 0 {
		subdomain = hostname[:idx]
	}
	if !strings.HasSuffix(subdomain, "-relay") {
		return false
	}
	core := strings.TrimSuffix(subdomain, "-relay")
	idx := strings.LastIndexByte(core, '-')
	if idx <= 0 || idx == len(core)-1 {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	slug, nodeID := core[:idx], core[idx+1:]
	log.Printf("[relay] service-host: host=%q subdomain=%q slug=%q node_id=%q", r.Host, subdomain, slug, nodeID)
	if !ValidServiceSlug(slug) {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	svc := f.store.getServiceFold(slug, nodeID)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	if !svc.Enabled {
		writeJSONError(w, http.StatusForbidden, "service_disabled")
		return true
	}
	targetURI := r.URL.RequestURI()
	f.forward(w, r, nodeID, targetURI, slug)
	return true
}

// forward sends a single HTTP request over a yamux stream to the device.
func (f *Forwarder) forward(w http.ResponseWriter, r *http.Request, nodeID, targetURI, serviceSlug string) {
	sess := f.hub.get(nodeID)
	if sess == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "node_offline")
		return
	}
	if serviceSlug != "" {
		r.Header.Set(serviceSlugHeader, serviceSlug)
	}
	if websocket.IsWebSocketUpgrade(r) {
		f.forwardWebSocket(w, r, sess, targetURI)
		return
	}
	f.forwardHTTP(w, r, sess, targetURI)
}

func (f *Forwarder) forwardHTTP(w http.ResponseWriter, r *http.Request, sess *yamux.Session, targetURI string) {
	stream, err := sess.OpenStream()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "connector_unavailable")
		return
	}
	defer stream.Close()

	if err := writeHTTPRequest(stream, r, targetURI); err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_write_failed")
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_read_failed")
		return
	}
	defer resp.Body.Close()
	copyHTTPResponse(w, resp)
}

func (f *Forwarder) forwardWebSocket(w http.ResponseWriter, r *http.Request, sess *yamux.Session, targetURI string) {
	stream, err := sess.OpenStream()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "connector_unavailable")
		return
	}
	defer stream.Close()

	if err := writeHTTPRequest(stream, r, targetURI); err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_write_failed")
		return
	}

	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_read_failed")
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		copyHTTPResponse(w, resp)
		return
	}

	up := websocket.Upgrader{
		HandshakeTimeout: 15 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return true },
	}
	browser, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()

	errCh := make(chan error, 2)
	go bridgeBrowserToStream(browser, stream, errCh)
	go bridgeStreamToBrowser(br, browser, errCh)
	<-errCh
}

// writeHTTPRequest writes an HTTP/1.1 request onto the stream with an exact
// request line and headers, so WebSocket upgrade semantics are preserved.
func writeHTTPRequest(w io.Writer, r *http.Request, targetURI string) error {
	if r == nil {
		return errors.New("nil request")
	}
	if _, err := fmt.Fprintf(w, "%s %s HTTP/1.1\r\n", r.Method, targetURI); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Host: %s\r\n", r.Host); err != nil {
		return err
	}

	isUpgrade := websocket.IsWebSocketUpgrade(r)

	// Fold multiple Cookie headers into a single one.
	cookies := r.Cookies()
	if len(cookies) > 0 {
		parts := make([]string, 0, len(cookies))
		for _, c := range cookies {
			parts = append(parts, c.Name+"="+c.Value)
		}
		r.Header.Set("Cookie", strings.Join(parts, "; "))
	}

	for name, values := range r.Header {
		if isHopByHop(name) && !(isUpgrade && (strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade"))) {
			continue
		}
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Transfer-Encoding") {
			continue
		}
		for _, v := range values {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", name, v); err != nil {
				return err
			}
		}
	}

	if r.Body != nil && r.ContentLength != 0 {
		if _, err := fmt.Fprint(w, "Transfer-Encoding: chunked\r\n\r\n"); err != nil {
			return err
		}
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if _, werr := fmt.Fprintf(w, "%x\r\n", n); werr != nil {
					return werr
				}
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprint(w, "\r\n"); werr != nil {
					return werr
				}
			}
			if err != nil {
				break
			}
		}
		_, err := fmt.Fprint(w, "0\r\n\r\n")
		return err
	}
	_, err := fmt.Fprint(w, "Content-Length: 0\r\n\r\n")
	return err
}

// copyHTTPResponse copies a relayed HTTP response to the client, dropping
// hop-by-hop headers so the Go HTTP server can manage framing.
func copyHTTPResponse(w http.ResponseWriter, resp *http.Response) {
	for name, values := range resp.Header {
		if isHopByHop(name) {
			continue
		}
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if resp.Body != nil {
		_, _ = io.Copy(w, resp.Body)
	}
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}
