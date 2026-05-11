# Web

前端脚手架：Vue 3 + Vite + TypeScript + Pinia + Vue Router + vue-i18n。

## 目标

- 可视化纳管集群（Cluster 管理）
- 创建跨集群分发应用（FederatedObject 管理）
- 创建全局优化调度计划（OptimizationPolicy 管理）
- 支持中/英双语

## 目录结构

- `src/router`：路由配置
- `src/i18n`：国际化配置（`en` / `zh-CN`）
- `src/views`：页面（Cluster / Application / Optimization）

## 开发

```bash
npm install
npm run dev
```

## 构建

```bash
npm run build
npm run preview
```

在web目录下直接启动开发服务即可：
npm run dev

## Docker（生产部署）

`kubemorph-web` 的生产镜像使用 Nginx 托管静态资源，并通过 Nginx 将前端请求代理到集群内的后端 Service（适配私有化部署场景）。

### 构建镜像

```bash
docker build -t kubemorph-web:latest .
```

### 运行镜像（本地验证）

```bash
docker run --rm -p 8080:80 \
	-e BACKEND_SERVICE_HOST=kubemorph-server \
	-e BACKEND_SERVICE_PORT=8080 \
	-e BACKEND_API_PREFIX=/api \
	-e BACKEND_API_VERSION_PREFIX=/api/v1 \
	kubemorph-web:latest
```

浏览器访问：`http://localhost:8080`

### Nginx 代理规则与可配置项

镜像启动时会基于 `nginx.conf.template` 渲染最终的 `/etc/nginx/conf.d/default.conf`，用于把前端的 API 请求转发到后端。

- `BACKEND_SERVICE_HOST`：后端 Service DNS（默认：`kubemorph-server`）
- `BACKEND_SERVICE_PORT`：后端 Service 端口（默认：`8080`）
- `BACKEND_API_PREFIX`：前端暴露的 API 前缀（默认：`/api`）
- `BACKEND_API_VERSION_PREFIX`：后端实际路由前缀（默认：`/api/v1`）

默认行为：`/api/*` 会被转发为 `http://${BACKEND_SERVICE_HOST}:${BACKEND_SERVICE_PORT}/api/v1/*`。