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
	"fmt"
	stdlog "log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/levels"
	"github.com/juju/persistent-cookiejar"
	"github.com/spf13/cobra"
)

var (
	logger = log.NewLogfmtLogger(os.Stderr)
	Log    = levels.New(logger)
)

type config struct {
	SystemID                   string
	BaseURL, DataURL, LogonURL string
	CookieJarPath              string

	dateFormat string
	initURLs   sync.Once

	jar   *cookiejar.Jar
	jarMu sync.Mutex
	*http.Client
	initClient sync.Once
}

func main() {
	stdlog.SetOutput(log.NewStdlibAdapter(logger))
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
			for data := range conf.dumpFromArgs(args) {
				for k, points := range data {
					for _, p := range points {
						fmt.Fprintf(os.Stdout, "%q;%q;%.3f\n", k, p.Time, p.Energy)
					}
				}
			}
		},
	}

	var (
		influxDB        = "http://localhost:8086"
		database        = "fronius"
		retentionPolicy = "default"
		servePath       = "/solarapi/v1/current/"
	)

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "accept push from the fronius datalogger",
		Run: func(_ *cobra.Command, args []string) {
			con, err := newInfluxClient(influxDB, database, retentionPolicy)
			if err != nil {
				Log.Crit().Log("msg", "influx connection", "error", err)
				os.Exit(1)
			}

			http.Handle(servePath, solarAPIAccept{con})
			addr := ":15015"
			if len(args) > 0 {
				addr = args[0]
			}
			Log.Info().Log("msg", "Start listening", "address", addr, "path", servePath)
			http.ListenAndServe(addr, nil)
		},
	}
	f := serveCmd.Flags()
	f.StringVar(&servePath, "serve.path", servePath, "HTTP endpoint to publish")
	f.StringVar(&influxDB, "server", influxDB, "influx server to connect to")
	f.StringVar(&database, "database", database, "influx database to insert data into")
	f.StringVar(&retentionPolicy, "retention", retentionPolicy, "retention policy to use")

	mainCmd := &cobra.Command{
		Use: "fronius",
		Run: func(_ *cobra.Command, args []string) {
			dumpCmd.Run(dumpCmd, args)
		},
	}
	mainCmd.AddCommand(dumpCmd, serveCmd)
	pflags := mainCmd.PersistentFlags()
	pflags.StringVar(&conf.CookieJarPath, "cookiejar", conf.CookieJarPath, "path to the cookie storage file")
	pflags.StringVar(&conf.BaseURL, "base", conf.BaseURL, "Solar.Web's base URL")
	pflags.StringVar(&conf.LogonURL, "logon", conf.LogonURL, "Logon URL")
	pflags.StringVar(&conf.DataURL, "data", conf.DataURL,
		"URL of the detail data; the Go reference date (2006-01-02) will be replaced with the current date, in the given format.")

	influxCmd := &cobra.Command{
		Use:   "influx",
		Short: "insert data into the InfluxDB specified with the --server flag",
		Run: func(_ *cobra.Command, args []string) {
			ic, err := newInfluxClient(influxDB, database, retentionPolicy)
			if err != nil {
				Log.Crit().Log("msg", "influx connection", "error", err)
				os.Exit(1)
			}

			points := make([]dataPoint, 0, 512)
			for data := range conf.dumpFromArgs(args) {
				for k, dps := range data {
					for _, p := range dps {
						points = append(points,
							dataPoint{Name: k, Value: p.Energy, Time: p.Time, Precision: "kWh"})
					}
				}
			}
			if err := ic.Put("fronius energy", points...); err != nil {
				Log.Error().Log("msg", "write batch to db", "error", err)
				os.Exit(2)
			}
		},
	}
	f = influxCmd.Flags()
	f.StringVar(&influxDB, "server", influxDB, "influx server to connect to")
	f.StringVar(&database, "database", database, "influx database to insert data into")
	f.StringVar(&retentionPolicy, "retention", retentionPolicy, "retention policy to use")
	mainCmd.AddCommand(influxCmd)

	if _, _, err := mainCmd.Find(os.Args[1:]); err != nil && strings.HasPrefix(err.Error(), "unknown command") {
		mainCmd.SetArgs(append([]string{"dump"}, os.Args[1:]...))
	}
	mainCmd.Execute()
}

func (conf *config) dumpFromArgs(args []string) chan Series {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "first argument must be the pvSystemID!")
		os.Exit(1)
	}
	conf.SystemID = args[0]
	c := make(chan Series, 1)
	go func() {
		if err := conf.getDaysSeries(c, args[1:]...); err != nil {
			Log.Error().Log("getDaysSeries", "args", args, "error", err)
			os.Exit(2)
		}
	}()
	return c
}
