# Hyperion Runtime (Aggregator Only)

Standalone aggregator service extracted from the Hyperion API monorepo. This repository contains only the aggregator logic while depending on shared services from the main `hyperion-api` repository.

## Architecture

This is a **minimal extraction** approach:
- **This repo**: `internal/aggregator/*` - Core aggregator, routing, market data, transaction building
- **Main repo**: All other services (config, HTTP, RPC, repository, etc.) via Go module dependency

## Quick Start

```bash
# Ensure you have the main hyperion-api repo as a sibling directory
cd /home/hxuan190/projects/dapps
ls -la
# Should see both:
# - hyperion-api/
# - hyperion-runtime/

# Build
cd hyperion-runtime
go build ./cmd/runtime

# Run
./runtime
```

## Dependencies

This repository uses a `replace` directive in `go.mod` to reference the main repository:

```go
replace github.com/thehyperflames/hyperion-api => ../hyperion-api
```

**Shared services from main repo**:
- Configuration (`internal/config`)
- HTTP server (`internal/http`)
- RPC services (`internal/services`)
- Repository layer (`internal/repository`)
- Market/candles (`internal/market`)
- Price service (`internal/price`)
- Common utilities (`internal/common`)

**This repo contains**:
- Aggregator service (`internal/aggregator`)
- Routing algorithms (BFS, ADMM, path evaluation)
- Market data management
- Transaction builder
- Entry point (`cmd/runtime/main.go`)

## Why This Approach?

- **Minimal duplication**: Shared services remain in one place
- **Easy development**: Changes to shared services don't need syncing
- **Clear boundaries**: Aggregator logic is isolated
- **Flexible**: Can be fully extracted later if needed

## Building

```bash
make build-hft
```

## Deployment

Same as main repo - the binary includes all dependencies.
# route-engine
