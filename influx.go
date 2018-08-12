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
	"net/url"
	"os"
	"time"

	"github.com/pkg/errors"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	influx "github.com/influxdb/influxdb/client"
)

type influxClient struct {
	*influx.Client
	Database, RetentionPolicy string

	Logger log.Logger
}

func newInfluxClient(influxDB, database, retentionPolicy string, logger log.Logger) (influxClient, error) {
	var ic influxClient
	u, err := url.Parse(influxDB)
	if err != nil {
		return ic, errors.Wrapf(err, "parse %q", influxDB)
	}
	influxConf := influx.Config{
		URL:      *u,
		Username: os.Getenv("INFLUX_USER"),
		Password: os.Getenv("INFLUX_PASSW"),
	}
	con, err := influx.NewClient(influxConf)
	if err != nil {
		return ic, errors.Wrapf(err, "%#v", influxConf)
	}
	level.Debug(logger).Log("connected", "server", con)
	if _, _, err = con.Ping(); err != nil {
		return ic, errors.Wrapf(err, "ping")
	}
	return influxClient{Client: con, Database: database, RetentionPolicy: retentionPolicy, Logger: logger}, nil
}

type dataPoint struct {
	time.Time
	Name  string
	Value float64
	Unit  string
}

func (c influxClient) Put(measurement string, points ...dataPoint) error {
	ip := make([]influx.Point, len(points))
	for i, p := range points {
		ip[i] = influx.Point{
			Measurement: measurement,
			Tags:        map[string]string{"name": p.Name},
			Fields:      map[string]interface{}{"energy": p.Value, "unit": p.Unit},
			Time:        p.Time,
		}
	}

	bps := influx.BatchPoints{
		Points:          ip,
		Database:        c.Database,
		RetentionPolicy: c.RetentionPolicy,
	}
	_, err := c.Client.Write(bps)
	return err
}
