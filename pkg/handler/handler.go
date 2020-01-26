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

package handler

import (
	"bytes"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	requestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "request_duration_seconds",
			Help: "Duration of HTTP requests.",
		},
		[]string{"type", "endpoint", "status_code"},
	)
)

func init() {
	prometheus.MustRegister(requestDurationSeconds)
}

func NewMultiplexingHandler(ownAddress string, timeout time.Duration, allMustSucceed, keepalive bool) *multiplexingHandler {
	return &multiplexingHandler{
		ownAddress:           ownAddress,
		timeout:              timeout,
		allMustSucceed:       allMustSucceed,
		keepalive:            keepalive,
		targetAddresses:      &[]string{},
		targetAddressesMutex: sync.Mutex{},
	}
}

type multiplexingHandler struct {
	ownAddress           string
	timeout              time.Duration
	allMustSucceed       bool
	keepalive            bool
	targetAddresses      *[]string
	targetAddressesMutex sync.Mutex
}

func (h *multiplexingHandler) GetTargetAddresses() []string {
	h.targetAddressesMutex.Lock()
	defer h.targetAddressesMutex.Unlock()
	return *h.targetAddresses
}

func (h *multiplexingHandler) SetTargetAddresses(addresses []string) {
	h.targetAddressesMutex.Lock()
	defer h.targetAddressesMutex.Unlock()
	h.targetAddresses = &addresses
}

func (h *multiplexingHandler) SetOwnAddress(addr string) {
	h.ownAddress = addr
}

func (h *multiplexingHandler) handleRequest(req *http.Request) *http.Response {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   h.timeout,
			KeepAlive: 10 * h.timeout,
		}).DialContext,
		DisableKeepAlives:     !h.keepalive,
		TLSHandshakeTimeout:   h.timeout,
		ResponseHeaderTimeout: h.timeout,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		resp = &http.Response{Request: req, StatusCode: 500, Status: fmt.Sprint(err), Body: ioutil.NopCloser(strings.NewReader(fmt.Sprint(err)))}
	}
	return resp
}

func (h *multiplexingHandler) decideFinalResponse(totalCount int, successfulResponses, failedResponses []*http.Response) *http.Response {
	failedCount := len(failedResponses)
	succeededCount := len(successfulResponses)

	if failedCount == 0 && succeededCount == 0 {
		return newResponse(http.StatusServiceUnavailable, "no endpoints to query")
	}
	if succeededCount >= totalCount {
		return randomResponse(successfulResponses)
	}
	if failedCount == totalCount {
		return randomResponse(failedResponses)
	}
	if failedCount > 0 && h.allMustSucceed {
		return randomResponse(failedResponses)
	}
	return newResponse(http.StatusServiceUnavailable, "unknown error")
}

func (h *multiplexingHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), h.timeout)
	defer cancelFunc()
	start := time.Now()
	reqId := uuid.New()
	reqLog := log.WithField("reqId", reqId)
	reqLog.Debugf("received request %v, mirroring to targets...", req.URL)
	alreadySent := false

	// Send requests to all targets in parallel and put the responses to channel
	targets := h.GetTargetAddresses()
	targetsCount := len(targets)

	responseChannel := make(chan *http.Response, targetsCount)
	wg := sync.WaitGroup{}

	for _, i := range rand.Perm(targetsCount) {
		duplicate := duplicateRequest(req).WithContext(ctx)
		if err := setRequestTarget(duplicate, targets[i], "http"); err != nil {
			reqLog.Errorf("Failed to replace new target address, error: %v", err)
			continue
		}
		wg.Add(1)
		go func() {
			responseChannel <- h.handleRequest(duplicate)
			wg.Done()
		}()
	}

	go func() {
		wg.Wait()
		close(responseChannel)
	}()

	// Check all responses from the channel
	requestCounter := 0
	var successfulResponses, failedResponses []*http.Response
mainLoop:
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() != context.DeadlineExceeded {
				continue
			}
			reqLog.Error("request timed out")
			cancelFunc()
			timeoutResponse := http.Response{
				StatusCode: http.StatusGatewayTimeout,
				Body:       ioutil.NopCloser(bytes.NewBufferString("request timed out")),
			}
			sendResponse(w, &timeoutResponse)
			return
		case resp, ok := <-responseChannel:
			if !ok {
				reqLog.Debug("done processing all broadcasted requests")
				break mainLoop
			}
			requestCounter++
			if resp.StatusCode >= 400 {
				buf := new(bytes.Buffer)
				if _, err := buf.ReadFrom(resp.Body); err != nil {
					reqLog.Errorf("failed to read response body: %v", err)
				}
				errorMsg := buf.String()
				reqLog.Warnf("replica=%v request=%v status_code=%v error=%v", requestCounter, resp.Request.URL, resp.StatusCode, errorMsg)
				failedResponses = append(failedResponses, resp)
			} else {
				reqLog.Debugf("replica=%v request=%v status_code=%v", requestCounter, resp.Request.URL, resp.StatusCode)
				successfulResponses = append(successfulResponses, resp)
				if !h.allMustSucceed && !alreadySent {
					sendResponse(w, resp)
					alreadySent = true
				}
			}
		}
	}

	finalResponse := h.decideFinalResponse(requestCounter, successfulResponses, failedResponses)

	dur := time.Since(start)
	reqLog.Infof("returned final status_code=%v for request=%v with duration=%v", finalResponse.StatusCode, finalResponse.Request.URL, dur)
	requestDurationSeconds.WithLabelValues("HTTP", req.URL.Path, strconv.Itoa(finalResponse.StatusCode)).Observe(float64(dur))
	if alreadySent {
		return
	}
	sendResponse(w, finalResponse)
}
