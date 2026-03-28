# GitHub Actions Docker 镜像自动构建配置

本项目使用 GitHub Actions 自动构建并推送 Docker 镜像到多个镜像仓库。

## 📦 自动构建触发条件

- **Push to main 分支**：自动构建 `latest` 标签
- **推送版本标签**（如 `v1.0.0`）：自动构建版本标签（`1.0.0`, `1.0`, `1`, `latest`）
- **手动触发**：在 GitHub Actions 页面手动运行

## 🔧 配置 GitHub Secrets

### GitHub Container Registry（默认已配置）✅

GitHub Container Registry (ghcr.io) **无需任何配置**，workflow 会自动推送：

- **镜像地址**：`ghcr.io/isboyjc/goproxy`
- **认证方式**：自动使用 `GITHUB_TOKEN`
- **推送策略**：每次构建都会推送

### Docker Hub 配置（可选）⚙️

Docker Hub 配置是**可选的**，如果不配置，workflow 会自动跳过 Docker Hub 推送，**不会影响 GHCR 构建**。

**如需推送到 Docker Hub**，按以下步骤配置：

1. 登录 [Docker Hub](https://hub.docker.com/)
2. 在 **Account Settings > Security** 创建 Access Token
3. 在 GitHub 仓库的 **Settings > Secrets and variables > Actions** 添加：
   - `DOCKERHUB_USERNAME`: 你的 Docker Hub 用户名
   - `DOCKERHUB_TOKEN`: 刚才创建的 Access Token

配置完成后，workflow 会同时推送到两个仓库：
- `docker.io/isboyjc/goproxy`
- `ghcr.io/isboyjc/goproxy`

## 🏗️ 构建特性

- ✅ **多架构支持**：`linux/amd64`、`linux/arm64`
- ✅ **多标签发布**：`latest`、版本号（如 `1.0.0`、`1.0`、`1`）
- ✅ **双仓库推送**：Docker Hub + GitHub Container Registry
- ✅ **构建缓存**：使用 GitHub Actions 缓存加速构建

## 📋 使用镜像

### 从 Docker Hub 拉取

```bash
docker pull isboyjc/goproxy:latest
docker run -d \
  --name proxygo \
  -p 127.0.0.1:7776:7776 \
  -p 127.0.0.1:7777:7777 \
  -p 7778:7778 \
  -e WEBUI_PASSWORD=your_password \
  -v "$(pwd)/data:/app/data" \
  isboyjc/goproxy:latest
```

### 从 GitHub Container Registry 拉取

```bash
docker pull ghcr.io/isboyjc/goproxy:latest
docker run -d \
  --name proxygo \
  -p 127.0.0.1:7776:7776 \
  -p 127.0.0.1:7777:7777 \
  -p 7778:7778 \
  -e WEBUI_PASSWORD=your_password \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/isboyjc/goproxy:latest
```

### 使用特定版本

```bash
# 使用特定版本号
docker pull ghcr.io/isboyjc/goproxy:1.0.0

# 使用主版本号（自动获取最新的 1.x.x）
docker pull ghcr.io/isboyjc/goproxy:1
```

## 🚀 发布新版本

创建并推送版本标签即可触发自动构建：

```bash
# 创建版本标签
git tag -a v1.0.0 -m "Release version 1.0.0"

# 推送标签到 GitHub
git push origin v1.0.0
```

GitHub Actions 会自动：
1. 构建多架构镜像
2. 推送到 Docker Hub 和 GHCR
3. 创建多个标签（`1.0.0`, `1.0`, `1`, `latest`）

## 🌐 端口绑定说明

默认示例使用 `127.0.0.1:7776` 和 `127.0.0.1:7777`（仅本地访问）：

- **场景**：在部署机器上本地使用代理
- **安全**：最安全，代理服务不对外暴露
- **限制**：其他机器无法使用该代理

**如需对外开放代理服务**（局域网/VPS 多机共享），修改端口绑定：

```bash
docker run -d \
  --name proxygo \
  -p 7776:7776 \      # ⚠️ 改为对外开放（无认证）
  -p 7777:7777 \      # ⚠️ 改为对外开放（无认证）
  -p 7778:7778 \
  -e WEBUI_PASSWORD=strong_password \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/isboyjc/goproxy:latest
```

**⚠️ 安全警告**：
- 代理服务本身**无认证机制**，对外开放后任何人都可使用
- 建议配合**防火墙白名单**或**云安全组**限制访问 IP
- WebUI 有登录认证，可以安全对外开放

更多部署场景说明，请查看 [README - 部署场景配置](../README.md#部署场景配置)

---

## 🔄 构建策略

Workflow 使用**分离构建策略**：

1. **GHCR 构建**（总是执行）：
   - 无需任何配置，自动推送到 `ghcr.io/isboyjc/goproxy`
   - 使用内置的 `GITHUB_TOKEN`
   - 即使 Docker Hub 未配置也不受影响

2. **Docker Hub 构建**（可选）：
   - 仅在配置了 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN` secrets 时执行
   - 未配置时自动跳过，不会报错

**优势**：
- ✅ 零配置即可使用（通过 GHCR）
- ✅ Docker Hub 完全可选
- ✅ 构建失败不会影响另一个仓库

## 📝 注意事项

- **权限配置**：首次推送到 GHCR 可能需要在仓库 **Settings > Actions > General** 中设置 **Workflow permissions** 为 `Read and write permissions`
- **GHCR 可见性**：默认镜像为私有，可在 **Packages** 页面修改为公开
- **Docker Hub 可选**：不配置 Docker Hub secrets 不会影响构建，workflow 会自动跳过
