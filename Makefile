# Operan backend. Windows tanpa make: pakai .\dev.ps1 <target> (isinya sama).

.PHONY: run build migrate seed test lint langflow

run:            ## jalankan API (membaca .env)
	go run ./cmd/server

build:
	go build -o bin/operan-api ./cmd/server

migrate:
	go run ./cmd/server -migrate

seed:           ## isi ulang data demo (MENGHAPUS semua tabel)
	go run ./cmd/seed -reset

test:
	go test ./...

lint:
	go vet ./...
	gofmt -l .

langflow:       ## Langflow lokal (butuh uv)
	LANGFLOW_SSRF_ALLOWED_HOSTS=localhost,127.0.0.1 uvx --python 3.12 langflow run --host 127.0.0.1 --port 7860
