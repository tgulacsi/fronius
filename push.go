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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"
)

type solarAPIAccept struct {
	influxClient
}

func (sa solarAPIAccept) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	Log.Info().Log("msg", r.Method, "url", r.URL, "header", fmt.Sprintf("%#v", r.Header))
	if r.Body != nil {
		defer func() {
			io.Copy(ioutil.Discard, r.Body)
			r.Body.Close()
		}()
	}
	var buf bytes.Buffer
	var data solarV1CurrentInverter
	if err := json.NewDecoder(io.TeeReader(r.Body, &buf)).Decode(&data); err != nil {
		Log.Error().Log("msg", "decode", "message", buf.String(), "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log.Debug().Log("msg", "decoded", "data", fmt.Sprintf("%#v", data))
	w.WriteHeader(200)

	if err := sa.influxClient.Put("fronius energy",
		[]dataPoint{
			{Name: "pac", Time: data.Head.Timestamp,
				Unit:  data.Body.Pac.Unit,
				Value: data.Body.Pac.Values["1"]},
			{Name: "day", Time: data.Head.Timestamp,
				Unit:  data.Body.Day.Unit,
				Value: data.Body.Day.Values["1"]},
			{Name: "year", Time: data.Head.Timestamp,
				Unit:  data.Body.Year.Unit,
				Value: data.Body.Year.Values["1"]},
			{Name: "total", Time: data.Head.Timestamp,
				Unit:  data.Body.Total.Unit,
				Value: data.Body.Total.Values["1"]},
		}...); err != nil {
		Log.Error().Log("msg", "write batch to db", "error", err)
	}
}

type solarV1CurrentInverter struct {
	Head currentHeader
	Body energyData
}

type currentHeader struct {
	Timestamp        time.Time
	RequestArguments struct {
		Query string
		Scope string
	}
	Status struct {
		Code                int
		Reason, UserMessage string
	}
}

type energyData struct {
	Pac   energy `json:"PAC"`
	Day   energy `json:"DAY_ENERGY"`
	Year  energy `json:"YEAR_ENERGY"`
	Total energy `json:"TOTAL_ENERGY"`
}

type energy struct {
	Unit   string
	Values map[string]float64
}
