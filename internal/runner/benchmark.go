package runner

import (
	"context"
	"fmt"
	"memeval/internal/benchmark"
	"memeval/internal/registry"
	"memeval/internal/result"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
)

func bindBenchmark(s *suite.Suite, dir string) (*result.BenchmarkRef, error) {
	ref, source := s.ProtocolRef, "suite"
	if ref == "" {
		optional := dir == "" || dir == "data/registry"
		if dir == "" || dir == "data/registry" {
			dir = filepath.Join(benchmark.Root(), "data/registry")
		}
		entries, err := registry.LoadDir(dir)
		if optional && os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if _, entry, ok := entries.LookupSuite(s.SuiteID); ok {
			ref = entry.ProtocolRef
			source = "registry"
		}
	}
	if ref == "" {
		return nil, nil
	}
	p, v, h, err := benchmark.Resolve(ref)
	if err != nil {
		return nil, fmt.Errorf("benchmark binding: %w", err)
	}
	return &result.BenchmarkRef{ProtocolID: p.ProtocolID, Version: p.Version, Variant: v, Ref: ref, SHA256: h, Source: source}, nil
}
func supportedExecution(s *suite.Suite) error {
	if s.Protocol.Ingestion != "per_event" && s.Protocol.Ingestion != "per_session" && !strings.HasPrefix(s.Protocol.Ingestion, "per_chunk:") {
		return fmt.Errorf("ingestion %s not implemented in this version", s.Protocol.Ingestion)
	}
	if s.Protocol.QueryTimePolicy == "as_of" {
		return fmt.Errorf("query_time_policy as_of not implemented in this version")
	}
	return s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.QueryPoint != "" && c.QueryPoint != "end_of_history" && c.QueryPoint != "at_index" && c.QueryPoint != "after_session_k" {
				return fmt.Errorf("query_point %s not implemented in this version", c.QueryPoint)
			}
		}
		return nil
	})
}
