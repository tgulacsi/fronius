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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	influx "github.com/influxdb/influxdb/client"
	"github.com/juju/persistent-cookiejar"
	"github.com/spf13/cobra"
	"gopkg.in/inconshreveable/log15.v2"
)

var Log = log15.New()

type config struct {
	SystemID                   string
	BaseURL, DataURL, LogonURL string
	CookieJarPath              string
}

func main() {
	Log.SetHandler(log15.StderrHandler)

	var (
		conf = config{
			CookieJarPath: "fronius.cookies",
			BaseURL:       "https://www.solarweb.com",
			LogonURL:      "{{BASE}}/Account/GuestLogOn?pvSystemId={{pvSystemID}}",
			DataURL:       "{{BASE}}/NewCharts/GetDetailData/{{pvSystemID}}/00000000-0000-0000-0000-000000000000/Day/{{2006/1/2}}",
		}
	)

	dumpCmd := &cobra.Command{
		Use:   "dump",
		Short: "dump data points from the given days (today is the default)",
		Run: func(_ *cobra.Command, args []string) {
			if len(args) == 0 {
				fmt.Fprintf(os.Stderr, "first argument must be the pvSystemID!")
				os.Exit(1)
			}
			conf.SystemID = args[0]
			c := make(chan Series, 1)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for data := range c {
					for k, points := range data {
						for _, p := range points {
							fmt.Fprintf(os.Stdout, "%q;%q;%.3f\n", k, p.Time, p.Energy)
						}
					}
				}
			}()
			if err := conf.getDaysSeries(c, args[1:]...); err != nil {
				Log.Error("getDaysSeries", "args", args, "error", err)
				os.Exit(2)
			}
			wg.Wait()
		},
	}

	mainCmd := &cobra.Command{
		Use: "fronius",
		Run: func(_ *cobra.Command, args []string) {
			dumpCmd.Run(dumpCmd, args)
		},
	}
	mainCmd.AddCommand(dumpCmd)
	pflags := mainCmd.PersistentFlags()
	pflags.StringVar(&conf.CookieJarPath, "cookiejar", conf.CookieJarPath, "path to the cookie storage file")
	pflags.StringVar(&conf.BaseURL, "base", conf.BaseURL, "Solar.Web's base URL")
	pflags.StringVar(&conf.LogonURL, "logon", conf.LogonURL, "Logon URL")
	pflags.StringVar(&conf.DataURL, "data", conf.DataURL,
		"URL of the detail data; the Go reference date (2006-01-02) will be replaced with the current date, in the given format.")

	influxDB := "http://localhost:8086"
	influxCmd := &cobra.Command{
		Use:   "influx",
		Short: "insert data into the InfluxDB specified with the --server flag",
		Run: func(_ *cobra.Command, args []string) {
			u, err := url.Parse(influxDB)
			if err != nil {
				Log.Crit("parse influx", "URL", influxDB, "error", err)
				os.Exit(1)
			}
			conf := influx.Config{
				URL:      *u,
				Username: os.Getenv("INFLUX_USER"),
				Password: os.Getenv("INFLUX_PASSW"),
			}
			con, err := influx.NewClient(conf)
			if err != nil {
				Log.Error("connect to influx DB", "config", conf, "error", err)
				os.Exit(1)
			}
			Log.Debug("connected", "server", con)
		},
	}
	influxCmd.Flags().StringVar(&influxDB, "server", influxDB, "influx database to insert data into")
	mainCmd.AddCommand(influxCmd)

	if _, _, err := mainCmd.Find(os.Args[1:]); err != nil && strings.HasPrefix(err.Error(), "unknown command") {
		mainCmd.SetArgs(append([]string{"dump"}, os.Args[1:]...))
	}
	mainCmd.Execute()
}

func (conf config) getDaysSeries(dst chan<- Series, days ...string) error {
	defer close(dst)
	dates := make([]time.Time, 0, len(days))
	if len(days) == 0 {
		dates = append(dates, time.Now().Truncate(24*time.Hour))
	} else {
		for _, arg := range days {
			dt, err := time.Parse("2006-01-02", arg)
			if err != nil {
				Log.Error("cannot parse given date as 2006-01-02", "date", arg, "error", err)
				continue
			}
			dates = append(dates, dt)
		}
	}

	repl := strings.NewReplacer("{{BASE}}", conf.BaseURL,
		"{{pvSystemID}}", conf.SystemID)
	conf.LogonURL = repl.Replace(conf.LogonURL)
	conf.DataURL = repl.Replace(conf.DataURL)
	dateFormat, found := "2006-01-02", false
	if i := strings.Index(conf.DataURL, "{{"); i >= 0 {
		if j := strings.Index(conf.DataURL[i+2:], "}}"); i >= 0 {
			if df := conf.DataURL[i+2 : i+2+j]; strings.Contains(df, "2006") {
				dateFormat, found = df, true
			}
		}
	}
	if found {
		Log.Debug("reference date format in dataURL: " + dateFormat)
	} else {
		Log.Warn(`cannot find the reference date ("2006-01-02") in ` + conf.DataURL + "!")
	}

	df := "{{" + dateFormat + "}}"
	for _, dt := range dates {
		dU := strings.Replace(conf.DataURL, df, dt.Format(dateFormat), 1)
		data, err := conf.get(dU)
		if err != nil {
			return err
		}
		dst <- data
	}
	return nil
}

type DataPoint struct {
	Time   time.Time
	Energy float64
}
type Series map[string][]DataPoint

func (conf config) get(dataURL string) (Series, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if err := jar.Load(conf.CookieJarPath); err != nil {
		Log.Warn("Load", "file", conf.CookieJarPath, "error", err)
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
		if resp, err = getURL(conf.LogonURL); err != nil {
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
