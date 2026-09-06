package plaud

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// LoginOptions steer the interactive consent flow.
type LoginOptions struct {
	// Open launches the consent URL in a browser; nil means the operator
	// pastes the URL printed to Progress.
	Open func(url string) error
	// Progress receives human guidance (the URL, "waiting…"); never token
	// material. Nil discards it.
	Progress io.Writer
	// Timeout bounds the wait for the browser callback (default 2 minutes).
	Timeout time.Duration
}

// ErrLoginTimeout is returned when the browser never came back.
var ErrLoginTimeout = errors.New("timed out waiting for the browser callback")

// Login runs the PKCE flow end to end: listens on the registered redirect
// URI, sends the operator to the consent page, exchanges the code and returns
// the raw token response. Nothing is persisted here — the caller decides
// where the refresh token goes.
func (c Config) Login(ctx context.Context, opts LoginOptions) (Token, error) {
	c = c.withDefaults()
	if opts.Progress == nil {
		opts.Progress = io.Discard
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	redirect, err := url.Parse(c.RedirectURI)
	if err != nil {
		return Token{}, fmt.Errorf("redirect URI: %w", err)
	}
	pkce, err := NewPKCE()
	if err != nil {
		return Token{}, err
	}

	listener, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return Token{}, fmt.Errorf("listen on %s: %w — another `plaud login` may still be running; wait a few seconds and retry", redirect.Host, err)
	}
	defer listener.Close()

	type outcome struct {
		token Token
		err   error
	}
	done := make(chan outcome, 1)
	settle := func(o outcome) {
		select {
		case done <- o:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// State is checked before anything else is honoured: a stray or forged
		// hit on the fixed localhost port (error or code) must not settle or
		// describe this attempt.
		if q.Get("state") != pkce.State {
			fmt.Fprint(w, page("Continue authorization in the original window.", "This page can be closed."))
			return
		}
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, page("Authorization failed", desc))
			settle(outcome{err: fmt.Errorf("authorization denied: %s", desc)})
			return
		}
		code := q.Get("code")
		if code == "" {
			fmt.Fprint(w, page("Continue authorization in the original window.", "This page can be closed."))
			return
		}
		token, err := c.Exchange(r.Context(), code, pkce)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, page("Authorization failed", err.Error()))
			settle(outcome{err: err})
			return
		}
		fmt.Fprint(w, page("Authorization successful!", "You can close this tab."))
		settle(outcome{token: token})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	consent := c.ConsentURL(pkce)
	fmt.Fprintf(opts.Progress, "Open this URL to authorize Plaud access:\n%s\n", consent)
	if opts.Open != nil {
		if err := opts.Open(consent); err != nil {
			fmt.Fprintf(opts.Progress, "(could not open a browser automatically: %v)\n", err)
		}
	}
	fmt.Fprintf(opts.Progress, "Waiting for the browser callback on %s (up to %s)…\n", c.RedirectURI, opts.Timeout)

	select {
	case o := <-done:
		return o.token, o.err
	case <-time.After(opts.Timeout):
		return Token{}, ErrLoginTimeout
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}
}

func page(title, detail string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>Plaud</title></head>` +
		`<body style="font-family:system-ui;padding:2rem;text-align:center;"><h1>` +
		html.EscapeString(title) + `</h1><p>` + html.EscapeString(detail) + `</p></body></html>`
}
