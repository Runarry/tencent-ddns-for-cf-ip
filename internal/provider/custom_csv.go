package provider

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

const CustomNodeID = "custom"

type CustomCSVConfig struct {
	Path string
	TopN int
}

type CustomCSVClient struct {
	path string
	topN int
}

type customCSVRow struct {
	candidate Candidate
	latencyMS float64
}

func NewCustomCSVClient(cfg CustomCSVConfig) *CustomCSVClient {
	topN := cfg.TopN
	if topN <= 0 {
		topN = 5
	}
	return &CustomCSVClient{
		path: strings.TrimSpace(cfg.Path),
		topN: topN,
	}
}

func (c *CustomCSVClient) Fetch(ctx context.Context) ([]Candidate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	file, err := os.Open(c.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return ParseCustomCSV(file, c.topN)
}

func ParseCustomCSV(r io.Reader, topN int) ([]Candidate, error) {
	if topN <= 0 {
		topN = 5
	}

	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("custom CSV is empty")
		}
		return nil, err
	}
	columns, err := customCSVColumns(header)
	if err != nil {
		return nil, err
	}

	byIP := map[string]customCSVRow{}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) <= columns.speed || len(record) <= columns.latency || len(record) <= columns.ip {
			continue
		}

		ip := strings.TrimSpace(record[columns.ip])
		if net.ParseIP(ip) == nil {
			continue
		}
		speedMB, err := parseCSVFloat(record[columns.speed])
		if err != nil || speedMB < 0 {
			continue
		}
		latencyMS, err := parseCSVFloat(record[columns.latency])
		if err != nil || latencyMS < 0 {
			continue
		}

		row := customCSVRow{
			candidate: Candidate{
				NodeID:   CustomNodeID,
				IP:       ip,
				APIPing:  fmt.Sprintf("%.2fms", latencyMS),
				Speed:    fmt.Sprintf("%.2fMB/s", speedMB),
				SpeedBPS: int64(math.Round(speedMB * 1024 * 1024)),
			},
			latencyMS: latencyMS,
		}
		current, exists := byIP[ip]
		if !exists || customCSVRowLess(row, current) {
			byIP[ip] = row
		}
	}

	rows := make([]customCSVRow, 0, len(byIP))
	for _, row := range byIP {
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return customCSVRowLess(rows[i], rows[j])
	})
	if len(rows) > topN {
		rows = rows[:topN]
	}

	candidates := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, row.candidate)
	}
	return candidates, nil
}

type customCSVColumnIndexes struct {
	ip      int
	latency int
	speed   int
}

func customCSVColumns(header []string) (customCSVColumnIndexes, error) {
	indexes := map[string]int{}
	for i, column := range header {
		column = strings.TrimPrefix(strings.TrimSpace(column), "\ufeff")
		indexes[column] = i
	}

	ip, okIP := indexes["IP 地址"]
	latency, okLatency := indexes["平均延迟"]
	speed, okSpeed := indexes["下载速度(MB/s)"]
	if !okIP || !okLatency || !okSpeed {
		return customCSVColumnIndexes{}, fmt.Errorf("custom CSV header must include IP 地址, 平均延迟, 下载速度(MB/s)")
	}
	return customCSVColumnIndexes{ip: ip, latency: latency, speed: speed}, nil
}

func parseCSVFloat(value string) (float64, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(strings.ToLower(value), "mb/s")
	value = strings.TrimSuffix(value, "ms")
	return strconv.ParseFloat(strings.TrimSpace(value), 64)
}

func customCSVRowLess(left customCSVRow, right customCSVRow) bool {
	if left.candidate.SpeedBPS != right.candidate.SpeedBPS {
		return left.candidate.SpeedBPS > right.candidate.SpeedBPS
	}
	if left.latencyMS != right.latencyMS {
		return left.latencyMS < right.latencyMS
	}
	return left.candidate.IP < right.candidate.IP
}
