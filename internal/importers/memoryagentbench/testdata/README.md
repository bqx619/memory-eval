# MemoryAgentBench importer fixtures

Source: HF `ai-hyz/MemoryAgentBench`, revision `7ea066982b140a19337e17e60d45d4076e042faf`; pinned in `data/upstream/memoryagentbench/source.lock.json`. `LICENSE` retains the upstream MIT text.

`GOPROXY=off GOFLAGS=-mod=mod go run scripts/generate_mab_fixtures.go` reads the first row of each parquet file, retains all questions and answers, and truncates context to at most three greedy 4096-token chunks. AR/CR/LRU/TTL contain 100/100/1/200 questions; re-chunking yields 3/2/3/3 chunks respectively.

These shortened real-format fixtures are not system-quality evidence. `TestConformance` writes two fresh bundles and checks validation, leak scanning, statistics, normalized headers and their bound index hashes. Each `.golden.json` is a bundle header whose index hash covers episode content. After review, update golden files with `go test ./internal/importers/memoryagentbench -update`.
