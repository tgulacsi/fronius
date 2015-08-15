// Copyright 2015 Tamás Gulácsi
//
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

// Package main of fronius gets the data from Solar.Web
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/juju/persistent-cookiejar"
	"gopkg.in/inconshreveable/log15.v2"
)

var Log = log15.New()

func main() {
	Log.SetHandler(log15.StderrHandler)
	var (
		flagCookieJarPath = flag.String("cookiejar", "fronius.cookies", "path to the cookie storage file")
		flagBaseURL       = flag.String("base", "https://www.solarweb.com", "Solar.Web's base URL")
		flagLogonURL      = flag.String("logon", "{{BASE}}/Account/GuestLogOn?pvSystemId={{pvSystemID}}", "Logon URL")
		flagDataURL       = flag.String("data", "{{BASE}}/NewCharts/GetDetailData/{{pvSystemID}}/00000000-0000-0000-0000-000000000000/Day/{{2006/1/2}}", "URL of the detail data")
	)
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "first argument must be the pvSystemID!")
		os.Exit(1)
	}
	pvSystemID := flag.Arg(0)
	dates := make([]time.Time, 0, flag.NArg())
	if flag.NArg() == 1 {
		dates = append(dates, time.Now().Truncate(24*time.Hour))
	} else {
		for _, arg := range flag.Args()[1:] {
			dt, err := time.Parse("2006-01-02", arg)
			if err != nil {
				Log.Error("cannot parse given date as 2006-01-02", "date", arg, "error", err)
				continue
			}
			dates = append(dates, dt)
		}
	}

	repl := strings.NewReplacer("{{BASE}}", *flagBaseURL,
		"{{pvSystemID}}", pvSystemID)
	logonURL := repl.Replace(*flagLogonURL)
	dataURL := repl.Replace(*flagDataURL)
	for _, dt := range dates {
		// TODO(tgulacsi): generalize this, get the date format from the URL.
		dU := strings.Replace(dataURL, "{{2006/1/2}}", dt.Format("2006/1/2"), 1)
		if err := get(*flagCookieJarPath, logonURL, dU); err != nil {
			Log.Error("get", "error", err)
			os.Exit(1)
		}
	}
}
func get(cookieJarPath, logonURL, dataURL string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	if err := jar.Load(cookieJarPath); err != nil {
		Log.Warn("Load", "file", cookieJarPath, "error", err)
	}

	errLogonNeeded := errors.New("logon needed")

	cl := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			Log.Debug("Redirect", "req", req, "via", via)
			if strings.HasPrefix(req.URL.Path, "/Account/LogOn") {
				return errLogonNeeded
			}
			return nil
		},
	}
	getURL := func(dataURL string) (*http.Response, error) {
		getLog := Log.New("url", dataURL)
		getLog.Debug("GET")
		resp, err := cl.Get(dataURL)
		if err != nil {
			if ue, ok := err.(*url.Error); ok {
				if ue.Err == errLogonNeeded {
					return resp, ue.Err
				}
			}
			getLog.Error("response", "error", err)
			return resp, err
		}
		if resp.StatusCode > 299 {
			getLog.Warn("response", "status", resp.Status, "headers", resp.Header)
		} else {
			getLog.Debug("response", "status", resp.Status)
		}
		return resp, err
	}

	resp, err := getURL(dataURL)
	if err != nil && err != errLogonNeeded {
		return err
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode == 302 || err == errLogonNeeded {
		if resp, err = getURL(logonURL); err != nil {
			return err
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		if resp.StatusCode > 299 {
			Log.Warn("logon", "status", resp.Status)
		}
		if err = jar.Save(); err != nil {
			return err
		}
		if resp, err = getURL(dataURL); err != nil {
			return err
		}
		if resp.Body != nil {
			defer resp.Body.Close()
		}
	}
	if resp.StatusCode > 299 {
		Log.Warn("data", "status", resp.Status)
	}

	_, _ = io.Copy(os.Stderr, resp.Body)
	return nil
}
