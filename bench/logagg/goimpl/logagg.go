// Package goimpl is the hand-written Go version of the logagg (CPU) workload.
package goimpl

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Stats summarizes requests to one endpoint.
type Stats struct {
	Endpoint string
	Count    int
	Errors   int
	TotalMs  int64
	MaxMs    int
}

// Aggregate parses access-log lines and summarizes them per endpoint.
func Aggregate(lines []string) ([]Stats, error) {
	byEndpoint := map[string]*Stats{}
	for _, line := range lines {
		parts := strings.Split(line, ",")
		if len(parts) != 5 {
			return nil, fmt.Errorf("malformed line: %s", line)
		}
		status, err := strconv.Atoi(parts[3])
		if err != nil {
			return nil, err
		}
		latency, err := strconv.Atoi(parts[4])
		if err != nil {
			return nil, err
		}
		s := byEndpoint[parts[2]]
		if s == nil {
			s = &Stats{Endpoint: parts[2]}
			byEndpoint[parts[2]] = s
		}
		s.Count++
		if status >= 500 {
			s.Errors++
		}
		s.TotalMs += int64(latency)
		if latency > s.MaxMs {
			s.MaxMs = latency
		}
	}
	out := make([]Stats, 0, len(byEndpoint))
	for _, s := range byEndpoint {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Stats) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	return out, nil
}
