/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"math"
	"time"

	prometheusapi "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// MetricsQuerier defines the interface for fetching preview environment SRE metrics.
type MetricsQuerier interface {
	QueryP99Latency(ctx context.Context, namespace string, window string) (float64, error)
	QueryErrorRate(ctx context.Context, namespace string, window string) (float64, error)
	QueryPodRestarts(ctx context.Context, namespace string) (int, error)
}

// PrometheusQuerier is the real implementation of MetricsQuerier using the Prometheus API.
type PrometheusQuerier struct {
	api prometheusv1.API
}

// NewPrometheusQuerier initializes a new PrometheusQuerier with the given address.
func NewPrometheusQuerier(address string) (MetricsQuerier, error) {
	client, err := prometheusapi.NewClient(prometheusapi.Config{
		Address: address,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus client: %w", err)
	}
	return &PrometheusQuerier{
		api: prometheusv1.NewAPI(client),
	}, nil
}

// QueryP99Latency queries the 99th percentile HTTP request latency.
func (p *PrometheusQuerier) QueryP99Latency(ctx context.Context, namespace string, window string) (float64, error) {
	query := fmt.Sprintf("histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{namespace=\"%s\"}[%s])) by (le))", namespace, window)
	val, err := p.querySingleValue(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to query P99 latency: %w", err)
	}
	// Convert seconds to milliseconds
	return val * 1000.0, nil
}

// QueryErrorRate queries the percentage of HTTP 5xx errors.
func (p *PrometheusQuerier) QueryErrorRate(ctx context.Context, namespace string, window string) (float64, error) {
	query := fmt.Sprintf("(sum(rate(http_requests_total{namespace=\"%s\", status=~\"5..\"}[%s])) / sum(rate(http_requests_total{namespace=\"%s\"}[%s]))) * 100", namespace, window, namespace, window)
	val, err := p.querySingleValue(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to query error rate: %w", err)
	}
	return val, nil
}

// QueryPodRestarts queries the total number of pod container restarts in the namespace.
func (p *PrometheusQuerier) QueryPodRestarts(ctx context.Context, namespace string) (int, error) {
	query := fmt.Sprintf("sum(kube_pod_container_status_restarts_total{namespace=\"%s\"})", namespace)
	val, err := p.querySingleValue(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to query pod restarts: %w", err)
	}
	return int(val), nil
}

func (p *PrometheusQuerier) querySingleValue(ctx context.Context, query string) (float64, error) {
	result, _, err := p.api.Query(ctx, query, time.Now())
	if err != nil {
		return 0, err
	}

	vector, ok := result.(model.Vector)
	if !ok {
		return 0, fmt.Errorf("unexpected prometheus query response type: %T", result)
	}

	if len(vector) == 0 {
		return 0, nil
	}

	val := float64(vector[0].Value)
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, nil
	}

	return val, nil
}

// MockMetricsQuerier is a mock implementation of MetricsQuerier for testing.
type MockMetricsQuerier struct {
	P99Latency float64
	ErrorRate  float64
	Restarts   int
	Err        error
}

func (m *MockMetricsQuerier) QueryP99Latency(ctx context.Context, namespace string, window string) (float64, error) {
	return m.P99Latency, m.Err
}

func (m *MockMetricsQuerier) QueryErrorRate(ctx context.Context, namespace string, window string) (float64, error) {
	return m.ErrorRate, m.Err
}

func (m *MockMetricsQuerier) QueryPodRestarts(ctx context.Context, namespace string) (int, error) {
	return m.Restarts, m.Err
}
