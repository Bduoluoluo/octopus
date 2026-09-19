# Octopus

LLM API 聚合、协议转换和负载均衡服务。

[English](README.md)

## 环境要求

需要 Go 1.25+、Node.js 22、pnpm 11.1.2。

```powershell
npm.cmd install -g pnpm@11.1.2
git clone https://github.com/Bduoluoluo/octopus.git
cd octopus
```

## 本地开发

需要使用两个终端。

终端一启动前端：

```powershell
cd web
pnpm.cmd install --frozen-lockfile
$env:NEXT_PUBLIC_API_BASE_URL = "http://127.0.0.1:8080"
pnpm.cmd dev
```

终端二在项目根目录启动后端：

```powershell
$env:OCTOPUS_SERVER_HOST = "127.0.0.1"
$env:OCTOPUS_DEBUG = "true"
go run main.go start
```

访问 <http://localhost:3000>。8080 是后端 API 端口，也提供内嵌的前端页面，但不是实时更新的开发页面。

Linux/macOS 将 `pnpm.cmd` 改为 `pnpm`，前端使用 `NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:8080 pnpm dev`，后端使用 `OCTOPUS_SERVER_HOST=127.0.0.1 OCTOPUS_DEBUG=true go run main.go start`。

调试模式允许 localhost 的 3000 端口来源。其他端口或 HTTPS 来源需在设置中配置跨域白名单。生产环境不要启用调试模式。

## 构建运行

先停止开发服务，在新终端中从项目根目录执行；每一步成功后再执行下一步。

Windows PowerShell：

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

Linux/macOS：

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

仅替换 `static/out`，不要删除整个 `static` 目录。修改前端后需要重新构建前后端。

访问 <http://localhost:8080>。编译后的程序可独立运行，不再需要 Node.js 或单独启动前端。

## Docker

推送 `v1.0.1` 这样的版本标签后，GitHub Actions 会将对应版本镜像发布到 GHCR：

```text
ghcr.io/bduoluoluo/octopus:latest
ghcr.io/bduoluoluo/octopus:latest-alpine
```

推荐的发布流程如下：

```bash
git add .
git commit -m "Release v1.0.1"
git push origin dev
git tag v1.0.1
git push origin v1.0.1
```

推送版本标签会发布 `v1.0.1` 和 `v1.0.1-alpine`，正式版本标签还会更新 `latest` 和 `latest-alpine`。`v1.0.1-rc.1` 这类预发布标签不会更新 `latest`。普通分支 push 不会发布 GHCR 镜像。也可以在仓库的 `Actions > Publish GHCR > Run workflow` 中输入已有版本标签手动执行。第一次发布的软件包可能默认为私有；如需免登录拉取，请在 GitHub Packages 中将软件包改为 `Public`。该工作流使用 GitHub 内置的 `GITHUB_TOKEN` 和 `packages: write` 权限，不需要额外配置 Token。

拉取并运行 Debian 镜像：

```bash
docker pull ghcr.io/bduoluoluo/octopus:latest
docker run -d --name octopus -v ./data:/app/data -p 8080:8080 ghcr.io/bduoluoluo/octopus:latest
```

Alpine 版本使用 `latest-alpine`。正式版本也会发布 `v1.0.0` 和 `v1.0.0-alpine` 标签，每次构建还会发布 `sha-<commit>` 和 `sha-<commit>-alpine` 标签。

先构建前端和 Linux 二进制文件，再构建镜像。以下命令请在 Linux、WSL 或 Git Bash 中执行：

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

构建 ARM64 时，将目录、Go 构建参数和 `TARGETPLATFORM` 中的 `amd64` 全部改为 `arm64`。需要 Alpine 镜像时，将 `Dockerfile.debian` 改为 `Dockerfile.alpine`。使用持久化卷运行本地镜像：

```bash
docker run -d --name octopus -v /path/to/data:/app/data -p 8080:8080 octopus:local
```

Dockerfile 要求二进制文件位于 `build/docker/linux/<架构>/octopus`。多平台 Buildx 镜像需要先准备 amd64 和 arm64 两个二进制文件。

## 登录与配置

- 初始账号密码：`admin` / `admin`，首次登录后立即修改密码。
- 配置文件：`data/config.json`，首次启动自动生成。
- 默认数据库：SQLite，路径为 `data/data.db`。保留并备份 `data/`，从相同工作目录启动以继续使用原数据。
- 常用环境变量：`OCTOPUS_SERVER_HOST`、`OCTOPUS_SERVER_PORT`（默认 `8080`）、`OCTOPUS_DATABASE_TYPE`、`OCTOPUS_DATABASE_PATH`。
- 设置中的“数据管理”可以配置日志保留时间和数据库大小上限。超过上限时，程序会删除最旧的一半中继日志。
- 渠道编辑支持单个和批量模型测试，会发送真实上游请求，可能产生费用。
- 渠道编辑中的“自定义缓存百分比”默认关闭。开启后可设置 0–100% 的左右区间，每次请求随机取一个固定比例；上下限相同即固定比例。缓存读入数量向下取整，按客户端协议返回，并同步影响本地用量与费用统计，不改变上游实际缓存或收费。已有缓存写入数量保留，缓存读入最多占用其余输入；上游未提供输入用量时不会凭空生成用量。
- OpenAI Chat/Responses 渠道的模型测试和分组测活使用流式 `/v1/responses` 请求，发送 `hi` 和内置 Codex instructions，模型名取当前被测模型，默认单次超时 10 秒。上游需要支持 Responses 接口；其他协议渠道使用原协议。
- 分组“检查”在故障转移模式下找到首个可用候选即停止；“完整探活”会继续测试其余候选。其他分组模式两者均逐项测试，均遵守渠道的“跳过健康检查”设置。

## 许可证

详见 [LICENSE](LICENSE)。基于 [xuanli27/octopus](https://github.com/xuanli27/octopus)，API 适配代码源于 [looplj/axonhub](https://github.com/looplj/axonhub)，模型价格数据来自 [models.dev](https://github.com/sst/models.dev)。
