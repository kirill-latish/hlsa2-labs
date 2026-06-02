// Package svchelp gathers tiny helpers shared by every saga
// participant service so the per-service main.go files can stay
// focused on the business mutation.
package svchelp

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/hlsa2-labs/lab4-2/internal/fault"
	"github.com/hlsa2-labs/lab4-2/internal/payloads"
)

// EnvOrDefault returns os.Getenv(key) or def if empty.
func EnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ApplyFault enforces the current fault spec for `svc` from the
// fault-injector. Callers should call this at the very top of every
// saga handler.
func ApplyFault(fc *fault.Client, svc string) error {
	if fc == nil {
		return nil
	}
	spec := fc.Get(svc)
	switch spec.Mode {
	case fault.ModeLatency:
		time.Sleep(time.Duration(spec.P99MS) * time.Millisecond)
	case fault.ModeFail:
		return errors.New(svc + ": injected failure")
	}
	return nil
}

// WriteOK responds 200 with an "ok" body. Used by saga endpoints.
func WriteOK(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": msg,
		"id":      uuid.NewString(),
	})
}

// WriteXA responds with an XAResponse and the requested status code.
func WriteXA(w http.ResponseWriter, status int, ok bool, state, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payloads.XAResponse{OK: ok, State: state, Error: errMsg})
}
