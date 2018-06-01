/*
© Copyright IBM Corporation 2018

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package metrics contains code to provide metrics for the queue manager
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ibm-messaging/mq-container/internal/logger"
	"github.com/ibm-messaging/mq-golang/mqmetric"
)

const (
	qmgrLabelValue = mqmetric.QMgrMapKey
	requestTimeout = 10
)

var (
	requestChannel  = make(chan bool)
	responseChannel = make(chan map[string]*metricData)
)

type metricData struct {
	name        string
	description string
	objectType  bool
	values      map[string]float64
}

var keepRunning = true
var first = true

func doConnect(qmName string) error {
	// Set connection configuration
	var connConfig mqmetric.ConnectionConfig
	connConfig.ClientMode = false
	connConfig.UserId = ""
	connConfig.Password = ""

	// Connect to the queue manager - open the command and dynamic reply queues
	err := mqmetric.InitConnectionStats(qmName, "SYSTEM.DEFAULT.MODEL.QUEUE", "", &connConfig)
	if err != nil {
		return fmt.Errorf("Failed to connect to queue manager %s: %v", qmName, err)
	}

	// Discover available metrics for the queue manager and subscribe to them
	err = mqmetric.DiscoverAndSubscribe("", true, "")
	if err != nil {
		return fmt.Errorf("Failed to discover and subscribe to metrics: %v", err)
	}

	return nil
}

// processMetrics processes publications of metric data and handles describe/collect requests
func processMetrics(log *logger.Logger, qmName string, wg *sync.WaitGroup) {
	var err error
	var metrics map[string]*metricData

	for keepRunning {
		err = doConnect(qmName)
		if err == nil {
			if first {
				first = false
				wg.Done()
			}
			metrics, _ = initialiseMetrics(log)
		}

		// Now loop until something goes wrong
		for err == nil {

			// Process publications of metric data
			err = mqmetric.ProcessPublications()

			// Handle describe/collect requests
			select {
			case collect := <-requestChannel:
				if collect {
					updateMetrics(metrics)
				}
				responseChannel <- metrics
			case <-time.After(requestTimeout * time.Second):
				log.Debugf("Metrics: No requests received within timeout period (%d seconds)", requestTimeout)
			}
		}
		log.Errorf("Metrics Error: %s", err.Error())

		// Close the connection
		mqmetric.EndConnection()

		// If we're told to keep running sleep for a bit before trying again
		time.Sleep(10 * time.Second)
	}
}

// initialiseMetrics sets initial details for all available metrics
func initialiseMetrics(log *logger.Logger) (map[string]*metricData, error) {

	metrics := make(map[string]*metricData)
	validMetrics := true

	for _, metricClass := range mqmetric.Metrics.Classes {
		for _, metricType := range metricClass.Types {
			if !strings.Contains(metricType.ObjectTopic, "%s") {
				for _, metricElement := range metricType.Elements {
					metric := metricData{
						name:        metricElement.MetricName,
						description: metricElement.Description,
					}
					key := makeKey(metricElement)
					if _, exists := metrics[key]; !exists {
						metrics[key] = &metric
					} else {
						log.Errorf("Metrics Error: Found duplicate metric key %s", key)
						validMetrics = false
					}
				}
			}
		}
	}

	if !validMetrics {
		return metrics, fmt.Errorf("Invalid metrics data - found duplicate metric keys")
	}
	return metrics, nil
}

// updateMetrics updates values for all available metrics
func updateMetrics(metrics map[string]*metricData) {

	for _, metricClass := range mqmetric.Metrics.Classes {
		for _, metricType := range metricClass.Types {
			if !strings.Contains(metricType.ObjectTopic, "%s") {
				for _, metricElement := range metricType.Elements {

					// Clear existing metric values
					metric := metrics[makeKey(metricElement)]
					metric.values = make(map[string]float64)

					// Update metric with cached values of publication data
					for label, value := range metricElement.Values {
						normalisedValue := mqmetric.Normalise(metricElement, label, value)
						metric.values[label] = normalisedValue
					}

					// Reset cached values of publication data for this metric
					metricElement.Values = make(map[string]int64)
				}
			}
		}
	}
}

// makeKey builds a unique key for each metric
func makeKey(metricElement *mqmetric.MonElement) string {
	return metricElement.Parent.Parent.Name + "/" + metricElement.Parent.Name + "/" + metricElement.MetricName
}