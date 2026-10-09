package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// confirmCall makes a call the proxy answers once the user answered its
// question — rpc yolo, apply_config, trust — so without rpc.CallTimeout,
// and decodes its result into result, if not nil. Ctrl+C gives up on it by
// closing the connection for writing, not whole: the proxy takes the
// question off the screen and only then answers, so what aish prints next
// comes below the question, not over its lines. A second Ctrl+C, or
// giveUpAfter, closes the connection. interrupted: Ctrl+C came; the proxy
// may have taken the Yes first, and then err is nil.
func confirmCall(path, method string, params, result any) (interrupted bool, err error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.Dial("unix", path)
	if err != nil {
		return false, err
	}
	conn := c.(*net.UnixConn)
	defer conn.Close()
	req := rpc.Request{Method: method}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() {
		var resp rpc.Response
		err := json.NewDecoder(conn).Decode(&resp)
		switch {
		case err != nil:
		case resp.Error != "":
			err = errors.New(resp.Error)
		case result != nil && resp.Result != nil:
			err = json.Unmarshal(resp.Result, result)
		}
		done <- err
	}()
	for {
		select {
		case err := <-done:
			return interrupted, err
		case <-sig:
			if interrupted {
				return true, errors.New("gave up on the proxy")
			}
			interrupted = true
			_ = conn.CloseWrite()
			_ = conn.SetReadDeadline(time.Now().Add(giveUpAfter))
		}
	}
}
