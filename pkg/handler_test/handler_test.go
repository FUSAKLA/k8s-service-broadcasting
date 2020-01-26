// Copyright 2019 FUSAKLA Martin Chodúr
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler_test

import (
	"fmt"
	"github.com/fusakla/k8s-service-broadcasting/pkg/handler"
	"github.com/magiconair/properties/assert"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

func getServerURL(URL string) string {
	return strings.TrimPrefix(URL, "http://")
}

type testCase struct {
	addresses      []string
	allMustSucceed bool
	keepalive      bool
	response       int
	timeout        time.Duration
}

func TestMultiplexingHandler_ServeHTTP(t *testing.T) {
	var (
		okServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := fmt.Fprintln(w, "OK", http.StatusOK)
			if err != nil {
				t.Error(err)
			}
		}))
		errServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "fail", http.StatusServiceUnavailable)
		}))
		okServerURL  = getServerURL(okServer.URL)
		errServerURL = getServerURL(errServer.URL)

		testCases = []testCase{

			{addresses: []string{okServerURL}, timeout: 0, allMustSucceed: true, keepalive: false, response: http.StatusGatewayTimeout},
			{addresses: []string{}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: false, response: http.StatusServiceUnavailable},

			{addresses: []string{okServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: false, response: http.StatusOK},
			{addresses: []string{okServerURL, okServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: false, response: http.StatusOK},
			{addresses: []string{okServerURL, okServerURL}, timeout: 60 * time.Second, allMustSucceed: false, keepalive: false, response: http.StatusOK},
			{addresses: []string{okServerURL, okServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: true, response: http.StatusOK},

			{addresses: []string{errServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: false, response: http.StatusServiceUnavailable},
			{addresses: []string{errServerURL}, timeout: 60 * time.Second, allMustSucceed: false, keepalive: false, response: http.StatusServiceUnavailable},
			{addresses: []string{errServerURL, errServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: true, response: http.StatusServiceUnavailable},

			{addresses: []string{okServerURL, errServerURL}, timeout: 60 * time.Second, allMustSucceed: true, keepalive: false, response: http.StatusServiceUnavailable},
			{addresses: []string{okServerURL, errServerURL}, timeout: 60 * time.Second, allMustSucceed: false, keepalive: false, response: http.StatusOK},
		}
	)
	defer okServer.Close()
	defer errServer.Close()

	fmt.Printf("okServer URL: %s\n", okServerURL)
	fmt.Printf("errServer URL: %s\n", errServerURL)

	for _, testCase := range testCases {
		fmt.Printf("\n\ntesting hosts: %v expected: %v\n", testCase.addresses, testCase.response)
		multiplexingHandler := handler.NewMultiplexingHandler("", testCase.timeout, testCase.allMustSucceed, testCase.keepalive)
		testedServer := httptest.NewServer(multiplexingHandler)
		multiplexingHandler.SetOwnAddress(testedServer.URL)
		multiplexingHandler.SetTargetAddresses(testCase.addresses)
		response, err := http.Get(testedServer.URL)
		if err != nil {
			t.Error(err)
		}
		testedServer.Close()
		assert.Equal(t, response.StatusCode, testCase.response)
		body, err := ioutil.ReadAll(response.Body)
		if err != nil {
			body = []byte{}
		}
		fmt.Printf("response status: %v body: %s\n", response.StatusCode, string(body))
	}

}
