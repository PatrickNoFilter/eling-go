package benchmark

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// MetricsTracker — records fine-grained execution metrics across runs.
// ──────────────────────────────────────────────────────────────────────

// MetricPoint is a single data point in a time series.
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	Label     string    `json:"label,omitempty"`
}

// TimeSeries tracks a metric over time.
type TimeSeries struct {
	Name   string        `json:"name"`
	Unit   string        `json:"unit"`
	Points []MetricPoint `json:"points"`
}

// MetricsTracker collects time-series metrics across benchmark runs.
type MetricsTracker struct {
	mu       sync.Mutex
	series   map[string]*TimeSeries
	counters map[string]int64
}

// NewMetricsTracker creates a new metrics tracker.
func NewMetricsTracker() *MetricsTracker {
	return &MetricsTracker{
		series:   make(map[string]*TimeSeries),
		counters: make(map[string]int64),
	}
}

// Record adds a data point to a named time series.
func (mt *MetricsTracker) Record(name, unit, label string, value float64) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	ts, ok := mt.series[name]
	if !ok {
		ts = &TimeSeries{Name: name, Unit: unit}
		mt.series[name] = ts
	}
	ts.Points = append(ts.Points, MetricPoint{
		Timestamp: time.Now(),
		Value:     value,
		Label:     label,
	})
}

// Increment increments a named counter.
func (mt *MetricsTracker) Increment(name string) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.counters[name]++
}

// Counter returns the current value of a counter.
func (mt *MetricsTracker) Counter(name string) int64 {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	return mt.counters[name]
}

// Series returns a named time series (or nil).
func (mt *MetricsTracker) Series(name string) *TimeSeries {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	return mt.series[name]
}

// AllSeries returns all tracked time series.
func (mt *MetricsTracker) AllSeries() map[string]*TimeSeries {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	result := make(map[string]*TimeSeries, len(mt.series))
	for k, v := range mt.series {
		cp := &TimeSeries{Name: v.Name, Unit: v.Unit}
		cp.Points = make([]MetricPoint, len(v.Points))
		copy(cp.Points, v.Points)
		result[k] = cp
	}
	return result
}

// AllCounters returns all tracked counters.
func (mt *MetricsTracker) AllCounters() map[string]int64 {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	result := make(map[string]int64, len(mt.counters))
	for k, v := range mt.counters {
		result[k] = v
	}
	return result
}

// Average computes the average of all values in a named series.
func (mt *MetricsTracker) Average(name string) (float64, bool) {
	ts := mt.Series(name)
	if ts == nil || len(ts.Points) == 0 {
		return 0, false
	}
	var sum float64
	for _, p := range ts.Points {
		sum += p.Value
	}
	return sum / float64(len(ts.Points)), true
}

// Min computes the minimum value in a named series.
func (mt *MetricsTracker) Min(name string) (float64, bool) {
	ts := mt.Series(name)
	if ts == nil || len(ts.Points) == 0 {
		return 0, false
	}
	min := ts.Points[0].Value
	for _, p := range ts.Points[1:] {
		if p.Value < min {
			min = p.Value
		}
	}
	return min, true
}

// Max computes the maximum value in a named series.
func (mt *MetricsTracker) Max(name string) (float64, bool) {
	ts := mt.Series(name)
	if ts == nil || len(ts.Points) == 0 {
		return 0, false
	}
	max := ts.Points[0].Value
	for _, p := range ts.Points[1:] {
		if p.Value > max {
			max = p.Value
		}
	}
	return max, true
}

// Percentile computes the p-th percentile (0-100) of a named series.
func (mt *MetricsTracker) Percentile(name string, p float64) (float64, bool) {
	ts := mt.Series(name)
	if ts == nil || len(ts.Points) == 0 {
		return 0, false
	}
	sorted := make([]float64, len(ts.Points))
	for i, pt := range ts.Points {
		sorted[i] = pt.Value
	}
	// Simple insertion sort (small n)
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}

	idx := int(float64(len(sorted)-1) * p / 100.0)
	return sorted[idx], true
}

// WriteCSV writes all time series as CSV to the given writer.
func (mt *MetricsTracker) WriteCSV(w io.Writer) error {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	// Header
	_, err := fmt.Fprintln(w, "series,label,timestamp,value,unit")
	if err != nil {
		return err
	}

	for name, ts := range mt.series {
		for _, pt := range ts.Points {
			_, err := fmt.Fprintf(w, "%s,%s,%s,%f,%s\n",
				name, pt.Label, pt.Timestamp.Format(time.RFC3339), pt.Value, ts.Unit)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Summary returns a multi-line string summary of all tracked metrics.
func (mt *MetricsTracker) Summary() string {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	var b strings.Builder
	b.WriteString("╔══════════════════════════════════════════╗\n")
	b.WriteString("║     Metrics Summary                      ║\n")
	b.WriteString("╚══════════════════════════════════════════╝\n\n")

	// Time series summaries
	if len(mt.series) > 0 {
		b.WriteString("Time Series:\n")
		for name, ts := range mt.series {
			if len(ts.Points) == 0 {
				continue
			}
			var sum, min, max float64
			sum = ts.Points[0].Value
			min = ts.Points[0].Value
			max = ts.Points[0].Value
			for _, pt := range ts.Points[1:] {
				sum += pt.Value
				if pt.Value < min {
					min = pt.Value
				}
				if pt.Value > max {
					max = pt.Value
				}
			}
			avg := sum / float64(len(ts.Points))
			b.WriteString(fmt.Sprintf("  📊 %s (%s):\n", name, ts.Unit))
			b.WriteString(fmt.Sprintf("      Points: %d\n", len(ts.Points)))
			b.WriteString(fmt.Sprintf("      Avg:    %.2f\n", avg))
			b.WriteString(fmt.Sprintf("      Min:    %.2f\n", min))
			b.WriteString(fmt.Sprintf("      Max:    %.2f\n", max))
		}
		b.WriteString("\n")
	}

	// Counters
	if len(mt.counters) > 0 {
		b.WriteString("Counters:\n")
		for name, val := range mt.counters {
			b.WriteString(fmt.Sprintf("  🔢 %s: %d\n", name, val))
		}
		b.WriteString("\n")
	}

	return b.String()
}
