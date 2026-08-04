package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthz_GET_Returns200WithOKBody(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewMux().ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/json", res.Header.Get("Content-Type"))
	require.Equal(t, `{"status":"ok"}`, rec.Body.String())
}

func TestHealthz_WrongMethod_IsNotRouted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
	}{
		{name: "post", method: http.MethodPost},
		{name: "put", method: http.MethodPut},
		{name: "delete", method: http.MethodDelete},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/healthz", nil)

			NewMux().ServeHTTP(rec, req)

			res := rec.Result()
			t.Cleanup(func() { _ = res.Body.Close() })

			require.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
		})
	}
}
