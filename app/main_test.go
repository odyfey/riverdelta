package main

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/odyfey/riverdelta/app/pgtest"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	pgtest.Main(m)
}

func TestServeStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	srv := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: readHeaderTimeout}

	require.NoError(t, serve(ctx, srv))
}

func TestServeAddressInUseReturnsError(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ln.Close()) })
	srv := &http.Server{Addr: ln.Addr().String(), ReadHeaderTimeout: readHeaderTimeout}

	err = serve(t.Context(), srv)
	require.ErrorContains(t, err, "failed to serve http")
	require.ErrorContains(t, err, "address already in use")
}
