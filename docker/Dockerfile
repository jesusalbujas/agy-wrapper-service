FROM golang:1.27.3-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/agy-wrapper .

FROM node:24-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    bash \
    build-essential \
    python3 \
    unzip \
    jq \
    git \
    && rm -rf /var/lib/apt/lists/*

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN bash -o pipefail -c 'curl -fsSL https://antigravity.google/cli/install.sh | bash'
RUN install -m 0755 /root/.local/bin/agy /usr/local/bin/agy

ENV PATH="/root/.local/bin:${PATH}"
ENV NODE_ENV=production

ARG AGY_UID=1000
ARG AGY_GID=1000
RUN mkdir -p /home/agy /run/agy /app/uploads \
    && touch /run/agy/dbus-session \
    && chown "${AGY_UID}:${AGY_GID}" /home/agy /run/agy /run/agy/dbus-session /app/uploads

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY --from=build /out/agy-wrapper /usr/local/bin/agy-wrapper

EXPOSE 3000
CMD ["agy-wrapper"]
