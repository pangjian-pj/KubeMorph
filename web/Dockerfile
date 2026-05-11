# syntax=docker/dockerfile:1

# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Install deps first (better layer cache)
COPY package*.json ./
RUN npm ci

# Copy sources and build
COPY . .
RUN npm run build


# Runtime stage
FROM nginx:1.27-alpine

# Static assets
COPY --from=builder /app/dist /usr/share/nginx/html

# Nginx config template + entrypoint to render it using env vars
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

# Default envs (can be overridden by Helm chart)
ENV BACKEND_SERVICE_HOST=kubemorph-server \
    BACKEND_SERVICE_PORT=8080 \
    BACKEND_API_PREFIX=/api \
    BACKEND_API_VERSION_PREFIX=/api/v1

EXPOSE 80

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
