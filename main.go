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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
		flagDataURL       = flag.String("data", "{{BASE}}/NewCharts/GetDetailData/{{pvSystemID}}/00000000-0000-0000-0000-000000000000/Day/{{2006/1/2}}",
			"URL of the detail data; the Go reference date (2006-01-02) will be replaced with the current date, in the given format.")
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
	dateFormat, found := "2006-01-02", false
	if i := strings.Index(dataURL, "{{"); i >= 0 {
		if j := strings.Index(dataURL[i+2:], "}}"); i >= 0 {
			if df := dataURL[i+2 : i+2+j]; strings.Contains(df, "2006") {
				dateFormat, found = df, true
			}
		}
	}
	if found {
		Log.Debug("reference date format in dataURL: " + dateFormat)
	} else {
		Log.Warn(`cannot find the reference date ("2006-01-02") in ` + dataURL + "!")
	}

	df := "{{" + dateFormat + "}}"
	for _, dt := range dates {
		dU := strings.Replace(dataURL, df, dt.Format(dateFormat), 1)
		data, err := get(*flagCookieJarPath, logonURL, dU)
		if err != nil {
			Log.Error("get", "error", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "%v", data)
	}
}

type DataPoint struct {
	Time   time.Time
	Energy float64
}
type Series map[string][]DataPoint

func get(cookieJarPath, logonURL, dataURL string) (Series, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode == 302 || err == errLogonNeeded {
		if resp, err = getURL(logonURL); err != nil {
			return nil, err
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		if resp.StatusCode > 299 {
			Log.Warn("logon", "status", resp.Status)
		}
		if err = jar.Save(); err != nil {
			return nil, err
		}
		if resp, err = getURL(dataURL); err != nil {
			return nil, err
		}
		if resp.Body != nil {
			defer resp.Body.Close()
		}
	}
	if resp.StatusCode > 299 {
		Log.Warn("data", "status", resp.Status)
	}

	type (
		titleText struct {
			Text string `json:"text"`
		}
		yAxis struct {
			Title titleText `json:"title"`
		}
		serie struct {
			Name  string       `json:"name"`
			YAxis int          `json:"yAxis"`
			Data  [][2]float64 `json:"data"`
		}
		detailData struct {
			YAxis  []yAxis `json:"yAxis"`
			Energy string  `json:"energy"`
			Unit   string  `json:"unit"`
			Series []serie `json:"series"`
		}
	)
	//_, _ = io.Copy(os.Stderr, resp.Body)
	dec := json.NewDecoder(resp.Body)
	var detail detailData
	if err := dec.Decode(&detail); err != nil {
		return nil, err
	}
	Log.Debug("detail", "data", detail)
	ds := make(Series, len(detail.Series))
	for _, s := range detail.Series {
		m := make([]DataPoint, len(s.Data))
		for i, dp := range s.Data {
			//Log.Debug("time", "time", dp[0], "energy", dp[1])
			// ms
			m[i].Time, m[i].Energy = time.Unix(int64(dp[0])/1000, int64(dp[0])%1000), dp[1]
		}
		ds[s.Name] = m
	}
	return ds, nil
}
