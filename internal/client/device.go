package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// ErrNoBrowserLogin means the server can't sign a CLI in through the
// browser, so it needs an API token instead.
var ErrNoBrowserLogin = errors.New("this server has no browser sign-in")

// DeviceLogin signs this machine in through the browser: it gets a code
// from the server, calls show with it so the person can open the page and
// approve, then polls until they do. device names the machine on the
// approval page and the token.
func DeviceLogin(ctx context.Context, server, device string, show func(api.DeviceCode)) (api.DeviceToken, error) {
	server = strings.TrimRight(server, "/")
	hc := &http.Client{Timeout: 30 * time.Second}
	post := func(path string, body any, out any) (int, error) {
		b, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, bytes.NewReader(b))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var e api.Error
			json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
			return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, e.Error)
		}
		return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out)
	}

	var dc api.DeviceCode
	code, err := post("/v1/device/codes", api.DeviceCodeRequest{Device: device}, &dc)
	if code == http.StatusNotFound {
		return api.DeviceToken{}, ErrNoBrowserLogin
	}
	if err != nil {
		return api.DeviceToken{}, err
	}
	show(dc)

	every := time.Duration(max(1, dc.Interval)) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return api.DeviceToken{}, ctx.Err()
		case <-time.After(every):
		}
		var res api.DeviceToken
		if _, err := post("/v1/device/token", api.DeviceTokenRequest{DeviceCode: dc.DeviceCode}, &res); err != nil {
			return api.DeviceToken{}, err
		}
		switch res.Status {
		case "ok":
			return res, nil
		case "denied":
			return res, errors.New("the sign-in was cancelled in the browser")
		case "expired":
			return res, errors.New("the code expired before it was approved; run ephemera login again")
		}
		if time.Now().After(deadline) {
			return res, errors.New("the code expired before it was approved; run ephemera login again")
		}
	}
}
