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

package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/juju/persistent-cookiejar"
	"github.com/tgulacsi/go/crypthlp"
)

func (conf *config) getDaysSeries(dst chan<- Series, days ...string) error {
	defer close(dst)
	dates := make([]time.Time, 0, len(days))
	if len(days) == 0 {
		dates = append(dates, time.Now().Truncate(24*time.Hour))
	} else {
		for _, arg := range days {
			dt, err := time.Parse("2006-01-02", arg)
			if err != nil {
				Log.Error().Log("msg", "cannot parse given date as 2006-01-02",
					"date", arg, "error", err)
				continue
			}
			dates = append(dates, dt)
		}
	}

	if len(dates) == 2 && dates[0].Before(dates[1].AddDate(0, 0, -1)) {
		last := dates[1]
		dates = dates[:1]
		dt := dates[0]
		for {
			dt = dt.AddDate(0, 0, 1)
			if !dt.Before(last) {
				break
			}
			dates = append(dates, dt)
		}
		dates = append(dates, last)
	}

	conf.initURLs.Do(func() {
		repl := strings.NewReplacer("{{BASE}}", conf.BaseURL,
			"{{pvSystemID}}", conf.SystemID)
		conf.LogonURL = repl.Replace(conf.LogonURL)
		conf.DataURL = repl.Replace(conf.DataURL)
		var found bool
		conf.dateFormat = "2006-01-02"
		if i := strings.Index(conf.DataURL, "{{"); i >= 0 {
			if j := strings.Index(conf.DataURL[i+2:], "}}"); i >= 0 {
				if df := conf.DataURL[i+2 : i+2+j]; strings.Contains(df, "2006") {
					conf.dateFormat, found = df, true
				}
			}
		}
		if found {
			Log.Debug().Log("msg", "reference date format in dataURL: "+conf.dateFormat)
		} else {
			Log.Warn().Log("msg", `cannot find the reference date ("2006-01-02") in `+conf.DataURL+"!")
		}
	})

	df := "{{" + conf.dateFormat + "}}"
	var wg sync.WaitGroup
	errs := make(chan error, len(dates))
	var err error
	var errWg sync.WaitGroup
	errWg.Add(1)
	go func() {
		errWg.Done()
		for e := range errs {
			if err == nil {
				err = e
			}
		}
	}()
	for _, dt := range dates {
		dU := strings.Replace(conf.DataURL, df, dt.Format(conf.dateFormat), 1)
		wg.Add(1)
		go func(dU string) {
			defer wg.Done()
			data, err := conf.get(dU)
			if err != nil {
				errs <- err
				return
			}
			dst <- data
		}(dU)
	}
	wg.Wait()
	close(errs)
	errWg.Wait()
	return nil
}

type DataPoint struct {
	Time   time.Time
	Energy float64
}
type Series map[string][]DataPoint

var errLogonNeeded = errors.New("logon needed")

func (conf *config) get(dataURL string) (Series, error) {
	var err error
	var key *crypthlp.Key
	conf.initClient.Do(func() {
		var err error
		if conf.jar, err = cookiejar.New(nil); err != nil {
			return
		}
		if k, sr, err := openJar(conf.CookieJarPath, []byte(conf.SystemID)); err != nil {
			Log.Warn().Log("action", "openJar", "file", conf.CookieJarPath, "error", err)
		} else if err = conf.jar.ReadFrom(sr); err != nil {
			Log.Warn().Log("action", "Load", "file", conf.CookieJarPath, "error", err)
		} else {
			key = &k
		}

		conf.Client = &http.Client{
			Jar:     conf.jar,
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				Log.Debug().Log("req", req, "via", via)
				if strings.HasPrefix(req.URL.Path, "/Account/LogOn") {
					return errLogonNeeded
				}
				return nil
			},
		}
	})
	if err != nil {
		return nil, err
	}

	getURL := func(dataURL string) (*http.Response, error) {
		getLog := Log.With("url", dataURL)
		getLog.Debug().Log("method", "GET")
		resp, err := conf.Client.Get(dataURL)
		if err != nil {
			if ue, ok := err.(*url.Error); ok {
				if ue.Err == errLogonNeeded {
					return resp, ue.Err
				}
			}
			getLog.Error().Log("error", err)
			return resp, err
		}
		if resp.StatusCode > 299 {
			getLog.Warn().Log("status", resp.Status, "headers", resp.Header)
		} else {
			getLog.Debug().Log("status", resp.Status)
		}
		return resp, err
	}

	resp, err := getURL(dataURL)
	if err != nil && err != errLogonNeeded {
		return nil, err
	}
	if resp.Body != nil {
		defer func() {
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	if resp.StatusCode == 302 || err == errLogonNeeded {
		if resp, err = getURL(conf.LogonURL); err != nil {
			return nil, err
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		if resp.StatusCode > 299 {
			Log.With("url", conf.LogonURL).Warn().Log("status", resp.Status)
		}
		sw, err := saveJar(conf.CookieJarPath, []byte(conf.SystemID), key)
		if err != nil {
			return nil, err
		}
		conf.jarMu.Lock()
		err = conf.jar.WriteTo(sw)
		conf.jarMu.Unlock()
		if err != nil {
			return nil, err
		}
		if err = sw.Close(); err != nil {
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
		Log.With("url", dataURL).Warn().Log("status", resp.Status)
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
	ds := make(Series, len(detail.Series))
	n := 0
	for _, s := range detail.Series {
		m := make([]DataPoint, len(s.Data))
		for i, dp := range s.Data {
			//Log.Debug().Log("time", dp[0], "energy", dp[1])
			// ms
			m[i].Time, m[i].Energy = time.Unix(int64(dp[0])/1000, int64(dp[0])%1000), dp[1]
			n++

		}
		ds[s.Name] = m
	}
	Log.Info().Log("number_of_data_points", n)
	return ds, nil
}

func openJar(filename string, passphrase []byte) (crypthlp.Key, io.Reader, error) {
	return crypthlp.Open(filename, passphrase)
}

func saveJar(filename string, passphrase []byte, key *crypthlp.Key) (io.WriteCloser, error) {
	if key != nil {
		fh, err := os.Create(filename)
		if err != nil {
			return nil, err
		}
		return key.CreateWriter(fh)
	}
	return crypthlp.Create(filename, passphrase, 5*time.Second)
}
