module github.com/Ivantseng123/agentdock/app

go 1.25.0

// worker replace is temporary — Phase 4 Task 34 moves inmem LocalAdapter
// assembly into cmd/agentdock so app no longer imports worker/.
replace (
	github.com/Ivantseng123/agentdock => ../
	github.com/Ivantseng123/agentdock/shared => ../shared
	github.com/Ivantseng123/agentdock/worker => ../worker
)

require (
	github.com/Ivantseng123/agentdock v0.0.0
	github.com/Ivantseng123/agentdock/shared v0.0.0
	github.com/Ivantseng123/agentdock/worker v0.0.0-00010101000000-000000000000
	github.com/fsnotify/fsnotify v1.9.0
	github.com/prometheus/client_golang v1.23.2
	github.com/slack-go/slack v0.20.0
	github.com/xuri/excelize/v2 v2.10.1
	golang.org/x/sync v0.20.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/go-github/v60 v60.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/redis/go-redis/v9 v9.18.0 // indirect
	github.com/richardlehane/mscfb v1.0.6 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)
