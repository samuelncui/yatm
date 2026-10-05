package tools

import (
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/sirupsen/logrus"
)

// NewDebugServer .
func NewDebugServer(addr string) {
	debugMux := http.NewServeMux()

	debugMux.HandleFunc("/debug/pprof/", pprof.Index)
	debugMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	debugMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	debugMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	debugMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if err := http.ListenAndServe(addr, debugMux); err != nil {
		if err == nil {
			return
		}

		if strings.Contains(err.Error(), "interrupt") {
			return
		}

		logrus.WithError(err).Errorf("debug server listen and serve fail: addr= %s", addr)
	}
}
