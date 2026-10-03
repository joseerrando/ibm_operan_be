# Operan backend helper for Windows (same targets as the Makefile).
#   .\dev.ps1 db | run | build | migrate | seed | test | lint | langflow
param([Parameter(Position = 0)][string]$Target = "help")

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

switch ($Target) {
  "run"      { go run ./cmd/server }
  "build"    { go build -o bin/operan-api.exe ./cmd/server }
  "migrate"  { go run ./cmd/server -migrate }
  "seed"     { go run ./cmd/seed -reset }
  "test"     { go test ./... }
  "lint"     { go vet ./...; gofmt -l . }
  "langflow" {
    # Tool agent Flow 5 memanggil backend di localhost; izinkan host itu di proteksi SSRF Langflow.
    $env:LANGFLOW_SSRF_ALLOWED_HOSTS = "localhost,127.0.0.1"
    $lf = "$env:USERPROFILE\langflow\.venv\Scripts\langflow.exe"
    if (-not (Test-Path $lf)) { $lf = "langflow" }
    & $lf run --host 127.0.0.1 --port 7860
  }
  "db" {
    # Membuat database + user lokal di MariaDB/MySQL XAMPP (root tanpa password).
    $mysql = "C:\xampp\mysql\bin\mysql.exe"
    & $mysql -uroot -e "CREATE DATABASE IF NOT EXISTS operan CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; CREATE DATABASE IF NOT EXISTS operan_test CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; CREATE USER IF NOT EXISTS 'operan'@'localhost' IDENTIFIED BY 'operan'; CREATE USER IF NOT EXISTS 'operan'@'127.0.0.1' IDENTIFIED BY 'operan'; GRANT ALL ON operan.* TO 'operan'@'localhost'; GRANT ALL ON operan.* TO 'operan'@'127.0.0.1'; GRANT ALL ON operan_test.* TO 'operan'@'localhost'; GRANT ALL ON operan_test.* TO 'operan'@'127.0.0.1'; FLUSH PRIVILEGES;"
    Write-Host "Database operan dan operan_test siap."
  }
  default {
    Write-Host "Pemakaian: .\dev.ps1 <db|run|build|migrate|seed|test|lint|langflow>"
  }
}
