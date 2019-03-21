/*
Copyright 2017 Google Inc.

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

package translator

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/GoogleCloudPlatform/k8s-stackdriver/prometheus-to-sd/config"
)

const customMetricsPrefix = "custom.googleapis.com"

// PrometheusResponse represents unprocessed response from Prometheus endpoint.
type PrometheusResponse struct {
	rawResponse string
}

// GetPrometheusMetrics scrapes metrics from the given host and port using /metrics handler.
func GetPrometheusMetrics(config *config.SourceConfig, caCerts []string) (*PrometheusResponse, error) {
	res, err := getPrometheusMetrics(config, caCerts)
	if err != nil {
		componentMetricsAvailable.WithLabelValues(config.Component).Set(0.0)
	} else {
		componentMetricsAvailable.WithLabelValues(config.Component).Set(1.0)
	}
	return res, err
}

func getPrometheusMetrics(config *config.SourceConfig, caCerts []string) (*PrometheusResponse, error) {
	url := fmt.Sprintf("%s://%s:%d%s", config.Scheme, config.Host, config.Port, config.Path)

	client := http.Client{}
	if len(caCerts) > 0 {
		crtPool, _ := x509.SystemCertPool()
		if crtPool == nil {
			crtPool = x509.NewCertPool()
		}

		for _, crt := range caCerts {
			certs, err := ioutil.ReadFile(crt)
			if err != nil {
				return nil, fmt.Errorf("CA certs file %s: %v", crt, err)
			}

			if ok := crtPool.AppendCertsFromPEM([]byte(certs)); !ok {
				return nil, fmt.Errorf("CA certs from file %s to the system certificate pool: %v", crt, err)
			}
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: crtPool}}
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body - %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed - %q, response: %q", resp.Status, string(body))
	}
	return &PrometheusResponse{rawResponse: string(body)}, nil
}

// Build performs parsing and processing of the prometheus metrics response.
func (p *PrometheusResponse) Build(config *config.CommonConfig, metricDescriptorCache *MetricDescriptorCache) (map[string]*dto.MetricFamily, error) {
	parser := &expfmt.TextParser{}
	metrics, err := parser.TextToMetricFamilies(strings.NewReader(p.rawResponse))
	if err != nil {
		return nil, err
	}
	if config.OmitComponentName {
		metrics = OmitComponentName(metrics, config.SourceConfig.Component)
	}
	if config.DowncaseMetricNames {
		metrics = DowncaseMetricNames(metrics)
	}
	// Convert summary metrics into metric family types we can easily import, since summary types
	// map to multiple stackdriver metrics.
	metrics = FlattenSummaryMetricFamilies(metrics)
	if strings.HasPrefix(config.SourceConfig.MetricsPrefix, customMetricsPrefix) {
		metricDescriptorCache.UpdateMetricDescriptors(metrics, config.SourceConfig.Whitelisted)
	} else {
		metricDescriptorCache.ValidateMetricDescriptors(metrics, config.SourceConfig.Whitelisted)
	}
	return metrics, nil
}
