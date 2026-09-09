# Octopus

LLM API aggregation, protocol conversion, and load balancing.

[简体中文](README_zh.md)

## Requirements

Go 1.25+, Node.js 22, pnpm 11.1.2.

```powershell
npm.cmd install -g pnpm@11.1.2
git clone https://github.com/Bduoluoluo/octopus.git
cd octopus
```

## Local Development

Use two terminals.

Frontend:

```powershell
cd web
pnpm.cmd install --frozen-lockfile
$env:NEXT_PUBLIC_API_BASE_URL = "http://127.0.0.1:8080"
pnpm.cmd dev
```

Backend, from the repository root:

```powershell
$env:OCTOPUS_SERVER_HOST = "127.0.0.1"
$env:OCTOPUS_DEBUG = "true"
go run main.go start
```

Open <http://localhost:3000>. Port 8080 is the backend API and serves the bundled frontend, not the live development frontend.

Linux/macOS: replace `pnpm.cmd` with `pnpm`, and use `NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:8080 pnpm dev` for the frontend and `OCTOPUS_SERVER_HOST=127.0.0.1 OCTOPUS_DEBUG=true go run main.go start` for the backend.

Debug mode allows localhost development origins on port 3000. Configure the CORS allowlist for other ports or HTTPS. Do not enable debug mode in production.

## Build and Run

Stop development services first. Use a fresh terminal in the repository root; run each step only after the previous one succeeds.

Windows PowerShell:

```powershell
cd web
pnpm.cmd install --frozen-lockfile
$env:NEXT_PUBLIC_API_BASE_URL = "."
pnpm.cmd build
cd ..
if (Test-Path static/out) { Remove-Item -LiteralPath static/out -Recurse -Force }
Copy-Item -LiteralPath web/out -Destination static/out -Recurse
go build -o octopus.exe .
$env:OCTOPUS_DEBUG = "false"
.\octopus.exe start
```

Linux/macOS:

```bash
cd web
pnpm install --frozen-lockfile
NEXT_PUBLIC_API_BASE_URL=. pnpm build
cd ..
rm -rf static/out
cp -R web/out static/out
go build -o octopus .
OCTOPUS_DEBUG=false ./octopus start
```

Replace only `static/out`, never the entire `static` directory. Rebuild both frontend and backend after frontend changes.

Open <http://localhost:8080>. The binary runs independently without Node.js or a separate frontend server.

## Docker

Push a version tag such as `v1.0.1` to publish the matching images to GHCR:

```text
ghcr.io/bduoluoluo/octopus:latest
ghcr.io/bduoluoluo/octopus:latest-alpine
```

The recommended release flow is:

```bash
git add .
git commit -m "Release v1.0.1"
git push origin dev
git tag v1.0.1
git push origin v1.0.1
```

The tag push publishes `v1.0.1` and `v1.0.1-alpine`, and also updates `latest` and `latest-alpine` for stable tags. Prerelease tags such as `v1.0.1-rc.1` do not update `latest`. Ordinary branch pushes do not publish a GHCR image. You can also run `Actions > Publish GHCR > Run workflow` and enter an existing version tag. The first package may be private by default; set its visibility to `Public` in GitHub Packages before pulling without login. The workflow uses the built-in `GITHUB_TOKEN` with `packages: write` permission.

Pull and run the published Debian image:

```bash
docker pull ghcr.io/bduoluoluo/octopus:latest
docker run -d --name octopus -v ./data:/app/data -p 8080:8080 ghcr.io/bduoluoluo/octopus:latest
```

Use `latest-alpine` for Alpine. Release-specific tags are also published as `v1.0.0` and `v1.0.0-alpine`; commit-specific tags use `sha-<commit>` and `sha-<commit>-alpine`.

Build the frontend and Linux binary first, then build the image. Run these commands from a Linux shell, WSL, or Git Bash:

```bash
cd web
pnpm install --frozen-lockfile
NEXT_PUBLIC_API_BASE_URL=. pnpm build
cd ..
rm -rf static/out
cp -R web/out static/out
mkdir -p build/docker/linux/amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o build/docker/linux/amd64/octopus .
docker build --build-arg TARGETPLATFORM=linux/amd64 -f scripts/dockerfiles/Dockerfile.debian -t octopus:local .
```

For ARM64, replace `amd64` with `arm64` in the directory, Go build, and `TARGETPLATFORM` values. Use `Dockerfile.alpine` instead of `Dockerfile.debian` for Alpine. Run the local image with a persistent data volume:

```bash
docker run -d --name octopus -v /path/to/data:/app/data -p 8080:8080 octopus:local
```

The Dockerfile expects the binary at `build/docker/linux/<architecture>/octopus`. A multi-platform Buildx image requires both the amd64 and arm64 binaries to be prepared first.

## Login and Configuration

- Initial credentials: `admin` / `admin`. Change the password immediately.
- Configuration: `data/config.json`, generated on first startup.
- Default database: SQLite at `data/data.db`. Preserve and back up `data/`; start from the same working directory to reuse it.
- Common variables: `OCTOPUS_SERVER_HOST`, `OCTOPUS_SERVER_PORT` (default `8080`), `OCTOPUS_DATABASE_TYPE`, and `OCTOPUS_DATABASE_PATH`.
- In Settings > Data Management, configure log retention and the database size limit. When the limit is exceeded, the oldest half of relay logs is removed.
- Channel editing supports single and batch model tests. These send real upstream requests and may incur charges.

## License

See [LICENSE](LICENSE). Based on [xuanli27/octopus](https://github.com/xuanli27/octopus); API adaptation derives from [looplj/axonhub](https://github.com/looplj/axonhub), and pricing data comes from [models.dev](https://github.com/sst/models.dev).
