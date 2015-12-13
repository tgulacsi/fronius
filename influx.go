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

	"gopkg.in/errgo.v1"

	influx "github.com/influxdb/influxdb/client"
)

type influxClient struct {
	*influx.Client
	Database, RetentionPolicy string
}

func newInfluxClient(influxDB, database, retentionPolicy string) (influxClient, error) {
	var ic influxClient
	u, err := url.Parse(influxDB)
	if err != nil {
		return ic, errgo.Notef(err, "parse %q", influxDB)
	}
	influxConf := influx.Config{
		URL:      *u,
		Username: os.Getenv("INFLUX_USER"),
		Password: os.Getenv("INFLUX_PASSW"),
	}
	con, err := influx.NewClient(influxConf)
	if err != nil {
		return ic, errgo.Notef(err, "%#v", influxConf)
	}
	Log.Debug().Log("connected", "server", con)
	if _, _, err = con.Ping(); err != nil {
		return ic, errgo.Notef(err, "ping")
	}
	return influxClient{Client: con, Database: database, RetentionPolicy: retentionPolicy}, nil
}

type dataPoint struct {
	time.Time
	Name      string
	Value     interface{}
	Precision string
}

func (c influxClient) Put(measurement string, points ...dataPoint) error {
	ip := make([]influx.Point, len(points))
	for i, p := range points {
		ip[i] = influx.Point{
			Measurement: measurement,
			Tags:        map[string]string{"name": p.Name},
			Fields:      map[string]interface{}{"energy": p.Value},
			Time:        p.Time,
			Precision:   p.Precision,
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
